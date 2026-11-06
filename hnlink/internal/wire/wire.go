// Package wire is the hnlink over-the-air frame format, version 1.
//
// Every frame is self-contained and 8-bit clean. Big-endian throughout.
//
//	off  len  field
//	0    1    magic 'H' (0x48)
//	1    1    version<<4 | type
//	2    8    object id: first 8 bytes of SHA-256(object)
//	10   ..   type-specific body
//	-4   4    CRC-32 (IEEE) of all preceding bytes
//
// Types:
//
//	MANIFEST (1): flags u8 | length u32 | symsize u16 | k u16 | m u16 |
//	              sha256 [32] | content u8 | origin-len u8 | origin [..]
//	              flags bit0 = END: the sender has finished a round and is
//	              listening for NEED.
//	SYMBOL   (2): esi u16 | data [symsize]
//	              esi < k: systematic (plain) data; k <= esi < k+m: repair.
//	NEED     (3): count u16 | have u16 | requester-len u8 | requester [..]
//	              count = symbols still needed to decode.
//	INV      (4): gen u16 | part u8 | parts u8 | origin-len u8 | origin [..] |
//	              heard-count u8 | (call-len u8 | call [..]) * heard-count |
//	              hashes [6]*n
//	              Inventory beacon: the first 6 bytes of SHA-256(Message-ID) of
//	              every article the origin holds in the exchange window, split
//	              over parts frames; all parts share gen. Part 0 also lists the
//	              stations the origin currently hears (other parts: count 0), which
//	              lets the election route through relays on simplex. The object
//	              id field is zero.
//
// An object is split into k symbols of symsize bytes (last zero-padded) and
// Reed-Solomon coded with m repair symbols; any k distinct symbols decode it.
// The receiver checks the full SHA-256 after decoding.
package wire

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	Magic   = 0x48
	Version = 1

	TManifest = 1
	TSymbol   = 2
	TNeed     = 3
	TInv      = 4

	FlagEnd = 0x01

	ContentRnews = 1

	hdrLen  = 10
	crcLen  = 4
	MaxCall = 16
	HashLen = 6
)

// AHash is the inventory key for one article.
type AHash [HashLen]byte

func ArticleHash(msgid string) (h AHash) {
	s := sha256.Sum256([]byte(msgid))
	copy(h[:], s[:])
	return
}

type ObjID [8]byte

func (o ObjID) String() string { return fmt.Sprintf("%x", o[:]) }

func IDOf(sum [32]byte) (id ObjID) { copy(id[:], sum[:8]); return }

type Manifest struct {
	ID      ObjID
	Flags   uint8
	Length  uint32
	SymSize uint16
	K, M    uint16
	SHA256  [32]byte
	Content uint8
	Origin  string
}

type Symbol struct {
	ID   ObjID
	ESI  uint16
	Data []byte
}

type Need struct {
	ID        ObjID
	Count     uint16
	Have      uint16
	Requester string
}

type Inv struct {
	Gen         uint16
	Part, Parts uint8
	Origin      string
	Heard       []string // part 0 only
	Hashes      []AHash
}

var (
	ErrShort   = errors.New("wire: short frame")
	ErrMagic   = errors.New("wire: not an hnlink frame")
	ErrVersion = errors.New("wire: unsupported version")
	ErrCRC     = errors.New("wire: bad CRC")
	ErrType    = errors.New("wire: unknown frame type")
	ErrField   = errors.New("wire: bad field")
)

func header(t byte, id ObjID, body int) []byte {
	b := make([]byte, hdrLen, hdrLen+body+crcLen)
	b[0] = Magic
	b[1] = Version<<4 | t
	copy(b[2:10], id[:])
	return b
}

func seal(b []byte) []byte {
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

func putCall(b []byte, s string) []byte {
	if len(s) > MaxCall {
		s = s[:MaxCall]
	}
	return append(append(b, byte(len(s))), s...)
}

func (m *Manifest) Marshal() []byte {
	b := header(TManifest, m.ID, 1+4+2+2+2+32+1+1+len(m.Origin))
	b = append(b, m.Flags)
	b = binary.BigEndian.AppendUint32(b, m.Length)
	b = binary.BigEndian.AppendUint16(b, m.SymSize)
	b = binary.BigEndian.AppendUint16(b, m.K)
	b = binary.BigEndian.AppendUint16(b, m.M)
	b = append(b, m.SHA256[:]...)
	b = append(b, m.Content)
	b = putCall(b, m.Origin)
	return seal(b)
}

func (s *Symbol) Marshal() []byte {
	b := header(TSymbol, s.ID, 2+len(s.Data))
	b = binary.BigEndian.AppendUint16(b, s.ESI)
	b = append(b, s.Data...)
	return seal(b)
}

func (n *Need) Marshal() []byte {
	b := header(TNeed, n.ID, 2+2+1+len(n.Requester))
	b = binary.BigEndian.AppendUint16(b, n.Count)
	b = binary.BigEndian.AppendUint16(b, n.Have)
	b = putCall(b, n.Requester)
	return seal(b)
}

func (v *Inv) Marshal() []byte {
	b := header(TInv, ObjID{}, 6+len(v.Origin)+HashLen*len(v.Hashes)+(MaxCall+1)*len(v.Heard))
	b = binary.BigEndian.AppendUint16(b, v.Gen)
	b = append(b, v.Part, v.Parts)
	b = putCall(b, v.Origin)
	heard := v.Heard
	if len(heard) > 255 {
		heard = heard[:255]
	}
	b = append(b, byte(len(heard)))
	for _, c := range heard {
		b = putCall(b, c)
	}
	for _, h := range v.Hashes {
		b = append(b, h[:]...)
	}
	return seal(b)
}

// Parse returns *Manifest, *Symbol, *Need or *Inv. Symbol.Data aliases f.
func Parse(f []byte) (any, error) {
	if len(f) < hdrLen+crcLen {
		return nil, ErrShort
	}
	if f[0] != Magic {
		return nil, ErrMagic
	}
	if f[1]>>4 != Version {
		return nil, ErrVersion
	}
	n := len(f) - crcLen
	if crc32.ChecksumIEEE(f[:n]) != binary.BigEndian.Uint32(f[n:]) {
		return nil, ErrCRC
	}
	var id ObjID
	copy(id[:], f[2:10])
	b := f[hdrLen:n]
	switch f[1] & 0x0f {
	case TManifest:
		if len(b) < 45 {
			return nil, ErrShort
		}
		m := &Manifest{ID: id, Flags: b[0],
			Length:  binary.BigEndian.Uint32(b[1:]),
			SymSize: binary.BigEndian.Uint16(b[5:]),
			K:       binary.BigEndian.Uint16(b[7:]),
			M:       binary.BigEndian.Uint16(b[9:]),
			Content: b[43]}
		copy(m.SHA256[:], b[11:43])
		cl := int(b[44])
		if cl > MaxCall || len(b) != 45+cl {
			return nil, ErrField
		}
		m.Origin = string(b[45:])
		if err := m.Validate(); err != nil {
			return nil, err
		}
		return m, nil
	case TSymbol:
		if len(b) < 3 {
			return nil, ErrShort
		}
		return &Symbol{ID: id, ESI: binary.BigEndian.Uint16(b), Data: b[2:]}, nil
	case TNeed:
		if len(b) < 5 {
			return nil, ErrShort
		}
		cl := int(b[4])
		if cl > MaxCall || len(b) != 5+cl {
			return nil, ErrField
		}
		return &Need{ID: id, Count: binary.BigEndian.Uint16(b), Have: binary.BigEndian.Uint16(b[2:]),
			Requester: string(b[5:])}, nil
	case TInv:
		if len(b) < 5 {
			return nil, ErrShort
		}
		v := &Inv{Gen: binary.BigEndian.Uint16(b), Part: b[2], Parts: b[3]}
		cl := int(b[4])
		if cl > MaxCall || len(b) < 5+cl || v.Parts == 0 || v.Part >= v.Parts {
			return nil, ErrField
		}
		v.Origin = string(b[5 : 5+cl])
		r := b[5+cl:]
		if len(r) < 1 {
			return nil, ErrShort
		}
		nh := int(r[0])
		r = r[1:]
		for i := 0; i < nh; i++ {
			if len(r) < 1 || int(r[0]) > MaxCall || len(r) < 1+int(r[0]) {
				return nil, ErrField
			}
			v.Heard = append(v.Heard, string(r[1:1+int(r[0])]))
			r = r[1+int(r[0]):]
		}
		if len(r)%HashLen != 0 {
			return nil, ErrField
		}
		for ; len(r) > 0; r = r[HashLen:] {
			var h AHash
			copy(h[:], r)
			v.Hashes = append(v.Hashes, h)
		}
		return v, nil
	}
	return nil, ErrType
}

// Validate checks a manifest's internal consistency.
func (m *Manifest) Validate() error {
	if m.SymSize == 0 || m.SymSize%64 != 0 || m.K == 0 || m.M == 0 ||
		int(m.K)+int(m.M) > 65536 ||
		uint64(m.Length) > uint64(m.K)*uint64(m.SymSize) ||
		uint64(m.Length) <= uint64(m.K-1)*uint64(m.SymSize) ||
		IDOf(m.SHA256) != m.ID {
		return ErrField
	}
	return nil
}

// Sum is the object hash used for IDs and the manifest.
func Sum(obj []byte) [32]byte { return sha256.Sum256(obj) }

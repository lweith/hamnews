// Package fec turns an object into Reed-Solomon coded symbols and back.
package fec

import (
	"errors"
	"fmt"

	"hamnews/hnlink/internal/wire"

	"github.com/klauspost/reedsolomon"
)

// MaxK bounds the data symbols per object (leopard GF16 needs k <= 32768 at m=k).
const MaxK = 16384

// Params picks k and m for an object of n bytes at symbol size s.
// m = k (rate 1/2) gives plenty of fresh repair symbols for fill rounds.
func Params(n, s int) (k, m int, err error) {
	if s <= 0 || s%64 != 0 {
		return 0, 0, fmt.Errorf("fec: symbol size %d must be a positive multiple of 64", s)
	}
	if n <= 0 {
		return 0, 0, errors.New("fec: empty object")
	}
	k = (n + s - 1) / s
	if k > MaxK {
		return 0, 0, fmt.Errorf("fec: object of %d bytes needs %d symbols (max %d); split it", n, k, MaxK)
	}
	m = k
	if m < 4 {
		m = 4
	}
	return k, m, nil
}

// Encoded is a fully coded object ready to send.
type Encoded struct {
	Manifest wire.Manifest
	Shards   [][]byte // k+m shards of SymSize bytes
}

func Encode(obj []byte, symsize int, content uint8, origin string) (*Encoded, error) {
	k, m, err := Params(len(obj), symsize)
	if err != nil {
		return nil, err
	}
	enc, err := reedsolomon.New(k, m)
	if err != nil {
		return nil, err
	}
	shards := make([][]byte, k+m)
	buf := make([]byte, (k+m)*symsize)
	copy(buf, obj)
	for i := range shards {
		shards[i] = buf[i*symsize : (i+1)*symsize : (i+1)*symsize]
	}
	if err := enc.Encode(shards); err != nil {
		return nil, err
	}
	sum := wire.Sum(obj)
	return &Encoded{
		Manifest: wire.Manifest{ID: wire.IDOf(sum), Length: uint32(len(obj)), SymSize: uint16(symsize),
			K: uint16(k), M: uint16(m), SHA256: sum, Content: content, Origin: origin},
		Shards: shards,
	}, nil
}

// Decoder collects symbols for one object. Symbols may arrive before the manifest.
type Decoder struct {
	ID       wire.ObjID
	Manifest *wire.Manifest
	syms     map[uint16][]byte
	symsize  int
	done     []byte
}

func NewDecoder(id wire.ObjID) *Decoder { return &Decoder{ID: id, syms: map[uint16][]byte{}} }

var ErrHash = errors.New("fec: decoded object failed SHA-256 check")

// SetManifest records the manifest, dropping symbols that don't fit it.
func (d *Decoder) SetManifest(m *wire.Manifest) {
	if d.Manifest != nil {
		return
	}
	mm := *m
	mm.Flags = 0
	d.Manifest = &mm
	for esi, b := range d.syms {
		if len(b) != int(m.SymSize) || int(esi) >= int(m.K)+int(m.M) {
			delete(d.syms, esi)
		}
	}
}

// Add stores a symbol (copied). Returns false if it was a duplicate or invalid.
func (d *Decoder) Add(s *wire.Symbol) bool {
	if d.done != nil {
		return false
	}
	if _, dup := d.syms[s.ESI]; dup {
		return false
	}
	if d.symsize == 0 {
		d.symsize = len(s.Data)
	}
	if len(s.Data) != d.symsize || len(s.Data) == 0 {
		return false
	}
	if m := d.Manifest; m != nil && (len(s.Data) != int(m.SymSize) || int(s.ESI) >= int(m.K)+int(m.M)) {
		return false
	}
	d.syms[s.ESI] = append([]byte(nil), s.Data...)
	return true
}

func (d *Decoder) Have() int { return len(d.syms) }

// Need is how many more distinct symbols are required, or -1 without a manifest.
func (d *Decoder) Need() int {
	if d.done != nil {
		return 0
	}
	if d.Manifest == nil {
		return -1
	}
	if n := int(d.Manifest.K) - len(d.syms); n > 0 {
		return n
	}
	return 0
}

func (d *Decoder) Done() []byte { return d.done }

// TryDecode reconstructs the object once k symbols and the manifest are present.
// It returns (nil, nil) if more symbols are needed.
func (d *Decoder) TryDecode() ([]byte, error) {
	if d.done != nil {
		return d.done, nil
	}
	m := d.Manifest
	if m == nil || len(d.syms) < int(m.K) {
		return nil, nil
	}
	k, n, ss := int(m.K), int(m.K)+int(m.M), int(m.SymSize)
	shards := make([][]byte, n)
	for esi, b := range d.syms {
		shards[esi] = b
	}
	enc, err := reedsolomon.New(k, int(m.M))
	if err != nil {
		return nil, err
	}
	if err := enc.ReconstructData(shards); err != nil {
		return nil, err
	}
	obj := make([]byte, 0, k*ss)
	for i := 0; i < k; i++ {
		obj = append(obj, shards[i]...)
	}
	obj = obj[:m.Length]
	if wire.Sum(obj) != m.SHA256 {
		// A corrupted symbol slipped through CRC-32; start over.
		d.syms = map[uint16][]byte{}
		return nil, ErrHash
	}
	d.done = obj
	d.syms = nil
	return obj, nil
}

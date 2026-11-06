// Package kiss carries hnlink frames as AX.25 UI frames over a KISS TNC: TCP
// (direwolf: port 8001; fldigi: Configure > Misc > KISS) or a serial port (a
// hardware TNC in KISS mode). The AX.25 source address is the station callsign,
// which also serves as station ID.
package kiss

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	fend  = 0xC0
	fesc  = 0xDB
	tfend = 0xDC
	tfesc = 0xDD

	ctlUI  = 0x03
	pidNL3 = 0xF0

	// Dest is the AX.25 destination used for every hnlink frame.
	Dest = "HNLINK"
)

// Escape wraps one KISS data frame for port 0.
func Escape(payload []byte) []byte {
	b := make([]byte, 0, len(payload)+8)
	b = append(b, fend, 0x00)
	for _, c := range payload {
		switch c {
		case fend:
			b = append(b, fesc, tfend)
		case fesc:
			b = append(b, fesc, tfesc)
		default:
			b = append(b, c)
		}
	}
	return append(b, fend)
}

// Reader splits a KISS byte stream into data-frame payloads (command 0, any port).
type Reader struct {
	r   *bufio.Reader
	buf []byte
}

func NewReader(r io.Reader) *Reader { return &Reader{r: bufio.NewReader(r)} }

func (k *Reader) Next() ([]byte, error) {
	in, esc := false, false
	k.buf = k.buf[:0]
	for {
		c, err := k.r.ReadByte()
		if err != nil {
			return nil, err
		}
		switch {
		case c == fend:
			if in && len(k.buf) > 1 && k.buf[0]&0x0f == 0 {
				return append([]byte(nil), k.buf[1:]...), nil
			}
			in, esc = true, false
			k.buf = k.buf[:0]
		case !in:
		case esc:
			switch c {
			case tfend:
				k.buf = append(k.buf, fend)
			case tfesc:
				k.buf = append(k.buf, fesc)
			default:
				in = false // protocol error: drop frame
			}
			esc = false
		case c == fesc:
			esc = true
		default:
			k.buf = append(k.buf, c)
		}
		if len(k.buf) > 4096 {
			in = false
		}
	}
}

// Addr is an AX.25 callsign-SSID.
type Addr struct {
	Call string
	SSID int
}

func ParseAddr(s string) (Addr, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	a := Addr{Call: s}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		n, err := strconv.Atoi(s[i+1:])
		if err != nil || n < 0 || n > 15 {
			return a, fmt.Errorf("bad SSID in %q", s)
		}
		a.Call, a.SSID = s[:i], n
	}
	if len(a.Call) == 0 || len(a.Call) > 6 {
		return a, fmt.Errorf("callsign %q must be 1-6 characters", a.Call)
	}
	for _, c := range a.Call {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return a, fmt.Errorf("callsign %q: letters and digits only", a.Call)
		}
	}
	return a, nil
}

func (a Addr) String() string {
	if a.SSID == 0 {
		return a.Call
	}
	return fmt.Sprintf("%s-%d", a.Call, a.SSID)
}

func encAddr(b []byte, a Addr, cmd, last bool) []byte {
	c := a.Call + "      "
	for i := 0; i < 6; i++ {
		b = append(b, c[i]<<1)
	}
	s := byte(0x60 | a.SSID<<1)
	if cmd {
		s |= 0x80
	}
	if last {
		s |= 0x01
	}
	return append(b, s)
}

func decAddr(b []byte) Addr {
	var sb strings.Builder
	for i := 0; i < 6; i++ {
		if c := b[i] >> 1; c != ' ' {
			sb.WriteByte(c)
		}
	}
	return Addr{Call: sb.String(), SSID: int(b[6]>>1) & 0x0f}
}

// UI builds an AX.25 UI frame (no FCS; the TNC adds it).
func UI(src Addr, info []byte) []byte {
	b := make([]byte, 0, 16+len(info))
	b = encAddr(b, Addr{Call: Dest}, true, false)
	b = encAddr(b, src, false, true)
	return append(append(b, ctlUI, pidNL3), info...)
}

var ErrNotOurs = errors.New("kiss: not an hnlink UI frame")

// ParseUI returns the source and info field of an hnlink UI frame.
func ParseUI(f []byte) (Addr, []byte, error) {
	if len(f) < 16 {
		return Addr{}, nil, ErrNotOurs
	}
	dst, src := decAddr(f[0:7]), decAddr(f[7:14])
	i := 14
	for f[i-1]&0x01 == 0 { // digipeaters
		if i+7 > len(f) {
			return Addr{}, nil, ErrNotOurs
		}
		i += 7
	}
	if i+2 > len(f) || f[i] != ctlUI || f[i+1] != pidNL3 || dst.Call != Dest {
		return Addr{}, nil, ErrNotOurs
	}
	return src, f[i+2:], nil
}

// Conn is a KISS connection, TCP or serial.
type Conn struct {
	c      io.ReadWriteCloser
	serial bool
	r      *Reader
	src    Addr
	mu     sync.Mutex

	lastHeard atomic.Int64 // unix nanos of the last frame of any kind
}

// LastHeard is when the TNC last passed up any frame (hnlink, Winlink, APRS...).
// direwolf hands every received frame to every KISS client, so this is a usable
// channel-activity signal.
func (k *Conn) LastHeard() time.Time { return time.Unix(0, k.lastHeard.Load()) }

// IsSerial reports whether addr names a serial port rather than a TCP address.
func IsSerial(addr string) bool {
	if strings.HasPrefix(addr, "/dev/") {
		return true
	}
	// Windows COMn[:BAUD]; not a hostname like "community.example:8001".
	u := strings.ToUpper(addr)
	if i := strings.IndexByte(u, ':'); i >= 0 {
		u = u[:i]
	}
	_, err := strconv.Atoi(strings.TrimPrefix(u, "COM"))
	return strings.HasPrefix(u, "COM") && err == nil
}

// ParseSerial splits "/dev/ttyUSB0" or "/dev/ttyUSB0:9600" (default 9600 baud).
func ParseSerial(addr string) (path string, baud int, err error) {
	path, baud = addr, 9600
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		path = addr[:i]
		if baud, err = strconv.Atoi(addr[i+1:]); err != nil {
			return "", 0, fmt.Errorf("serial %q: bad baud rate", addr)
		}
	}
	return path, baud, nil
}

// Dial connects to a KISS TNC: "HOST:PORT" for TCP (direwolf), or a serial
// device "/dev/ttyUSB0[:BAUD]" for a hardware TNC already in KISS mode.
func Dial(addr string, src Addr) (*Conn, error) {
	if IsSerial(addr) {
		path, baud, err := ParseSerial(addr)
		if err != nil {
			return nil, err
		}
		c, err := openSerial(path, baud)
		if err != nil {
			return nil, err
		}
		return &Conn{c: c, r: NewReader(c), src: src, serial: true}, nil
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Conn{c: c, r: NewReader(c), src: src}, nil
}

// Serial reports whether this is a serial (hardware TNC) connection.
func (k *Conn) Serial() bool { return k.serial }

// SetTxDelay sends the KISS TXDELAY command (port 0). A hardware TNC keys up
// for this long before sending; direwolf takes it from its own config instead.
func (k *Conn) SetTxDelay(d time.Duration) error {
	v := d / (10 * time.Millisecond)
	if v < 0 {
		v = 0
	} else if v > 255 {
		v = 255
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	_, err := k.c.Write([]byte{fend, 0x01, byte(v), fend})
	return err
}

func (k *Conn) Send(frame []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	_, err := k.c.Write(Escape(UI(k.src, frame)))
	return err
}

// Recv returns the next hnlink frame and its sender, skipping other traffic.
func (k *Conn) Recv() (string, []byte, error) {
	for {
		f, err := k.r.Next()
		if err != nil {
			return "", nil, err
		}
		k.lastHeard.Store(time.Now().UnixNano())
		if src, info, err := ParseUI(f); err == nil {
			return src.String(), info, nil
		}
	}
}

func (k *Conn) Close() error { return k.c.Close() }

// OpenSerial opens a serial port raw at baud (Linux only, for now).
func OpenSerial(path string, baud int) (io.ReadWriteCloser, error) { return openSerial(path, baud) }

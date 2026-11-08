package main

// KISS framing (TNC serial protocol) and the AX.25 addresses at the front of
// a data frame. Pure functions: no I/O, so they can be tested without a TNC.

const (
	fend  = 0xC0
	fesc  = 0xDB
	tfend = 0xDC
	tfesc = 0xDD

	// maxFrame bounds one KISS frame. A longer run without a FEND is junk
	// (wrong baud, or not KISS at all) and is dropped rather than grown.
	maxFrame = 2048
)

// kissFrame is one decoded KISS frame.
type kissFrame struct {
	Port int    // TNC port, high nibble of the type byte
	Cmd  int    // 0 = data frame; others are TNC settings (TXDELAY, ...)
	Data []byte // the AX.25 frame for a data frame
}

// kissDecoder turns a byte stream into KISS frames one byte at a time.
type kissDecoder struct {
	buf      []byte
	esc      bool
	skipping bool // an overlong run: drop bytes until the next FEND
	Errors   int  // bad escapes and overlong frames
}

// feed takes one byte and returns a frame when one completes.
func (d *kissDecoder) feed(b byte) (kissFrame, bool) {
	if b == fend {
		d.esc = false
		if d.skipping {
			d.skipping = false
			d.buf = nil
			return kissFrame{}, false
		}
		if len(d.buf) == 0 {
			return kissFrame{}, false // back-to-back FENDs between frames
		}
		f := d.buf
		d.buf = nil
		return kissFrame{Port: int(f[0] >> 4), Cmd: int(f[0] & 0x0F), Data: f[1:]}, true
	}
	if d.skipping {
		return kissFrame{}, false
	}
	if d.esc {
		d.esc = false
		switch b {
		case tfend:
			b = fend
		case tfesc:
			b = fesc
		default:
			d.Errors++ // an escape that means nothing: keep the byte, count it
		}
	} else if b == fesc {
		d.esc = true
		return kissFrame{}, false
	}
	if len(d.buf) >= maxFrame {
		d.Errors++
		d.buf = nil
		d.skipping = true
		return kissFrame{}, false
	}
	d.buf = append(d.buf, b)
	return kissFrame{}, false
}

// ax25Addrs returns the source and destination of an AX.25 frame as CALL-SSID.
// ok is false if the frame is too short to hold two addresses.
func ax25Addrs(f []byte) (src, dst string, ok bool) {
	if len(f) < 14 {
		return "", "", false
	}
	dst = ax25Call(f[0:7])
	src = ax25Call(f[7:14])
	return src, dst, true
}

// ax25Call decodes one 7-byte address field: six shifted callsign characters
// and an SSID byte.
func ax25Call(a []byte) string {
	var c []byte
	for _, b := range a[:6] {
		ch := b >> 1
		if ch == ' ' {
			continue
		}
		c = append(c, ch)
	}
	ssid := (a[6] >> 1) & 0x0F
	return string(c) + "-" + itoa(int(ssid))
}

// itoa is a small int-to-string for SSIDs (0-15), avoiding strconv here.
func itoa(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

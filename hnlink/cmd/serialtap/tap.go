package main

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"
)

// tap is the state shared by the two directions of the link and by the TCP
// listeners.
type tap struct {
	st   *stats
	logf *os.File // nil disables the frame log
	hub  *hub
	gate *tncGate
	txq  *txQueue
}

// pump copies src to dst, byte for byte, and decodes what passes. Writes come
// before decoding, so the tap adds no delay to the link. Host bytes go through
// the gate, so injected frames can't split one of them.
func (t *tap) pump(dir string, src io.Reader, dst io.Writer) error {
	var d kissDecoder
	var gate *tncGate
	if dir == "host" {
		gate = t.gate
	}
	buf := make([]byte, 512)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if werr := t.chunk(dir, buf[:n], dst, &d, gate); werr != nil {
				return werr
			}
		}
		if err != nil {
			return err
		}
	}
}

// chunk writes one read's worth of bytes and decodes them. With a gate, the
// gate is held across the write and the decode, so the boundary state matches
// the bytes actually sent.
func (t *tap) chunk(dir string, b []byte, dst io.Writer, d *kissDecoder, gate *tncGate) error {
	if gate != nil {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		gate.lastHost = time.Now()
	}
	if _, err := dst.Write(b); err != nil {
		return err
	}
	t.st.addBytes(dir, len(b))
	for _, x := range b {
		f, ok := d.feed(x)
		if gate != nil {
			gate.boundary = ok // a frame just closed: the stream is between frames
		}
		if ok {
			t.frame(dir, f)
		}
	}
	t.st.addErrors(dir, d.Errors)
	d.Errors = 0
	return nil
}

// frame counts and logs one decoded frame, then offers its data to the
// listeners. Data frames go to every listener in either direction; TNC
// settings (cmd != 0) are counted but not passed on.
func (t *tap) frame(dir string, f kissFrame) {
	t.st.addFrame(dir, f)
	if t.logf != nil {
		fmt.Fprintf(t.logf, "%s %s %d %d %s\n", time.Now().UTC().Format(time.RFC3339Nano),
			dir, f.Port, f.Cmd, hex.EncodeToString(f.Data))
	}
	if f.Cmd == 0 {
		t.hub.deliver(f.Data)
	}
}

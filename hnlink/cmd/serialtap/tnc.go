package main

import (
	"io"
	"log"
	"sync"
	"time"
)

// hostIdle is how long the host side may stay silent before an injected frame
// goes out even though the host's stream isn't at a frame boundary. A host
// that stops mid-frame for this long has lost that frame anyway.
const hostIdle = 2 * time.Second

// tncGate serializes writes to the TNC. Host bytes are written in whatever
// chunks the serial read returns, so an injected frame must not land inside
// one of the host's frames. The gate records whether the host stream is at a
// frame boundary and lets an injected frame in only there, or once the host
// has been silent for hostIdle.
type tncGate struct {
	mu       sync.Mutex
	w        io.Writer
	boundary bool      // the host stream is between frames
	lastHost time.Time // when the host last wrote
}

func newTNCGate(w io.Writer) *tncGate {
	return &tncGate{w: w, boundary: true, lastHost: time.Now()}
}

// writeTx writes one whole KISS frame to the TNC once the host stream allows.
func (g *tncGate) writeTx(frame []byte) error {
	for {
		g.mu.Lock()
		if g.boundary || time.Since(g.lastHost) >= hostIdle {
			break
		}
		g.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	defer g.mu.Unlock()
	_, err := g.w.Write(frame)
	return err
}

// lineTime is how long n bytes take on the wire at baud, 8N1.
func lineTime(n, baud int) time.Duration {
	return time.Duration(n) * 10 * time.Second / time.Duration(baud)
}

// runTx sends queued frames to the TNC at the line rate. Each frame goes out
// whole, and the next waits for the previous one's line time. Runs until the
// TNC write fails.
func (t *tap) runTx(baud int) {
	for range t.txq.ready {
		for {
			enc, ok := t.txq.pop()
			if !ok {
				break
			}
			if err := t.gate.writeTx(enc); err != nil {
				log.Printf("serialtap: write to TNC: %v", err)
				return
			}
			f := unkiss(enc)
			t.st.addFrame("net", kissFrame{Cmd: 0, Data: f})
			t.hub.deliver(f)
			time.Sleep(lineTime(len(enc), baud))
		}
	}
}

// unkiss returns the AX.25 payload of one KISS-encoded data frame.
func unkiss(enc []byte) []byte {
	var d kissDecoder
	for _, b := range enc {
		if f, ok := d.feed(b); ok {
			return f.Data
		}
	}
	return nil
}

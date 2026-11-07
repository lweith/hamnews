package link

import (
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"hamnews/hnlink/internal/nntp"
	"hamnews/hnlink/internal/rnews"
)

// hub is a shared half-duplex broadcast channel with independent per-receiver loss.
type hub struct {
	mu    sync.Mutex
	ports []*port
	link  func(a, b string) bool // nil: everyone hears everyone
	loss  float64
	rng   *rand.Rand
	heard time.Time
	drop  func(from string, f []byte) bool // optional: lose a transmission entirely
}

type port struct {
	h    *hub
	call string
	in   chan [2]any
	last time.Time
	mu   sync.Mutex
}

func (h *hub) attach(call string) *port {
	p := &port{h: h, call: call, in: make(chan [2]any, 10000)}
	h.mu.Lock()
	h.ports = append(h.ports, p)
	h.mu.Unlock()
	return p
}

func (p *port) Send(f []byte) error {
	h := p.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.drop != nil && h.drop(p.call, f) {
		return nil
	}
	for _, q := range h.ports {
		if q == p || (h.link != nil && !h.link(p.call, q.call)) || h.rng.Float64() < h.loss {
			continue
		}
		c := append([]byte(nil), f...)
		if h.rng.Float64() < h.loss/10 { // occasional corruption that the CRC must catch
			c[h.rng.Intn(len(c))] ^= 0x10
		}
		q.mu.Lock()
		q.last = time.Now()
		q.mu.Unlock()
		q.in <- [2]any{p.call, c}
	}
	return nil
}

func (p *port) Recv() (string, []byte, error) {
	m := <-p.in
	return m[0].(string), m[1].([]byte), nil
}

func (p *port) LastHeard() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.last
}

// memStore is a fake innd.
type memStore struct {
	mu   sync.Mutex
	arts map[string][]byte
}

func (s *memStore) Inventory() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id := range s.arts {
		ids = append(ids, id)
	}
	return ids, nil
}

func (s *memStore) Articles(ids []string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for _, id := range ids {
		if a, ok := s.arts[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (s *memStore) Ingest(b []byte, _ io.Writer) (nntp.Tally, error) {
	var t nntp.Tally
	arts, err := rnews.Split(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range arts {
		id := rnews.Header(a, "Message-ID")
		if _, ok := s.arts[id]; ok {
			t.Duplicate++
			continue
		}
		s.arts[id] = append([]byte(nil), a...)
		t.Accepted++
	}
	return t, err
}

func (s *memStore) ids() []string {
	ids, _ := s.Inventory()
	sort.Strings(ids)
	return ids
}

func article(call string, n int, size int) (string, []byte) {
	id := fmt.Sprintf("<%d.%s@hamnews>", n, call)
	body := make([]byte, size)
	for i := range body {
		body[i] = byte(' ' + (i*7+n)%90)
		if i%70 == 69 {
			body[i] = '\n'
		}
	}
	return id, fmt.Appendf(nil, "Path: %s!not-for-mail\nFrom: %s\nNewsgroups: ham.test\nSubject: test %d\nMessage-ID: %s\n\n%s\n",
		call, call, n, id, body)
}

func fastCfg(call string, verbose bool) Config {
	c := Defaults(call)
	c.Bitrate, c.TxDelay, c.Quiet = 400000, time.Millisecond, 5*time.Millisecond
	c.InvEvery, c.InvDebounce, c.RefreshEvery = 400*time.Millisecond, 30*time.Millisecond, 100*time.Millisecond
	c.PeerTTL, c.ResendHold = 10*time.Second, 1500*time.Millisecond
	c.Listen, c.NeedSpread = 60*time.Millisecond, 40*time.Millisecond
	c.NeedIdle = 300 * time.Millisecond
	c.MaxObject = 16 << 10
	if verbose {
		c.Logger = log.New(os.Stderr, call+" ", log.Lmicroseconds)
	}
	return c
}

func TestNetConverges(t *testing.T) {
	for _, loss := range []float64{0, 0.2, 0.4} {
		t.Run(fmt.Sprintf("loss=%.0f%%", loss*100), func(t *testing.T) { converge(t, loss) })
	}
}

func converge(t *testing.T, loss float64) {
	convergeOn(t, &hub{loss: loss, rng: rand.New(rand.NewSource(42))},
		[]string{"KN6FLN-5", "W6ABC", "K6XYZ-2", "N6NEW"})
}

// Simplex line: each station hears only its neighbours. Ordered so the
// lowest callsign is at one end, the case that stalled "lowest holder sends".
func TestSimplexLineRelays(t *testing.T) {
	calls := []string{"AA1A", "BB2B", "CC3C", "DD4D", "N6NEW"}
	pos := map[string]int{}
	for i, c := range calls {
		pos[c] = i
	}
	h := &hub{loss: 0.15, rng: rand.New(rand.NewSource(7)), link: func(a, b string) bool {
		d := pos[a] - pos[b]
		return d == 1 || d == -1
	}}
	convergeOn(t, h, calls)
}

func convergeOn(t *testing.T, h *hub, calls []string) {
	stores := map[string]*memStore{}
	var engines []*Engine
	want := map[string]bool{}
	for i, c := range calls {
		s := &memStore{arts: map[string][]byte{}}
		// Overlapping holdings; N6NEW starts empty.
		for n := 0; n < 12*max(1, 3-i) && c != "N6NEW"; n++ {
			id, a := article(c, n, 300+n*40)
			s.arts[id] = a
			want[id] = true
		}
		if c == "W6ABC" { // something W6ABC also has from KN6FLN-5
			id, a := article("KN6FLN-5", 0, 300)
			s.arts[id] = a
		}
		stores[c] = s
		e := New(fastCfg(c, testing.Verbose()), h.attach(c), s)
		engines = append(engines, e)
		go e.Run()
	}
	defer func() {
		for _, e := range engines {
			e.Stop()
		}
	}()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, c := range calls {
			if len(stores[c].ids()) != len(want) {
				ok = false
			}
		}
		if ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, c := range calls {
		got := stores[c].ids()
		if len(got) != len(want) {
			t.Errorf("%s has %d/%d articles", c, len(got), len(want))
		}
		for _, id := range got {
			a := stores[c].arts[id]
			if rnews.Header(a, "Message-ID") != id {
				t.Errorf("%s: %s corrupted", c, id)
			}
		}
	}
	var st []string
	for i, e := range engines {
		e.Do(func(e *Engine) {
			st = append(st, fmt.Sprintf("%s: tx %d rx %d sent %d decoded %d needs %d ingest %v",
				calls[i], e.Stats.FramesTx, e.Stats.FramesRx, e.Stats.ObjectsSent, e.Stats.ObjectsDecoded, e.Stats.Needs, e.Stats.Ingested))
		})
	}
	for _, s := range st {
		t.Log(s)
	}
}

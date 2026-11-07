package link

import (
	"sync"
	"testing"
	"time"

	"hamnews/hnlink/internal/wire"
)

// stubRadio records what the engine transmits and delivers what the test injects.
type stubRadio struct {
	mu   sync.Mutex
	sent int
	in   chan []byte
}

func (r *stubRadio) Send(f []byte) error {
	r.mu.Lock()
	r.sent++
	r.mu.Unlock()
	return nil
}

func (r *stubRadio) Recv() (string, []byte, error) {
	f := <-r.in
	return "W6ABC", f, nil
}

func (r *stubRadio) LastHeard() time.Time { return time.Time{} }

func (r *stubRadio) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent
}

// retryEngine starts an engine with three articles and no periodic beacon,
// waits for its startup beacon, then adds a live peer that holds nothing, so
// an answer is owed.
func retryEngine(t *testing.T, tries int, retry time.Duration) (*Engine, *stubRadio, func()) {
	t.Helper()
	cfg := fastCfg("KN6FLN-5", testing.Verbose())
	cfg.InvEvery = time.Hour
	cfg.BeaconRetry = retry
	cfg.BeaconRetries = tries
	s := &memStore{arts: map[string][]byte{}}
	for n := 0; n < 3; n++ {
		id, a := article("KN6FLN-5", n, 300)
		s.arts[id] = a
	}
	r := &stubRadio{in: make(chan []byte, 10)}
	e := New(cfg, r, s)
	go e.Run()
	waitFor(t, func() bool { return r.count() >= 1 })
	e.Do(func(e *Engine) {
		e.peers["W6ABC"] = &peer{have: true, inv: map[wire.AHash]bool{}, seen: time.Now()}
	})
	return e, r, e.Stop
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestBeaconRetriesUnanswered(t *testing.T) {
	_, r, stop := retryEngine(t, 2, 100*time.Millisecond)
	defer stop()
	waitFor(t, func() bool { return r.count() >= 3 })
	time.Sleep(500 * time.Millisecond) // no further retries once the budget is spent
	if n := r.count(); n != 3 {
		t.Fatalf("sent %d beacons, want 3 (startup plus 2 retries)", n)
	}
}

func TestBeaconNoRetryWhenHeard(t *testing.T) {
	_, r, stop := retryEngine(t, 2, 500*time.Millisecond)
	defer stop()
	// Hearing the peer starts a beacon (a new neighbour changes our state).
	// The peer then answers that beacon, so it must not be retried.
	empty := (&wire.Inv{Gen: 1, Part: 0, Parts: 1, Origin: "W6ABC"}).Marshal()
	r.in <- empty
	waitFor(t, func() bool { return r.count() >= 2 })
	time.Sleep(30 * time.Millisecond)
	r.in <- empty
	time.Sleep(1200 * time.Millisecond)
	if n := r.count(); n != 2 {
		t.Fatalf("sent %d beacons, want 2 (answered beacon must not be retried)", n)
	}
}

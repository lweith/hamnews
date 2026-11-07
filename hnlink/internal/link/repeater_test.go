package link

import (
	"testing"
	"time"

	"hamnews/hnlink/internal/wire"
)

// repeaterEngine starts a repeater-mode engine on a 500 ms slot period with the
// given heartbeat, and waits for its first (owed) beacon.
func repeaterEngine(t *testing.T, heartbeat time.Duration) (*Engine, *stubRadio, *memStore, func()) {
	t.Helper()
	cfg := fastCfg("KN6FLN-5", testing.Verbose())
	cfg.Mode = "repeater"
	cfg.InvEvery = 500 * time.Millisecond
	cfg.Heartbeat = heartbeat
	s := &memStore{arts: map[string][]byte{}}
	r := &stubRadio{in: make(chan []byte, 10)}
	e := New(cfg, r, s)
	go e.Run()
	waitFor(t, func() bool { return r.count() >= 1 })
	return e, r, s, e.Stop
}

// A synchronized repeater station stays silent: after answering the new peer
// once, it transmits nothing more while the peer agrees with it.
func TestRepeaterSilentWhenSynced(t *testing.T) {
	_, r, _, stop := repeaterEngine(t, time.Hour)
	defer stop()
	empty := (&wire.Inv{Gen: 1, Part: 0, Parts: 1, Origin: "W6ABC"}).Marshal()
	r.in <- empty
	waitFor(t, func() bool { return r.count() >= 2 }) // the answer
	before := r.count()
	time.Sleep(1500 * time.Millisecond) // three slot periods
	if n := r.count(); n != before {
		t.Fatalf("sent %d beacons while synchronized, want none", n-before)
	}
}

// A new article we hold, which the peer lacks, is announced in our next slot.
func TestRepeaterSpeaksForNewArticle(t *testing.T) {
	_, r, s, stop := repeaterEngine(t, time.Hour)
	defer stop()
	r.in <- (&wire.Inv{Gen: 1, Part: 0, Parts: 1, Origin: "W6ABC"}).Marshal()
	waitFor(t, func() bool { return r.count() >= 2 })
	time.Sleep(1200 * time.Millisecond)
	before := r.count()
	id, a := article("KN6FLN-5", 0, 300)
	s.mu.Lock()
	s.arts[id] = a
	s.mu.Unlock()
	waitFor(t, func() bool { return r.count() > before })
}

// The heartbeat keeps a quiet station visible even with nothing to say.
func TestRepeaterHeartbeat(t *testing.T) {
	_, r, _, stop := repeaterEngine(t, 800*time.Millisecond)
	defer stop()
	r.in <- (&wire.Inv{Gen: 1, Part: 0, Parts: 1, Origin: "W6ABC"}).Marshal()
	waitFor(t, func() bool { return r.count() >= 2 })
	before := r.count()
	waitFor(t, func() bool { return r.count() > before })
}

// Slots must spread across the whole period. Two stations in the same few
// seconds of a 30-minute period would collide every time.
func TestSlotOffsetsSpreadAcrossPeriod(t *testing.T) {
	period := 30 * time.Minute
	calls := []string{"KN6FLN-4", "KN6FLN-5", "KN6FLN-6", "W6ABC", "K6XYZ-2", "N6NEW"}
	var lo, hi time.Duration = period, 0
	for _, c := range calls {
		off := slotOffset(c, period)
		if off < 0 || off >= period {
			t.Fatalf("%s offset %v outside the period", c, off)
		}
		lo, hi = min(lo, off), max(hi, off)
	}
	if spread := hi - lo; spread < period/2 {
		t.Fatalf("offsets for %d stations span only %v of a %v period", len(calls), spread, period)
	}
}

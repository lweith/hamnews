package link

import (
	"math/rand"
	"testing"
	"time"
)

// Two repeaters, two hnlink instances, one innd. An article heard on channel A
// must reach a station on channel B through the shared store, and the reverse
// must hold too. Neither channel should need the other's peers.
func TestBridgeCarriesBetweenChannels(t *testing.T) {
	hubA := &hub{rng: rand.New(rand.NewSource(1))}
	hubB := &hub{rng: rand.New(rand.NewSource(2))}

	innd := &memStore{arts: map[string][]byte{}} // shared by both instances
	remoteA := &memStore{arts: map[string][]byte{}}
	for n := 0; n < 6; n++ {
		id, a := article("W6ABC", n, 300)
		remoteA.arts[id] = a
	}
	remoteB := &memStore{arts: map[string][]byte{}} // holds nothing; must receive everything

	var engines []*Engine
	start := func(call string, h *hub, s Store) {
		e := New(fastCfg(call, testing.Verbose()), h.attach(call), s)
		engines = append(engines, e)
		go e.Run()
	}
	start("KN6FLN-5", hubA, innd) // instance A, channel A
	start("W6ABC", hubA, remoteA)
	start("KN6FLN-6", hubB, innd) // instance B, channel B
	start("K6XYZ-2", hubB, remoteB)
	defer func() {
		for _, e := range engines {
			e.Stop()
		}
	}()

	want := remoteA.ids()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if len(innd.ids()) == len(want) && len(remoteB.ids()) == len(want) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := innd.ids(); len(got) != len(want) {
		t.Fatalf("shared innd holds %d articles, want %d", len(got), len(want))
	}
	if got := remoteB.ids(); len(got) != len(want) {
		t.Fatalf("channel B station holds %d articles, want %d", len(got), len(want))
	}
}

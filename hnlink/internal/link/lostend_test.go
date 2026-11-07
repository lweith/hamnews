package link

import (
	"math/rand"
	"testing"

	"hamnews/hnlink/internal/wire"
)

// dropEnds loses every END manifest, as if both copies were garbled.
func dropEnds(_ string, f []byte) bool {
	p, err := wire.Parse(f)
	if err != nil {
		return false
	}
	m, ok := p.(*wire.Manifest)
	return ok && m.Flags&wire.FlagEnd != 0
}

// With every END lost, receivers must still get the articles: they NEED
// what they lack after NeedIdle, and the sender repairs from the object it
// kept after finishing.
func TestConvergesWithLostEnds(t *testing.T) {
	convergeOn(t, &hub{rng: rand.New(rand.NewSource(7)), drop: dropEnds},
		[]string{"KN6FLN-5", "W6ABC", "K6XYZ-2"})
}

package main

// Baud-rate detection. The link is 8N1 (the TNC default); the tap tries each
// common rate and keeps the one whose bytes parse as KISS frames.

// detectRates are the rates tried, fastest first, for a TNC on 8N1.
var detectRates = []int{9600, 4800, 2400, 1200}

// rateScore is how a stream read at one rate looks.
type rateScore struct {
	Rate   int
	Frames int // complete KISS frames with no framing errors
	Errors int // bad escapes and overlong runs
	Bytes  int
}

// scoreStream decodes a read at one rate and counts what parses. A stream of
// valid KISS frames at the right rate scores many frames and few errors; the
// wrong rate scores garbage, which rarely closes a frame cleanly.
func scoreStream(rate int, b []byte) rateScore {
	var d kissDecoder
	s := rateScore{Rate: rate, Bytes: len(b)}
	for _, x := range b {
		if f, ok := d.feed(x); ok && len(f.Data) >= 14 {
			s.Frames++
		}
	}
	s.Errors = d.Errors
	return s
}

// bestRate picks the rate with the most clean frames. ok is false when no rate
// produced a frame: the link is not KISS, or nothing is talking.
func bestRate(scores []rateScore) (best rateScore, ok bool) {
	for _, s := range scores {
		if s.Frames == 0 {
			continue
		}
		// fewer errors breaks a tie in frame count
		if !ok || s.Frames > best.Frames || (s.Frames == best.Frames && s.Errors < best.Errors) {
			best, ok = s, true
		}
	}
	return best, ok
}

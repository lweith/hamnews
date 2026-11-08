package main

import "testing"

// The budget is line time, so it scales with the link rate.
func TestTxBudgetFollowsBaud(t *testing.T) {
	for _, tc := range []struct {
		baud, want int
	}{
		{1200, 7200},
		{2400, 14400},
		{9600, 57600},
	} {
		if got := newTxQueue(tc.baud, defaultTxSeconds).budget; got != tc.want {
			t.Errorf("%d baud: budget %d, want %d", tc.baud, got, tc.want)
		}
	}
}

// At 1200 baud, 60 s is 7200 bytes: 36 frames of 200 fit, the 37th doesn't.
func TestTxRefusesPastBudget(t *testing.T) {
	q := newTxQueue(1200, 60)
	frame := kissEncode(make([]byte, 197)) // 200 bytes on the wire (2 FENDs, type, payload)
	if len(frame) != 200 {
		t.Fatalf("test frame is %d bytes, want 200", len(frame))
	}
	accepted := 0
	for i := 0; i < 40; i++ {
		if q.push(frame) {
			accepted++
		}
	}
	if accepted != 36 {
		t.Errorf("accepted %d frames, want 36", accepted)
	}
	if n := q.refused.Load(); n != 4 {
		t.Errorf("refused %d, want 4", n)
	}
}

// Popping frees budget, so a later frame fits again.
func TestTxPopFreesBudget(t *testing.T) {
	q := newTxQueue(1200, 1) // 120 bytes
	big := kissEncode(make([]byte, 100))
	if !q.push(big) {
		t.Fatal("first frame refused")
	}
	if q.push(big) {
		t.Fatal("second frame accepted past budget")
	}
	if _, ok := q.pop(); !ok {
		t.Fatal("pop found nothing")
	}
	if !q.push(big) {
		t.Error("frame refused after pop freed the budget")
	}
}

// A single frame bigger than the whole budget is always refused.
func TestTxRefusesOversizeFrame(t *testing.T) {
	q := newTxQueue(1200, 1)
	if q.push(kissEncode(make([]byte, 200))) {
		t.Error("frame larger than the budget was accepted")
	}
	if n, _ := q.queuedBytes(); n != 0 {
		t.Errorf("queued %d bytes after refusal, want 0", n)
	}
}

// Frames leave in the order they went in.
func TestTxFIFO(t *testing.T) {
	q := newTxQueue(9600, 60)
	for _, s := range []string{"one", "two", "three"} {
		q.push(kissEncode([]byte(s)))
	}
	for _, want := range []string{"one", "two", "three"} {
		f, ok := q.pop()
		if !ok {
			t.Fatalf("queue empty before %q", want)
		}
		fs, _ := decodeAll(f)
		if len(fs) != 1 || string(fs[0].Data) != want {
			t.Errorf("popped %q, want %q", fs, want)
		}
	}
	if _, ok := q.pop(); ok {
		t.Error("pop on empty queue returned a frame")
	}
}

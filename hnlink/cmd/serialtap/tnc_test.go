package main

import (
	"bytes"
	"testing"
	"time"
)

// An injected frame must not go out while the host is partway through a frame.
func TestGateHoldsFrameMidFrame(t *testing.T) {
	var buf bytes.Buffer
	g := newTNCGate(&buf)
	g.mu.Lock()
	g.boundary = false
	g.lastHost = time.Now()
	g.mu.Unlock()

	frame := kissEncode([]byte("x"))
	done := make(chan error, 1)
	go func() { done <- g.writeTx(frame) }()

	time.Sleep(50 * time.Millisecond)
	g.mu.Lock()
	if buf.Len() != 0 {
		t.Fatalf("wrote %x while the host was mid-frame", buf.Bytes())
	}
	g.boundary = true
	g.mu.Unlock()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !bytes.Equal(buf.Bytes(), frame) {
		t.Errorf("wrote %x, want %x", buf.Bytes(), frame)
	}
}

// A host that has been silent long enough has lost its partial frame, so the
// injected one goes out even though the boundary state never cleared.
func TestGateReleasesAfterHostIdle(t *testing.T) {
	var buf bytes.Buffer
	g := newTNCGate(&buf)
	g.mu.Lock()
	g.boundary = false
	g.lastHost = time.Now().Add(-hostIdle - time.Second)
	g.mu.Unlock()

	if err := g.writeTx(kissEncode([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("frame held although the host has been idle")
	}
}

func TestLineTime(t *testing.T) {
	// 10 bytes at 1200 baud, 8N1: 100 bits, so 1/12 s.
	if got := lineTime(10, 1200); got != time.Second/12 {
		t.Errorf("lineTime(10, 1200) = %v, want %v", got, time.Second/12)
	}
}

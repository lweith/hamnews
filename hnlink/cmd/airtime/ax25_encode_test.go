package main

import "testing"

// A callsign that won't fit the six-character field is refused, not cut short.
func TestEncCallRefusesUnencodable(t *testing.T) {
	for _, c := range []string{"TOOLONG1", "KN6FLN-16", "KN6FLN-x", ""} {
		if _, ok := encCall(c, false, false); ok {
			t.Errorf("encCall(%q) encoded; want refusal", c)
		}
	}
	if b := build(&Frame{Dst: "APRS", Src: "TOOLONG1", Type: "UI", Cmd: -1, PID: 0xF0}); b != nil {
		t.Errorf("build with a long source returned %d bytes; want nil", len(b))
	}
}

func TestIsLoopback(t *testing.T) {
	for host, want := range map[string]bool{"127.0.0.1": true, "::1": true, "0.0.0.0": false, "::": false, "10.99.0.4": false, "localhost": false} {
		if got := isLoopback(host); got != want {
			t.Errorf("isLoopback(%q) = %v; want %v", host, got, want)
		}
	}
}

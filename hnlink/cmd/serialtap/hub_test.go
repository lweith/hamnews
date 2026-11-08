package main

import "testing"

// frame builds an AX.25 frame from src to dst with a short payload.
func frame(dst string, dssid int, src string, sssid int) []byte {
	return append(append(ax25(dst, dssid, false), ax25(src, sssid, true)...), 0x03, 0xF0, 'h', 'i')
}

func TestParseFilter(t *testing.T) {
	for _, tc := range []struct {
		specs   []string
		monitor bool
		bad     bool
	}{
		{nil, true, false},
		{[]string{"*"}, true, false},
		{[]string{"KN6FLN-5"}, false, false},
		{[]string{"kn6fln-*", "W8HHF-10"}, false, false},
		{[]string{"KN6FLN"}, false, true},   // no SSID
		{[]string{"KN6FLN-?"}, false, true}, // unsupported wildcard
		{[]string{"-*"}, false, true},       // no callsign
	} {
		f, err := parseFilter(tc.specs)
		if tc.bad {
			if err == nil {
				t.Errorf("%v: accepted, want error", tc.specs)
			}
			continue
		}
		if err != nil {
			t.Errorf("%v: %v", tc.specs, err)
			continue
		}
		if f.monitor() != tc.monitor {
			t.Errorf("%v: monitor = %v, want %v", tc.specs, f.monitor(), tc.monitor)
		}
	}
}

func TestFilterMatchesSourceOrDestination(t *testing.T) {
	f, _ := parseFilter([]string{"KN6FLN-5"})
	if !f.match(frame("W8HHF", 10, "KN6FLN", 5)) {
		t.Error("source match rejected")
	}
	if !f.match(frame("KN6FLN", 5, "W8HHF", 10)) {
		t.Error("destination match rejected")
	}
	if f.match(frame("W8HHF", 10, "N8ZAK", 0)) {
		t.Error("unrelated frame accepted")
	}
	if f.match(frame("W8HHF", 10, "KN6FLN", 6)) {
		t.Error("wrong SSID accepted")
	}
}

func TestWildcardSSID(t *testing.T) {
	f, _ := parseFilter([]string{"KN6FLN-*"})
	for _, ssid := range []int{0, 5, 11} {
		if !f.match(frame("W8HHF", 10, "KN6FLN", ssid)) {
			t.Errorf("KN6FLN-%d rejected by KN6FLN-*", ssid)
		}
	}
	if f.match(frame("W8HHF", 10, "KN6FLX", 0)) {
		t.Error("different callsign accepted")
	}
}

func TestMonitorGetsEverything(t *testing.T) {
	mon := newClient("mon", filter{all: true}, false)
	h := newHub(mon)
	h.deliver(frame("W8HHF", 10, "KN6FLN", 5))
	h.deliver(frame("APRS", 0, "N8ZAK", 0))
	if got := len(mon.queue); got != 2 {
		t.Errorf("monitor queued %d, want 2", got)
	}
}

func TestFilteredClientOnlySeesItsTraffic(t *testing.T) {
	f, _ := parseFilter([]string{"KN6FLN-5"})
	st := newClient("winlink", f, true)
	h := newHub(st)
	h.deliver(frame("W8HHF", 10, "KN6FLN", 5))
	h.deliver(frame("APRS", 0, "N8ZAK", 0))
	if got := len(st.queue); got != 1 {
		t.Errorf("station client queued %d, want 1", got)
	}
}

// A slow monitor's full queue drops its own frames and nothing else.
func TestSlowMonitorDropsOnlyForItself(t *testing.T) {
	slow := newClient("slow", filter{all: true}, false)
	slow.queue = make(chan []byte, 2) // a small queue, to force drops
	fast := newClient("fast", filter{all: true}, false)
	h := newHub(slow, fast)
	for i := 0; i < 10; i++ {
		h.deliver(frame("W8HHF", 10, "KN6FLN", 5))
	}
	if d := slow.drops.Load(); d != 8 {
		t.Errorf("slow monitor drops = %d, want 8", d)
	}
	if d := fast.drops.Load(); d != 0 {
		t.Errorf("fast client drops = %d, want 0", d)
	}
	if n := len(fast.queue); n != 10 {
		t.Errorf("fast client queued %d, want 10", n)
	}
}

// A station queue is deep: a burst that drops monitor frames doesn't drop
// station frames.
func TestStationQueueIsDeeper(t *testing.T) {
	f, _ := parseFilter([]string{"KN6FLN-5"})
	st := newClient("winlink", f, true)
	mon := newClient("mon", filter{all: true}, false)
	h := newHub(st, mon)
	burst := monitorDepth + 100
	for i := 0; i < burst; i++ {
		h.deliver(frame("W8HHF", 10, "KN6FLN", 5))
	}
	if d := mon.drops.Load(); d != 100 {
		t.Errorf("monitor drops = %d, want 100", d)
	}
	if d := st.drops.Load(); d != 0 {
		t.Errorf("station dropped %d frames in a burst it can hold", d)
	}
	if cap(st.queue) <= cap(mon.queue) {
		t.Errorf("station queue cap %d not deeper than monitor %d", cap(st.queue), cap(mon.queue))
	}
}

func TestKISSEncodeRoundTrip(t *testing.T) {
	payload := []byte{fend, fesc, 0x41, 0x42}
	fs, errs := decodeAll(kissEncode(payload))
	if errs != 0 || len(fs) != 1 || string(fs[0].Data) != string(payload) {
		t.Errorf("round trip: frames %v errors %d", fs, errs)
	}
}

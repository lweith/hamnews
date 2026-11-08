package main

import (
	"strings"
	"testing"
)

// ax25 encodes a call-SSID as the 7-byte address field, last if end is set.
func ax25(call string, ssid int, end bool) []byte {
	b := make([]byte, 7)
	for i := 0; i < 6; i++ {
		ch := byte(' ')
		if i < len(call) {
			ch = call[i]
		}
		b[i] = ch << 1
	}
	b[6] = 0x60 | byte(ssid&0x0F)<<1
	if end {
		b[6] |= 1
	}
	return b
}

// kissWrap makes one KISS data frame for an AX.25 payload, escaping it.
func kissWrap(payload []byte) []byte {
	out := []byte{fend, 0x00}
	for _, x := range payload {
		switch x {
		case fend:
			out = append(out, fesc, tfend)
		case fesc:
			out = append(out, fesc, tfesc)
		default:
			out = append(out, x)
		}
	}
	return append(out, fend)
}

func decodeAll(b []byte) ([]kissFrame, int) {
	var d kissDecoder
	var fs []kissFrame
	for _, x := range b {
		if f, ok := d.feed(x); ok {
			fs = append(fs, f)
		}
	}
	return fs, d.Errors
}

func TestKISSRoundTripWithEscapes(t *testing.T) {
	// the payload contains both special bytes, so it exercises escaping
	payload := append(append(ax25("KN6FLN", 5, false), ax25("W8HHF", 10, true)...), 0x03, fend, fesc, 0xF0, 'h', 'i')
	fs, errs := decodeAll(kissWrap(payload))
	if len(fs) != 1 || errs != 0 {
		t.Fatalf("got %d frames, %d errors; want 1, 0", len(fs), errs)
	}
	if string(fs[0].Data) != string(payload) {
		t.Errorf("payload mismatch:\n got %x\nwant %x", fs[0].Data, payload)
	}
	if fs[0].Cmd != 0 || fs[0].Port != 0 {
		t.Errorf("port/cmd = %d/%d; want 0/0", fs[0].Port, fs[0].Cmd)
	}
}

func TestKISSBackToBackAndTNCCommand(t *testing.T) {
	data := kissWrap([]byte("one"))
	// a TXDELAY setting (cmd 1) between two data frames, and a stray FEND run
	data = append(data, fend, fend)
	data = append(data, fend, 0x01, 30, fend) // cmd 1, value 30
	data = append(data, kissWrap([]byte("two"))...)
	fs, _ := decodeAll(data)
	if len(fs) != 3 {
		t.Fatalf("got %d frames; want 3", len(fs))
	}
	if fs[1].Cmd != 1 || fs[1].Data[0] != 30 {
		t.Errorf("middle frame = cmd %d data %x; want cmd 1 data 1e", fs[1].Cmd, fs[1].Data)
	}
}

func TestKISSBadEscapeCounted(t *testing.T) {
	fs, errs := decodeAll([]byte{fend, 0x00, fesc, 'x', fend})
	if errs != 1 {
		t.Errorf("errors = %d; want 1", errs)
	}
	if len(fs) != 1 {
		t.Errorf("frames = %d; want 1 (the bad escape keeps its byte)", len(fs))
	}
}

func TestKISSOverlongDropped(t *testing.T) {
	junk := make([]byte, maxFrame+10)
	for i := range junk {
		junk[i] = 'A'
	}
	fs, errs := decodeAll(append(junk, fend, 0x00, 'o', 'k', fend))
	if errs == 0 {
		t.Error("overlong run not counted as an error")
	}
	if len(fs) != 1 || string(fs[0].Data) != "ok" {
		t.Errorf("frames after the junk = %v; want only the good one", fs)
	}
}

func TestAX25Addrs(t *testing.T) {
	f := append(ax25("W8HHF", 10, false), ax25("KN6FLN", 5, true)...)
	src, dst, ok := ax25Addrs(f)
	if !ok || src != "KN6FLN-5" || dst != "W8HHF-10" {
		t.Errorf("addrs = %q %q %v; want KN6FLN-5 W8HHF-10 true", src, dst, ok)
	}
	if _, _, ok := ax25Addrs(f[:10]); ok {
		t.Error("short frame accepted")
	}
}

func TestScoreAndPickRate(t *testing.T) {
	var clean []byte
	for i := 0; i < 5; i++ {
		frame := append(append(ax25("W8HHF", 10, false), ax25("KN6FLN", 5, true)...), 'a', 'b', 'c', 'd', 'e')
		clean = append(clean, kissWrap(frame)...)
	}
	// the wrong rate: the same bytes read as noise
	noise := make([]byte, len(clean))
	for i := range noise {
		noise[i] = clean[i] ^ 0x5A
	}
	good := scoreStream(1200, clean)
	bad := scoreStream(9600, noise)
	if good.Frames != 5 {
		t.Errorf("clean stream frames = %d; want 5", good.Frames)
	}
	best, ok := bestRate([]rateScore{bad, good})
	if !ok || best.Rate != 1200 {
		t.Errorf("best = %+v ok=%v; want 1200", best, ok)
	}
	if _, ok := bestRate([]rateScore{bad}); ok {
		t.Error("noise alone picked a rate")
	}
}

func TestRenderMetrics(t *testing.T) {
	st := newStats()
	st.addBytes("host", 30)
	frame := append(append(ax25("W8HHF", 10, false), ax25("KN6FLN", 5, true)...), 0x03, 0xF0)
	st.addFrame("host", kissFrame{Cmd: 0, Data: frame})
	st.addFrame("tnc", kissFrame{Cmd: 1, Data: []byte{30}})
	out := st.render()
	for _, want := range []string{
		`serialtap_bytes_total{dir="host"} 30`,
		`serialtap_frames_total{dir="host",cmd="0"} 1`,
		`serialtap_frames_total{dir="tnc",cmd="1"} 1`,
		`serialtap_src_frames_total{dir="host",call="KN6FLN-5"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q\n%s", want, out)
		}
	}
}

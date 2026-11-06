package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Captured from direwolf 1.8 by test/echo-probe.sh: AGW 'K' data (leading
// KISS byte + frame) for a received UI frame, and the 'T' text for the same
// frame transmitted.
var (
	capK = []byte("\x00\xa0\xa4\x9e\x84\x8a@`\xa8\x8a\xa6\xa8@@o\x03\xf0echo-probe 1793979385")
	capT = []byte(" 1:Fm TEST-7 To PROBE <UI pid=F0 Len=21 PF=0 >[10:36:17]\recho-probe 1793979375\r\x00")
)

func TestParseRaw(t *testing.T) {
	f, err := ParseAX25(capK[1:])
	if err != nil {
		t.Fatal(err)
	}
	if f.Src != "TEST-7" || f.Dst != "PROBE" || f.Type != "UI" || f.PID != 0xF0 || f.Info != 21 || f.Bytes != 37 {
		t.Fatalf("%+v", f)
	}
}

func TestParseMonitor(t *testing.T) {
	f, err := ParseMonitor(0, capT)
	if err != nil {
		t.Fatal(err)
	}
	if !f.TX || f.Src != "TEST-7" || f.Dst != "PROBE" || f.Type != "UI" || f.PID != 0xF0 || f.Info != 21 || f.Bytes != 37 {
		t.Fatalf("%+v", f)
	}
	// Rebuilt frame equals the received one except the assumed C bit.
	rx := append([]byte(nil), capK[1:]...)
	rx[6] |= 0x80
	if f.Raw == nil {
		t.Fatal("no rebuilt frame")
	}
	want := append(rx[:len(rx)-len("1793979385")], "1793979375"...)
	if !bytes.Equal(f.Raw, want) {
		t.Fatalf("rebuilt\n % x\nwant\n % x", f.Raw, want)
	}
	// Digipeated, connected-mode.
	g, err := ParseMonitor(1, []byte(" 2:Fm KN6FLN-1 To W6ABC-10 Via WIDE1-1*,WIDE2-1 <I R1 S3 pid=F0 Len=100 >\rx"))
	if err != nil {
		t.Fatal(err)
	}
	if g.Type != "I" || len(g.Via) != 2 || g.Bytes != 14+14+2+100 || g.Raw != nil {
		t.Fatalf("%+v", g)
	}
}

func TestStatsAndMetrics(t *testing.T) {
	s := NewStats(1200, 300*time.Millisecond)
	s.started = time.Now().Add(-time.Hour)
	rx, _ := ParseAX25(capK[1:])
	tx, _ := ParseMonitor(0, capT)
	now := time.Now()
	s.Add(now, rx)
	s.Add(now.Add(100*time.Millisecond), rx) // same burst: no second key-up
	s.Add(now, tx)
	sn := s.Snapshot(now.Add(time.Second))
	if len(sn.Rows) != 2 {
		t.Fatalf("rows %+v", sn.Rows)
	}
	var r Row
	for _, x := range sn.Rows {
		if x.Dir == "rx" {
			r = x
		}
	}
	one := float64(37+4) * 8 * 1.03 / 1200
	if r.Frames != 2 || r.Bytes != 74 || abs(r.Airtime-(2*one+0.3)) > 1e-9 {
		t.Fatalf("rx row %+v (want airtime %g)", r, 2*one+0.3)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	m := rec.Body.String()
	for _, want := range []string{
		`airtime_frames_total{port="0",call="TEST-7",dir="rx"} 2`,
		`airtime_frames_total{port="0",call="TEST-7",dir="tx"} 1`,
		`airtime_frames_by_type_total{port="0",call="TEST-7",dir="rx",type="UI"} 2`,
		`airtime_channel_busy_ratio{port="0",window="1m"}`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %q:\n%s", want, m)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

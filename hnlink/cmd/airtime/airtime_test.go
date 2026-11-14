package main

import (
	"bytes"
	"encoding/hex"
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

// Heard by direwolf-hri200 on W8HHF/R, 2026-11-07/08 (airtime-hri200's trace).
var (
	// KE8WTG-9>T1ZZLZ,WIDE1-1,WIDE2-1: Mic-E, Yaesu FTM-300D, N 41 00, W 083 00.
	hMicE = unhex("a862b4b498b460968a70aea88ef2ae92888a624062ae92888a64406303f0606f581c70536b765c602236217d68656c6c6f5f310d")
	// KN6FLN>KN6FLN-11 on a SABME link: I command N(S)=2 N(R)=6 "m19\r".
	hI128 = unhex("969c6c8c989cf6969c6c8c989c61040cf06d31390d")
	// KN6FLN>KN6FLN-11: RR response N(R)=8 F=0.
	hRR128 = unhex("969c6c8c989c76969c6c8c989ce10110")
	// KN6FLN>KN6FLN-11: UA F=1.
	hUA = unhex("969c6c8c989c76969c6c8c989ce173")
)

func unhex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

func TestParseRaw(t *testing.T) {
	f, err := ParseAX25(capK[1:], nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Src != "TEST-7" || f.Dst != "PROBE" || f.Type != "UI" || f.PID != 0xF0 || f.Info != 21 || f.Bytes != 37 {
		t.Fatalf("%+v", f)
	}
}

func TestParseMod128(t *testing.T) {
	// Without knowing the link, the frames themselves say modulo 128.
	f, err := ParseAX25(hI128, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != "I" || !f.Mod128 || f.NS != 2 || f.NR != 6 || f.PF || f.Cmd != 1 || f.PID != 0xF0 || string(f.Data) != "m19\r" {
		t.Fatalf("I: %+v", f)
	}
	f, _ = ParseAX25(hRR128, nil)
	if f.Type != "RR" || !f.Mod128 || f.NR != 8 || f.PF || f.Cmd != -1 || f.Info != 0 {
		t.Fatalf("RR: %+v", f)
	}
	f, _ = ParseAX25(hUA, nil)
	if f.Type != "UA" || !f.PF || f.Cmd != -1 {
		t.Fatalf("UA: %+v", f)
	}
	// A known modulo-8 link reads the same I frame's control as one byte.
	f, _ = ParseAX25(hI128, func(a, b string) (bool, bool) { return false, true })
	if f.Mod128 || f.PID != 0x0c {
		t.Fatalf("as modulo 8: %+v", f)
	}
}

func TestParseMonitor(t *testing.T) {
	f, err := ParseMonitor(0, capT, nil)
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
	// Info truncated: counted, not rebuilt.
	g, err := ParseMonitor(1, []byte(" 2:Fm KN6FLN-1 To W6ABC-10 Via WIDE1-1*,WIDE2-1 <I S3 R1 pid=F0 Len=100 P=0 >[01:02:03]\rx"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if g.Type != "I" || len(g.Via) != 2 || g.Bytes != 14+14+2+100 || g.Raw != nil || g.NS != 3 || g.NR != 1 {
		t.Fatalf("%+v", g)
	}
}

// The text direwolf would send for the frames heard, rebuilt to the same bytes.
func TestRebuildConnected(t *testing.T) {
	m128 := func(a, b string) (bool, bool) { return true, true }
	for _, c := range []struct {
		text string
		want []byte
	}{
		{" 1:Fm KN6FLN To KN6FLN-11 <I S2 R6 pid=F0 Len=4 P=0 >[22:31:44]\rm19\r\r\x00", hI128},
		{" 1:Fm KN6FLN To KN6FLN-11 <RR R8 F=0 >[22:31:50]\r\x00", hRR128},
		{" 1:Fm KN6FLN To KN6FLN-11 <UA F=1 >[22:31:59]\r\x00", hUA},
	} {
		f, err := ParseMonitor(0, []byte(c.text), m128)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(f.Raw, c.want) || f.Bytes != len(c.want) {
			t.Errorf("%q:\n got % x\nwant % x", c.text, f.Raw, c.want)
		}
	}
}

func TestAPRSMicE(t *testing.T) {
	f, _ := ParseAX25(hMicE, nil)
	if c := classify(f); c != "aprs" {
		t.Fatalf("class %q", c)
	}
	a := decodeAPRS(f, nil)
	if a.Kind != "mic-e" || a.Device != "Yaesu FTM-300D" || a.Comment != "hello" || a.Symbol != `\v` {
		t.Fatalf("%+v", a)
	}
	if a.Lat == nil || *a.Lat != 41 || *a.Lon != -83 {
		t.Fatalf("position %v %v", *a.Lat, *a.Lon)
	}
}

func TestAPRSKinds(t *testing.T) {
	for info, kind := range map[string]string{
		"!4903.50N/07201.75W-Test":              "position",
		"@092345z4903.50N/07201.75W_090":        "weather",
		":KN6FLN   :hello{1":                    "message",
		":KN6FLN   :ack1":                       "ack",
		":BLN1     :news":                       "bulletin",
		">on the air":                           "status",
		";LEADER   *092345z4903.50N/07201.75W>": "object",
		"T#005,199,000,255,073,123,01100001":    "telemetry",
	} {
		f := &Frame{Type: "UI", PID: 0xF0, Dst: "APDW17", Data: []byte(info)}
		if !isAPRS(f) {
			t.Errorf("%q not APRS", info)
			continue
		}
		if a := decodeAPRS(f, nil); a.Kind != kind {
			t.Errorf("%q: %s, want %s", info, a.Kind, kind)
		}
	}
	p := decodeAPRS(&Frame{Type: "UI", PID: 0xF0, Dst: "APDW17", Data: []byte("!4903.50N/07201.75W-Test")}, nil)
	if *p.Lat != 49.05833 || *p.Lon != -72.02917 || p.Comment != "Test" {
		t.Errorf("position %+v %v %v", p, *p.Lat, *p.Lon)
	}
	// hnlink and plain beacons are not APRS.
	if c := classify(&Frame{Type: "UI", PID: 0xF0, Dst: "HNLINK", Data: []byte(">hri200")}); c != "hnlink" {
		t.Errorf("hnlink: %s", c)
	}
	if c := classify(&Frame{Type: "UI", PID: 0xF0, Dst: "TEST", Data: []byte("hello")}); c != "beacon" {
		t.Errorf("beacon: %s", c)
	}
}

func TestTocalls(t *testing.T) {
	tc, err := loadTocalls("testdata/tocalls.txt")
	if err != nil {
		t.Fatal(err)
	}
	for dst, want := range map[string]string{"APDW17": "Dire Wolf, WB2OSZ", "APAW01": "AGWPE", "APK005": "Kenwood TH-D75", "APZZZZ": ""} {
		if got := tc.lookup(dst); got != want {
			t.Errorf("%s: %q, want %q", dst, got, want)
		}
	}
}

// A session as tmra saw one: its own frames (sent) and the far end's (heard).
func TestSessions(t *testing.T) {
	s := NewStats(1200, 300*time.Millisecond)
	t0 := time.Now()
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	mon := func(ms int, text string) {
		f, err := ParseMonitor(0, []byte(text), s.Mod128(0))
		if err != nil {
			t.Fatal(err)
		}
		s.Add(at(ms), f)
	}
	heard := func(ms int, raw []byte) {
		f, err := ParseAX25(raw, s.Mod128(0))
		if err != nil {
			t.Fatal(err)
		}
		s.Add(at(ms), f)
	}
	sabme := build(&Frame{Dst: "KN6FLN-11", Src: "KN6FLN", Type: "SABME", Cmd: 1, PF: true, PID: -1})
	heard(0, sabme)
	mon(900, " 1:Fm KN6FLN-11 To KN6FLN <UA F=1 >[00:00:00]\r\x00")
	mon(2000, " 1:Fm KN6FLN-11 To KN6FLN <I S0 R0 pid=F0 Len=5 P=0 >[00:00:00]\rhello\r\x00")
	mon(2100, " 1:Fm KN6FLN-11 To KN6FLN <I S1 R0 pid=F0 Len=5 P=0 >[00:00:00]\rworld\r\x00")
	mon(12000, " 1:Fm KN6FLN-11 To KN6FLN <RR R0 P=1 >[00:00:00]\r\x00") // T1: poll
	mon(13000, " 1:Fm KN6FLN-11 To KN6FLN <I S1 R0 pid=F0 Len=5 P=0 >[00:00:00]\rworld\r\x00")
	heard(14000, build(&Frame{Dst: "KN6FLN-11", Src: "KN6FLN", Type: "RR", Cmd: -1, NR: 2, Mod128: true, PID: -1}))
	heard(20000, build(&Frame{Dst: "KN6FLN-11", Src: "KN6FLN", Type: "I", Cmd: 1, NS: 0, NR: 2, Mod128: true, PID: 0xF0, Data: []byte("m19\r")}))
	sn := s.Snapshot(at(21000))
	if len(sn.Sessions.Open) != 1 {
		t.Fatalf("open %+v", sn.Sessions)
	}
	o := sn.Sessions.Open[0]
	if o.From != "KN6FLN" || o.To != "KN6FLN-11" || o.Modulo != 128 || o.Opening != "SABME" || o.Setup != 0.9 {
		t.Fatalf("session %+v", o)
	}
	door, user := o.Dirs[1], o.Dirs[0]
	if door.IFrames != 3 || door.NewIFrames != 2 || door.NewBytes != 10 || door.Repeats != 1 || door.Polls != 1 {
		t.Errorf("door's direction %+v", door)
	}
	if user.NewBytes != 4 || user.RR != 1 {
		t.Errorf("user's direction %+v", user)
	}
	heard(30000, build(&Frame{Dst: "KN6FLN-11", Src: "KN6FLN", Type: "DISC", Cmd: 1, PF: true, PID: -1}))
	mon(30500, " 1:Fm KN6FLN-11 To KN6FLN <UA F=1 >[00:00:00]\r\x00")
	sn = s.Snapshot(at(31000))
	if len(sn.Sessions.Open) != 0 || len(sn.Sessions.Closed) != 1 || sn.Sessions.Closed[0].Closing != "DISC" || sn.Sessions.Closed[0].Duration != 30 {
		t.Fatalf("after DISC %+v", sn.Sessions)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	m := rec.Body.String()
	for _, want := range []string{
		`airtime_sessions_open{port="0"} 0`,
		`airtime_sessions_total{port="0",from="KN6FLN",to="KN6FLN-11"} 1`,
		`airtime_sessions_closed_total{how="DISC"} 1`,
		`airtime_session_seconds_total{port="0",from="KN6FLN",to="KN6FLN-11"} 30`,
		`airtime_link_info_bytes_total{port="0",src="KN6FLN-11",dst="KN6FLN",kind="new"} 10`,
		`airtime_link_info_bytes_total{port="0",src="KN6FLN-11",dst="KN6FLN",kind="repeat"} 5`,
		`airtime_link_events_total{port="0",src="KN6FLN-11",dst="KN6FLN",event="poll"} 1`,
		`airtime_class_frames_total{port="0",class="connected",dir="tx"} 6`,
		`airtime_class_frames_total{port="0",class="aprs",dir="rx"} 0`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
}

func TestStatsAndMetrics(t *testing.T) {
	s := NewStats(1200, 300*time.Millisecond)
	s.started = time.Now().Add(-time.Hour)
	rx, _ := ParseAX25(capK[1:], nil)
	tx, _ := ParseMonitor(0, capT, nil)
	mice, _ := ParseAX25(hMicE, nil)
	now := time.Now()
	s.Add(now, rx)
	s.Add(now.Add(100*time.Millisecond), rx) // same burst: no second key-up
	s.Add(now, tx)
	s.Add(now, mice)
	sn := s.Snapshot(now.Add(time.Second))
	if len(sn.Rows) != 3 {
		t.Fatalf("rows %+v", sn.Rows)
	}
	var r Row
	for _, x := range sn.Rows {
		if x.Dir == "rx" && x.Call == "TEST-7" {
			r = x
		}
	}
	one := float64(37+4) * 8 * 1.03 / 1200
	if r.Frames != 2 || r.Bytes != 74 || abs(r.Airtime-(2*one+0.3)) > 1e-9 {
		t.Fatalf("rx row %+v (want airtime %g)", r, 2*one+0.3)
	}
	if len(sn.APRS) != 1 || sn.APRS[0].Call != "KE8WTG-9" || sn.APRS[0].Device != "Yaesu FTM-300D" {
		t.Fatalf("APRS %+v", sn.APRS)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	m := rec.Body.String()
	for _, want := range []string{
		`airtime_frames_total{port="0",call="TEST-7",dir="rx"} 2`,
		`airtime_frames_total{port="0",call="TEST-7",dir="tx"} 1`,
		`airtime_frames_by_type_total{port="0",call="TEST-7",dir="rx",type="UI"} 2`,
		`airtime_channel_busy_ratio{port="0",window="1m"}`,
		`airtime_class_frames_total{port="0",class="beacon",dir="rx"} 2`,
		`airtime_class_frames_total{port="0",class="aprs",dir="rx"} 1`,
		`airtime_aprs_packets_total{port="0",kind="mic-e",dir="rx"} 1`,
		`airtime_aprs_stations{port="0",window="15m"} 1`,
		`airtime_aprs_devices{port="0",device="Yaesu FTM-300D"} 1`,
		`airtime_aprs_digipeated_total{port="0"} 0`,
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

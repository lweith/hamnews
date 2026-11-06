package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type key struct {
	Port int
	Call string
	TX   bool
}

type Row struct {
	Port    int              `json:"port"`
	Call    string           `json:"call"`
	Dir     string           `json:"dir"` // "rx" heard, "tx" this station
	Frames  int64            `json:"frames"`
	Bytes   int64            `json:"bytes"`
	Airtime float64          `json:"airtime_s"`
	First   time.Time        `json:"first"`
	Last    time.Time        `json:"last"`
	Types   map[string]int64 `json:"types"`
	PIDs    map[string]int64 `json:"pids"`
	Dests   map[string]int64 `json:"dests"`
	Via     int64            `json:"via"` // frames that went through a digipeater
	end     time.Time        // when this source's last frame finished (burst detection)
}

type event struct {
	t   time.Time
	air float64
}

type Channel struct {
	Port  int                `json:"port"`
	Name  string             `json:"name"`
	Busy  map[string]float64 `json:"busy"` // fraction of the window with a carrier, estimated
	Calls int                `json:"calls"`
}

type Stats struct {
	mu      sync.Mutex
	bitrate float64
	txdelay time.Duration
	rows    map[key]*Row
	events  map[int][]event // per port, last 15 min
	up      bool
	names   []string
	started time.Time
}

func NewStats(bitrate float64, txdelay time.Duration) *Stats {
	return &Stats{bitrate: bitrate, txdelay: txdelay, rows: map[key]*Row{}, events: map[int][]event{}, started: time.Now()}
}

func (s *Stats) SetUp(up bool, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.up = up
	if names != nil {
		s.names = names
	}
}

// frameAir estimates seconds on the air: flags, FCS and ~3% bit stuffing.
func (s *Stats) frameAir(bytes int) float64 {
	return float64(bytes+4) * 8 * 1.03 / s.bitrate
}

func (s *Stats) Add(now time.Time, f *Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{f.Port, f.Src, f.TX}
	r := s.rows[k]
	if r == nil {
		dir := "rx"
		if f.TX {
			dir = "tx"
		}
		r = &Row{Port: f.Port, Call: f.Src, Dir: dir, First: now,
			Types: map[string]int64{}, PIDs: map[string]int64{}, Dests: map[string]int64{}}
		s.rows[k] = r
	}
	air := s.frameAir(f.Bytes)
	// A new key-up (more than half a second since this source's last frame
	// ended) costs a TXDELAY; frames in the same burst don't.
	start := now.Add(-time.Duration(air * float64(time.Second)))
	if r.end.IsZero() || start.Sub(r.end) > 500*time.Millisecond {
		air += s.txdelay.Seconds()
	}
	r.end = now
	r.Frames++
	r.Bytes += int64(f.Bytes)
	r.Airtime += air
	r.Last = now
	r.Types[f.Type]++
	if f.PID >= 0 {
		r.PIDs[pidName(f.PID)]++
	}
	r.Dests[f.Dst]++
	if len(f.Via) > 0 {
		r.Via++
	}
	ev := append(s.events[f.Port], event{now, air})
	cut := 0
	for cut < len(ev) && now.Sub(ev[cut].t) > 15*time.Minute {
		cut++
	}
	s.events[f.Port] = ev[cut:]
}

func pidName(p int) string {
	switch p {
	case 0xF0:
		return "F0 text"
	case 0xCF:
		return "CF NET/ROM"
	case 0xCC:
		return "CC IP"
	case 0xCD:
		return "CD ARP"
	case 0x08:
		return "08 segment"
	}
	return fmt.Sprintf("%02X", p)
}

var windows = []struct {
	name string
	d    time.Duration
}{{"1m", time.Minute}, {"5m", 5 * time.Minute}, {"15m", 15 * time.Minute}}

type Snapshot struct {
	Up       bool      `json:"agw_connected"`
	Since    time.Time `json:"since"`
	Bitrate  float64   `json:"bitrate"`
	Channels []Channel `json:"channels"`
	Rows     []Row     `json:"stations"`
}

func (s *Stats) Snapshot(now time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	sn := Snapshot{Up: s.up, Since: s.started, Bitrate: s.bitrate}
	calls := map[int]map[string]bool{}
	for k, r := range s.rows {
		sn.Rows = append(sn.Rows, copyRow(r))
		if calls[k.Port] == nil {
			calls[k.Port] = map[string]bool{}
		}
		calls[k.Port][k.Call] = true
	}
	ports := map[int]bool{}
	for p := range s.events {
		ports[p] = true
	}
	for p := range calls {
		ports[p] = true
	}
	for p := range ports {
		ch := Channel{Port: p, Busy: map[string]float64{}, Calls: len(calls[p])}
		if p < len(s.names) {
			ch.Name = s.names[p]
		}
		for _, w := range windows {
			var sum float64
			for _, e := range s.events[p] {
				if now.Sub(e.t) <= w.d {
					sum += e.air
				}
			}
			// Before a full window has passed, divide by the time actually watched.
			span := w.d
			if up := now.Sub(s.started); up < span {
				span = up
			}
			if span > 0 {
				ch.Busy[w.name] = min(1, sum/span.Seconds())
			}
		}
		sn.Channels = append(sn.Channels, ch)
	}
	sort.Slice(sn.Channels, func(i, j int) bool { return sn.Channels[i].Port < sn.Channels[j].Port })
	sort.Slice(sn.Rows, func(i, j int) bool {
		a, b := sn.Rows[i], sn.Rows[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Airtime > b.Airtime
	})
	return sn
}

func copyRow(r *Row) Row {
	c := *r
	c.Types, c.PIDs, c.Dests = cp(r.Types), cp(r.PIDs), cp(r.Dests)
	return c
}

func cp(m map[string]int64) map[string]int64 {
	o := make(map[string]int64, len(m))
	for k, v := range m {
		o[k] = v
	}
	return o
}

//go:embed index.html
var page []byte

func (s *Stats) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.Snapshot(time.Now()))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		s.writeMetrics(w, time.Now())
	})
	return mux
}

func lbl(s string) string { return strconv.Quote(s) }

func (s *Stats) writeMetrics(w http.ResponseWriter, now time.Time) {
	sn := s.Snapshot(now)
	var b strings.Builder
	up := 0
	if sn.Up {
		up = 1
	}
	fmt.Fprintf(&b, "# HELP airtime_agw_up 1 while connected to direwolf's AGW port.\n# TYPE airtime_agw_up gauge\nairtime_agw_up %d\n", up)
	b.WriteString("# HELP airtime_channel_busy_ratio Estimated fraction of the window the channel carried frames.\n# TYPE airtime_channel_busy_ratio gauge\n")
	for _, c := range sn.Channels {
		for _, wn := range windows {
			fmt.Fprintf(&b, "airtime_channel_busy_ratio{port=\"%d\",window=\"%s\"} %g\n", c.Port, wn.name, c.Busy[wn.name])
		}
	}
	type m struct{ name, help string }
	for _, mt := range []m{
		{"airtime_frames_total", "Frames by source call-SSID; dir=tx is this station."},
		{"airtime_bytes_total", "AX.25 bytes (without flags and FCS) by source call-SSID."},
		{"airtime_seconds_total", "Estimated seconds on the air by source call-SSID, including key-up."},
	} {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n", mt.name, mt.help, mt.name)
		for _, r := range sn.Rows {
			l := fmt.Sprintf("{port=\"%d\",call=%s,dir=\"%s\"}", r.Port, lbl(r.Call), r.Dir)
			switch mt.name {
			case "airtime_frames_total":
				fmt.Fprintf(&b, "%s%s %d\n", mt.name, l, r.Frames)
			case "airtime_bytes_total":
				fmt.Fprintf(&b, "%s%s %d\n", mt.name, l, r.Bytes)
			default:
				fmt.Fprintf(&b, "%s%s %g\n", mt.name, l, r.Airtime)
			}
		}
	}
	b.WriteString("# HELP airtime_frames_by_type_total Frames by source call-SSID and AX.25 frame type.\n# TYPE airtime_frames_by_type_total counter\n")
	for _, r := range sn.Rows {
		types := make([]string, 0, len(r.Types))
		for t := range r.Types {
			types = append(types, t)
		}
		sort.Strings(types)
		for _, t := range types {
			fmt.Fprintf(&b, "airtime_frames_by_type_total{port=\"%d\",call=%s,dir=\"%s\",type=%s} %d\n", r.Port, lbl(r.Call), r.Dir, lbl(t), r.Types[t])
		}
	}
	w.Write([]byte(b.String()))
}

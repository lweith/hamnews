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
	Classes map[string]int64 `json:"classes"`
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

// Traffic classes: what a frame is for (classify). Served always, each from 0.
var classes = []string{"aprs", "connected", "netrom", "ip", "hnlink", "beacon", "other"}

// classify names what a frame carries:
//
//	aprs       APRS (isAPRS)
//	hnlink     hamnews' link protocol (UI to HNLINK)
//	netrom     NET/ROM (PID CF): NODES broadcasts, and its I frames between nodes
//	ip         IP or ARP (PID CC, CD)
//	connected  connected-mode links: I, S, SABM(E), UA, DISC, DM, FRMR, XID, TEST
//	beacon     other UI frames: IDs, beacons, unproto chat
//	other      anything else (an unknown U frame)
func classify(f *Frame) string {
	switch {
	case isAPRS(f):
		return "aprs"
	case f.Type == "UI" && strings.HasPrefix(f.Dst, "HNLINK"):
		return "hnlink"
	case f.PID == 0xCF:
		return "netrom"
	case f.PID == 0xCC || f.PID == 0xCD:
		return "ip"
	case linkTypes[f.Type] || f.Type == "XID" || f.Type == "TEST":
		return "connected"
	case f.Type == "UI":
		return "beacon"
	}
	return "other"
}

type classKey struct {
	Port  int
	Class string
	TX    bool
}

type classTotals struct {
	Frames, Bytes int64
	Airtime       float64
}

// Station is an APRS station heard (or this station's own APRS).
type Station struct {
	Port       int              `json:"port"`
	Call       string           `json:"call"`
	Dir        string           `json:"dir"`
	First      time.Time        `json:"first"`
	Last       time.Time        `json:"last"`
	Packets    int64            `json:"packets"`
	Kinds      map[string]int64 `json:"kinds"`
	Digipeated int64            `json:"digipeated"` // heard through a digipeater
	Path       string           `json:"path"`       // the last packet's
	Dest       string           `json:"dest"`
	Last1      APRS             `json:"last_packet"`
	Device     string           `json:"device,omitempty"`
	Lat        *float64         `json:"lat,omitempty"` // last position reported
	Lon        *float64         `json:"lon,omitempty"`
	Symbol     string           `json:"symbol,omitempty"`
}

type aprsKey struct {
	Port int
	Kind string
	TX   bool
}

type Stats struct {
	mu       sync.Mutex
	bitrate  float64
	txdelay  time.Duration
	rows     map[key]*Row
	events   map[int][]event // per port, last 15 min
	up       bool
	names    []string
	started  time.Time
	classes  map[classKey]*classTotals
	aprs     map[aprsKey]int64
	aprsDigi map[int]int64 // per port, APRS heard through a digipeater
	stations map[key]*Station
	tocalls  *tocalls
	sessions *Sessions
}

func NewStats(bitrate float64, txdelay time.Duration) *Stats {
	return &Stats{bitrate: bitrate, txdelay: txdelay, rows: map[key]*Row{}, events: map[int][]event{}, started: time.Now(),
		classes: map[classKey]*classTotals{}, aprs: map[aprsKey]int64{}, aprsDigi: map[int]int64{},
		stations: map[key]*Station{}, sessions: newSessions()}
}

func (s *Stats) SetUp(up bool, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.up = up
	if names != nil {
		s.names = names
	}
}

// Mod128 is for ParseAX25 and ParseMonitor: what's known of a link's modulo.
func (s *Stats) Mod128(port int) func(a, b string) (bool, bool) {
	return func(a, b string) (bool, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.sessions.Mod128(port)(a, b)
	}
}

// frameAir estimates seconds on the air: flags, FCS and ~3% bit stuffing.
func (s *Stats) frameAir(bytes int) float64 {
	return float64(bytes+4) * 8 * 1.03 / s.bitrate
}

func (s *Stats) Add(now time.Time, f *Frame) {
	if f.Class == "" {
		f.Class = classify(f)
	}
	if f.Class == "aprs" && f.APRS == nil {
		f.APRS = decodeAPRS(f, s.tocalls)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := key{f.Port, f.Src, f.TX}
	r := s.rows[k]
	if r == nil {
		r = &Row{Port: f.Port, Call: f.Src, Dir: dirName(f.TX), First: now,
			Types: map[string]int64{}, PIDs: map[string]int64{}, Dests: map[string]int64{}, Classes: map[string]int64{}}
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
	r.Classes[f.Class]++
	if f.PID >= 0 {
		r.PIDs[pidName(f.PID)]++
	}
	r.Dests[f.Dst]++
	if len(f.Via) > 0 {
		r.Via++
	}
	ck := classKey{f.Port, f.Class, f.TX}
	c := s.classes[ck]
	if c == nil {
		c = &classTotals{}
		s.classes[ck] = c
	}
	c.Frames++
	c.Bytes += int64(f.Bytes)
	c.Airtime += air
	if f.APRS != nil {
		s.addAPRS(now, f)
	}
	if f.Class == "connected" || (f.Class == "netrom" && f.Type != "UI") {
		s.sessions.Frame(f, now)
	}
	s.sessions.Expire(now)
	ev := append(s.events[f.Port], event{now, air})
	cut := 0
	for cut < len(ev) && now.Sub(ev[cut].t) > 15*time.Minute {
		cut++
	}
	s.events[f.Port] = ev[cut:]
}

func dirName(tx bool) string {
	if tx {
		return "tx"
	}
	return "rx"
}

func repeated(via []string) bool {
	for _, v := range via {
		if strings.HasSuffix(v, "*") {
			return true
		}
	}
	return false
}

func (s *Stats) addAPRS(now time.Time, f *Frame) {
	a := f.APRS
	s.aprs[aprsKey{f.Port, a.Kind, f.TX}]++
	k := key{f.Port, f.Src, f.TX}
	st := s.stations[k]
	if st == nil {
		st = &Station{Port: f.Port, Call: f.Src, Dir: dirName(f.TX), First: now, Kinds: map[string]int64{}}
		s.stations[k] = st
	}
	st.Last = now
	st.Packets++
	st.Kinds[a.Kind]++
	if !f.TX && repeated(f.Via) {
		st.Digipeated++
		s.aprsDigi[f.Port]++
	}
	st.Path, st.Dest, st.Last1 = strings.Join(f.Via, ","), f.Dst, *a
	if a.Device != "" {
		st.Device = a.Device
	}
	if a.Lat != nil && a.Kind != "object" && a.Kind != "item" {
		st.Lat, st.Lon, st.Symbol = a.Lat, a.Lon, a.Symbol
	}
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

// APRS station-count windows.
var stationWindows = []struct {
	name string
	d    time.Duration
}{{"15m", 15 * time.Minute}, {"1h", time.Hour}, {"24h", 24 * time.Hour}}

type ClassRow struct {
	Port    int     `json:"port"`
	Class   string  `json:"class"`
	Dir     string  `json:"dir"`
	Frames  int64   `json:"frames"`
	Bytes   int64   `json:"bytes"`
	Airtime float64 `json:"airtime_s"`
}

type Snapshot struct {
	Up       bool             `json:"agw_connected"`
	Since    time.Time        `json:"since"`
	Bitrate  float64          `json:"bitrate"`
	Channels []Channel        `json:"channels"`
	Classes  []ClassRow       `json:"classes"`
	Rows     []Row            `json:"stations"`
	APRS     []Station        `json:"aprs"`
	Sessions SessionsSnapshot `json:"sessions"`
}

func (s *Stats) Snapshot(now time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions.Expire(now)
	sn := Snapshot{Up: s.up, Since: s.started, Bitrate: s.bitrate, Sessions: s.sessions.snapshot(now)}
	calls := map[int]map[string]bool{}
	for k, r := range s.rows {
		sn.Rows = append(sn.Rows, copyRow(r))
		if calls[k.Port] == nil {
			calls[k.Port] = map[string]bool{}
		}
		calls[k.Port][k.Call] = true
	}
	for k, c := range s.classes {
		sn.Classes = append(sn.Classes, ClassRow{k.Port, k.Class, dirName(k.TX), c.Frames, c.Bytes, c.Airtime})
	}
	sort.Slice(sn.Classes, func(i, j int) bool {
		a, b := sn.Classes[i], sn.Classes[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Airtime > b.Airtime
	})
	for _, st := range s.stations {
		c := *st
		c.Kinds = cp(st.Kinds)
		sn.APRS = append(sn.APRS, c)
	}
	sort.Slice(sn.APRS, func(i, j int) bool { return sn.APRS[i].Last.After(sn.APRS[j].Last) })
	for _, p := range s.ports() {
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
	sort.Slice(sn.Rows, func(i, j int) bool {
		a, b := sn.Rows[i], sn.Rows[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.Airtime > b.Airtime
	})
	return sn
}

// ports: every port seen, and port 0 always, so its series exist from the start.
func (s *Stats) ports() []int {
	m := map[int]bool{0: true}
	for p := range s.events {
		m[p] = true
	}
	for k := range s.rows {
		m[k.Port] = true
	}
	var ps []int
	for p := range m {
		ps = append(ps, p)
	}
	sort.Ints(ps)
	return ps
}

func copyRow(r *Row) Row {
	c := *r
	c.Types, c.PIDs, c.Dests, c.Classes = cp(r.Types), cp(r.PIDs), cp(r.Dests), cp(r.Classes)
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

func sortedKeys[K comparable](m map[K]int64, less func(a, b K) bool) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return less(ks[i], ks[j]) })
	return ks
}

func (s *Stats) writeMetrics(w http.ResponseWriter, now time.Time) {
	sn := s.Snapshot(now)
	var b strings.Builder
	head := func(name, typ, help string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	up := 0
	if sn.Up {
		up = 1
	}
	head("airtime_agw_up", "gauge", "1 while connected to direwolf's AGW port.")
	fmt.Fprintf(&b, "airtime_agw_up %d\n", up)
	head("airtime_channel_busy_ratio", "gauge", "Estimated fraction of the window the channel carried frames.")
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
		head(mt.name, "counter", mt.help)
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
	head("airtime_frames_by_type_total", "counter", "Frames by source call-SSID and AX.25 frame type.")
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

	// Traffic classes: every class and direction for every port, from 0.
	cls := map[classKey]ClassRow{}
	for _, c := range sn.Classes {
		cls[classKey{c.Port, c.Class, c.Dir == "tx"}] = c
	}
	for _, mt := range []m{
		{"airtime_class_frames_total", "Frames by traffic class (aprs, connected, netrom, ip, hnlink, beacon, other) and direction."},
		{"airtime_class_bytes_total", "AX.25 bytes by traffic class and direction."},
		{"airtime_class_seconds_total", "Estimated seconds on the air by traffic class and direction, including key-up."},
	} {
		head(mt.name, "counter", mt.help)
		for _, c := range sn.Channels {
			for _, class := range classes {
				for _, tx := range []bool{false, true} {
					r := cls[classKey{c.Port, class, tx}]
					l := fmt.Sprintf("{port=\"%d\",class=\"%s\",dir=\"%s\"}", c.Port, class, dirName(tx))
					switch mt.name {
					case "airtime_class_frames_total":
						fmt.Fprintf(&b, "%s%s %d\n", mt.name, l, r.Frames)
					case "airtime_class_bytes_total":
						fmt.Fprintf(&b, "%s%s %d\n", mt.name, l, r.Bytes)
					default:
						fmt.Fprintf(&b, "%s%s %g\n", mt.name, l, r.Airtime)
					}
				}
			}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// APRS.
	head("airtime_aprs_packets_total", "counter", "APRS packets by kind (position, mic-e, message, ...) and direction.")
	for _, c := range sn.Channels {
		for _, kind := range aprsKinds {
			for _, tx := range []bool{false, true} {
				fmt.Fprintf(&b, "airtime_aprs_packets_total{port=\"%d\",kind=\"%s\",dir=\"%s\"} %d\n", c.Port, kind, dirName(tx), s.aprs[aprsKey{c.Port, kind, tx}])
			}
		}
	}
	head("airtime_aprs_digipeated_total", "counter", "APRS packets heard through a digipeater.")
	for _, c := range sn.Channels {
		fmt.Fprintf(&b, "airtime_aprs_digipeated_total{port=\"%d\"} %d\n", c.Port, s.aprsDigi[c.Port])
	}
	head("airtime_aprs_stations", "gauge", "Distinct APRS stations heard in the window.")
	devices := map[int]map[string]int64{}
	for _, c := range sn.Channels {
		for _, wn := range stationWindows {
			n := 0
			for _, st := range s.stations {
				if st.Port == c.Port && !st.Last.Before(now.Add(-wn.d)) {
					n++
					if wn.name == "24h" {
						if devices[c.Port] == nil {
							devices[c.Port] = map[string]int64{}
						}
						d := st.Device
						if d == "" {
							d = "unknown"
						}
						devices[c.Port][d]++
					}
				}
			}
			fmt.Fprintf(&b, "airtime_aprs_stations{port=\"%d\",window=\"%s\"} %d\n", c.Port, wn.name, n)
		}
	}
	head("airtime_aprs_devices", "gauge", "APRS stations heard in the last 24 hours, by device (from the tocall or Mic-E).")
	for _, c := range sn.Channels {
		for _, d := range sortedKeys(devices[c.Port], func(a, b string) bool { return a < b }) {
			fmt.Fprintf(&b, "airtime_aprs_devices{port=\"%d\",device=%s} %d\n", c.Port, lbl(d), devices[c.Port][d])
		}
	}

	// Connected-mode sessions.
	ss := s.sessions
	head("airtime_sessions_open", "gauge", "Connected-mode sessions open now.")
	open := map[int]int{}
	for _, o := range ss.open {
		open[o.Port]++
	}
	for _, c := range sn.Channels {
		fmt.Fprintf(&b, "airtime_sessions_open{port=\"%d\"} %d\n", c.Port, open[c.Port])
	}
	pairs := make([]dirKey, 0, len(ss.pairs))
	for k := range ss.pairs {
		pairs = append(pairs, k)
	}
	sortDirKeys(pairs)
	head("airtime_sessions_total", "counter", "Connected-mode sessions opened, by who opened them (from) and to whom.")
	for _, k := range pairs {
		fmt.Fprintf(&b, "airtime_sessions_total{port=\"%d\",from=%s,to=%s} %d\n", k.Port, lbl(k.From), lbl(k.To), ss.pairs[k].Opened)
	}
	head("airtime_sessions_closed_total", "counter", "Connected-mode sessions closed, by how: DISC, DM, refused (DM to a SABM), idle, reopened.")
	for _, how := range sortedKeys(ss.closing, func(a, b string) bool { return a < b }) {
		fmt.Fprintf(&b, "airtime_sessions_closed_total{how=%s} %d\n", lbl(how), ss.closing[how])
	}
	head("airtime_session_seconds_total", "counter", "Total length of closed connected-mode sessions, by from and to.")
	for _, k := range pairs {
		fmt.Fprintf(&b, "airtime_session_seconds_total{port=\"%d\",from=%s,to=%s} %g\n", k.Port, lbl(k.From), lbl(k.To), ss.pairs[k].Seconds)
	}
	dirs := make([]dirKey, 0, len(ss.dirs))
	for k := range ss.dirs {
		dirs = append(dirs, k)
	}
	sortDirKeys(dirs)
	head("airtime_link_iframes_total", "counter", "I frames on connected-mode links by direction (src to dst); kind=new is first sendings, repeat is sent again.")
	for _, k := range dirs {
		t := ss.dirs[k]
		l := fmt.Sprintf("port=\"%d\",src=%s,dst=%s", k.Port, lbl(k.From), lbl(k.To))
		fmt.Fprintf(&b, "airtime_link_iframes_total{%s,kind=\"new\"} %d\nairtime_link_iframes_total{%s,kind=\"repeat\"} %d\n", l, t.NewIFrames, l, t.Repeats)
	}
	head("airtime_link_info_bytes_total", "counter", "Info bytes in I frames on connected-mode links by direction; kind=new is data, repeat is resent.")
	for _, k := range dirs {
		t := ss.dirs[k]
		l := fmt.Sprintf("port=\"%d\",src=%s,dst=%s", k.Port, lbl(k.From), lbl(k.To))
		fmt.Fprintf(&b, "airtime_link_info_bytes_total{%s,kind=\"new\"} %d\nairtime_link_info_bytes_total{%s,kind=\"repeat\"} %d\n", l, t.NewBytes, l, t.InfoBytes-t.NewBytes)
	}
	head("airtime_link_events_total", "counter", "Recovery on connected-mode links by direction: rej, srej, rnr (busy), poll (a T1 timeout), frmr.")
	for _, k := range dirs {
		t := ss.dirs[k]
		l := fmt.Sprintf("port=\"%d\",src=%s,dst=%s", k.Port, lbl(k.From), lbl(k.To))
		for _, e := range []struct {
			n string
			v int64
		}{{"rej", t.REJ}, {"srej", t.SREJ}, {"rnr", t.RNR}, {"poll", t.Polls}, {"frmr", t.FRMR}} {
			fmt.Fprintf(&b, "airtime_link_events_total{%s,event=\"%s\"} %d\n", l, e.n, e.v)
		}
	}
	w.Write([]byte(b.String()))
}

func sortDirKeys(ks []dirKey) {
	sort.Slice(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.From != b.From {
			return a.From < b.From
		}
		return a.To < b.To
	})
}

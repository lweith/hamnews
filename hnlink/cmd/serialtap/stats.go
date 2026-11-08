package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// stats counts what crossed the tap, per direction. Directions are "host"
// (the computer side, bytes read from -a) and "tnc" (bytes read from -b).
type stats struct {
	mu       sync.Mutex
	bytes    map[string]int64
	frames   map[string]map[int]int64 // dir -> KISS command -> frames
	errors   map[string]int64         // dir -> framing errors
	bySrc    map[string]int64         // "dir call" -> data frames
	srcBytes map[string]int64         // "dir call" -> AX.25 bytes
}

func newStats() *stats {
	return &stats{
		bytes:    map[string]int64{},
		frames:   map[string]map[int]int64{},
		errors:   map[string]int64{},
		bySrc:    map[string]int64{},
		srcBytes: map[string]int64{},
	}
}

func (s *stats) addBytes(dir string, n int) {
	s.mu.Lock()
	s.bytes[dir] += int64(n)
	s.mu.Unlock()
}

// addFrame records one KISS frame.
func (s *stats) addFrame(dir string, f kissFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.frames[dir] == nil {
		s.frames[dir] = map[int]int64{}
	}
	s.frames[dir][f.Cmd]++
	if f.Cmd != 0 {
		return
	}
	src, _, ok := ax25Addrs(f.Data)
	if !ok {
		src = "unparsed"
	}
	k := dir + " " + src
	s.bySrc[k]++
	s.srcBytes[k] += int64(len(f.Data))
}

func (s *stats) addErrors(dir string, n int) {
	if n == 0 {
		return
	}
	s.mu.Lock()
	s.errors[dir] += int64(n)
	s.mu.Unlock()
}

// handler serves the counters in Prometheus text format.
func (s *stats) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" && r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprint(w, s.render())
	})
}

func (s *stats) render() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder

	b.WriteString("# HELP serialtap_bytes_total Bytes that crossed the tap, by direction.\n")
	b.WriteString("# TYPE serialtap_bytes_total counter\n")
	for _, d := range sortedKeys(s.bytes) {
		fmt.Fprintf(&b, "serialtap_bytes_total{dir=%q} %d\n", d, s.bytes[d])
	}

	b.WriteString("# HELP serialtap_frames_total KISS frames by direction and command (0 = data).\n")
	b.WriteString("# TYPE serialtap_frames_total counter\n")
	for _, d := range sortedKeys(s.frames) {
		cmds := make([]int, 0, len(s.frames[d]))
		for c := range s.frames[d] {
			cmds = append(cmds, c)
		}
		sort.Ints(cmds)
		for _, c := range cmds {
			fmt.Fprintf(&b, "serialtap_frames_total{dir=%q,cmd=\"%d\"} %d\n", d, c, s.frames[d][c])
		}
	}

	b.WriteString("# HELP serialtap_errors_total KISS framing errors by direction.\n")
	b.WriteString("# TYPE serialtap_errors_total counter\n")
	for _, d := range sortedKeys(s.errors) {
		fmt.Fprintf(&b, "serialtap_errors_total{dir=%q} %d\n", d, s.errors[d])
	}

	b.WriteString("# HELP serialtap_src_frames_total Data frames by direction and source call-SSID.\n")
	b.WriteString("# TYPE serialtap_src_frames_total counter\n")
	for _, k := range sortedKeys(s.bySrc) {
		d, call := split2(k)
		fmt.Fprintf(&b, "serialtap_src_frames_total{dir=%q,call=%q} %d\n", d, call, s.bySrc[k])
	}

	b.WriteString("# HELP serialtap_src_bytes_total AX.25 bytes by direction and source call-SSID.\n")
	b.WriteString("# TYPE serialtap_src_bytes_total counter\n")
	for _, k := range sortedKeys(s.srcBytes) {
		d, call := split2(k)
		fmt.Fprintf(&b, "serialtap_src_bytes_total{dir=%q,call=%q} %d\n", d, call, s.srcBytes[k])
	}
	return b.String()
}

// sortedKeys returns the keys of a string-keyed map in order, for stable output.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// split2 splits "dir call" into its two parts.
func split2(k string) (string, string) {
	i := strings.IndexByte(k, ' ')
	if i < 0 {
		return k, ""
	}
	return k[:i], k[i+1:]
}

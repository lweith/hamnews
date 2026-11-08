package main

import (
	"fmt"
	"strings"
)

// filter decides which frames a listener gets. An empty filter (or "*") is a
// monitor: every frame. Otherwise a frame passes when its source or its
// destination matches one of the patterns.
//
// Patterns are CALL-SSID (exact), CALL-* (any SSID of CALL), or *.
type filter struct {
	all      bool
	patterns []string
}

// parseFilter reads a listener's filter from the config. No patterns, or a
// lone "*", means monitor (everything).
func parseFilter(specs []string) (filter, error) {
	var f filter
	for _, s := range specs {
		s = strings.ToUpper(strings.TrimSpace(s))
		switch {
		case s == "":
			continue
		case s == "*":
			f.all = true
		case strings.HasSuffix(s, "-*"):
			if strings.TrimSuffix(s, "-*") == "" {
				return filter{}, fmt.Errorf("filter %q: no callsign", s)
			}
			f.patterns = append(f.patterns, s)
		case strings.Contains(s, "-") && !strings.ContainsAny(s, "*?"):
			f.patterns = append(f.patterns, s)
		default:
			return filter{}, fmt.Errorf("filter %q: want CALL-SSID, CALL-*, or *", s)
		}
	}
	if len(f.patterns) == 0 {
		f.all = true
	}
	return f, nil
}

// monitor reports whether the filter passes every frame.
func (f filter) monitor() bool { return f.all }

// match reports whether an AX.25 frame passes the filter.
func (f filter) match(data []byte) bool {
	if f.all {
		return true
	}
	src, dst, ok := ax25Addrs(data)
	if !ok {
		return false
	}
	return f.hit(src) || f.hit(dst)
}

func (f filter) hit(call string) bool {
	for _, p := range f.patterns {
		if strings.HasSuffix(p, "-*") {
			if strings.HasPrefix(call, strings.TrimSuffix(p, "*")) {
				return true
			}
		} else if call == p {
			return true
		}
	}
	return false
}

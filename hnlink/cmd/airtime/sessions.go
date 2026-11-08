package main

import (
	"sort"
	"time"
)

// SessionIdle closes a session nothing has been seen of for this long.
const SessionIdle = 10 * time.Minute

// keepClosed is how many closed sessions the page and JSON list.
const keepClosed = 100

// Dir is one direction of a connected-mode session, as seen from here: the
// frames this station sent (all of them) or heard (those it decoded).
type Dir struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Frames     int64  `json:"frames"`
	Bytes      int64  `json:"bytes"`       // whole frames
	IFrames    int64  `json:"iframes"`     // I frames, first sendings and repeats
	InfoBytes  int64  `json:"info_bytes"`  // their info fields
	NewIFrames int64  `json:"new_iframes"` // I frames with an N(S) not seen before: the data
	NewBytes   int64  `json:"new_bytes"`   // their info: what was delivered (if acknowledged)
	Repeats    int64  `json:"repeats"`     // I frames sent again (an N(S) already seen)
	RR         int64  `json:"rr"`
	RNR        int64  `json:"rnr"`
	REJ        int64  `json:"rej"`
	SREJ       int64  `json:"srej"`
	Polls      int64  `json:"polls"` // supervisory commands with P set: a T1 timeout, asking where the other end is
	FRMR       int64  `json:"frmr"`
	next       int    // next new N(S), -1 before the first I frame
}

// Session is one connected-mode link between two stations on a port.
type Session struct {
	ID       int        `json:"id"`
	Port     int        `json:"port"`
	From     string     `json:"from"` // who opened it (SABM/SABME), or whose frame was first seen
	To       string     `json:"to"`
	Opening  string     `json:"opening"` // SABM, SABME, or "in progress" (started before airtime saw it)
	Modulo   int        `json:"modulo"`  // 8 or 128; 0 not known yet
	Opened   time.Time  `json:"opened"`
	Up       *time.Time `json:"up,omitempty"` // the UA to the SABM(E)
	Last     time.Time  `json:"last"`
	Closed   *time.Time `json:"closed,omitempty"`
	Closing  string     `json:"closing,omitempty"` // DISC, DM, idle, reopened
	Duration float64    `json:"duration_s"`
	Dirs     [2]*Dir    `json:"dirs"` // [0] From to To, [1] back
	Setup    float64    `json:"setup_s,omitempty"`
	lastSABM time.Time
	counted  bool // added to the closed totals
}

type linkKey struct {
	Port int
	A, B string // sorted
}

func mkKey(port int, x, y string) linkKey {
	if y < x {
		x, y = y, x
	}
	return linkKey{port, x, y}
}

// dirKey: totals per direction, over all sessions.
type dirKey struct {
	Port     int
	From, To string
}

// Totals of what sessions carried in one direction, over all sessions. These
// are the counters served; they only go up.
type dirTotals struct {
	IFrames, InfoBytes, NewIFrames, NewBytes, Repeats int64
	REJ, SREJ, RNR, Polls, FRMR                       int64
}

type pairTotals struct {
	Opened, Closed int64
	Seconds        float64
}

// Sessions follows connected-mode sessions. The caller holds Stats.mu.
type Sessions struct {
	open    map[linkKey]*Session
	closed  []*Session // newest last
	nextID  int
	dirs    map[dirKey]*dirTotals
	pairs   map[dirKey]*pairTotals // From = who opened it
	closing map[string]int64       // by Closing
	mod     map[linkKey]int        // modulo last known, kept past the session for late frames
}

func newSessions() *Sessions {
	return &Sessions{open: map[linkKey]*Session{}, dirs: map[dirKey]*dirTotals{},
		pairs: map[dirKey]*pairTotals{}, closing: map[string]int64{}, mod: map[linkKey]int{}}
}

// Mod128 says whether frames between a and b on port use two-byte control
// fields, if known.
func (s *Sessions) Mod128(port int) func(a, b string) (bool, bool) {
	return func(a, b string) (bool, bool) {
		m := s.mod[mkKey(port, a, b)]
		return m == 128, m != 0
	}
}

func (s *Sessions) newSession(k linkKey, f *Frame, how string, now time.Time) *Session {
	s.nextID++
	ss := &Session{ID: s.nextID, Port: f.Port, From: f.Src, To: f.Dst, Opening: how, Opened: now, Last: now,
		Modulo: s.mod[k]}
	ss.Dirs[0] = &Dir{From: f.Src, To: f.Dst, next: -1}
	ss.Dirs[1] = &Dir{From: f.Dst, To: f.Src, next: -1}
	s.open[k] = ss
	p := s.pair(dirKey{f.Port, f.Src, f.Dst})
	p.Opened++
	return ss
}

func (s *Sessions) pair(k dirKey) *pairTotals {
	p := s.pairs[k]
	if p == nil {
		p = &pairTotals{}
		s.pairs[k] = p
	}
	return p
}

func (s *Sessions) close(k linkKey, ss *Session, how string, at time.Time) {
	delete(s.open, k)
	ss.Closed, ss.Closing = &at, how
	ss.Duration = at.Sub(ss.Opened).Seconds()
	p := s.pair(dirKey{ss.Port, ss.From, ss.To})
	p.Closed++
	p.Seconds += ss.Duration
	s.closing[how]++
	s.closed = append(s.closed, ss)
	if len(s.closed) > keepClosed {
		s.closed = s.closed[len(s.closed)-keepClosed:]
	}
}

// Expire closes sessions idle for SessionIdle, as of their last frame.
func (s *Sessions) Expire(now time.Time) {
	for k, ss := range s.open {
		if now.Sub(ss.Last) > SessionIdle {
			s.close(k, ss, "idle", ss.Last)
		}
	}
}

// Frame notes a frame of a connected-mode link (linkTypes, XID, TEST).
func (s *Sessions) Frame(f *Frame, now time.Time) {
	k := mkKey(f.Port, f.Src, f.Dst)
	ss := s.open[k]
	switch f.Type {
	case "SABM", "SABME":
		m := 8
		if f.Type == "SABME" {
			m = 128
		}
		s.mod[k] = m
		if ss != nil && (ss.Up != nil || ss.Opening == "in progress") {
			// A link reset or a new connection over an established one.
			s.close(k, ss, "reopened", now)
			ss = nil
		}
		if ss == nil {
			ss = s.newSession(k, f, f.Type, now)
		}
		ss.Modulo = m
		ss.lastSABM = now
	case "UA":
		if ss == nil {
			s.lateUA(k, now)
			return
		}
		if ss.Up == nil && f.Src == ss.To && !ss.lastSABM.IsZero() && ss.Opening != "in progress" {
			t := now
			ss.Up = &t
			ss.Setup = now.Sub(ss.Opened).Seconds()
		}
	case "DISC", "DM":
		if ss == nil {
			return // a retried DISC, or a refusal of a SABM not seen
		}
		ss.Last = now
		s.count(ss, f)
		if f.Type == "DM" && ss.Up == nil && ss.Opening != "in progress" {
			s.close(k, ss, "refused", now)
		} else {
			s.close(k, ss, f.Type, now)
		}
		return
	default: // I, S, FRMR, XID, TEST
		if ss == nil {
			if f.Type == "XID" || f.Type == "TEST" {
				return // before a SABME: negotiation, not a session yet
			}
			ss = s.newSession(k, f, "in progress", now)
		}
	}
	if ss.Modulo == 0 && (f.Type == "I" || f.supervisory()) {
		ss.Modulo = 8
		if f.Mod128 {
			ss.Modulo = 128
		}
		s.mod[k] = ss.Modulo
	}
	ss.Last = now
	s.count(ss, f)
}

// lateUA: the UA to a DISC, after the session closed. Counted to it.
func (s *Sessions) lateUA(k linkKey, now time.Time) {
	for i := len(s.closed) - 1; i >= 0; i-- {
		c := s.closed[i]
		if mkKey(c.Port, c.From, c.To) == k && now.Sub(*c.Closed) < time.Minute {
			c.Last = now
			return
		}
	}
}

func (s *Sessions) count(ss *Session, f *Frame) {
	d := ss.Dirs[0]
	if f.Src != d.From {
		d = ss.Dirs[1]
	}
	t := s.dirs[dirKey{f.Port, f.Src, f.Dst}]
	if t == nil {
		t = &dirTotals{}
		s.dirs[dirKey{f.Port, f.Src, f.Dst}] = t
	}
	d.Frames++
	d.Bytes += int64(f.Bytes)
	if f.supervisory() && f.Cmd == 1 && f.PF {
		d.Polls++
		t.Polls++
	}
	switch f.Type {
	case "I":
		d.IFrames++
		d.InfoBytes += int64(f.Info)
		t.IFrames++
		t.InfoBytes += int64(f.Info)
		if d.isNew(f.NS, ss.Modulo) {
			d.NewIFrames++
			d.NewBytes += int64(f.Info)
			t.NewIFrames++
			t.NewBytes += int64(f.Info)
		} else {
			d.Repeats++
			t.Repeats++
		}
	case "RR":
		d.RR++
	case "RNR":
		d.RNR++
		t.RNR++
	case "REJ":
		d.REJ++
		t.REJ++
	case "SREJ":
		d.SREJ++
		t.SREJ++
	case "FRMR":
		d.FRMR++
		t.FRMR++
	}
}

// isNew: whether an I frame's N(S) is one this direction hasn't sent yet. One
// up to half the sequence space behind the next new one is a repeat; any
// other is new (frames before it may have gone undecoded here).
func (d *Dir) isNew(ns, modulo int) bool {
	if modulo == 0 {
		modulo = 8
	}
	ns %= modulo
	if d.next < 0 {
		d.next = (ns + 1) % modulo
		return true
	}
	behind := (d.next - ns + modulo) % modulo
	if behind >= 1 && behind <= modulo/2 {
		return false
	}
	d.next = (ns + 1) % modulo
	return true
}

// SessionsSnapshot is what the page and JSON show.
type SessionsSnapshot struct {
	Open   []Session `json:"open"`
	Closed []Session `json:"closed"` // newest first
}

func (s *Sessions) snapshot(now time.Time) SessionsSnapshot {
	var sn SessionsSnapshot
	for _, ss := range s.open {
		c := copySession(ss)
		c.Duration = now.Sub(ss.Opened).Seconds()
		sn.Open = append(sn.Open, c)
	}
	sort.Slice(sn.Open, func(i, j int) bool { return sn.Open[i].ID < sn.Open[j].ID })
	for i := len(s.closed) - 1; i >= 0; i-- {
		sn.Closed = append(sn.Closed, copySession(s.closed[i]))
	}
	return sn
}

func copySession(ss *Session) Session {
	c := *ss
	for i := range c.Dirs {
		d := *ss.Dirs[i]
		c.Dirs[i] = &d
	}
	return c
}

// Package web is hnlink's local status-and-posting page.
//
// It binds to localhost by default. Anything posted here is transmitted on the
// air under the station's callsign, so state-changing requests must carry the
// X-Hnlink header with a JSON body (a cross-site page cannot send that without
// a CORS preflight, which is never granted) and a localhost Host header (DNS
// rebinding).
package web

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"hamnews/hnlink/internal/link"
	"hamnews/hnlink/internal/nntp"
)

//go:embed index.html
var indexHTML []byte

// Ring keeps the last lines written to it (wire it into the logger).
type Ring struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func NewRing(n int) *Ring { return &Ring{max: n} }

func (r *Ring) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			r.lines = append(r.lines, l)
		}
	}
	if len(r.lines) > r.max {
		r.lines = r.lines[len(r.lines)-r.max:]
	}
	return len(b), nil
}

func (r *Ring) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

type Server struct {
	Engine  func() *link.Engine // current engine, nil while the TNC is down
	NNTP    string
	Wildmat string
	From    string // From: for posts
	KISS    string
	Events  *Ring
	Started time.Time
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
		w.Write(indexHTML)
	})
	m.HandleFunc("GET /api/status", s.status)
	m.HandleFunc("GET /api/articles", s.articles)
	m.HandleFunc("GET /api/article", s.article)
	m.HandleFunc("GET /api/groups", s.groups)
	m.HandleFunc("POST /api/post", s.post)
	return guard(m)
}

// guard rejects requests whose Host isn't loopback (DNS rebinding) and
// state-changing requests that lack the X-Hnlink header (CSRF).
func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if hh, _, err := net.SplitHostPort(host); err == nil {
			host = hh
		}
		host = strings.Trim(host, "[]")
		ip := net.ParseIP(host)
		if !(host == "localhost" || (ip != nil && ip.IsLoopback())) {
			http.Error(w, "use http://localhost", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && (r.Header.Get("X-Hnlink") != "1" ||
			!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"kiss": s.KISS, "nntp": s.NNTP, "from": s.From, "connected": false,
		"uptime": time.Since(s.Started).Round(time.Second).String(), "events": s.Events.Lines(),
	}
	if e := s.Engine(); e != nil {
		out["connected"] = true
		out["link"] = e.Snapshot()
	}
	writeJSON(w, out)
}

func (s *Server) client() (*nntp.Client, error) { return nntp.Dial(s.NNTP, true) }

func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	c, err := s.client()
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	defer c.Close()
	gs, err := c.Groups(s.Wildmat)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	sort.Strings(gs)
	writeJSON(w, gs)
}

func (s *Server) articles(w http.ResponseWriter, r *http.Request) {
	c, err := s.client()
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	defer c.Close()
	gs, err := c.Groups(s.Wildmat)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	var all []nntp.Overview
	for _, g := range gs {
		ov, err := c.Recent(g, 100)
		if err != nil {
			continue
		}
		all = append(all, ov...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].When.After(all[j].When) })
	if len(all) > 200 {
		all = all[:200]
	}
	writeJSON(w, all)
}

func (s *Server) article(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") || strings.ContainsAny(id, " \t\r\n") {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("bad message-id"))
		return
	}
	c, err := s.client()
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	defer c.Close()
	a, err := c.Article(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err)
		return
	}
	head, body, _ := strings.Cut(string(a), "\n\n")
	hdr := map[string]string{}
	for _, l := range strings.Split(head, "\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			hdr[strings.ToLower(k)] = strings.TrimSpace(v)
		}
	}
	writeJSON(w, map[string]any{"headers": hdr, "body": body})
}

func (s *Server) post(w http.ResponseWriter, r *http.Request) {
	var p struct{ Group, Subject, Body, References string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&p); err != nil {
		jsonErr(w, http.StatusBadRequest, err)
		return
	}
	p.Subject = strings.TrimSpace(p.Subject)
	if p.Group == "" || p.Subject == "" || strings.TrimSpace(p.Body) == "" {
		jsonErr(w, http.StatusBadRequest, fmt.Errorf("group, subject and text are required"))
		return
	}
	c, err := s.client()
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	defer c.Close()
	if err := c.Post(s.From, p.Group, p.Subject, p.References, p.Body); err != nil {
		jsonErr(w, http.StatusBadGateway, err)
		return
	}
	if e := s.Engine(); e != nil {
		e.Refresh() // offer it on the air promptly
	}
	writeJSON(w, map[string]string{"ok": "posted"})
}

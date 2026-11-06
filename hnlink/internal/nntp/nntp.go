// Package nntp is the small NNTP client hnlink needs: NEWNEWS + ARTICLE to read
// the local server's window, IHAVE to inject received articles. Pure TCP, so it
// works against innd on Linux or in WSL from a native Windows build.
package nntp

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strings"
	"time"

	"hamnews/hnlink/internal/rnews"
)

type Client struct {
	t    *textproto.Conn
	Addr string
}

// Dial connects. reader=true sends MODE READER (for NEWNEWS/ARTICLE); for IHAVE
// stay in transit mode, which innd grants to hosts in incoming.conf.
func Dial(addr string, reader bool) (*Client, error) {
	nc, err := dialTCP(addr)
	if err != nil {
		return nil, err
	}
	t := textproto.NewConn(nc)
	c := &Client{t: t, Addr: addr}
	if _, _, err := t.ReadCodeLine(20); err != nil {
		t.Close()
		return nil, fmt.Errorf("nntp %s: greeting: %w", addr, err)
	}
	if reader {
		if _, _, err := c.cmd(20, "MODE READER"); err != nil {
			t.Close()
			return nil, err
		}
	}
	return c, nil
}

// dialTCP resolves "localhost" itself (::1, then 127.0.0.1). Static builds use
// Go's resolver, which skips NSS, so hosts relying on nss-myhostname instead of
// an /etc/hosts entry would otherwise send "localhost" to DNS.
func dialTCP(addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !strings.EqualFold(host, "localhost") {
		return net.DialTimeout("tcp", addr, 10*time.Second)
	}
	var first error
	for _, ip := range []string{"::1", "127.0.0.1"} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, port), 5*time.Second)
		if err == nil {
			return c, nil
		}
		if first == nil {
			first = err
		}
	}
	return nil, first
}

func (c *Client) Close() error {
	c.t.PrintfLine("QUIT")
	return c.t.Close()
}

func (c *Client) cmd(expect int, format string, a ...any) (int, string, error) {
	id, err := c.t.Cmd(format, a...)
	if err != nil {
		return 0, "", err
	}
	c.t.StartResponse(id)
	defer c.t.EndResponse(id)
	return c.t.ReadCodeLine(expect)
}

// NewNews lists message-IDs that arrived since t in groups matching wildmat.
func (c *Client) NewNews(wildmat string, since time.Time) ([]string, error) {
	u := since.UTC()
	id, err := c.t.Cmd("NEWNEWS %s %s GMT", wildmat, u.Format("20060102 150405"))
	if err != nil {
		return nil, err
	}
	c.t.StartResponse(id)
	defer c.t.EndResponse(id)
	if _, _, err := c.t.ReadCodeLine(230); err != nil {
		return nil, fmt.Errorf("NEWNEWS: %w", err)
	}
	lines, err := c.t.ReadDotLines()
	if err != nil {
		return nil, err
	}
	var ids []string
	seen := map[string]bool{}
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "<") && !seen[l] {
			seen[l] = true
			ids = append(ids, l)
		}
	}
	return ids, nil
}

var ErrNoArticle = errors.New("nntp: no such article")

// Article returns the full article with LF line endings, dot-unstuffed.
func (c *Client) Article(msgid string) ([]byte, error) {
	id, err := c.t.Cmd("ARTICLE %s", msgid)
	if err != nil {
		return nil, err
	}
	c.t.StartResponse(id)
	defer c.t.EndResponse(id)
	code, msg, err := c.t.ReadCodeLine(220)
	if err != nil {
		if code == 430 || code == 423 {
			return nil, ErrNoArticle
		}
		return nil, fmt.Errorf("ARTICLE %s: %d %s", msgid, code, msg)
	}
	return c.t.ReadDotBytes()
}

type IHaveResult int

const (
	Accepted IHaveResult = iota
	Duplicate
	Rejected
	Deferred
)

func (r IHaveResult) String() string {
	return [...]string{"accepted", "duplicate", "rejected", "deferred"}[r]
}

// IHave offers one article (LF line endings). The returned string is the
// server's reply text for logging.
func (c *Client) IHave(art []byte) (IHaveResult, string, error) {
	msgid := rnews.Header(art, "Message-ID")
	if msgid == "" || strings.ContainsAny(msgid, " \t\r\n") {
		return Rejected, "no usable Message-ID", nil
	}
	code, msg, err := c.cmd(0, "IHAVE %s", msgid)
	if err != nil && code == 0 {
		return 0, "", err
	}
	switch code {
	case 335:
	case 435:
		return Duplicate, msg, nil
	case 436:
		return Deferred, msg, nil
	case 437:
		return Rejected, msg, nil
	default:
		return 0, msg, fmt.Errorf("IHAVE %s: %d %s", msgid, code, msg)
	}
	w := c.t.DotWriter()
	if _, err := w.Write(art); err != nil {
		return 0, "", err
	}
	if err := w.Close(); err != nil {
		return 0, "", err
	}
	code, msg, err = c.t.ReadCodeLine(0)
	if err != nil && code == 0 {
		return 0, "", err
	}
	switch code {
	case 235:
		return Accepted, msg, nil
	case 436:
		return Deferred, msg, nil
	case 437:
		return Rejected, msg, nil
	}
	return 0, msg, fmt.Errorf("IHAVE %s body: %d %s", msgid, code, msg)
}

// Store is the server-side view hnlink needs.
type Store struct {
	Addr    string
	Wildmat string        // groups exchanged, e.g. "*,!control,!control.*,!junk"
	Window  time.Duration // exchange window (14 days)
}

// Inventory returns the message-IDs in the window.
func (s *Store) Inventory() ([]string, error) {
	c, err := Dial(s.Addr, true)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.NewNews(s.Wildmat, time.Now().Add(-s.Window))
}

// Articles fetches articles; missing ones are skipped.
func (s *Store) Articles(ids []string) (map[string][]byte, error) {
	c, err := Dial(s.Addr, true)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := map[string][]byte{}
	for _, id := range ids {
		a, err := c.Article(id)
		if errors.Is(err, ErrNoArticle) {
			continue
		}
		if err != nil {
			return out, err
		}
		out[id] = a
	}
	return out, nil
}

type Tally struct{ Accepted, Duplicate, Rejected, Deferred int }

func (t Tally) String() string {
	return fmt.Sprintf("%d accepted, %d duplicate, %d rejected, %d deferred",
		t.Accepted, t.Duplicate, t.Rejected, t.Deferred)
}

// Ingest offers every article of an rnews batch by IHAVE.
func (s *Store) Ingest(batch []byte, log io.Writer) (Tally, error) {
	var t Tally
	arts, perr := rnews.Split(batch)
	c, err := Dial(s.Addr, false)
	if err != nil {
		return t, err
	}
	defer c.Close()
	for _, a := range arts {
		r, msg, err := c.IHave(a)
		if err != nil {
			return t, err
		}
		switch r {
		case Accepted:
			t.Accepted++
		case Duplicate:
			t.Duplicate++
		case Rejected:
			t.Rejected++
			if log != nil {
				fmt.Fprintf(log, "rejected %s: %s\n", rnews.Header(a, "Message-ID"), msg)
			}
		case Deferred:
			t.Deferred++
		}
	}
	return t, perr
}

// Groups lists groups matching wildmat that allow posting ('y' or 'm' flag).
func (c *Client) Groups(wildmat string) ([]string, error) {
	id, err := c.t.Cmd("LIST ACTIVE %s", wildmat)
	if err != nil {
		return nil, err
	}
	c.t.StartResponse(id)
	defer c.t.EndResponse(id)
	if _, _, err := c.t.ReadCodeLine(215); err != nil {
		return nil, fmt.Errorf("LIST ACTIVE: %w", err)
	}
	lines, err := c.t.ReadDotLines()
	if err != nil {
		return nil, err
	}
	var gs []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) >= 4 && (f[3] == "y" || f[3] == "m") {
			gs = append(gs, f[0])
		}
	}
	return gs, nil
}

// Overview is one article's summary.
type Overview struct {
	Group     string    `json:"group"`
	Number    int64     `json:"number"`
	Subject   string    `json:"subject"`
	From      string    `json:"from"`
	Date      string    `json:"date"`
	MessageID string    `json:"message_id"`
	Bytes     int64     `json:"bytes"`
	When      time.Time `json:"when"`
}

// Recent returns overview data for up to n of the newest articles in group.
func (c *Client) Recent(group string, n int) ([]Overview, error) {
	_, msg, err := c.cmd(211, "GROUP %s", group)
	if err != nil {
		return nil, err
	}
	var count, lo, hi int64
	fmt.Sscan(msg, &count, &lo, &hi)
	if count == 0 || hi < lo {
		return nil, nil
	}
	if hi-lo >= int64(n) {
		lo = hi - int64(n) + 1
	}
	id, err := c.t.Cmd("OVER %d-%d", lo, hi)
	if err != nil {
		return nil, err
	}
	c.t.StartResponse(id)
	defer c.t.EndResponse(id)
	if _, _, err := c.t.ReadCodeLine(224); err != nil {
		return nil, nil // e.g. 423: nothing in range
	}
	lines, err := c.t.ReadDotLines()
	if err != nil {
		return nil, err
	}
	var out []Overview
	for _, l := range lines {
		f := strings.Split(l, "\t")
		if len(f) < 7 {
			continue
		}
		o := Overview{Group: group, Subject: f[1], From: f[2], Date: f[3], MessageID: f[4]}
		fmt.Sscan(f[0], &o.Number)
		fmt.Sscan(f[6], &o.Bytes)
		for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 -0700"} {
			if t, err := time.Parse(layout, strings.TrimSpace(strings.Split(f[3], " (")[0])); err == nil {
				o.When = t
				break
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// Post submits a new article through nnrpd, which adds Message-ID, Date and Path.
func (c *Client) Post(from, group, subject, refs, body string) error {
	for _, v := range []string{from, group, subject, refs} {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("header fields may not contain line breaks")
		}
	}
	if _, msg, err := c.cmd(340, "POST"); err != nil {
		return fmt.Errorf("POST: %v %s", err, msg)
	}
	w := c.t.DotWriter()
	fmt.Fprintf(w, "From: %s\nNewsgroups: %s\nSubject: %s\n", from, group, subject)
	if refs != "" {
		fmt.Fprintf(w, "References: %s\n", refs)
	}
	fmt.Fprintf(w, "Content-Type: text/plain; charset=UTF-8\nContent-Transfer-Encoding: 8bit\n\n")
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	io.WriteString(w, body)
	if err := w.Close(); err != nil {
		return err
	}
	code, msg, err := c.t.ReadCodeLine(240)
	if err != nil {
		return fmt.Errorf("post refused: %d %s", code, msg)
	}
	return nil
}

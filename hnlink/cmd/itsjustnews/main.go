// itsjustnews: receive-only monitor for hnlink traffic.
//
// For anyone who wants to check that what hnlink stations send is plain usenet
// and not obscured meaning (47 CFR 97.113(a)(4)): it listens to a KISS TNC
// (direwolf's KISS TCP port) or reads a KISS capture file, explains every hnlink
// frame, reassembles the Reed-Solomon coded objects exactly as a station does,
// and prints every article in full. It has no transmit path; it never writes
// to the TNC.
//
// The code is systematic: the first K symbols of an object are the rnews batch
// itself, cut into pieces. With -frames those pieces are printed as text too,
// so the plaintext is visible even before an object decodes.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hamnews/hnlink/internal/fec"
	"hamnews/hnlink/internal/kiss"
	"hamnews/hnlink/internal/rnews"
	"hamnews/hnlink/internal/wire"
)

var version = "0.1"

type monitor struct {
	out     *bufio.Writer
	frames  bool
	quiet   bool
	save    string
	decs    map[wire.ObjID]*fec.Decoder
	senders map[wire.ObjID]map[string]bool
	shown   map[wire.ObjID]bool
	other   int
}

func main() {
	addr := flag.String("kiss", "127.0.0.1:8001", "KISS TNC: HOST:PORT (direwolf), or a serial hardware TNC /dev/ttyUSB0[:BAUD]")
	file := flag.String("file", "", "read a KISS capture file instead of a TNC (e.g. one made with -record)")
	record := flag.String("record", "", "append the raw KISS stream to this file (evidence; replay with -file)")
	frames := flag.Bool("frames", false, "show every frame, including the plaintext inside data symbols")
	quiet := flag.Bool("q", false, "print decoded articles only")
	save := flag.String("save", "", "also write each decoded batch to DIR as <objid>.rnews")
	ver := flag.Bool("version", false, "print version")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: itsjustnews [-kiss HOST:PORT | -file CAPTURE] [-frames] [-q] [-save DIR] [-record FILE]\n\n"+
			"Receive-only: decodes hnlink traffic to plain text. Never transmits.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *ver {
		fmt.Println("itsjustnews", version)
		return
	}
	if *save != "" {
		if err := os.MkdirAll(*save, 0o755); err != nil {
			fatal(err)
		}
	}
	m := &monitor{out: bufio.NewWriter(os.Stdout), frames: *frames, quiet: *quiet, save: *save,
		decs: map[wire.ObjID]*fec.Decoder{}, senders: map[wire.ObjID]map[string]bool{}, shown: map[wire.ObjID]bool{}}

	var rec io.Writer
	if *record != "" {
		f, err := os.OpenFile(*record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fatal(err)
		}
		defer f.Close()
		rec = f
	}

	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			fatal(err)
		}
		m.run(f, rec)
		f.Close()
		m.summary()
		return
	}
	for {
		c, err := dialRO(*addr)
		if err != nil {
			m.logf("KISS %s: %v; retrying in 15s", *addr, err)
			time.Sleep(15 * time.Second)
			continue
		}
		m.logf("listening on KISS %s (receive only)", *addr)
		// Read side only: the connection is never written to.
		m.run(c, rec)
		c.Close()
		m.logf("KISS %s: connection closed; reconnecting in 15s", *addr)
		time.Sleep(15 * time.Second)
	}
}

// dialRO opens the TNC for reading. Nothing is ever written to it: no KISS
// parameters, no frames.
func dialRO(addr string) (io.ReadCloser, error) {
	if kiss.IsSerial(addr) {
		path, baud, err := kiss.ParseSerial(addr)
		if err != nil {
			return nil, err
		}
		return kiss.OpenSerial(path, baud)
	}
	return net.Dial("tcp", addr)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "itsjustnews:", err)
	os.Exit(1)
}

func (m *monitor) logf(f string, a ...any) {
	if !m.quiet {
		fmt.Fprintf(m.out, "%s  %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(f, a...))
	}
	m.out.Flush()
}

func (m *monitor) run(r io.Reader, rec io.Writer) {
	kr := kiss.NewReader(r)
	for {
		f, err := kr.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				m.logf("read: %v", err)
			}
			return
		}
		if rec != nil {
			rec.Write(kiss.Escape(f))
		}
		src, info, err := kiss.ParseUI(f)
		if err != nil {
			m.other++ // Winlink, APRS, anything not addressed to HNLINK
			continue
		}
		m.frame(src.String(), info)
	}
}

func (m *monitor) frame(src string, info []byte) {
	v, err := wire.Parse(info)
	if err != nil {
		// Not obscured either: show what failed to parse, byte for byte.
		m.logf("%-9s ?? unparseable frame (%v), %d bytes: %q", src, err, len(info), info)
		return
	}
	switch f := v.(type) {
	case *wire.Inv:
		if m.frames || f.Part == 0 {
			heard := ""
			if len(f.Heard) > 0 {
				heard = ", hears " + strings.Join(f.Heard, " ")
			}
			m.logf("%-9s INV    holds %d articles (part %d/%d, gen %d%s)", src, len(f.Hashes), f.Part+1, f.Parts, f.Gen, heard)
		}
	case *wire.Need:
		m.logf("%-9s NEED   %s: wants %d more symbols (has %d)", src, f.ID, f.Count, f.Have)
	case *wire.Manifest:
		d := m.dec(f.ID, src)
		first := d.Manifest == nil
		d.SetManifest(f)
		if first || m.frames {
			m.logf("%-9s MANIFEST %s: %d bytes from %s, %d data + %d repair symbols of %d bytes, sha256 %x",
				src, f.ID, f.Length, f.Origin, f.K, f.M, f.SymSize, f.SHA256[:8])
		}
		m.try(f.ID)
	case *wire.Symbol:
		d := m.dec(f.ID, src)
		fresh := d.Add(f)
		if m.frames {
			kind := "repair"
			if d.Manifest != nil && int(f.ESI) < int(d.Manifest.K) {
				kind = "data"
			}
			m.logf("%-9s SYMBOL %s #%d (%s, %d bytes%s)", src, f.ID, f.ESI, kind, len(f.Data), map[bool]string{true: "", false: ", duplicate"}[fresh])
			if kind == "data" {
				m.logf("          plaintext: %q", f.Data)
			}
		}
		m.try(f.ID)
	}
}

func (m *monitor) dec(id wire.ObjID, src string) *fec.Decoder {
	d := m.decs[id]
	if d == nil {
		d = fec.NewDecoder(id)
		m.decs[id] = d
		m.senders[id] = map[string]bool{}
	}
	m.senders[id][src] = true
	return d
}

func (m *monitor) try(id wire.ObjID) {
	if m.shown[id] {
		return
	}
	d := m.decs[id]
	obj, err := d.TryDecode()
	if err != nil {
		m.logf("         %s: decode failed (%v); collecting again", id, err)
		return
	}
	if obj == nil {
		return
	}
	m.shown[id] = true
	var from []string
	for s := range m.senders[id] {
		from = append(from, s)
	}
	arts, err := rnews.Split(obj)
	m.logf("DECODED  %s: %d bytes, sha256 verified, %d article(s), sent by %s",
		id, len(obj), len(arts), strings.Join(from, " "))
	if err != nil {
		m.logf("         not a clean rnews batch (%v); raw object follows", err)
		fmt.Fprintf(m.out, "%s\n", obj)
	}
	for i, a := range arts {
		fmt.Fprintf(m.out, "\n----- %s article %d/%d  %s\n", id, i+1, len(arts), rnews.Header(a, "Message-ID"))
		m.out.Write(a)
		if len(a) > 0 && a[len(a)-1] != '\n' {
			m.out.WriteByte('\n')
		}
		fmt.Fprintf(m.out, "----- end\n\n")
	}
	m.out.Flush()
	if m.save != "" {
		p := filepath.Join(m.save, id.String()+".rnews")
		if err := os.WriteFile(p, obj, 0o644); err != nil {
			m.logf("save: %v", err)
		}
	}
}

func (m *monitor) summary() {
	pending := 0
	for id, d := range m.decs {
		if !m.shown[id] {
			pending++
			if !m.quiet {
				fmt.Fprintf(m.out, "incomplete %s: %d symbols, need %d more\n", id, d.Have(), d.Need())
			}
		}
	}
	if !m.quiet {
		fmt.Fprintf(m.out, "%d object(s) decoded, %d incomplete, %d non-hnlink frame(s) skipped\n",
			len(m.shown), pending, m.other)
	}
	m.out.Flush()
}

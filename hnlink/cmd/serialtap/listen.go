package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

// listenKind says what a TCP listener is for.
type listenKind int

const (
	monitorListen listenKind = iota // every frame, read-only, shallow queue
	stationListen                   // one callsign's frames, read-only, deep queue
	rwListen                        // receives like a station; its frames go to the TNC
)

var kindNames = map[listenKind]string{
	monitorListen: "monitor",
	stationListen: "station",
	rwListen:      "read-write",
}

// writeTimeout drops a client that stops reading, so its connection can't hold
// a goroutine forever. Its queue fills and drops frames in the meantime.
const writeTimeout = 30 * time.Second

// listenSpec is one TCP listener: where it listens, what it receives, and what
// kind of client it serves.
type listenSpec struct {
	addr   string
	filter filter
	kind   listenKind
}

// parseListen reads "ADDR" or "ADDR=FILTER[,FILTER...]" for the given kind. A
// monitor takes no filter. A station must have one. The read-write listener
// may have one, and with none it receives everything.
func parseListen(spec string, kind listenKind) (listenSpec, error) {
	addr, fl, hasFilter := strings.Cut(spec, "=")
	if addr == "" {
		return listenSpec{}, fmt.Errorf("%q: no address", spec)
	}
	if kind == monitorListen && hasFilter {
		return listenSpec{}, fmt.Errorf("%q: a monitor takes no filter", spec)
	}
	if kind == stationListen && strings.TrimSpace(fl) == "" {
		return listenSpec{}, fmt.Errorf("%q: a station needs a filter", spec)
	}
	var specs []string
	if hasFilter {
		specs = strings.Split(fl, ",")
	}
	f, err := parseFilter(specs)
	if err != nil {
		return listenSpec{}, fmt.Errorf("%q: %v", spec, err)
	}
	return listenSpec{addr: addr, filter: f, kind: kind}, nil
}

// listenFlag collects one repeatable command-line flag of a single kind.
type listenFlag struct {
	kind  listenKind
	specs []listenSpec
}

func (l *listenFlag) String() string { return "" }

func (l *listenFlag) Set(s string) error {
	sp, err := parseListen(s, l.kind)
	if err != nil {
		return err
	}
	l.specs = append(l.specs, sp)
	return nil
}

// describe says what a listener serves, for the log.
func (sp listenSpec) describe() string {
	if sp.filter.monitor() {
		return kindNames[sp.kind] + ", all frames"
	}
	return kindNames[sp.kind] + ", " + strings.Join(sp.filter.patterns, ",")
}

// startListener opens a listener and serves it in the background. in is the
// TNC's transmit queue for the read-write listener, and nil for the others.
func startListener(sp listenSpec, h *hub, in *txQueue) error {
	ln, err := net.Listen("tcp", sp.addr)
	if err != nil {
		return err
	}
	log.Printf("serialtap: listening on %s (%s)", ln.Addr(), sp.describe())
	go serveClients(ln, sp, h, in)
	return nil
}

// serveClients accepts connections on ln until it is closed. Each connection
// gets its own client, with the listener's filter and its own queue.
func serveClients(ln net.Listener, sp listenSpec, h *hub, in *txQueue) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("serialtap: %s: accept stopped: %v", ln.Addr(), err)
			return
		}
		go serveConn(conn, sp, h, in)
	}
}

// serveConn runs one client connection. Frames from the hub are written to the
// connection. Input is discarded for a read-only client, and read as KISS
// frames for the read-write one.
func serveConn(conn net.Conn, sp listenSpec, h *hub, in *txQueue) {
	peer := conn.RemoteAddr().String()
	c := newClient(peer, sp.filter, sp.kind != monitorListen)
	h.add(c)
	log.Printf("serialtap: %s connected to %s (%s)", peer, sp.addr, kindNames[sp.kind])

	done := make(chan struct{})
	go func() {
		defer close(done)
		if in != nil {
			readTx(conn, in, peer)
		} else {
			io.Copy(io.Discard, conn)
		}
	}()

	defer func() {
		h.remove(c)
		conn.Close()
		log.Printf("serialtap: %s disconnected from %s", peer, sp.addr)
	}()
	for {
		select {
		case enc := <-c.queue:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := conn.Write(enc); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// readTx turns a read-write client's KISS input into transmit frames. Only
// data frames on port 0 are queued. TNC settings from the client are ignored,
// since the tap doesn't change the TNC's configuration.
func readTx(conn io.Reader, in *txQueue, peer string) {
	var d kissDecoder
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		for _, x := range buf[:n] {
			f, ok := d.feed(x)
			if !ok {
				continue
			}
			if f.Cmd != 0 || f.Port != 0 {
				log.Printf("serialtap: %s: ignored KISS command %d on port %d", peer, f.Cmd, f.Port)
				continue
			}
			in.push(kissEncode(f.Data))
		}
		if d.Errors > 0 {
			log.Printf("serialtap: %s: %d KISS framing errors", peer, d.Errors)
			d.Errors = 0
		}
		if err != nil {
			return
		}
	}
}

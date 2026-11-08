package main

import (
	"log"
	"sync"
	"sync/atomic"
)

// Queue depths. A station client (one that transmits, or whose session depends
// on every frame addressed to it) gets a deep queue and must never drop
// silently. A monitor gets a shallow queue and may drop, with the count kept.
const (
	stationDepth = 4096
	monitorDepth = 256
)

// kissEncode wraps one AX.25 frame as a KISS data frame on port 0, escaping
// the special bytes. It's the inverse of kissDecoder.feed.
func kissEncode(payload []byte) []byte {
	out := make([]byte, 0, len(payload)+4)
	out = append(out, fend, 0x00)
	for _, x := range payload {
		switch x {
		case fend:
			out = append(out, fesc, tfend)
		case fesc:
			out = append(out, fesc, tfesc)
		default:
			out = append(out, x)
		}
	}
	return append(out, fend)
}

// client is one connection's consumer. Its queue is bounded, and a full queue
// drops this client's frames only. Station clients log each drop, since a
// lost frame breaks their session; monitor drops are counted and not logged.
type client struct {
	name    string
	filter  filter
	station bool
	queue   chan []byte
	drops   atomic.Int64
	sent    atomic.Int64
}

// newClient makes a station client when station is set, else a monitor. The
// filter is the listener's own; a monitor's filter is the match-all one.
func newClient(name string, f filter, station bool) *client {
	depth := monitorDepth
	if station {
		depth = stationDepth
	}
	return &client{name: name, filter: f, station: station, queue: make(chan []byte, depth)}
}

// offer queues a frame if the filter passes it. It never blocks.
func (c *client) offer(data, encoded []byte) {
	if !c.filter.match(data) {
		return
	}
	select {
	case c.queue <- encoded:
		c.sent.Add(1)
	default:
		n := c.drops.Add(1)
		if c.station {
			log.Printf("serialtap: station client %s queue full: frame dropped (%d so far)", c.name, n)
		}
	}
}

// hub fans frames out to every connected client. Clients come and go with
// their TCP connections, so the list is guarded by a lock.
type hub struct {
	mu      sync.RWMutex
	clients []*client
}

func newHub(clients ...*client) *hub {
	return &hub{clients: clients}
}

func (h *hub) add(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients = append(h.clients, c)
}

func (h *hub) remove(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i, x := range h.clients {
		if x == c {
			h.clients = append(h.clients[:i], h.clients[i+1:]...)
			return
		}
	}
}

// count reports how many clients are connected.
func (h *hub) count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// deliver offers one frame to every client.
func (h *hub) deliver(data []byte) {
	enc := kissEncode(data)
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.clients {
		c.offer(data, enc)
	}
}

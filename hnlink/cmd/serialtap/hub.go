package main

import (
	"log"
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

// client is one listener's consumer. Its queue is bounded, and a full queue
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

// hub fans received frames out to clients. The clients are fixed when the
// tap starts, from its config, so the list needs no locking.
type hub struct {
	clients []*client
}

func newHub(clients ...*client) *hub {
	return &hub{clients: clients}
}

// deliver offers one received frame to every client.
func (h *hub) deliver(data []byte) {
	enc := kissEncode(data)
	for _, c := range h.clients {
		c.offer(data, enc)
	}
}

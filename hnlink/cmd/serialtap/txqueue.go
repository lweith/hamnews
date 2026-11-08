package main

import (
	"log"
	"sync"
	"sync/atomic"
)

// defaultTxSeconds is how much transmit backlog the tap will hold, in seconds
// of line time. It is measured in bytes at the link's actual rate, so the same
// budget means the same wait at 1200, 2400 or 9600 baud.
const defaultTxSeconds = 60

// txQueue holds KISS frames waiting to go to the TNC. It drains at the line
// rate, so its limit is a byte budget: budget = seconds × baud / 10, since 8N1
// is 10 bits per byte on the wire. A frame that would push the queue past the
// budget is refused and logged. Refusing is better than a backlog that is
// minutes old by the time it goes out.
type txQueue struct {
	mu      sync.Mutex
	frames  [][]byte
	bytes   int
	budget  int
	refused atomic.Int64
}

// newTxQueue sizes the queue for the given line rate and seconds of backlog.
func newTxQueue(baud int, seconds int) *txQueue {
	return &txQueue{budget: baud * seconds / 10}
}

// push queues one KISS-encoded frame. It never blocks. It reports whether the
// frame was accepted.
func (q *txQueue) push(encoded []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.bytes+len(encoded) > q.budget {
		n := q.refused.Add(1)
		log.Printf("serialtap: tx queue full (%d of %d bytes queued): frame of %d bytes refused (%d so far)",
			q.bytes, q.budget, len(encoded), n)
		return false
	}
	q.frames = append(q.frames, encoded)
	q.bytes += len(encoded)
	return true
}

// pop removes the oldest frame, if any.
func (q *txQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.frames) == 0 {
		return nil, false
	}
	f := q.frames[0]
	q.frames[0] = nil
	q.frames = q.frames[1:]
	q.bytes -= len(f)
	return f, true
}

// queuedBytes reports the bytes waiting, and the budget they are checked against.
func (q *txQueue) queuedBytes() (queued, budget int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.bytes, q.budget
}

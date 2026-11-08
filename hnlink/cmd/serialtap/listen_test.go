package main

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

func TestParseListen(t *testing.T) {
	for _, tc := range []struct {
		spec string
		kind listenKind
		bad  bool
	}{
		{"127.0.0.1:8100", monitorListen, false},
		{"127.0.0.1:8100=W8HHF-10", monitorListen, true}, // monitors take no filter
		{"127.0.0.1:8101=W8HHF-10", stationListen, false},
		{"127.0.0.1:8101", stationListen, true},  // a station needs a filter
		{"127.0.0.1:8101=", stationListen, true}, // an empty one is still no filter
		{"127.0.0.1:8101=W8HHF", stationListen, true},
		{"127.0.0.1:8200", rwListen, false},
		{"127.0.0.1:8200=W8HHF-10", rwListen, false},
		{"=W8HHF-10", stationListen, true}, // no address
	} {
		_, err := parseListen(tc.spec, tc.kind)
		if tc.bad && err == nil {
			t.Errorf("%q (%s): accepted, want error", tc.spec, kindNames[tc.kind])
		}
		if !tc.bad && err != nil {
			t.Errorf("%q (%s): %v", tc.spec, kindNames[tc.kind], err)
		}
	}
}

// startTest serves a listener on an ephemeral port and returns its address.
func startTest(t *testing.T, sp listenSpec, h *hub, in *txQueue) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go serveClients(ln, sp, h, in)
	return ln.Addr().String()
}

// waitClients waits until n clients are registered with the hub.
func waitClients(t *testing.T, h *hub, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h.count() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hub has %d clients, want %d", h.count(), n)
}

func readKISS(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func TestMonitorSocketGetsEveryFrame(t *testing.T) {
	h := newHub()
	addr := startTest(t, listenSpec{addr: "test", filter: filter{all: true}, kind: monitorListen}, h, nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitClients(t, h, 1)

	f := frame("W8HHF", 10, "KN6FLN", 5)
	h.deliver(f)
	if got := readKISS(t, conn, len(kissEncode(f))); !bytes.Equal(got, kissEncode(f)) {
		t.Errorf("monitor got %x, want %x", got, kissEncode(f))
	}
}

// A station socket sees only its filter's frames. The non-matching frame goes
// first, so any leak would show up as the first bytes read.
func TestStationSocketIsFiltered(t *testing.T) {
	h := newHub()
	f, _ := parseFilter([]string{"W8HHF-10"})
	addr := startTest(t, listenSpec{addr: "test", filter: f, kind: stationListen}, h, nil)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitClients(t, h, 1)

	other := frame("KN6FLN", 5, "N8ZAK", 0)
	match := frame("W8HHF", 10, "KN6FLN", 5)
	h.deliver(other)
	h.deliver(match)
	if got := readKISS(t, conn, len(kissEncode(match))); !bytes.Equal(got, kissEncode(match)) {
		t.Errorf("station got %x, want the matching frame %x", got, kissEncode(match))
	}
}

// A read-write client's data frames go to the TNC queue. Its TNC settings are
// dropped.
func TestReadWriteSocketFeedsTxQueue(t *testing.T) {
	h := newHub()
	q := newTxQueue(1200, 60)
	addr := startTest(t, listenSpec{addr: "test", kind: rwListen}, h, q)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	payload := frame("W8HHF", 10, "KN6FLN", 5)
	conn.Write([]byte{fend, 0x01, 30, fend}) // a TXDELAY setting: must not reach the queue
	conn.Write(kissEncode(payload))

	var got []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if enc, ok := q.pop(); ok {
			got = enc
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("no frame reached the tx queue")
	}
	if !bytes.Equal(unkiss(got), payload) {
		t.Errorf("queued %x, want %x", unkiss(got), payload)
	}
	if extra, ok := q.pop(); ok {
		t.Errorf("second frame queued: %x (the TNC setting should not be)", extra)
	}
}

// Several read-write clients, on any number of listeners, feed one transmit
// queue, and each frame goes out whole.
func TestReadWriteListenersShareTxQueue(t *testing.T) {
	h := newHub()
	q := newTxQueue(1200, 60)
	sp := listenSpec{addr: "test", filter: filter{all: true}, kind: rwListen}
	addr1 := startTest(t, sp, h, q)
	addr2 := startTest(t, sp, h, q)

	var sent [][]byte
	for i, addr := range []string{addr1, addr2} {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		payload := frame("W8HHF", 10, "KN6FLN", 5+i)
		sent = append(sent, payload)
		conn.Write(kissEncode(payload))
	}

	var got [][]byte
	deadline := time.Now().Add(2 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		if enc, ok := q.pop(); ok {
			got = append(got, unkiss(enc))
		} else {
			time.Sleep(5 * time.Millisecond)
		}
	}
	if len(got) != 2 {
		t.Fatalf("queued %d frames from two writers, want 2", len(got))
	}
	for _, want := range sent {
		found := false
		for _, g := range got {
			if bytes.Equal(g, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("frame %x from one writer never queued", want)
		}
	}
}

// serialtap sits in a serial link between a computer and a TNC. It passes every
// byte both ways unchanged, decodes KISS frames as they go by, and counts them
// for Prometheus. Its TCP listeners give clients KISS frames, filtered per
// listener, and read-write listeners can send frames to the TNC.
//
//	serialtap -a /dev/ttyV0 -b /dev/ttyUSB0 -baud 1200      tap a link
//	serialtap -b /dev/ttyUSB0 -detect                       find the baud rate
//
// -a is the computer side, -b the TNC side. Both ends are set to 8N1 at the
// given rate with stty.
//
// Listeners (all optional, each bound to the address you give):
//
//	-monitor 127.0.0.1:8100                   every frame, read-only
//	-station 127.0.0.1:8101=W8HHF-10          frames to or from W8HHF-10, read-only
//	-rw      127.0.0.1:8200[=FILTER]          receives like a station; its frames go to the TNC
//	                                         (repeatable; no filter means it receives everything)
//
// Frames the tap sends to the TNC are whole frames, held back until the host's
// stream is between frames, and paced to the line rate.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"syscall"
	"time"
)

func main() {
	a := flag.String("a", "", "computer-side serial device (passed through)")
	b := flag.String("b", "", "TNC-side serial device")
	baud := flag.Int("baud", 1200, "line rate for the link, 8N1")
	detect := flag.Bool("detect", false, "find the TNC's baud rate on -b, then exit")
	sample := flag.Duration("sample", 10*time.Second, "how long to listen at each rate in -detect")
	httpAddr := flag.String("http", "127.0.0.1:9181", "Prometheus metrics at /metrics (\"\" disables)")
	logPath := flag.String("log", "", "append each KISS frame as a hex line (\"\" disables)")

	monitors := listenFlag{kind: monitorListen}
	stations := listenFlag{kind: stationListen}
	rw := listenFlag{kind: rwListen}
	flag.Var(&monitors, "monitor", "TCP ADDR for a monitor: every frame, read-only (repeatable)")
	flag.Var(&stations, "station", "TCP ADDR=FILTER for a station: matching frames, read-only (repeatable)")
	flag.Var(&rw, "rw", "TCP ADDR[=FILTER] for a read-write client: its frames go to the TNC (repeatable)")
	flag.Parse()

	if *detect {
		if *b == "" {
			log.Fatal("-detect needs -b")
		}
		if err := runDetect(*b, *sample); err != nil {
			log.Fatal(err)
		}
		return
	}
	if *a == "" || *b == "" {
		log.Fatal("need -a and -b")
	}

	st := newStats()
	var logf *os.File
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		logf = f
		defer f.Close()
	}
	if *httpAddr != "" {
		ln, err := net.Listen("tcp", *httpAddr)
		if err != nil {
			log.Fatalf("-http %s: %v", *httpAddr, err)
		}
		log.Printf("serialtap: metrics on http://%s/metrics", ln.Addr())
		go http.Serve(ln, st.handler())
	}

	for _, dev := range []string{*a, *b} {
		if err := setSerial(dev, *baud); err != nil {
			log.Fatalf("%s: %v", dev, err)
		}
	}
	fa, err := os.OpenFile(*a, os.O_RDWR, 0)
	if err != nil {
		log.Fatal(err)
	}
	fb, err := os.OpenFile(*b, os.O_RDWR, 0)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serialtap: %s <-> %s at %d 8N1", *a, *b, *baud)

	t := &tap{
		st:   st,
		logf: logf,
		hub:  newHub(),
		gate: newTNCGate(fb),
		txq:  newTxQueue(*baud, defaultTxSeconds),
	}
	for _, sp := range monitors.specs {
		if err := startListener(sp, t.hub, nil); err != nil {
			log.Fatalf("-monitor %s: %v", sp.addr, err)
		}
	}
	for _, sp := range stations.specs {
		if err := startListener(sp, t.hub, nil); err != nil {
			log.Fatalf("-station %s: %v", sp.addr, err)
		}
	}
	for _, sp := range rw.specs {
		if err := startListener(sp, t.hub, t.txq); err != nil {
			log.Fatalf("-rw %s: %v", sp.addr, err)
		}
	}
	go t.runTx(*baud)

	done := make(chan error, 2)
	go func() { done <- t.pump("host", fa, fb) }()
	go func() { done <- t.pump("tnc", fb, fa) }()
	err = <-done
	log.Fatalf("serialtap: link stopped: %v", err)
}

// setSerial puts a device into raw 8N1 at the given rate.
func setSerial(dev string, baud int) error {
	flagName := "-F" // GNU stty (Linux)
	if runtime.GOOS == "darwin" {
		flagName = "-f" // BSD stty (macOS)
	}
	out, err := exec.Command("stty", flagName, dev, strconv.Itoa(baud),
		"cs8", "-cstopb", "-parenb", "-ixon", "-ixoff", "raw", "-echo").CombinedOutput()
	if err != nil {
		return fmt.Errorf("stty: %v: %s", err, out)
	}
	return nil
}

// runDetect listens on a device at each candidate rate and reports which one
// yields KISS frames. It only reads: nothing is written to the TNC.
func runDetect(dev string, dur time.Duration) error {
	var scores []rateScore
	for _, rate := range detectRates {
		if err := setSerial(dev, rate); err != nil {
			return err
		}
		b, err := readFor(dev, dur)
		if err != nil {
			return err
		}
		s := scoreStream(rate, b)
		scores = append(scores, s)
		fmt.Printf("%5d baud: %6d bytes, %4d KISS frames, %4d framing errors\n", rate, s.Bytes, s.Frames, s.Errors)
	}
	best, ok := bestRate(scores)
	if !ok {
		fmt.Println("no KISS frames at any rate: the link is not KISS, or nothing is transmitting")
		return nil
	}
	fmt.Printf("best: %d baud (%d frames)\n", best.Rate, best.Frames)
	return nil
}

// readFor reads a device for dur, without blocking past it.
func readFor(dev string, dur time.Duration) ([]byte, error) {
	f, err := os.OpenFile(dev, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []byte
	buf := make([]byte, 512)
	end := time.Now().Add(dur)
	for time.Now().Before(end) {
		n, err := f.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && err != io.EOF {
			return out, err
		}
		if n == 0 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	return out, nil
}

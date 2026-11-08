// serialtap sits in a serial link between a computer and a TNC. It passes every
// byte both ways unchanged, decodes KISS frames as they go by, and counts them
// for Prometheus. It is receive-only in effect: it never adds or changes bytes.
//
//	serialtap -a /dev/ttyV0 -b /dev/ttyUSB0 -baud 1200      tap a link
//	serialtap -b /dev/ttyUSB0 -detect                       find the baud rate
//
// -a is the computer side, -b the TNC side. Both ends are set to 8N1 at the
// given rate with stty.
package main

import (
	"encoding/hex"
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

	done := make(chan error, 2)
	go func() { done <- pump("host", fa, fb, st, logf) }()
	go func() { done <- pump("tnc", fb, fa, st, logf) }()
	err = <-done
	log.Fatalf("serialtap: link stopped: %v", err)
}

// pump copies src to dst, byte for byte, and decodes what passes. The write
// happens before any decoding, so the tap adds no delay to the link.
func pump(dir string, src io.Reader, dst io.Writer, st *stats, logf *os.File) error {
	var d kissDecoder
	buf := make([]byte, 512)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			st.addBytes(dir, n)
			for _, x := range buf[:n] {
				if f, ok := d.feed(x); ok {
					st.addFrame(dir, f)
					if logf != nil {
						fmt.Fprintf(logf, "%s %s %d %d %s\n", time.Now().UTC().Format(time.RFC3339Nano),
							dir, f.Port, f.Cmd, hex.EncodeToString(f.Data))
					}
				}
			}
			st.addErrors(dir, d.Errors)
			d.Errors = 0
		}
		if err != nil {
			return err
		}
	}
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

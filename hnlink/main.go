// hnlink: ham-radio usenet exchange.
//
//	hnlink run   --call KN6FLN-5 [--kiss 127.0.0.1:8001]   automatic exchange on a local net
//	hnlink batch [--days 14] FILE                          write an rnews batch (for FLAMP)
//	hnlink load  FILE...                                   load received rnews batches
//
// All server access is NNTP (default localhost:119), so this runs anywhere
// innd is reachable, including natively on Windows against innd in WSL.
package main

import (
	"bytes"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"hamnews/hnlink/internal/kiss"
	"hamnews/hnlink/internal/link"
	"hamnews/hnlink/internal/metrics"
	"hamnews/hnlink/internal/nntp"
	"hamnews/hnlink/internal/rnews"
	"hamnews/hnlink/internal/web"
)

const version = "0.1"

// Carry every group, control articles included: innd applies newgroup and
// rmgroup under its control.ctl rules, so hnlink stays a dumb pipe.
const defaultGroups = "*"

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  hnlink run   --call CALL[-SSID] [options]   automatic exchange over a KISS TNC (direwolf)
  hnlink batch [options] FILE                 write the window's articles as an rnews batch
  hnlink load  [options] FILE...              load rnews batches (.gz ok); repeats are harmless
  hnlink version

'hnlink CMD -h' lists a command's options.
`)
	os.Exit(2)
}

func main() {
	log.SetFlags(log.LstdFlags)
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = cmdRun(os.Args[2:])
	case "batch":
		err = cmdBatch(os.Args[2:])
	case "load":
		err = cmdLoad(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Println("hnlink", version)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hnlink:", err)
		os.Exit(1)
	}
}

type storeFlags struct {
	nntp   *string
	groups *string
	days   *float64
}

func addStore(fs *flag.FlagSet) storeFlags {
	return storeFlags{
		nntp:   fs.String("nntp", "localhost:119", "news server (innd)"),
		groups: fs.String("groups", defaultGroups, "wildmat of groups to exchange"),
		days:   fs.Float64("days", 14, "exchange window in days"),
	}
}

func (s storeFlags) store() *nntp.Store {
	return &nntp.Store{Addr: *s.nntp, Wildmat: *s.groups, Window: time.Duration(*s.days * 24 * float64(time.Hour))}
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	sf := addStore(fs)
	call := fs.String("call", "", "station callsign with SSID for hnlink, e.g. KN6FLN-5 (required)")
	kissAddr := fs.String("kiss", "127.0.0.1:8001", "KISS TNC: HOST:PORT (direwolf), or a serial hardware TNC /dev/ttyUSB0[:BAUD] (default 9600)")
	bitrate := fs.Float64("bitrate", 1200, "channel bit rate, for pacing")
	txdelay := fs.Duration("txdelay", 300*time.Millisecond, "key-up delay per burst, for pacing")
	burst := fs.Int("burst", 6, "frames per key-up")
	quiet := fs.Duration("quiet", 3*time.Second, "channel must be idle this long before transmitting")
	symsize := fs.Int("symsize", 192, "symbol bytes per frame (multiple of 64)")
	mode := fs.String("mode", "simplex", "simplex: relay for peers you can reach; repeater: everyone hears everyone, listen always, transmit in a callsign slot only when there is something to say")
	inv := fs.Duration("inv", 30*time.Minute, "inventory beacon interval")
	verbose := fs.Bool("v", false, "log protocol detail")
	httpAddr := fs.String("http", "127.0.0.1:8073", "status/posting page address (\"\" to disable)")
	metricsAddr := fs.String("metrics", "127.0.0.1:9178", "Prometheus /metrics address; bind a LAN address to let a remote scraper in (\"\" to disable)")
	from := fs.String("from", "", "From: for posts made on the page (default: the callsign)")
	logFile := fs.String("log", "", "also append the log to this file")
	fs.Parse(args)
	if *call == "" || fs.NArg() != 0 {
		fs.Usage()
		os.Exit(2)
	}
	src, err := kiss.ParseAddr(*call)
	if err != nil {
		return err
	}
	cfg := link.Defaults(src.String())
	cfg.Bitrate, cfg.TxDelay, cfg.Burst, cfg.Quiet, cfg.SymSize, cfg.InvEvery =
		*bitrate, *txdelay, *burst, *quiet, *symsize, *inv
	if cfg.SymSize%64 != 0 || cfg.SymSize < 64 || cfg.SymSize > 1024 {
		return errors.New("--symsize must be a multiple of 64 between 64 and 1024")
	}
	if *mode != "simplex" && *mode != "repeater" {
		return errors.New("--mode must be simplex or repeater")
	}
	cfg.Mode = *mode
	if cfg.Burst < 1 || cfg.Bitrate <= 0 {
		return errors.New("--burst and --bitrate must be positive")
	}
	ring := web.NewRing(300)
	var out io.Writer = os.Stderr
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		out = io.MultiWriter(os.Stderr, f)
	}
	logger := log.New(io.MultiWriter(out, ring), "", log.LstdFlags)
	if *verbose {
		cfg.Logger = logger
	} else {
		cfg.Logger = log.New(filterWriter{io.MultiWriter(out, ring)}, "", log.LstdFlags)
	}
	st := sf.store()
	// Wait for the news server (on Windows, WSL may still be starting at logon).
	for i := 0; ; i++ {
		_, err := st.Inventory()
		if err == nil {
			break
		}
		if i == 0 {
			logger.Printf("news server %s: %v; waiting for it", *sf.nntp, err)
		}
		time.Sleep(10 * time.Second)
	}
	logger.Printf("hnlink %s: %s on KISS %s, news %s, %g-day window", version, src, *kissAddr, *sf.nntp, *sf.days)

	var cur atomic.Pointer[link.Engine]
	if *httpAddr != "" {
		if *from == "" {
			*from = src.Call
		}
		ws := &web.Server{Engine: cur.Load, NNTP: *sf.nntp, Wildmat: *sf.groups, From: *from,
			KISS: *kissAddr, Events: ring, Started: time.Now()}
		ln, err := net.Listen("tcp", *httpAddr)
		if err != nil {
			return fmt.Errorf("--http %s: %w", *httpAddr, err)
		}
		logger.Printf("page: http://%s/", strings.Replace(ln.Addr().String(), "127.0.0.1", "localhost", 1))
		go http.Serve(ln, ws.Handler())
	}

	if *metricsAddr != "" {
		ln, err := net.Listen("tcp", *metricsAddr)
		if err != nil {
			return fmt.Errorf("--metrics %s: %w", *metricsAddr, err)
		}
		logger.Printf("metrics: http://%s/metrics", ln.Addr())
		go http.Serve(ln, metrics.Handler(cur.Load, version))
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	for {
		conn, err := kiss.Dial(*kissAddr, src)
		if err == nil && conn.Serial() {
			// A hardware TNC keys up for its own TXDELAY; make it match the pacing.
			if err = conn.SetTxDelay(*txdelay); err != nil {
				conn.Close()
			}
		}
		if err != nil {
			logger.Printf("KISS %s: %v; retrying in 15s", *kissAddr, err)
		} else {
			e := link.New(cfg, conn, st)
			cur.Store(e)
			res := make(chan error, 1)
			go func() { res <- e.Run() }()
			select {
			case <-sig:
				e.Stop()
				conn.Close()
				return nil
			case err := <-res:
				cur.Store(nil)
				conn.Close()
				logger.Printf("KISS %s: %v; reconnecting in 15s", *kissAddr, err)
			}
		}
		select {
		case <-sig:
			return nil
		case <-time.After(15 * time.Second):
		}
	}
}

// filterWriter keeps the log to what an operator cares about without -v.
type filterWriter struct{ w io.Writer }

func (f filterWriter) Write(b []byte) (int, error) {
	s := string(b)
	for _, k := range []string{"accepted", "new peer", "expired", "elected", "giving up", "inventory", "fetch", "encode", "ingest", "send:", "truncated", "SHA-256", "INV gen", "object", "NEED"} {
		if strings.Contains(s, k) {
			return f.w.Write(b)
		}
	}
	return len(b), nil
}

func cmdBatch(args []string) error {
	fs := flag.NewFlagSet("batch", flag.ExitOnError)
	sf := addStore(fs)
	gz := fs.Bool("z", false, "gzip the batch (leave off if FLAMP compression is on)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	st := sf.store()
	ids, err := st.Inventory()
	if err != nil {
		return err
	}
	arts, err := st.Articles(ids)
	if err != nil {
		return err
	}
	var batch []byte
	n := 0
	for _, id := range ids {
		if a, ok := arts[id]; ok {
			batch = rnews.Append(batch, a)
			n++
		}
	}
	out := batch
	if *gz {
		var b bytes.Buffer
		w := gzip.NewWriter(&b)
		w.Write(batch)
		w.Close()
		out = b.Bytes()
	}
	if err := os.WriteFile(fs.Arg(0), out, 0o644); err != nil {
		return err
	}
	fmt.Printf("hnlink batch: %d articles (last %gd) -> %s (%d bytes)\n", n, *sf.days, fs.Arg(0), len(out))
	return nil
}

func cmdLoad(args []string) error {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	addr := fs.String("nntp", "localhost:119", "news server (innd)")
	fs.Parse(args)
	if fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	st := &nntp.Store{Addr: *addr}
	var total nntp.Tally
	bad := 0
	for _, f := range fs.Args() {
		b, err := os.ReadFile(f)
		if err == nil && len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
			var r *gzip.Reader
			if r, err = gzip.NewReader(bytes.NewReader(b)); err == nil {
				b, err = io.ReadAll(r)
			}
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "hnlink load: %s: %v\n", f, err)
			bad++
			continue
		}
		t, err := st.Ingest(b, os.Stderr)
		total.Accepted += t.Accepted
		total.Duplicate += t.Duplicate
		total.Rejected += t.Rejected
		total.Deferred += t.Deferred
		if err != nil {
			fmt.Fprintf(os.Stderr, "hnlink load: %s: %v (%v)\n", f, err, t)
			bad++
			continue
		}
		fmt.Printf("hnlink load: %s: %v\n", f, t)
	}
	if len(fs.Args()) > 1 {
		fmt.Printf("hnlink load: total %v\n", total)
	}
	if bad > 0 {
		return fmt.Errorf("%d file(s) had errors", bad)
	}
	return nil
}

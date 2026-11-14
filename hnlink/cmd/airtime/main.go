// airtime: who is using the channel, by callsign-SSID.
//
// An AGW client of direwolf. It turns on raw frames ('k': every frame heard,
// byte for byte) and monitoring ('m': including the frames this station
// transmits, which KISS clients never see), and keeps per channel, per
// call-SSID, per direction: frames, bytes, estimated airtime, frame types,
// PIDs, destinations. It serves a page, JSON and Prometheus metrics, and can
// write a pcap trace (LINKTYPE_AX25, Wireshark decodes it).
//
// It never transmits: the only things it sends direwolf are the two AGW
// "monitor on" requests and a port-information query.
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"hamnews/hnlink/internal/buildinfo"
)

func main() {
	agw := flag.String("agw", "127.0.0.1:8000", "direwolf AGW port")
	httpAddr := flag.String("http", "127.0.0.1:8075", "page, /api/stats and /metrics (\"\" disables)")
	pcap := flag.String("pcap", "", "append every frame to this pcap file (AX.25, for Wireshark)")
	bitrate := flag.Float64("bitrate", 1200, "channel bit rate, for airtime estimates")
	txdelay := flag.Duration("txdelay", 300*time.Millisecond, "key-up time added to each burst, for airtime estimates")
	verbose := flag.Bool("v", false, "log every frame (a text packet trace)")
	tocallsPath := flag.String("tocalls", "/usr/share/direwolf/tocalls.txt", "APRS tocall list, to name stations' software and radios (\"\" for none)")
	ver := flag.Bool("version", false, "print version")
	flag.Parse()
	if *ver {
		fmt.Println(buildinfo.Get().String("airtime"))
		return
	}
	st := NewStats(*bitrate, *txdelay)
	tc, err := loadTocalls(*tocallsPath)
	if err != nil {
		log.Printf("-tocalls: %v (APRS devices named from Mic-E only)", err)
	}
	st.tocalls = tc
	var tr *pcapWriter
	if *pcap != "" {
		var err error
		if tr, err = openPcap(*pcap); err != nil {
			log.Fatal(err)
		}
	}
	if *httpAddr != "" {
		ln, err := net.Listen("tcp", *httpAddr)
		if err != nil {
			log.Fatalf("--http %s: %v", *httpAddr, err)
		}
		log.Printf("%s: page http://%s/  metrics http://%s/metrics", buildinfo.Get().String("airtime"), ln.Addr(), ln.Addr())
		if host, _, err := net.SplitHostPort(ln.Addr().String()); err != nil || !isLoopback(host) {
			log.Printf("WARNING: --http %s is not loopback: the page, /api/stats and /metrics have no authentication", ln.Addr())
		}
		go http.Serve(ln, st.Handler())
	}
	for {
		err := run(*agw, st, tr, *verbose)
		st.SetUp(false, nil)
		log.Printf("AGW %s: %v; reconnecting in 15s", *agw, err)
		time.Sleep(15 * time.Second)
	}
}

// AGW header: 36 bytes, little-endian.
type agwHdr struct {
	Port     uint8
	_        [3]byte
	Kind     byte
	_        byte
	PID      uint8
	_        byte
	From, To [10]byte
	Len      uint32
	User     uint32
}

func agwSend(w io.Writer, kind byte) error {
	return binary.Write(w, binary.LittleEndian, agwHdr{Kind: kind})
}

func cstr(b []byte) string {
	if i := strings.IndexByte(string(b), 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func run(addr string, st *Stats, tr *pcapWriter, verbose bool) error {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer c.Close()
	for _, k := range []byte{'G', 'k', 'm'} { // port info; raw frames on; monitoring on
		if err := agwSend(c, k); err != nil {
			return err
		}
	}
	log.Printf("AGW %s: connected (receive only)", addr)
	st.SetUp(true, nil)
	r := bufio.NewReader(c)
	for {
		var h agwHdr
		if err := binary.Read(r, binary.LittleEndian, &h); err != nil {
			return err
		}
		if h.Len > 1<<16 {
			return errors.New("agw: oversized record")
		}
		data := make([]byte, h.Len)
		if _, err := io.ReadFull(r, data); err != nil {
			return err
		}
		var fr *Frame
		switch h.Kind {
		case 'G': // "N;Port1 description;Port2 ...;"
			parts := strings.Split(strings.TrimRight(cstr(data), ";"), ";")
			if len(parts) > 1 {
				st.SetUp(true, parts[1:])
			}
			continue
		case 'K': // heard: one leading byte (KISS command), then the frame
			if len(data) < 2 {
				continue
			}
			fr, err = ParseAX25(data[1:], st.Mod128(int(h.Port)))
			if fr != nil {
				fr.Port = int(h.Port)
			}
		case 'T': // this station transmitted it
			fr, err = ParseMonitor(int(h.Port), data, st.Mod128(int(h.Port)))
		default: // U/I/S: monitor text of heard frames; 'K' already has them exactly
			continue
		}
		if err != nil {
			log.Printf("port %d: %v", h.Port, err)
			continue
		}
		now := time.Now()
		st.Add(now, fr)
		if tr != nil && fr.Raw != nil {
			if err := tr.Write(now, fr.Raw); err != nil {
				log.Printf("pcap: %v", err)
			}
		}
		if verbose {
			dir := "rx"
			if fr.TX {
				dir = "TX"
			}
			via := ""
			if len(fr.Via) > 0 {
				via = " via " + strings.Join(fr.Via, ",")
			}
			pid := ""
			if fr.PID >= 0 {
				pid = fmt.Sprintf(" pid=%02X", fr.PID)
			}
			seq := ""
			if fr.NS >= 0 {
				seq += fmt.Sprintf(" S%d", fr.NS)
			}
			if fr.NR >= 0 {
				seq += fmt.Sprintf(" R%d", fr.NR)
			}
			log.Printf("%d %s %s>%s%s %s%s%s len=%d %s", fr.Port, dir, fr.Src, fr.Dst, via, fr.Type, seq, pid, fr.Info, fr.Class)
		}
	}
}

// pcapWriter appends classic pcap records, LINKTYPE_AX25 (3).
type pcapWriter struct {
	mu sync.Mutex
	f  *os.File
}

func openPcap(path string) (*pcapWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err == nil && fi.Size() == 0 {
		hdr := []any{uint32(0xa1b2c3d4), uint16(2), uint16(4), int32(0), uint32(0), uint32(65535), uint32(3)}
		for _, v := range hdr {
			binary.Write(f, binary.LittleEndian, v)
		}
	}
	return &pcapWriter{f: f}, nil
}

func (p *pcapWriter) Write(t time.Time, frame []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rec := []uint32{uint32(t.Unix()), uint32(t.Nanosecond() / 1000), uint32(len(frame)), uint32(len(frame))}
	if err := binary.Write(p.f, binary.LittleEndian, rec); err != nil {
		return err
	}
	_, err := p.f.Write(frame)
	return err
}

// isLoopback reports whether a listen host (an IP from ln.Addr) is loopback.
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Package metrics exports hnlink's state in the Prometheus text format.
//
// It has no dependencies: the format is a few lines of text, and the
// station's own status snapshot already holds everything worth counting.
// Peers are labelled by callsign and SSID, so a dashboard can show who is
// participating and how well each link is doing.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"hamnews/hnlink/internal/link"
)

// Handler serves /metrics. eng returns the current engine, nil while the TNC
// is down. It is read-only, but it is not guarded like the status page: a
// scraper on another host must be able to reach it, so bind it deliberately.
func Handler(eng func() *link.Engine, version string) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		var st *link.Status
		if e := eng(); e != nil {
			s := e.Snapshot()
			st = &s
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		Write(w, st, version)
	})
	return m
}

// Write renders one scrape. A nil status means the TNC is down.
func Write(w io.Writer, st *link.Status, version string) {
	g := func(name, help string, v float64, labels ...string) {
		header(w, name, help, "gauge")
		sample(w, name, v, labels...)
	}
	c := func(name, help string, v float64, labels ...string) {
		header(w, name, help, "counter")
		sample(w, name, v, labels...)
	}

	if st == nil {
		g("hnlink_up", "1 when hnlink is connected to its TNC and news server.", 0)
		return
	}
	call, ssid := station(st.Call)
	g("hnlink_up", "1 when hnlink is connected to its TNC and news server.", 1)
	g("hnlink_build_info", "Build and station identity; always 1.", 1,
		"version", version, "mode", st.Mode, "call", call, "ssid", ssid)
	g("hnlink_local_articles", "Articles in the exchange window this station holds.", float64(st.Local))
	g("hnlink_inventory_generation", "Generation of the last inventory this station sent. Resets on restart.", float64(st.Gen))
	g("hnlink_neighbors", "Stations heard within PeerTTL.", float64(len(st.Neighbors)))
	g("hnlink_queued_batches", "Batches waiting to be sent.", float64(st.Queued))
	busy := 0.0
	if st.Sending != nil {
		busy = 1
	}
	g("hnlink_sending", "1 while an object is on the air.", busy)
	g("hnlink_receiving_objects", "Objects being received and not yet decoded.", float64(len(st.Receiving)))

	c("hnlink_frames_tx_bytes_total", "Bytes of frames sent on the air.", float64(st.Stats.FramesTx))
	c("hnlink_frames_rx_total", "Valid frames received from the air.", float64(st.Stats.FramesRx))
	c("hnlink_objects_sent_total", "Objects sent (one batch of articles each).", float64(st.Stats.ObjectsSent))
	c("hnlink_objects_decoded_total", "Objects received and decoded.", float64(st.Stats.ObjectsDecoded))
	c("hnlink_needs_total", "NEED requests sent for missing symbols.", float64(st.Stats.Needs))
	header(w, "hnlink_articles_ingested_total", "Articles offered to the news server, by result.", "counter")
	t := st.Stats.Ingested
	for _, kv := range []struct {
		result string
		n      int
	}{{"accepted", t.Accepted}, {"duplicate", t.Duplicate}, {"rejected", t.Rejected}, {"deferred", t.Deferred}} {
		sample(w, "hnlink_articles_ingested_total", float64(kv.n), "result", kv.result)
	}

	peerGauge := func(name, help string, f func(link.PeerStatus) float64) {
		header(w, name, help, "gauge")
		for _, p := range st.Peers {
			pc, ps := station(p.Call)
			sample(w, name, f(p), "call", pc, "ssid", ps)
		}
	}
	peerGauge("hnlink_peer_last_heard_timestamp_seconds", "Unix time a frame from this station was last heard.",
		func(p link.PeerStatus) float64 {
			if p.LastHeard.IsZero() {
				return 0
			}
			return float64(p.LastHeard.Unix())
		})
	peerGauge("hnlink_peer_articles", "Articles in this station's last complete inventory.",
		func(p link.PeerStatus) float64 { return float64(p.Articles) })
	peerGauge("hnlink_peer_they_lack", "Articles we hold that this station does not.",
		func(p link.PeerStatus) float64 { return float64(p.TheyLack) })
	peerGauge("hnlink_peer_we_lack", "Articles this station holds that we do not.",
		func(p link.PeerStatus) float64 { return float64(p.WeLack) })
	peerGauge("hnlink_peer_hears_us", "1 when this station's inventory lists us as heard.",
		func(p link.PeerStatus) float64 { return b2f(p.HearsUs) })
	peerGauge("hnlink_peer_heard_stations", "Stations this station reports hearing.",
		func(p link.PeerStatus) float64 { return float64(len(p.Heard)) })
}

// station splits "KN6FLN-5" into callsign and SSID; a bare call has SSID 0.
func station(call string) (string, string) {
	i := strings.LastIndex(call, "-")
	if i < 0 {
		return call, "0"
	}
	return call[:i], call[i+1:]
}

func header(w io.Writer, name, help, typ string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func sample(w io.Writer, name string, v float64, labels ...string) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%s=\"%s\"", labels[i], escape(labels[i+1]))
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(v, 'f', -1, 64))
	b.WriteByte('\n')
	io.WriteString(w, b.String())
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

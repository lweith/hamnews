package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"hamnews/hnlink/internal/link"
	"hamnews/hnlink/internal/nntp"
)

func TestWriteLabelsStationsByCallAndSSID(t *testing.T) {
	st := &link.Status{
		Call: "KN6FLN-4", Mode: "repeater", Local: 11, Gen: 7,
		Neighbors: []string{"KN6FLN-5"},
		Stats:     link.Stats{ObjectsSent: 3, Ingested: nntp.Tally{Accepted: 2, Duplicate: 1}},
		Peers: []link.PeerStatus{{
			Call: "KN6FLN-5", Articles: 11, TheyLack: 0, WeLack: 2, HearsUs: true,
			LastHeard: time.Unix(1794072000, 0), Heard: []string{"KN6FLN-4"},
		}},
	}
	var b bytes.Buffer
	Write(&b, st, "0.1")
	out := b.String()
	for _, want := range []string{
		`hnlink_up 1`,
		`hnlink_build_info{version="0.1",mode="repeater",call="KN6FLN",ssid="4"} 1`,
		`hnlink_local_articles 11`,
		`hnlink_objects_sent_total 3`,
		`hnlink_articles_ingested_total{result="accepted"} 2`,
		`hnlink_articles_ingested_total{result="duplicate"} 1`,
		`hnlink_peer_articles{call="KN6FLN",ssid="5"} 11`,
		`hnlink_peer_we_lack{call="KN6FLN",ssid="5"} 2`,
		`hnlink_peer_hears_us{call="KN6FLN",ssid="5"} 1`,
		`hnlink_peer_last_heard_timestamp_seconds{call="KN6FLN",ssid="5"} 1794072000`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWriteDownWhenTNCDisconnected(t *testing.T) {
	var b bytes.Buffer
	Write(&b, nil, "0.1")
	if !strings.Contains(b.String(), "hnlink_up 0\n") || strings.Contains(b.String(), "hnlink_local_articles") {
		t.Fatalf("unexpected output when down:\n%s", b.String())
	}
}

func TestEscapeLabelValue(t *testing.T) {
	if got := escape(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("escape = %q", got)
	}
}

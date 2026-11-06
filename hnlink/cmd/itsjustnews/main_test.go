package main

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"hamnews/hnlink/internal/fec"
	"hamnews/hnlink/internal/kiss"
	"hamnews/hnlink/internal/rnews"
	"hamnews/hnlink/internal/wire"
)

func TestDecodesCaptureToPlaintext(t *testing.T) {
	body := strings.Repeat("the quick brown fox jumps over the lazy dog\n", 40)
	art := "Path: k6zzz!not-for-mail\nFrom: K6ZZZ\nNewsgroups: ham.test\nSubject: hi\nMessage-ID: <x.1@k6zzz>\n\n" + body
	batch := rnews.Append(nil, []byte(art))
	enc, err := fec.Encode(batch, 192, wire.ContentRnews, "K6ZZZ-5")
	if err != nil {
		t.Fatal(err)
	}
	src, _ := kiss.ParseAddr("K6ZZZ-5")
	var capture bytes.Buffer
	put := func(info []byte) { capture.Write(kiss.Escape(kiss.UI(src, info))) }
	put((&wire.Inv{Gen: 1, Parts: 1, Origin: "K6ZZZ-5", Hashes: []wire.AHash{wire.ArticleHash("<x.1@k6zzz>")}}).Marshal())
	put(enc.Manifest.Marshal())
	k := int(enc.Manifest.K)
	for esi, sh := range enc.Shards {
		if esi == 0 || esi == 2 { // lost on the air: repair symbols must cover them
			continue
		}
		put((&wire.Symbol{ID: enc.Manifest.ID, ESI: uint16(esi), Data: sh}).Marshal())
		if esi > k+2 {
			break
		}
	}
	capture.Write(kiss.Escape([]byte("some other station's traffic, not addressed to HNLINK"))) // skipped

	var out bytes.Buffer
	m := &monitor{out: bufio.NewWriter(&out), frames: true, decs: map[wire.ObjID]*fec.Decoder{},
		senders: map[wire.ObjID]map[string]bool{}, shown: map[wire.ObjID]bool{}}
	m.run(&capture, nil)
	m.summary()
	s := out.String()
	for _, want := range []string{"DECODED", "sha256 verified", "Subject: hi", "the quick brown fox", "plaintext:", "1 object(s) decoded", "1 non-hnlink"} {
		if !strings.Contains(s, want) {
			t.Errorf("output lacks %q:\n%s", want, s)
		}
	}
}

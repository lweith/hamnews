package kiss

import (
	"bytes"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	info := []byte{0x00, fend, fesc, 'x', fend, fend, tfend, 0xff}
	src, _ := ParseAddr("kn6fln-3")
	var stream bytes.Buffer
	stream.Write([]byte{fend, fend, 0x12, 0x34}) // noise and empty frames
	stream.Write(Escape(UI(src, info)))
	stream.Write(Escape([]byte("not ax25 at all, definitely too long")))
	stream.Write(Escape(UI(src, nil)))
	r := NewReader(&stream)
	f, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	a, got, err := ParseUI(f)
	if err != nil || a.String() != "KN6FLN-3" || !bytes.Equal(got, info) {
		t.Fatalf("%v %v %x", err, a, got)
	}
	f, _ = r.Next()
	if _, _, err := ParseUI(f); err != ErrNotOurs {
		t.Fatal("foreign frame accepted")
	}
	f, _ = r.Next()
	if _, got, err := ParseUI(f); err != nil || len(got) != 0 {
		t.Fatal("empty info")
	}
}

func TestParseAddr(t *testing.T) {
	for _, s := range []string{"", "TOOLONG1", "K6-16", "K6-x", "K6!"} {
		if _, err := ParseAddr(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

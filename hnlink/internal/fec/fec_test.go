package fec

import (
	"bytes"
	"math/rand"
	"testing"

	"hamnews/hnlink/internal/wire"
)

func roundtrip(t *testing.T, n, ss int, lose float64, seed int64) {
	t.Helper()
	r := rand.New(rand.NewSource(seed))
	obj := make([]byte, n)
	r.Read(obj)
	e, err := Encode(obj, ss, wire.ContentRnews, "kn6fln")
	if err != nil {
		t.Fatal(err)
	}
	// Go through the wire format both ways.
	mf, err := wire.Parse(e.Manifest.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	d := NewDecoder(e.Manifest.ID)
	order := r.Perm(len(e.Shards))
	got := false
	for i, esi := range order {
		if r.Float64() < lose {
			continue
		}
		f, err := wire.Parse((&wire.Symbol{ID: e.Manifest.ID, ESI: uint16(esi), Data: e.Shards[esi]}).Marshal())
		if err != nil {
			t.Fatal(err)
		}
		d.Add(f.(*wire.Symbol))
		if i == len(order)/3 {
			d.SetManifest(mf.(*wire.Manifest)) // manifest arrives late
		}
		out, err := d.TryDecode()
		if err != nil {
			t.Fatal(err)
		}
		if out != nil {
			if !bytes.Equal(out, obj) {
				t.Fatal("mismatch")
			}
			if d.Have() != 0 || d.Need() != 0 {
				t.Fatal("state not cleared")
			}
			got = true
			break
		}
	}
	if !got {
		t.Fatalf("n=%d ss=%d: not decoded, have %d need %d", n, ss, d.Have(), d.Need())
	}
}

func TestRoundtrip(t *testing.T) {
	for i, c := range []struct {
		n, ss int
	}{{1, 64}, {191, 192}, {192, 192}, {193, 192}, {5000, 192}, {60000, 192}, {200000, 448}} {
		roundtrip(t, c.n, c.ss, 0.3, int64(i))
	}
}

func TestCorruptSymbolCaughtByHash(t *testing.T) {
	obj := bytes.Repeat([]byte("hamnews "), 100)
	e, _ := Encode(obj, 64, wire.ContentRnews, "x")
	d := NewDecoder(e.Manifest.ID)
	d.SetManifest(&e.Manifest)
	bad := append([]byte(nil), e.Shards[0]...)
	bad[3] ^= 1
	d.Add(&wire.Symbol{ID: e.Manifest.ID, ESI: 0, Data: bad})
	for i := 1; i < int(e.Manifest.K); i++ {
		d.Add(&wire.Symbol{ID: e.Manifest.ID, ESI: uint16(i), Data: e.Shards[i]})
	}
	if _, err := d.TryDecode(); err != ErrHash {
		t.Fatalf("want ErrHash, got %v", err)
	}
}

func TestWireRejects(t *testing.T) {
	f := (&wire.Need{Count: 3, Have: 9, Requester: "w6xyz"}).Marshal()
	f[len(f)-5] ^= 0x40
	if _, err := wire.Parse(f); err != wire.ErrCRC {
		t.Fatalf("want ErrCRC, got %v", err)
	}
	v := &wire.Inv{Gen: 7, Part: 1, Parts: 2, Origin: "kn6fln",
		Hashes: []wire.AHash{wire.ArticleHash("<a@b>"), wire.ArticleHash("<c@d>")}}
	p, err := wire.Parse(v.Marshal())
	if err != nil || len(p.(*wire.Inv).Hashes) != 2 || p.(*wire.Inv).Origin != "kn6fln" {
		t.Fatalf("inv roundtrip: %v %+v", err, p)
	}
}

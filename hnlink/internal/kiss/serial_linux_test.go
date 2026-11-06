package kiss

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPTY returns the master side and the slave's path: a real tty, so the
// termios path in openSerial is exercised exactly as with a USB serial TNC.
func openPTY(t *testing.T) (*os.File, string) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skip("no /dev/ptmx:", err)
	}
	fd := int(m.Fd())
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	return m, fmt.Sprintf("/dev/pts/%d", n)
}

func TestSerialTNC(t *testing.T) {
	master, slave := openPTY(t)
	defer master.Close()
	src, _ := ParseAddr("W1TST-5")
	c, err := Dial(slave+":9600", src)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.Serial() {
		t.Fatal("not serial")
	}
	if err := c.SetTxDelay(300 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	// Bytes that need escaping, and that a cooked tty would mangle (CR, ^C, ^S).
	payload := []byte{'H', 0xC0, 0xDB, '\r', '\n', 0x03, 0x13, 0x11, 0x7f}
	if err := c.Send(payload); err != nil {
		t.Fatal(err)
	}
	want := append([]byte{fend, 0x01, 30, fend}, Escape(UI(src, payload))...)
	got := make([]byte, 0, len(want))
	buf := make([]byte, 256)
	master.SetReadDeadline(time.Now().Add(2 * time.Second))
	for len(got) < len(want) {
		n, err := master.Read(buf)
		if err != nil {
			t.Fatalf("read from TNC side: %v (got % x)", err, got)
		}
		got = append(got, buf[:n]...)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("TNC received\n % x\nwant\n % x", got, want)
	}

	// And the other way: a frame "heard" by the TNC, with non-hnlink traffic first.
	peer, _ := ParseAddr("K6ZZZ-5")
	other := UI(peer, []byte("x"))
	other[0] ^= 0x02 // H -> J in the destination: not addressed to HNLINK
	master.Write(Escape(other))
	master.Write(Escape(UI(peer, payload)))
	type res struct {
		from string
		info []byte
		err  error
	}
	ch := make(chan res, 1)
	go func() { f, i, e := c.Recv(); ch <- res{f, i, e} }()
	select {
	case r := <-ch:
		if r.err != nil || r.from != "K6ZZZ-5" || !bytes.Equal(r.info, payload) {
			t.Fatalf("Recv = %q % x %v", r.from, r.info, r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Recv timed out")
	}
}

func TestParseSerial(t *testing.T) {
	for _, c := range []struct {
		in   string
		ser  bool
		path string
		baud int
	}{
		{"/dev/ttyUSB0", true, "/dev/ttyUSB0", 9600},
		{"/dev/ttyACM1:19200", true, "/dev/ttyACM1", 19200},
		{"COM3:1200", true, "COM3", 1200},
		{"127.0.0.1:8001", false, "", 0},
		{"community.example:8001", false, "", 0},
	} {
		if IsSerial(c.in) != c.ser {
			t.Errorf("IsSerial(%q) = %v", c.in, !c.ser)
			continue
		}
		if !c.ser {
			continue
		}
		p, b, err := ParseSerial(c.in)
		if err != nil || p != c.path || b != c.baud {
			t.Errorf("ParseSerial(%q) = %q %d %v", c.in, p, b, err)
		}
	}
}

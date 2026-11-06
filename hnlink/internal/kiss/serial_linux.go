package kiss

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

var bauds = map[int]uint32{
	1200: unix.B1200, 2400: unix.B2400, 4800: unix.B4800, 9600: unix.B9600,
	19200: unix.B19200, 38400: unix.B38400, 57600: unix.B57600, 115200: unix.B115200,
	230400: unix.B230400,
}

// openSerial opens a tty raw, 8N1, no flow control, at baud.
func openSerial(path string, baud int) (io.ReadWriteCloser, error) {
	rate, ok := bauds[baud]
	if !ok {
		return nil, fmt.Errorf("serial %s: unsupported baud rate %d", path, baud)
	}
	// O_NONBLOCK so open doesn't wait for carrier; cleared once CLOCAL is set.
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("serial %s: %w", path, err)
	}
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("serial %s: not a tty: %w", path, err)
	}
	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | rate
	t.Ispeed, t.Ospeed = rate, rate
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("serial %s: %w", path, err)
	}
	if err := unix.SetNonblock(fd, false); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("serial %s: %w", path, err)
	}
	unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIOFLUSH) // drop anything stale
	return os.NewFile(uintptr(fd), path), nil
}

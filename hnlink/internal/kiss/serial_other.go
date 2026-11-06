//go:build !linux

package kiss

import (
	"fmt"
	"io"
	"runtime"
)

func openSerial(path string, baud int) (io.ReadWriteCloser, error) {
	return nil, fmt.Errorf("serial KISS is not supported on %s yet; use direwolf's KISS TCP port", runtime.GOOS)
}

// Package rnews reads and writes rnews batches ("#! rnews N\n" + N bytes, repeated),
// the same format FLAMP nets carry and INN's rnews ingests.
package rnews

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Append adds one article (LF line endings) to a batch.
func Append(batch, art []byte) []byte {
	if len(art) == 0 || art[len(art)-1] != '\n' {
		art = append(art[:len(art):len(art)], '\n')
	}
	batch = fmt.Appendf(batch, "#! rnews %d\n", len(art))
	return append(batch, art...)
}

var ErrFormat = errors.New("rnews: malformed batch")

// Split returns the articles in a batch (sub-slices of b).
func Split(b []byte) ([][]byte, error) {
	var out [][]byte
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 || !bytes.HasPrefix(b, []byte("#! rnews ")) {
			return out, ErrFormat
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(b[9:nl])))
		if err != nil || n < 0 || n > len(b)-nl-1 {
			return out, ErrFormat
		}
		out = append(out, b[nl+1:nl+1+n])
		b = b[nl+1+n:]
	}
	return out, nil
}

// Header returns the value of the first header named name (case-insensitive),
// unfolding continuation lines.
func Header(art []byte, name string) string {
	var val strings.Builder
	in := false
	for _, line := range strings.Split(string(art), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			break
		}
		if in {
			if line[0] == ' ' || line[0] == '\t' {
				val.WriteString(" " + strings.TrimSpace(line))
				continue
			}
			break
		}
		if i := strings.IndexByte(line, ':'); i > 0 && strings.EqualFold(line[:i], name) {
			val.WriteString(strings.TrimSpace(line[i+1:]))
			in = true
		}
	}
	return val.String()
}

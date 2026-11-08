// Package buildinfo says which build a binary is: a semantic version set here
// by hand, and the commit and time it was built from. The commit and time come
// from -ldflags (see the Makefile or the README), or else from the VCS data the
// Go toolchain embeds, when it does.
package buildinfo

import (
	"runtime/debug"
	"strings"
	"time"
)

// Version is the semantic version (MAJOR.MINOR.PATCH). Bump it by hand in the
// commit that makes the change.
const Version = "0.2.0"

// Set at link time:
//
//	go build -ldflags "-X hamnews/hnlink/internal/buildinfo.commit=$(git rev-parse HEAD) \
//	  -X hamnews/hnlink/internal/buildinfo.dirty=$(git diff --quiet || echo true) \
//	  -X hamnews/hnlink/internal/buildinfo.built=$(date -u +%FT%TZ)" ./cmd/airtime
var (
	commit string // full revision
	dirty  string // "true" if the tree had uncommitted changes
	built  string // RFC 3339 build time
)

// Info is what a binary knows about its own build.
type Info struct {
	Version  string
	Commit   string    // full revision, "" if the build had no VCS data
	Modified bool      // the tree had uncommitted changes
	Time     time.Time // commit time, zero if unknown
}

// Get reads the build's commit, dirty flag and time: the link-time values if
// set, else the VCS data the toolchain embedded.
func Get() Info {
	i := Info{Version: Version, Commit: commit, Modified: dirty == "true"}
	if t, err := time.Parse(time.RFC3339, built); err == nil {
		i.Time = t
	}
	if i.Commit != "" || !i.Time.IsZero() {
		return i
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return i
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			i.Commit = s.Value
		case "vcs.modified":
			i.Modified = s.Value == "true"
		case "vcs.time":
			i.Time, _ = time.Parse(time.RFC3339, s.Value)
		}
	}
	return i
}

// String is one line for -version and the startup log, e.g.
// "airtime 0.2.0 (commit bc7a795e4f1a, dirty, 2026-11-08T04:00:00Z)".
func (i Info) String(name string) string {
	var parts []string
	if i.Commit != "" {
		c := i.Commit
		if len(c) > 12 {
			c = c[:12]
		}
		parts = append(parts, "commit "+c)
	}
	if i.Modified {
		parts = append(parts, "dirty")
	}
	if !i.Time.IsZero() {
		parts = append(parts, i.Time.UTC().Format(time.RFC3339))
	}
	s := name + " " + i.Version
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}

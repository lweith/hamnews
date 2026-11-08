package buildinfo

import (
	"strings"
	"testing"
	"time"
)

func TestSemver(t *testing.T) {
	parts := strings.Split(Version, ".")
	if len(parts) != 3 {
		t.Fatalf("Version %q is not MAJOR.MINOR.PATCH", Version)
	}
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" {
			t.Fatalf("Version %q has a non-numeric part %q", Version, p)
		}
	}
}

func TestString(t *testing.T) {
	ts := time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)
	i := Info{Version: "1.2.3", Commit: "bc7a795e4f1a0123456789", Modified: true, Time: ts}
	if got, want := i.String("airtime"), "airtime 1.2.3 (commit bc7a795e4f1a, dirty, 2026-11-08T04:00:00Z)"; got != want {
		t.Errorf("String() = %q; want %q", got, want)
	}
	if got, want := (Info{Version: "1.2.3"}).String("x"), "x 1.2.3"; got != want {
		t.Errorf("String() without VCS = %q; want %q", got, want)
	}
}

func TestGetHasVersion(t *testing.T) {
	if Get().Version != Version {
		t.Errorf("Get().Version = %q", Get().Version)
	}
}

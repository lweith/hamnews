package main

import (
	"bufio"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// APRS is what airtime decodes of an APRS packet: enough to say what kind of
// traffic it is, what sent it, and where it was.
type APRS struct {
	Kind    string   `json:"kind"`              // position, mic-e, message, status, object, ... (aprsKinds)
	Device  string   `json:"device,omitempty"`  // from the Mic-E suffix or the tocall
	Lat     *float64 `json:"lat,omitempty"`     // degrees north
	Lon     *float64 `json:"lon,omitempty"`     // degrees east
	Symbol  string   `json:"symbol,omitempty"`  // table and code, e.g. "/>"
	To      string   `json:"to,omitempty"`      // a message's addressee
	Comment string   `json:"comment,omitempty"` // or status text, trimmed
}

// aprsKinds are the kinds served, always, so each series starts at zero.
var aprsKinds = []string{"position", "mic-e", "message", "ack", "bulletin", "status", "object", "item",
	"weather", "telemetry", "query", "third-party", "capabilities", "nmea", "other"}

// isAPRS: a UI frame with no layer 3, to a destination APRS uses (a tocall,
// a Mic-E latitude, or one of the generic ones), whose first byte is an APRS
// data type.
func isAPRS(f *Frame) bool {
	if f.Type != "UI" || f.PID != 0xF0 || len(f.Data) == 0 {
		return false
	}
	if strings.IndexByte("!=/@`'\x1c\x1d:>;)_T?}<$#*{", f.Data[0]) < 0 {
		return false
	}
	d, _, _ := strings.Cut(f.Dst, "-")
	switch {
	case strings.HasPrefix(d, "AP"), d == "BEACON", d == "ID", d == "CQ", d == "QST", strings.HasPrefix(d, "GPS"),
		strings.HasPrefix(d, "SPC"), strings.HasPrefix(d, "SYM"), strings.HasPrefix(d, "DX"), strings.HasPrefix(d, "WX"):
		return true
	}
	return micEDest(d)
}

// micEDest: a destination of six Mic-E latitude characters.
func micEDest(d string) bool {
	if len(d) != 6 {
		return false
	}
	for i := 0; i < 6; i++ {
		c := d[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'L' || c >= 'P' && c <= 'Z') {
			return false
		}
	}
	return true
}

func decodeAPRS(f *Frame, tc *tocalls) *APRS {
	info := strings.TrimRight(string(f.Data), "\r\n")
	a := &APRS{Kind: "other"}
	if dev := tc.lookup(f.Dst); dev != "" {
		a.Device = dev
	}
	if info == "" {
		return a
	}
	switch c := info[0]; c {
	case '!', '=':
		a.Kind = "position"
		a.position(info[1:])
	case '/', '@':
		a.Kind = "position"
		if len(info) > 8 {
			a.position(info[8:])
		}
	case '`', '\'', 0x1c, 0x1d:
		a.Kind = "mic-e"
		a.micE(f.Dst, info)
	case ':':
		a.Kind = "message"
		if len(info) >= 11 && info[10] == ':' {
			a.To = strings.TrimSpace(info[1:10])
			text := info[11:]
			switch {
			case strings.HasPrefix(text, "ack") || strings.HasPrefix(text, "rej"):
				a.Kind = "ack"
			case strings.HasPrefix(a.To, "BLN") || strings.HasPrefix(a.To, "NWS"):
				a.Kind = "bulletin"
			case strings.HasPrefix(text, "PARM.") || strings.HasPrefix(text, "UNIT.") ||
				strings.HasPrefix(text, "EQNS.") || strings.HasPrefix(text, "BITS."):
				a.Kind = "telemetry"
			}
			a.Comment = text
		}
	case '>':
		a.Kind, a.Comment = "status", info[1:]
	case ';':
		a.Kind = "object"
		if len(info) > 18 {
			a.To = strings.TrimSpace(info[1:10])
			a.position(info[18:])
		}
	case ')':
		a.Kind = "item"
		if j := strings.IndexAny(info, "!_"); j > 0 {
			a.To = info[1:j]
			a.position(info[j+1:])
		}
	case '_', '#', '*':
		a.Kind = "weather"
	case 'T':
		a.Kind = "telemetry"
	case '?':
		a.Kind = "query"
	case '}':
		a.Kind = "third-party"
	case '<':
		a.Kind = "capabilities"
	case '$':
		a.Kind = "nmea"
	}
	if a.Symbol != "" && a.Symbol[1] == '_' && a.Kind == "position" {
		a.Kind = "weather" // a position with the weather station symbol carries a report
	}
	if len(a.Comment) > 80 {
		a.Comment = a.Comment[:80]
	}
	return a
}

// position decodes "DDMM.mmN/DDDMM.mmW>comment" or the compressed form.
func (a *APRS) position(s string) {
	if len(s) >= 19 && s[0] >= '0' && s[0] <= '9' {
		lat, ok1 := dm(s[0:2], s[2:7], s[7], 'S')
		lon, ok2 := dm(s[9:12], s[12:17], s[17], 'W')
		if ok1 && ok2 {
			a.Lat, a.Lon = &lat, &lon
		}
		a.Symbol = string([]byte{s[8], s[18]})
		a.Comment = strings.TrimSpace(s[19:])
		return
	}
	if len(s) >= 13 { // compressed: table, 4 lat, 4 lon, code, ...
		y, x := b91(s[1:5]), b91(s[5:9])
		if y >= 0 && x >= 0 {
			lat, lon := 90-float64(y)/380926, -180+float64(x)/190463
			a.Lat, a.Lon = &lat, &lon
		}
		a.Symbol = string([]byte{s[0], s[9]})
		a.Comment = strings.TrimSpace(s[13:])
	}
}

// dm turns degrees and "MM.mm" (spaces for position ambiguity) into degrees.
func dm(deg, min string, hemi, neg byte) (float64, bool) {
	d, err1 := strconv.Atoi(deg)
	m, err2 := strconv.ParseFloat(strings.ReplaceAll(min, " ", "0"), 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	v := float64(d) + m/60
	if hemi == neg {
		v = -v
	}
	return math.Round(v*1e5) / 1e5, true
}

func b91(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		c := int(s[i]) - 33
		if c < 0 || c > 90 {
			return -1
		}
		n = n*91 + c
	}
	return n
}

// micE decodes the latitude from the destination and the longitude, symbol
// and maker from the info (APRS 1.0.1 chapter 10; makers as direwolf 1.7).
func (a *APRS) micE(dst, info string) {
	d, _, _ := strings.Cut(dst, "-")
	if !micEDest(d) || len(info) < 9 {
		return
	}
	digit := func(c byte) byte {
		switch {
		case c >= '0' && c <= '9':
			return c
		case c >= 'A' && c <= 'J':
			return c - 'A' + '0'
		case c >= 'P' && c <= 'Y':
			return c - 'P' + '0'
		}
		return '0' // K, L, Z: an ambiguous (blanked) digit
	}
	var ld [6]byte
	for i := range ld {
		ld[i] = digit(d[i])
	}
	north := d[3] >= 'P'
	lonOff := d[4] >= 'P'
	west := d[5] >= 'P'
	lat := float64(ld[0]-'0')*10 + float64(ld[1]-'0') +
		(float64(ld[2]-'0')*10+float64(ld[3]-'0')+float64(ld[4]-'0')/10+float64(ld[5]-'0')/100)/60
	if !north {
		lat = -lat
	}
	deg := int(info[1]) - 28
	if lonOff {
		deg += 100
	}
	if deg >= 180 && deg <= 189 {
		deg -= 80
	} else if deg >= 190 && deg <= 199 {
		deg -= 190
	}
	mins := int(info[2]) - 28
	if mins >= 60 {
		mins -= 60
	}
	hund := int(info[3]) - 28
	lon := float64(deg) + (float64(mins)+float64(hund)/100)/60
	if west {
		lon = -lon
	}
	lat, lon = math.Round(lat*1e5)/1e5, math.Round(lon*1e5)/1e5
	a.Lat, a.Lon = &lat, &lon
	a.Symbol = string([]byte{info[8], info[7]})
	rest := info[9:]
	if dev, c := micEMaker(rest); dev != "" {
		a.Device = dev
		rest = c
	}
	// A base-91 altitude ("xxx}") leads the comment.
	if len(rest) >= 4 && rest[3] == '}' {
		rest = rest[4:]
	}
	a.Comment = strings.TrimSpace(rest)
}

// micEMaker reads the maker from the Mic-E comment's first and last
// characters, and returns the comment without them.
func micEMaker(s string) (string, string) {
	if s == "" {
		return "", s
	}
	first, last := s[0], s[len(s)-1]
	switch {
	case first == '>' && last == '=':
		return "Kenwood TH-D72", s[1 : len(s)-1]
	case first == '>' && last == '^':
		return "Kenwood TH-D74", s[1 : len(s)-1]
	case first == '>' && last == '&':
		return "Kenwood TH-D75", s[1 : len(s)-1]
	case first == '>':
		return "Kenwood TH-D7A", s[1:]
	case first == ']' && last == '=':
		return "Kenwood TM-D710", s[1 : len(s)-1]
	case first == ']':
		return "Kenwood TM-D700", s[1:]
	}
	if len(s) >= 3 && first == '`' && s[len(s)-2] == '_' {
		if m := map[byte]string{' ': "VX-8", '"': "FTM-350", '#': "VX-8G", '$': "FT1D", '%': "FTM-400DR",
			')': "FTM-100D", '(': "FT2D", '0': "FT3D", '3': "FT5D", '1': "FTM-300D", '5': "FTM-500D"}[last]; m != "" {
			return "Yaesu " + m, s[1 : len(s)-2]
		}
	}
	if len(s) >= 3 && first == '\'' && s[len(s)-2] == '|' {
		if m := map[byte]string{'3': "TinyTrack3", '4': "TinyTrack4"}[last]; m != "" {
			return "Byonics " + m, s[1 : len(s)-2]
		}
	}
	return "", s
}

// tocalls maps APRS destination ("tocall") prefixes to the software or radio
// that uses them, from WB4APR's list as direwolf installs it
// (/usr/share/direwolf/tocalls.txt). Lower-case letters in the list's
// entries (APAWxx, APK0xx) are wildcards.
type tocalls struct {
	byPrefix map[string]string
	maxLen   int
}

var tocallLine = regexp.MustCompile(`^\s+(?:AP[A-Z0-9]?\s+)?(AP[A-Z0-9]*[a-z]*)\s+(\S.*)$`)

func loadTocalls(path string) (*tocalls, error) {
	t := &tocalls{byPrefix: map[string]string{}}
	if path == "" {
		return t, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return t, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := tocallLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		p := strings.TrimRightFunc(m[1], func(r rune) bool { return r >= 'a' && r <= 'z' })
		if len(p) < 3 {
			continue
		}
		if _, dup := t.byPrefix[p]; !dup {
			t.byPrefix[p] = strings.TrimSpace(m[2])
		}
		t.maxLen = max(t.maxLen, len(p))
	}
	return t, sc.Err()
}

func (t *tocalls) lookup(dst string) string {
	if t == nil {
		return ""
	}
	d, _, _ := strings.Cut(dst, "-")
	for n := min(len(d), t.maxLen); n >= 3; n-- {
		if v, ok := t.byPrefix[d[:n]]; ok {
			if strings.HasPrefix(v, "Generic") {
				return "" // APRS, APZxxx...: says nothing about the sender
			}
			return v
		}
	}
	return ""
}

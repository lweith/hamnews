package main

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Frame is what airtime knows about one AX.25 frame on the air.
type Frame struct {
	Port int    // direwolf radio channel
	TX   bool   // sent by this station (AGW 'T'), else heard (AGW 'K')
	Src  string // CALL-SSID
	Dst  string
	Via  []string // digipeaters, "*" suffix once repeated
	Type string   // UI, I, RR, RNR, REJ, SREJ, SABM, SABME, DISC, DM, UA, FRMR, XID, TEST, U?
	PID  int      // -1 if none
	Info int      // info field bytes
	// Bytes: whole frame without flags and FCS. Exact when Raw is set; otherwise
	// an estimate from the header fields (a sent frame whose monitor text was
	// cut short, or one that build couldn't encode).
	Bytes int
	Raw   []byte // the frame: exact if heard, rebuilt from direwolf's monitor text if sent

	Cmd    int  // 1 command, -1 response, 0 neither (AX.25 v1, or sloppy APRS)
	PF     bool // poll (command) or final (response)
	NS, NR int  // I: N(S) and N(R); RR, RNR, REJ, SREJ: N(R). Else -1
	Mod128 bool // I or S frame with a two-byte control field (SABME link)
	Data   []byte

	Class string // see classify
	APRS  *APRS  // decoded, if Class is "aprs"
}

// Session-frame types: the frames of a connected-mode link.
var linkTypes = map[string]bool{"I": true, "RR": true, "RNR": true, "REJ": true, "SREJ": true,
	"SABM": true, "SABME": true, "DISC": true, "DM": true, "UA": true, "FRMR": true}

func (f *Frame) supervisory() bool {
	return f.Type == "RR" || f.Type == "RNR" || f.Type == "REJ" || f.Type == "SREJ"
}

func decCall(b []byte) (string, bool) {
	var c strings.Builder
	for _, x := range b[:6] {
		if ch := x >> 1; ch != ' ' {
			c.WriteByte(ch)
		}
	}
	s := c.String()
	if ssid := int(b[6]>>1) & 0x0f; ssid != 0 {
		s += "-" + strconv.Itoa(ssid)
	}
	return s, b[6]&0x80 != 0
}

var commonPIDs = map[byte]bool{0xF0: true, 0xCF: true, 0xCC: true, 0xCD: true, 0x08: true, 0xCE: true}

// ParseAX25 decodes a raw AX.25 frame (no flags, no FCS). mod128, if not nil,
// says whether the link between two stations is known to use two-byte control
// fields (SABME), and whether it is known at all; unknown, the frame decides.
func ParseAX25(f []byte, mod128 func(a, b string) (bool, bool)) (*Frame, error) {
	if len(f) < 15 {
		return nil, errors.New("ax25: short frame")
	}
	fr := &Frame{PID: -1, NS: -1, NR: -1, Bytes: len(f), Raw: append([]byte(nil), f...)}
	var dc, sc bool
	fr.Dst, dc = decCall(f[0:7])
	fr.Src, sc = decCall(f[7:14])
	switch {
	case dc && !sc:
		fr.Cmd = 1
	case !dc && sc:
		fr.Cmd = -1
	}
	i := 14
	for f[i-1]&0x01 == 0 {
		if i+7 > len(f) {
			return nil, errors.New("ax25: bad address field")
		}
		d, h := decCall(f[i : i+7])
		if h {
			d += "*"
		}
		fr.Via = append(fr.Via, d)
		i += 7
	}
	if i >= len(f) {
		return nil, errors.New("ax25: no control field")
	}
	ctl := f[i]
	switch {
	case ctl&0x01 == 0:
		fr.Type = "I"
	case ctl&0x03 == 0x01:
		fr.Type = map[byte]string{0x01: "RR", 0x05: "RNR", 0x09: "REJ", 0x0d: "SREJ"}[ctl&0x0f]
	default:
		fr.Type = map[byte]string{0x03: "UI", 0x2f: "SABM", 0x6f: "SABME", 0x43: "DISC", 0x0f: "DM",
			0x63: "UA", 0x87: "FRMR", 0xaf: "XID", 0xe3: "TEST"}[ctl&^0x10]
		if fr.Type == "" {
			fr.Type = "U?"
		}
	}
	if fr.Type == "I" || fr.supervisory() {
		m, known := false, false
		if mod128 != nil {
			m, known = mod128(fr.Src, fr.Dst)
		}
		if !known {
			m = guessMod128(fr.Type, f[i:])
		}
		fr.Mod128 = m && i+1 < len(f)
		if fr.Mod128 {
			c1 := f[i+1]
			fr.NR, fr.PF = int(c1>>1), c1&1 != 0
			if fr.Type == "I" {
				fr.NS = int(ctl >> 1)
			}
			i += 2
		} else {
			fr.NR, fr.PF = int(ctl>>5), ctl&0x10 != 0
			if fr.Type == "I" {
				fr.NS = int(ctl>>1) & 7
			}
			i++
		}
	} else {
		fr.PF = ctl&0x10 != 0
		i++
	}
	if fr.Type == "I" || fr.Type == "UI" {
		if i >= len(f) {
			return nil, errors.New("ax25: no PID")
		}
		fr.PID = int(f[i])
		i++
	}
	fr.Info = len(f) - i
	fr.Data = fr.Raw[i:]
	return fr, nil
}

// guessMod128 decides from the frame alone, for a link whose start wasn't
// seen. An S frame carries nothing after its control field, so a second byte
// is the rest of a two-byte one. An I frame's PID follows its control field:
// a common PID in the second byte means one byte, else in the third means two.
func guessMod128(typ string, c []byte) bool {
	if typ != "I" {
		return len(c) == 2
	}
	if len(c) > 1 && commonPIDs[c[1]] {
		return false
	}
	return len(c) > 2 && commonPIDs[c[2]]
}

// direwolf's monitor text for a transmitted frame (server.c mon_addrs and
// mon_desc), e.g.
//
//	" 1:Fm TEST-7 To PROBE Via WIDE1-1 <UI pid=F0 Len=21 PF=0 >[10:36:17]\rinfo...\r"
//	" 1:Fm KN6FLN-11 To KN6FLN <I S5 R2 pid=F0 Len=200 P=0 >[22:31:44]\r..."
//	" 1:Fm KN6FLN-11 To KN6FLN <RR R3 F=1 >[22:31:44]\r"
//
// P= marks a command, F= a response, PF= neither. The info, if any, follows
// the timestamp byte for byte (binary since direwolf 1.7), then "\r\0".
var monRe = regexp.MustCompile(`^\s*(\d+):Fm (\S+) To (\S+)(?: Via (\S+))? <(\S+)([^>]*)>`)

var monTypes = map[string]string{"UI": "UI", "I": "I", "RR": "RR", "RNR": "RNR", "REJ": "REJ", "SREJ": "SREJ",
	"SABM": "SABM", "SABME": "SABME", "DISC": "DISC", "DM": "DM", "UA": "UA", "FRMR": "FRMR", "XID": "XID", "TEST": "TEST"}

// ParseMonitor decodes an AGW 'T' record (a frame this station transmitted)
// and rebuilds the frame. mod128 is as for ParseAX25; I and S frames of a
// link whose start wasn't seen are taken as modulo 8 unless a sequence number
// says otherwise. AGW ports are numbered from 0; the text numbers them from 1.
func ParseMonitor(port int, data []byte, mod128 func(a, b string) (bool, bool)) (*Frame, error) {
	s := string(data)
	m := monRe.FindStringSubmatchIndex(s)
	if m == nil {
		return nil, fmt.Errorf("agw: unrecognised monitor text %q", strings.TrimRight(s, "\x00"))
	}
	sub := func(n int) string {
		if m[2*n] < 0 {
			return ""
		}
		return s[m[2*n]:m[2*n+1]]
	}
	fr := &Frame{Port: port, TX: true, Src: sub(2), Dst: sub(3), PID: -1, NS: -1, NR: -1}
	if v := sub(4); v != "" {
		fr.Via = strings.Split(v, ",")
	}
	fr.Type = monTypes[sub(5)]
	if fr.Type == "" {
		fr.Type = "U?"
	}
	for _, kv := range strings.Fields(sub(6)) {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "pid":
			if p, err := strconv.ParseUint(v, 16, 8); err == nil {
				fr.PID = int(p)
			}
		case k == "Len":
			fr.Info, _ = strconv.Atoi(v)
		case k == "P" || k == "F" || k == "PF":
			fr.PF = v == "1"
			fr.Cmd = map[string]int{"P": 1, "F": -1}[k]
		case len(k) > 1 && (k[0] == 'S' || k[0] == 'R') && v == "":
			n, err := strconv.Atoi(k[1:])
			if err != nil {
				break
			}
			if k[0] == 'S' {
				fr.NS = n
			} else {
				fr.NR = n
			}
		}
	}
	if fr.Type == "I" || fr.supervisory() {
		known := false
		if mod128 != nil {
			fr.Mod128, known = mod128(fr.Src, fr.Dst)
		}
		if !known {
			fr.Mod128 = fr.NS > 7 || fr.NR > 7
		}
	}
	// The info: after the timestamp's "]\r", Len bytes.
	rest := s[m[1]:]
	if j := strings.Index(rest, "]\r"); j >= 0 && fr.Info > 0 && j+2+fr.Info <= len(rest) {
		fr.Data = []byte(rest[j+2 : j+2+fr.Info])
	}
	if fr.Type != "U?" && (fr.Info == 0 || fr.Data != nil) {
		fr.Raw = build(fr)
	}
	fr.Bytes = 14 + 7*len(fr.Via) + 1 + fr.Info
	if fr.Mod128 {
		fr.Bytes++
	}
	if fr.PID >= 0 {
		fr.Bytes++
	}
	if fr.Raw != nil {
		fr.Bytes = len(fr.Raw)
	}
	return fr, nil
}

// encCall encodes one address. It reports false for a callsign longer than
// six characters, a blank one, or an SSID outside 0-15: those would be
// silently changed on the way out, so the frame can't be rebuilt.
func encCall(s string, last, h bool) ([]byte, bool) {
	call, ssid := s, 0
	if c, n, ok := strings.Cut(s, "-"); ok {
		call = c
		v, err := strconv.Atoi(n)
		if err != nil || v < 0 || v > 15 {
			return nil, false
		}
		ssid = v
	}
	if call == "" || len(call) > 6 {
		return nil, false
	}
	b := make([]byte, 7)
	for i := 0; i < 6; i++ {
		ch := byte(' ')
		if i < len(call) {
			ch = call[i]
		}
		b[i] = ch << 1
	}
	b[6] = 0x60 | byte(ssid)<<1
	if last {
		b[6] |= 0x01
	}
	if h {
		b[6] |= 0x80
	}
	return b, true
}

var uCtl = map[string]byte{"UI": 0x03, "SABM": 0x2f, "SABME": 0x6f, "DISC": 0x43, "DM": 0x0f,
	"UA": 0x63, "FRMR": 0x87, "XID": 0xaf, "TEST": 0xe3}
var sCtl = map[string]byte{"RR": 0x01, "RNR": 0x05, "REJ": 0x09, "SREJ": 0x0d}

// build makes a frame from its decoded fields.
// It returns nil if an address can't be encoded exactly (see encCall).
func build(fr *Frame) []byte {
	dst, ok := encCall(fr.Dst, false, false)
	if !ok {
		return nil
	}
	b := dst
	src, ok := encCall(fr.Src, len(fr.Via) == 0, false)
	if !ok {
		return nil
	}
	b = append(b, src...)
	for i, v := range fr.Via {
		via, ok := encCall(strings.TrimSuffix(v, "*"), i == len(fr.Via)-1, strings.HasSuffix(v, "*"))
		if !ok {
			return nil
		}
		b = append(b, via...)
	}
	// C bits: command in the destination's SSID byte, response in the
	// source's. Neither said (PF=): a command, as most UI senders send.
	if fr.Cmd >= 0 {
		b[6] |= 0x80
	} else {
		b[13] |= 0x80
	}
	pf := byte(0)
	if fr.PF {
		pf = 1
	}
	ns, nr := max(fr.NS, 0), max(fr.NR, 0)
	switch {
	case fr.Type == "I" && fr.Mod128:
		b = append(b, byte(ns<<1), byte(nr<<1)|pf)
	case fr.Type == "I":
		b = append(b, byte(nr&7)<<5|pf<<4|byte(ns&7)<<1)
	case fr.supervisory() && fr.Mod128:
		b = append(b, sCtl[fr.Type], byte(nr<<1)|pf)
	case fr.supervisory():
		b = append(b, byte(nr&7)<<5|pf<<4|sCtl[fr.Type])
	default:
		b = append(b, uCtl[fr.Type]|pf<<4)
	}
	if fr.PID >= 0 {
		b = append(b, byte(fr.PID))
	}
	return append(b, fr.Data...)
}

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
	Port  int    // direwolf radio channel
	TX    bool   // sent by this station (AGW 'T'), else heard (AGW 'K')
	Src   string // CALL-SSID
	Dst   string
	Via   []string // digipeaters, "*" suffix once repeated
	Type  string   // UI, I, RR, RNR, REJ, SREJ, SABM, SABME, DISC, DM, UA, FRMR, XID, TEST, U?
	PID   int      // -1 if none
	Info  int      // info field bytes
	Bytes int      // whole frame without flags and FCS
	Raw   []byte   // received: exact frame. Transmitted UI: rebuilt from the monitor text (C/R bits assumed). Else nil
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

// ParseAX25 decodes a raw AX.25 frame (no flags, no FCS).
func ParseAX25(f []byte) (*Frame, error) {
	if len(f) < 15 {
		return nil, errors.New("ax25: short frame")
	}
	fr := &Frame{PID: -1, Bytes: len(f), Raw: append([]byte(nil), f...)}
	fr.Dst, _ = decCall(f[0:7])
	fr.Src, _ = decCall(f[7:14])
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
	i++
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
	if fr.Type == "I" || fr.Type == "UI" {
		if i >= len(f) {
			return nil, errors.New("ax25: no PID")
		}
		fr.PID = int(f[i])
		i++
	}
	fr.Info = len(f) - i
	return fr, nil
}

// direwolf's monitor text for a transmitted frame, e.g.
//
//	" 1:Fm TEST-7 To PROBE Via WIDE1-1 <UI pid=F0 Len=21 PF=0 >[10:36:17]\rinfo...\r"
var monRe = regexp.MustCompile(`^\s*(\d+):Fm (\S+) To (\S+)(?: Via (\S+))? <(\S+)([^>]*)>`)

var monTypes = map[string]string{"UI": "UI", "I": "I", "RR": "RR", "RNR": "RNR", "REJ": "REJ", "SREJ": "SREJ",
	"SABM": "SABM", "SABME": "SABME", "DISC": "DISC", "DM": "DM", "UA": "UA", "FRMR": "FRMR", "XID": "XID", "TEST": "TEST"}

// ParseMonitor decodes an AGW 'T' record (a frame this station transmitted).
// AGW ports are numbered from 0; the text numbers them from 1.
func ParseMonitor(port int, data []byte) (*Frame, error) {
	s := strings.TrimRight(string(data), "\x00")
	m := monRe.FindStringSubmatch(s)
	if m == nil {
		return nil, fmt.Errorf("agw: unrecognised monitor text %q", s)
	}
	fr := &Frame{Port: port, TX: true, Src: m[2], Dst: m[3], PID: -1}
	if m[4] != "" {
		fr.Via = strings.Split(m[4], ",")
	}
	fr.Type = monTypes[m[5]]
	if fr.Type == "" {
		fr.Type = "U?"
	}
	for _, kv := range strings.Fields(m[6]) {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "pid":
			if p, err := strconv.ParseUint(v, 16, 8); err == nil {
				fr.PID = int(p)
			}
		case "Len":
			fr.Info, _ = strconv.Atoi(v)
		}
	}
	fr.Bytes = 14 + 7*len(fr.Via) + 1 + fr.Info
	if fr.PID >= 0 {
		fr.Bytes++
	}
	// A UI frame with printable info can be rebuilt from the text, for the trace.
	if fr.Type == "UI" && fr.PID >= 0 {
		if _, after, ok := strings.Cut(s, "\r"); ok {
			info := strings.TrimSuffix(after, "\r")
			if len(info) == fr.Info {
				fr.Raw = buildUI(fr, []byte(info))
			}
		}
	}
	return fr, nil
}

func encCall(s string, last, h bool) []byte {
	call, ssid := s, 0
	if c, n, ok := strings.Cut(s, "-"); ok {
		call = c
		ssid, _ = strconv.Atoi(n)
	}
	b := make([]byte, 7)
	for i := 0; i < 6; i++ {
		ch := byte(' ')
		if i < len(call) {
			ch = call[i]
		}
		b[i] = ch << 1
	}
	b[6] = 0x60 | byte(ssid&0x0f)<<1
	if last {
		b[6] |= 0x01
	}
	if h {
		b[6] |= 0x80
	}
	return b
}

func buildUI(fr *Frame, info []byte) []byte {
	b := encCall(fr.Dst, false, false)
	b = append(b, encCall(fr.Src, len(fr.Via) == 0, false)...)
	for i, v := range fr.Via {
		b = append(b, encCall(strings.TrimSuffix(v, "*"), i == len(fr.Via)-1, strings.HasSuffix(v, "*"))...)
	}
	// The monitor text drops the command/response bits; assume a command frame
	// (C bit in the destination SSID), as hnlink and most UI senders send.
	b[6] |= 0x80
	b = append(b, 0x03, byte(fr.PID))
	return append(b, info...)
}

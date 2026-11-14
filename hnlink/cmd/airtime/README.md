# airtime

Who is using the channel, by callsign-SSID. A receive-only AGW client of direwolf.

KISS clients see only received frames; direwolf never shows them what another
client transmitted. AGW monitoring does, so airtime turns on AGW raw frames (`k`:
every frame heard, byte for byte) and monitoring (`m`: including this station's own
transmissions, from Winlink, hnlink, APRS or anything else sharing the modem). Per
radio channel, per call-SSID, per direction it counts:

- frames, bytes, estimated airtime (frame length at the bit rate, plus one TXDELAY
  per key-up)
- AX.25 frame types (UI, I, RR, SABM, …), PIDs, destinations, digipeated frames
- channel occupancy over 1, 5 and 15 minutes
- traffic class of every frame: `aprs`, `connected`, `netrom`, `ip`, `hnlink`,
  `beacon` (other UI), `other`
- APRS: packets by kind (position, mic-e, message, ack, bulletin, status, object,
  item, weather, telemetry, …), stations heard in 15 min / 1 h / 24 h, each
  station's device (from its tocall, or the Mic-E maker code, as direwolf names
  them), last position, symbol, comment, path, digipeated packets
- connected-mode sessions: who opened them, modulo (8 or 128, from SABM/SABME or
  the frames), setup time (SABM to UA), length, how they ended (DISC, DM,
  refused, idle, reopened); per direction I frames and info bytes, first
  sendings apart from repeats (data vs. resent), REJ/SREJ, RNR, polls (T1
  timeouts) and FRMR

```sh
airtime                                   # direwolf AGW on 127.0.0.1:8000, page on :8075
airtime -v                                # also log every frame: a text packet trace
airtime -pcap trace.pcap                  # and a pcap for Wireshark (LINKTYPE_AX25)
```

| URL | |
|---|---|
| `/` | page: channels, traffic, sessions, APRS stations, stations; refreshes every 5 s |
| `/api/stats` | the same as JSON |
| `/metrics` | Prometheus, below |

| metric | labels | |
|---|---|---|
| `airtime_frames_total`, `airtime_bytes_total`, `airtime_seconds_total` | `port,call,dir` | by source call-SSID |
| `airtime_frames_by_type_total` | `port,call,dir,type` | AX.25 frame types |
| `airtime_class_{frames,bytes,seconds}_total` | `port,class,dir` | by traffic class |
| `airtime_channel_busy_ratio` | `port,window` | |
| `airtime_aprs_packets_total` | `port,kind,dir` | |
| `airtime_aprs_digipeated_total` | `port` | |
| `airtime_aprs_stations` | `port,window` (15m, 1h, 24h) | distinct stations |
| `airtime_aprs_devices` | `port,device` | stations in 24 h by device |
| `airtime_sessions_open` | `port` | |
| `airtime_sessions_total`, `airtime_session_seconds_total` | `port,from,to` | from opened it |
| `airtime_sessions_closed_total` | `how` | |
| `airtime_link_iframes_total`, `airtime_link_info_bytes_total` | `port,src,dst,kind` (new, repeat) | per direction |
| `airtime_link_events_total` | `port,src,dst,event` (rej, srej, rnr, poll, frmr) | per direction |
| `airtime_agw_up` | | |

`dir="tx"` is this station; `dir="rx"` is everything heard. `-tocalls` names the
tocall list (default `/usr/share/direwolf/tocalls.txt`, as direwolf installs it).
Counts are kept in memory: they start again with airtime, which Prometheus copes
with.

**What it can't see.** Frames direwolf could not decode (bad CRC) never reach any
client, so a heard direction's repeats are only those decoded twice; this
station's own direction is complete. Other stations' airtime counts their frames
but not their real TXDELAY. In the trace, received frames are exact. Transmitted
frames are rebuilt from direwolf's monitor text (type, N(S), N(R), P/F with
command or response, PID, the info byte for byte), modulo from the session; a
`PF=` frame (neither command nor response, AX.25 v1 or sloppy APRS) is written as
a command. Wireshark needs `ax25.extended` for modulo-128 links.

As a service: `/etc/airtime.conf`, `systemctl enable --now airtime`. direwolf needs
`AGWPORT` (default 8000). Set `DNSSD 0` in direwolf.conf unless you want direwolf
advertising its ports on the LAN.

## Version

`airtime -version` prints the semantic version (`buildinfo.Version`, bumped by
hand) and the commit, dirty flag and build time the binary was built from. The
same line starts the log. Set the commit and time at link time:

```
cd hnlink
go build -ldflags "-X hamnews/hnlink/internal/buildinfo.commit=$(git -C .. rev-parse HEAD) \
  -X hamnews/hnlink/internal/buildinfo.dirty=$(git -C .. diff --quiet && echo false || echo true) \
  -X hamnews/hnlink/internal/buildinfo.built=$(date -u +%FT%TZ)" -o airtime ./cmd/airtime
```

A plain `go build` has no commit unless the toolchain embedded VCS data (it
doesn't for this module layout), so the line then shows only the version.

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

```sh
airtime                                   # direwolf AGW on 127.0.0.1:8000, page on :8075
airtime -v                                # also log every frame: a text packet trace
airtime -pcap trace.pcap                  # and a pcap for Wireshark (LINKTYPE_AX25)
```

| URL | |
|---|---|
| `/` | page: channels and stations, refreshes every 5 s |
| `/api/stats` | the same as JSON |
| `/metrics` | Prometheus: `airtime_frames_total`, `airtime_bytes_total`, `airtime_seconds_total` `{port,call,dir}`, `airtime_frames_by_type_total`, `airtime_channel_busy_ratio{port,window}`, `airtime_agw_up` |

`dir="tx"` is this station; `dir="rx"` is everything heard.

**What it can't see.** Frames direwolf could not decode (bad CRC) never reach any
client. Other stations' airtime counts their frames but not their real TXDELAY. In
the trace, received frames are exact. Transmitted frames are rebuilt from direwolf's
monitor text: exact for UI frames except the command/response bits (assumed
command), and left out for connected-mode frames, which are still counted.

As a service: `/etc/airtime.conf`, `systemctl enable --now airtime`. direwolf needs
`AGWPORT` (default 8000). Set `DNSSD 0` in direwolf.conf unless you want direwolf
advertising its ports on the LAN.

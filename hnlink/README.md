# hnlink

Automatic Usenet exchange for a local amateur radio net, plus the batch tools for
FLAMP nets. It sits between your news server (hamnews/INN on `localhost:119`) and a
KISS TNC (direwolf). Stations beacon what they hold, work out among themselves who
sends what, and send it once, erasure-coded so one repair transmission fixes every
receiver at once. It relays across simplex hops automatically. The wire format is in
[PROTOCOL.md](PROTOCOL.md).

```
 Thunderbird / tin ──NNTP──▶ hamnews (innd) ◀──NNTP── hnlink ──KISS/TCP──▶ direwolf ──audio/PTT──▶ radio
                                                       │
                                     http://localhost:8073 (status + posting)
```

hnlink needs only TCP: NNTP to innd and KISS to the TNC. It runs unprivileged and
writes no files (except `--log`). The same program runs on Linux (x86-64, arm64,
armv7) and natively on Windows.

## Commands

```
hnlink run   --call CALL-SSID [options]   automatic exchange (the daemon)
hnlink batch [--days 14] FILE             write the window's articles as an rnews batch (FLAMP send)
hnlink load  FILE...                      load rnews batches, .gz ok (FLAMP receive); repeats harmless
hnlink version
```

`batch` and `load` work over NNTP, so they need no sudo and run on any machine that
can reach the server.

### `run` options

| Option | Default | |
|---|---|---|
| `--call` | (required) | AX.25 callsign-SSID to transmit as. Use an SSID not used by anything else on this TNC (Winlink, APRS) |
| `--kiss` | `127.0.0.1:8001` | the TNC: `HOST:PORT` for KISS TCP (direwolf), or `/dev/ttyUSB0[:BAUD]` for a hardware TNC in KISS mode (default 9600 baud; Linux). On serial, hnlink sets the TNC's TXDELAY from `--txdelay` |
| `--nntp` | `localhost:119` | news server |
| `--groups` | `*,!control,!control.*,!junk` | wildmat of groups to exchange |
| `--days` | `14` | exchange window |
| `--http` | `127.0.0.1:8073` | status/posting page; `""` disables |
| `--from` | the callsign | From: on posts made on the page |
| `--log` | | also append the log to this file |
| `--bitrate` | `1200` | channel bit rate, used to pace transmissions |
| `--txdelay` | `300ms` | key-up time per burst, for pacing; match direwolf's TXDELAY |
| `--burst` | `6` | frames per key-up |
| `--quiet` | `3s` | channel must be idle this long before keying |
| `--symsize` | `192` | bytes of payload per frame (multiple of 64) |
| `--inv` | `30m` | inventory beacon interval |
| `-v` | | log protocol detail |

The packages run it as the systemd service `hnlink`, with these options set in
`/etc/hnlink.conf` (`CALL`, `FROM`, `KISS`, `NNTP`, `HTTP`, and anything else in
`OPTS`). The Windows installer registers a logon task instead.

### Hardware TNC on a serial port

`KISS=/dev/ttyUSB0:9600` in `/etc/hnlink.conf`. The TNC must already be in KISS
mode. hnlink opens the port raw (8N1, no flow control) and sends KISS TXDELAY, and
takes the port for itself. Nothing else can use that TNC while hnlink runs, so to
share a radio with Winlink, put direwolf on it instead. The service sandbox hides
devices; allow the port with `systemctl edit hnlink`:

```ini
[Service]
PrivateDevices=no
DeviceAllow=/dev/ttyUSB0 rw
SupplementaryGroups=uucp
```

(`dialout` instead of `uucp` on Debian, Ubuntu and Pi OS.) itsjustnews takes the
same `-kiss /dev/ttyUSB0:9600` and only reads.

## Monitoring: itsjustnews

`itsjustnews` (its own package) is a receive-only decoder for anyone checking that
the traffic is plain usenet (§97.113(a)(4)). It reads direwolf's KISS port, or a
capture made with `-record`, and has no transmit path. It explains every frame,
decodes the objects exactly as `hnlink run` does, and prints the articles in full.
`-frames` also shows the plain text carried in each data symbol (the code is
systematic). See OPERATIONS.md §4.1.

## The page

`http://localhost:8073` shows:
- the stations heard, how many articles each has, what each lacks, and whom each hears;
- what is on the air now (sending, and receiving with progress);
- the message list, with a reader;
- a box for posting and replying;
- the activity log.

It listens on localhost only. **Anything posted there is transmitted under your
callsign**, so the page refuses cross-site and non-localhost requests. Don't
expose it with `--http 0.0.0.0:…` unless everyone who can reach it is a licensed
operator you trust with your callsign. Any newsreader works too (Thunderbird: add a
"Newsgroup account", server `localhost`, port 119).

## direwolf

hnlink needs a KISS port. A minimal `direwolf.conf` for a USB sound interface:

```
ADEVICE plughw:1,0          # your interface; 'aplay -l' / 'arecord -l' list them
ACHANNELS 1
CHANNEL 0
MYCALL KN6FLN               # direwolf's own call (beacons, digipeating if any)
MODEM 1200
TXDELAY 30                  # 300 ms; match hnlink --txdelay
PTT CM108                   # or: PTT /dev/ttyUSB0 RTS, or VOX via the radio
KISSPORT 8001
AGWPORT 8000
```

- **PTT.** direwolf ships a `cm108` tool that lists USB audio devices with a usable
  GPIO for PTT (the CM108/CM119 family). Run it with the interface plugged in. If
  your interface isn't listed, use whatever its PTT really is: a serial RTS/DTR line,
  a hamlib-controlled radio (`PTT RIG ...`), or VOX.
- **Yaesu HRI-200.** It isn't documented as a CM108-family device and I haven't
  checked one. Plug it in and run `lsusb` and `cm108`. If it shows as USB audio
  with GPIO, use `PTT CM108`; if it shows only audio plus a USB serial port, try
  that port's RTS. Set its audio levels with direwolf's level display: aim for
  received audio around 50.
- **9600 baud** (`MODEM 9600`, radio data port): run hnlink with `--bitrate 9600`.
  Larger `--symsize` (448) cuts per-frame overhead. Keep `--burst` modest so other
  users of the channel get turns.
- **direwolf 1.8 `KISSPORT`** adds ports. The default 8001 stays open unless the
  config says `KISSPORT 0` first.

### Sharing the TNC with Winlink (or anything else)

direwolf serves several KISS and AGW clients at once and gives every received frame
to each of them. Run hnlink on its own SSID (`KN6FLN-5`) beside a Winlink node on
the same direwolf:
- Pat: `"engine": "agwpe"` → direwolf `AGWPORT 8000`.
- linbpq: KISS over TCP → 8001.

hnlink sends only UI frames to `HNLINK`, which connected-mode software ignores. It
treats every frame it hears, Winlink sessions included, as a busy channel and waits
for quiet, in short bursts.

Linux's in-kernel AX.25 stack (kissattach) is gone from recent mainline kernels.
Use direwolf's AGW or KISS ports instead.

## Operating notes

- **Station ID.** Every frame carries your callsign as the AX.25 source address.
  hnlink transmits unattended, which is automatic control: allowed for data on 6 m
  and up (§97.109(d)), and on HF only in the §97.221 automatically-controlled
  sub-bands.
- **Groups must match** across the net. A station without a group refuses its
  articles, and hnlink offers them again every hour (`ResendHold`) until the group
  exists.
- **Cancels don't travel.** A cancel removes an article at your station only (see
  OPERATIONS.md §7). Your hnlink stops offering it, but stations that already have it
  keep it.
- **First contact.** Two stations converge within a few minutes of hearing each
  other's beacons: about 20 s debounce, then a few seconds to elect, then airtime.
  At 1200 baud a 6 KB batch is about a minute of airtime.

## Logs

- Linux: `journalctl -u hnlink -f`. Restart with `-v` in the unit for protocol detail.
- Windows: `%LOCALAPPDATA%\hamnews\hnlink.log`, or `hamnews status`.

Typical lines:

```
new peer W6ABC-5
peer W6ABC-5 gen 4: 37 articles
elected to send 3 article(s)
object 900caae397b5b495: 3 article(s), 6236 bytes, k=33
object 68cbe9347ed601e5 from W6ABC-5: 2 accepted, 0 duplicate, 0 rejected, 0 deferred
```

## Building

```sh
go build . && go build ./cmd/itsjustnews
go test ./...         # includes multi-station simulations over a lossy channel (~30 s)
```

Dependencies are vendored. The only one is `github.com/klauspost/reedsolomon`. The
end-to-end test (two innd instances, two direwolf modems linked through PipeWire
null sinks, real 1200-baud AFSK) is `../test/e2e.sh`. Packages are built by
`../packaging/build-deb.sh` and `../packaging/build-arch.sh`.

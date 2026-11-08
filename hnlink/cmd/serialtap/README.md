# serialtap

A passive tap for a serial link between a computer and a TNC. It passes every
byte both ways unchanged, decodes KISS frames as they go by, counts them for
Prometheus, and offers KISS over TCP to clients: monitors, filtered stations,
and read-write clients that can send frames to the TNC.

Status: not deployed, not tested against real hardware. Unit tests use
synthetic KISS frames and loopback sockets.

## Usage

```
serialtap -a /dev/ttyV0 -b /dev/ttyUSB0 -baud 1200     tap a link
serialtap -b /dev/ttyUSB0 -detect                      find the TNC's baud rate
```

- `-a` is the computer side, `-b` the TNC side. Both are set to 8N1 at `-baud`
  with `stty`.
- `-detect` reads `-b` at 9600, 4800, 2400 and 1200 for `-sample` each (default
  10 s) and reports which rate yields KISS frames. It doesn't write to the TNC.
- `-http` serves Prometheus metrics at `/metrics` (default `127.0.0.1:9181`;
  `""` disables).
- `-log PATH` appends each KISS frame as a hex line.

## TCP listeners

All listeners are optional. Each one is an address, optionally followed by a
filter. Give the address as `HOST:PORT`. Bind to `127.0.0.1` or an address
you trust: nothing authenticates clients.

| Flag | Clients get | Input from client |
|------|-------------|-------------------|
| `-monitor ADDR` | every frame | discarded |
| `-station ADDR=FILTER` | frames whose source or destination matches | discarded |
| `-rw ADDR[=FILTER]` | frames matching the filter (none = all) | data frames go to the TNC |

Filters are a comma-separated list of `CALL-SSID` (exact) or `CALL-*` (any SSID
of CALL). A station must have a filter. A monitor must not. Repeat `-monitor`,
`-station` or `-rw` for more listeners.

Example:

```
serialtap -a /dev/ttyV0 -b /dev/ttyUSB0 -baud 1200 \
  -monitor 127.0.0.1:8100 \
  -station 127.0.0.1:8101=W8HHF-10 \
  -rw 127.0.0.1:8200
```

Each connection gets its own queue. A monitor's queue is 256 frames and drops
silently, with the count kept. A station's queue is 4096 frames, and drops are
logged, since a lost frame breaks a session.

## Transmit

Frames from `-rw` clients are queued for the TNC. Rules:

- Only KISS data frames on port 0 are accepted. TNC settings from clients (for
  example TXDELAY) are dropped, so the tap never changes the TNC's
  configuration.
- The queue is sized in seconds of line time: `seconds × baud / 10` bytes, with
  `defaultTxSeconds` = 60. A frame that would exceed the budget is refused and
  logged.
- Frames are written whole, and paced so the next one waits for the previous
  one's line time.
- A frame is written only when the host's byte stream is between frames, or
  after the host has been silent for 2 s. This keeps injected frames from
  splitting one of the host's frames. The 2 s fallback means a host that stalls
  mid-frame for that long will lose that frame.
- Transmitted frames are offered to all listeners, the same as frames the host
  sends.

## Metrics

- `serialtap_bytes_total{dir}`
- `serialtap_frames_total{dir,cmd}`
- `serialtap_errors_total{dir}`
- `serialtap_src_frames_total{dir,call}`, `serialtap_src_bytes_total{dir,call}`

`dir` is `host` (computer to TNC), `tnc` (TNC to computer), or `net` (frames
sent from a read-write client). `cmd` is the KISS command: 0 for data.

Per-listener metrics aren't done yet.

## Files

- `kiss.go`: KISS decoder and AX.25 address parsing
- `detect.go`: baud-rate scoring
- `filter.go`: listener filters
- `hub.go`: per-connection clients, queues, fan-out
- `listen.go`: listener flags, accept loop, client connections
- `tap.go`: the two link directions and frame handling
- `tnc.go`: the TNC write gate, transmit pacing
- `txqueue.go`: transmit queue and byte budget
- `stats.go`: Prometheus output

## Not done

- Per-listener metrics
- Config file (flags only)
- Airtime estimate (1200 baud plus TXDELAY)
- systemd unit and Prometheus job
- Hardware check: `-detect` and a real TNC on the line

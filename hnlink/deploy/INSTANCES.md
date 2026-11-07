# Running more than one hnlink on a host

One host can run several channels, for example two repeaters. Each channel gets
its own **instance**: one radio, one direwolf, one hnlink. All instances on the
host share one innd, so an article heard on one channel is offered on the others.
That's the bridge. Deployment is templated (`systemd/*@.service`), and each instance
reads its own files (`etc/*/<instance>.*`).

## Naming

The instance name (`repeater-a`) is the unit's `%i`. It names the config files and
the journal entries. Use lowercase letters, digits, and dashes.

## Per-instance files

| Path | Holds |
|---|---|
| `/etc/hnlink/<instance>.conf` | CALL, KISS, NNTP, HTTP, METRICS, MODE, OPTS |
| `/etc/direwolf/<instance>.conf` | the audio device, PTT, and the KISS and AGW ports |
| `/etc/rigctld/<instance>.env` | rig model, serial device, and rigctld port (only if you use rig control) |

Copy the `.example` files and edit them. Every value marked PLACEHOLDER must be
set for that radio.

## Ports

Give every instance its own ports. Nothing is shared except innd.

| Instance | direwolf KISS | direwolf AGW | hnlink HTTP | hnlink metrics | rigctld |
|---|---|---|---|---|---|
| repeater-a | 8001 | 8000 | 8073 | 9178 | 4532 |
| repeater-b | 8002 | 8010 | 8074 | 9179 | 4533 |

Add a row for each new instance, keeping the same pattern: KISS and HTTP and
metrics count up from the first instance, and AGW and rigctld can be any free
port. Bind everything to 127.0.0.1 except metrics, which Prometheus must reach
(see below). direwolf binds all interfaces for KISS and AGW, so the firewall
must still drop them from outside the host until direwolf's binding is confirmed.

## Callsigns

Every station on the net needs a unique CALL-SSID, including two instances on one
host. Record each one here as it's assigned:

| CALL-SSID | Instance | Channel | Notes |
|---|---|---|---|
| KN6FLN-4 | (tmra host) | 146.835 MHz, TMRA | |
| KN6FLN-5 | yakspeed, repeater-a | | |
| KN6FLN-6 | yakspeed, repeater-b | | proposed |

## Mode

Both instances on a channel must use the same mode as the other stations on
that channel. Repeater mode is for stations that hear each other, which is the
case for a repeater. A channel with simplex stations uses `MODE=simplex`.

## Monitoring

- Logs: `journalctl -u hnlink@repeater-a -u hnlink@repeater-b`
- Prometheus: one target per instance, each labelled with its instance and hostname.
  The `hostname` label alone can't tell two instances on one host apart.
- Metrics bind: a LAN address for a remote Prometheus, as with any other exporter.
  The endpoint has no authentication.

## Bridge behaviour

- An article is held once per host, in innd. The history file prevents loops:
  an article already held isn't offered again.
- Each channel's inventory, elections, and repair happen on its own radio.
  Stations on channel B never see channel A's traffic directly.
- Anything heard on one channel is carried to the other. That's the trust decision:
  everything either channel carries goes to both. If that's not wanted, a channel
  needs its own innd.
- The test `TestBridgeCarriesBetweenChannels` checks this path with simulated
  channels and a shared store.

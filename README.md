# hamnews

Usenet for amateur radio: every station runs its own news server, and the servers
keep each other in sync over the air. No internet, no repeater, no central server.

- **Local net (VHF/UHF, simplex is fine): hnlink.** Stations announce what they hold.
  Anything missing goes out once, as Reed-Solomon coded AX.25 UI frames through
  direwolf, and *every* station that hears it decodes it. That is one transmission
  for the whole channel, not one per peer. Stations that can't hear each other are
  filled by relay, hop by hop.
- **Long haul (HF): FLAMP.** `hnlink batch` writes an rnews batch for a FLAMP net;
  `hnlink load` takes one in. A gateway station on both carries a local net's
  traffic out and brings other nets' traffic back.
- **Monitoring: itsjustnews.** A receive-only decoder that shows anyone what is on
  the air, frame by frame and article by article. Nothing is obscured
  (§97.113(a)(4)), and this lets you check that for yourself.
- **Channel accounting: airtime.** Who uses the channel, by callsign-SSID, including
  this station's own Winlink, APRS and hnlink transmissions, which KISS clients
  never see. Page, Prometheus metrics, and a pcap trace for Wireshark.

All of it hangs off one direwolf on the radio: each program is just another client
of its KISS or AGW port, and none needs to know about the others.

| Directory | |
|---|---|
| `hnlink/` | hnlink, itsjustnews and airtime (Go, vendored; `cmd/`); `PROTOCOL.md` is the over-the-air spec |
| `packaging/` | Debian (`debian/`, `build-deb.sh`), Arch (`arch/PKGBUILD`, `build-arch.sh`), shared config and units (`common/`), `release.sh` |
| `windows/` | WSL-based installer for Windows stations |
| `flamp/` | FLAMP 2.2.14 patch for the received-filename traversal bug (OPERATIONS.md §2.1) |
| `test/` | end-to-end test over real AFSK (`e2e.sh`), package install tests in booted containers (`run-all.sh`) |
| `OPERATIONS.md` | setting up a station and running the nets |

## Packages

| | amd64 | arm64 | armhf (ARMv6+, every Pi) | x86_64 Arch |
|---|---|---|---|---|
| `hamnews` (INN 2.7.4, own namespace, coexists with `inn2`) | ✓ | ✓ | ✓ | ✓ |
| `hnlink` | ✓ | ✓ | ✓ | ✓ |
| `itsjustnews` | ✓ | ✓ | ✓ | ✓ |
| `airtime` | ✓ | ✓ | ✓ | ✓ |

Debian packages are built in Debian 12 (Raspbian 12 for armhf), so they install on
Debian 12+, Ubuntu 24.04+ and Raspberry Pi OS. Install and setup: OPERATIONS.md §3.

## Building

```sh
sudo packaging/mkroots.sh        # once: debootstrap bookworm amd64/arm64 + Raspbian armhf build roots
packaging/build-deb.sh           # -> dist/deb/
packaging/build-arch.sh          # -> dist/arch/
packaging/release.sh             # -> dist/release/hamnews-<ver>/ with SHA256SUMS
test/run-all.sh                  # build everything, install-test each in a booted container
```

arm64 and armhf are built under qemu-user (binfmt with the `F` flag). The INN
tarball is fetched from ftp.isc.org and checked against Russ Allbery's signing key
(bundled in `packaging/arch/keys/`).

## License

INN is under its own permissive licenses (see its `LICENSE`). The hamnews packaging,
hnlink, itsjustnews and airtime are ISC (`LICENSE`).

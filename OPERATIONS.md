# hamnews: net operations guide

KN6FLN · 2026-11-05

Every station runs a small local news server, hamnews (INN 2.7.4). Read and post
with any newsreader (tin, Thunderbird) or the hnlink page. News moves between
stations two ways:

- **Local net, automatic: hnlink.** Stations on a shared VHF/UHF channel exchange news
  on their own through direwolf. Each beacons what it holds; the stations work out who
  sends what; each article goes out once and is repaired for everyone at once. It
  relays across simplex hops. Nobody has to run a net.
- **Long haul, by hand: FLAMP.** Between areas, rnews batch files go over fldigi/FLAMP
  nets. A **gateway** station that is on both loads what arrives, and its local net
  spreads it. It also batches its local net's traffic for the next FLAMP net.

There is no central server and no internet. Duplicate delivery is always harmless:
every server refuses Message-IDs it already has.

---

## 1. How it works

```
                       ┌──────────── your station ─────────────┐
 Thunderbird / tin ──NNTP──▶ hamnews (innd, localhost:119)
 hnlink page ──────────────────────▲   ▲
                                   │   │ NNTP
                     hnlink run ───┘   └─── hnlink batch / load
                         │                        │
                     direwolf (KISS)        FLAMP tx/ and rx/ files
                         │                        │
                  local net (VHF/UHF)      fldigi/FLAMP net (long haul)
```

- **Store and forward.** A station offers every article it holds that *arrived* within
  the window (14 days): its own posts and everything it received, whichever way.
  That is how traffic reaches stations out of the original sender's range, and how a
  gateway joins the two kinds of net.
- **Dedupe by Message-ID.** Each server's history refuses articles it has already
  seen, so overlap, repeats and loops do no harm. Each station's `pathhost` is added
  to the `Path:` header, which records the route.
- **Rolling window.** There are no per-peer queues. Anything a station misses comes
  around again while it is still in the window.

## 2. Before joining the net

### 2.1 FLAMP security warning

Stock FLAMP 2.2.14 and earlier trust the filename in a transmission. With *Auto save rx
files* on (the default), any station can overwrite files in your home directory by
sending a name like `../../.bashrc`. The patched build (`flamp 2.2.14-2`) fixes this.
hnlink is not affected: nothing it receives is ever used as a filename.

### 2.2 Station identity

| Setting | Where | Rule |
|---|---|---|
| `pathhost`, `domain` | `/etc/hamnews/inn.conf` | **Your callsign**, lowercase, unique on the net. Message-IDs become `<…@host.callsign>` |
| From address | tin `mail_address=`, Thunderbird identity, `FROM=` in `/etc/hnlink.conf` | `CALL <you@example>`. It goes out over RF in every post |
| hnlink callsign | `CALL=` in `/etc/hnlink.conf` | Your call plus an SSID used by nothing else on your TNC, e.g. `KN6FLN-5` |

On Debian, Ubuntu and Raspberry Pi OS the packages ask for these (`dpkg-reconfigure
hamnews`, `dpkg-reconfigure hnlink` to change them). On Arch, edit the two files.

### 2.3 Net-wide agreements

These must match at every station. Settle them before the first exchange:

1. **The group list.** A station refuses articles for groups it does not have, and
   **does not relay them**. Every station creates exactly the same groups (§7).
2. **Window length.** 14 days by default. Every station should use the same value or
   longer.
3. **FLAMP file naming.** `<call>-<YYYYMMDD>[-<n>].rnews`, for example
   `kn6fln-20261105.rnews`. Letters, digits, `-` and `.` only.
4. **Local net channel.** A frequency and mode (1200 baud AFSK unless everyone can do
   9600) that everyone's direwolf uses.

## 3. Station setup

Three packages, for Debian 12+/Ubuntu 24.04+/Raspberry Pi OS (amd64, arm64, and armhf
built for ARMv6, so every Pi) and Arch (x86_64):

| Package | What |
|---|---|
| `hamnews` | the news server: INN 2.7.4 in its own namespace (`/etc/hamnews`, `/usr/lib/hamnews`, `/var/lib/hamnews`, `/var/spool/hamnews`). It does not touch the distro's `inn2`/`inn` |
| `hnlink` | the local-net exchange, its status page, and the FLAMP batch tools |
| `itsjustnews` | receive-only monitor (§4.1); needs neither of the others |
| `airtime` | channel use by callsign-SSID from direwolf's AGW port: page on `:8075`, Prometheus `/metrics`, optional pcap trace (§4.2) |

Debian family:

```sh
sudo apt install ./hamnews_0.1-1_arm64.deb ./hnlink_0.1-1_arm64.deb
```

Answer the callsign questions (or preseed them: `echo 'hamnews hamnews/callsign string
kn6fln' | sudo debconf-set-selections`, and `hnlink/call`, `hnlink/from` likewise).
Then put the net's group list in `/etc/hamnews/groups` and `sudo systemctl restart
hamnews`; missing groups are created at every start.

Arch:

```sh
sudo pacman -U hamnews-0.1-1-x86_64.pkg.tar.zst hnlink-0.1-1-x86_64.pkg.tar.zst
sudoedit /etc/hamnews/inn.conf /etc/hamnews/groups /etc/hnlink.conf
sudo systemctl enable --now hamnews hnlink
```

Windows: `windows\install-windows.cmd -Call <yourcall> -From "<CALL> <you@example.com>"`.
The news server is the same `.deb` inside WSL; hnlink, FLAMP and direwolf run on
Windows. See `windows/README.md`.

Until `pathhost` is set, hamnews is skipped at start (not failed, not restarted);
likewise hnlink until `CALL` is set. Upgrades keep every edited file in
`/etc/hamnews` (dpkg conffiles; pacman `backup`). `inn.conf` and `/etc/hnlink.conf`
are generated on Debian from the debconf answers, and hand edits to them win.

### 3.1 Configuration (`/etc/hamnews/`)

**inn.conf** (the parts that matter)
```
pathhost:       <yourcall>
domain:         <yourcall>
server:         localhost
bindaddress:    127.0.0.1            # set a LAN address to serve other machines
bindaddress6:   "::1"
mta:            "/bin/true %s"
ovmethod:       tradindexed
hismethod:      hisv6
enableoverview: true
pgpverify:      false
docancels:      auth
```
`docancels` must be set explicitly. This build has no Cancel-Lock support, so the
compiled default is `none` and cancels would be stored but never carried out.

**storage.conf**: one `tradspool` method, class 0.
**newsfeeds**: `ME:*::`, with no outgoing feeds.
**incoming.conf**: `peer ME` for `localhost, 127.0.0.1, ::1`. hnlink and rnews feed by
IHAVE from this machine.
**readers.conf**: localhost may read and post everything.
**expire.ctl**: `/remember/:11` and `*:A:1:365:never`.

To let other machines read (a laptop on your LAN, for example), set `bindaddress` to a LAN
address and add a `readers.conf` block for them. **Anyone who can post can make your
station transmit under your callsign**, so give such access only to licensed
operators, or make it read-only.

### 3.2 Services

| Unit | What |
|---|---|
| `hamnews.service` | innd, as user `news` |
| `hnlink.service` | `hnlink run` with the settings in `/etc/hnlink.conf`, sandboxed with `DynamicUser`. It retries until direwolf and innd are up |
| `direwolf.service` | yours to configure (see `/usr/share/doc/hnlink/README.md`) |

### 3.3 FLAMP settings

| Setting | Value | Why |
|---|---|---|
| Version | 2.2.14 (patched 2.2.14-2 if available) | §2.1 |
| Compression | **on** | rnews text compresses well. Don't also use `hnlink batch -z` |
| Auto save rx | on | received files land in `~/.nbems/FLAMP/rx/YYYY_MM_DD/` |

### 3.4 Reading and posting

- **tin**: `export NNTPSERVER=localhost` in `~/.bashrc`, then `tin -r`.
- **Thunderbird**: add a *Newsgroup account*, server `localhost`, port 119, no TLS.
  Set your From identity, and leave Organization empty.
- **The hnlink page**: `http://localhost:8073`. It lists messages, reads, posts and
  replies.

## 4. Local net: hnlink

Once direwolf is on the air and `hnlink.service` is running, there is nothing to do.
Post as usual; the next exchange carries it. Watch the page or
`journalctl -u hnlink -f`:

- **Stations heard.** Each station, how many articles it has, what it still needs from
  you, what you need from it, and whom it hears. Counts come from each station's last
  beacon. A station marked "doesn't hear us yet" can't receive from you directly;
  another station will relay to it.
- **On the air.** What you are sending (round, fill requests) and what you are
  receiving (blocks so far of how many needed).
- **Timing.** After you post, expect it to go out within about half a minute if the
  channel is free. A neighbour converges in a few minutes; each further simplex hop
  adds a few minutes more.

Etiquette on a shared channel:
- hnlink waits for 3 s of silence before keying and sends at most 6 frames per
  key-up, so voice users and Winlink sessions get turns. Raise `--quiet` on a busy
  channel.
- Inventory beacons are small but periodic (every 30 min, and after changes). On a
  channel that is also a voice net, consider a separate frequency.
- Unattended data transmission is automatic control: allowed on 6 m and up, and on HF
  only in the §97.221 sub-bands.

### 4.1 Monitoring: itsjustnews

Anyone can check that what goes out is plain usenet and that nothing obscures its
meaning (§97.113(a)(4)). `itsjustnews` connects to a direwolf KISS port (or reads a
capture file). It never transmits. It explains every hnlink frame, decodes the coded
blocks exactly as a station does, and prints each article in full:

```sh
itsjustnews                         # direwolf on 127.0.0.1:8001
itsjustnews -frames                 # every frame, including the plain text inside data blocks
itsjustnews -record net.kiss        # keep the raw stream; replay later with -file net.kiss
itsjustnews -q -save ./decoded      # articles only, and each batch as an .rnews file
```

The coding is systematic: the first K blocks of each object *are* the rnews text, cut
into pieces, and the repair blocks only stand in for lost ones. With `-frames` the
text is visible even before an object completes. The over-the-air format is in
`/usr/share/doc/hnlink/PROTOCOL.md`.

### 4.2 Channel accounting: airtime

`airtime` shows who is using the channel: frames, bytes and estimated airtime for
each callsign-SSID heard, and for this station's own transmissions (Winlink, APRS,
hnlink: everything on the modem). It also shows channel occupancy over 1, 5 and
15 minutes. `systemctl enable --now airtime`, then `http://localhost:8075`.
Prometheus scrapes `/metrics`. `-pcap FILE` (in `OPTS=` in `/etc/airtime.conf`)
keeps a packet trace that Wireshark decodes. It needs direwolf's `AGWPORT`, and only
listens. Details: `/usr/share/doc/airtime/README.md`.

## 5. Long haul: FLAMP

### 5.1 Sending

```sh
hnlink batch ~/.nbems/FLAMP/tx/kn6fln-$(date +%Y%m%d).rnews
```

It reports the article count and size. Add the file in FLAMP and transmit.

- `--days N` changes the window (default 14). `-z` gzips the batch; use it only when
  FLAMP compression is off.
- junk and `control.*` are never sent.
- An empty batch (`0 articles`) is normal on a quiet net; skip that send.

### 5.2 Receiving

When FLAMP shows the file complete (all blocks received, CRC good):

```sh
hnlink load ~/.nbems/FLAMP/rx/*/*.rnews*
```

- Received files are in **dated subdirectories** of `rx/`; the `*/` in the command
  matters.
- It prints, per file, the counts **accepted** (new, now readable and offered onward),
  **duplicate** (already had them, which is normal), **rejected** (innd would not take
  them; the reason is printed, most often a missing group, §7) and **deferred** (try
  again later).
- Running it again over the same files is harmless.

## 6. Gateway stations

A gateway is on a local net (hnlink) and takes part in FLAMP nets. It needs nothing
special:

1. **After a FLAMP net:** `hnlink load` the received batches. hnlink offers the new
   articles to the local net automatically.
2. **Before a FLAMP net:** `hnlink batch` includes everything that reached the gateway
   in the window, so it carries the local net's traffic, as well as the gateway's own
   posts, out over the long haul.

Two gateways on the same local net cause no harm: duplicates are refused
everywhere. Agree who sends on which FLAMP net, to save airtime.

## 7. Groups and cancels

```sh
echo ham.<name> | sudo tee -a /etc/hamnews/groups && sudo systemctl restart hamnews
sudo -u news /usr/lib/hamnews/bin/ctlinnd rmgroup ham.<name>    # and delete its line
```

Restarting creates any group listed in `/etc/hamnews/groups` that the server lacks; it
never removes one. Keep the net's list somewhere everyone can copy it (the Windows
installer takes it as `groups.txt`). In tin,
press `y` to show every group and `s` to subscribe.

**Neither group changes nor cancels travel over RF.**
- New or removed groups are agreed by voice or by a post, and **every station changes
  its own list**. A station missing a group refuses that traffic and does not relay
  it. hnlink keeps offering such articles to it every hour until the group exists.
- A cancel (`Shift-D` in tin) removes the article **only at your station**, and stops
  you offering it again. Copies other stations already have stay. This is
  deliberate. Without Cancel-Lock nothing checks who sent a cancel, so honouring
  cancels from RF would let any station delete anyone's posts.

## 8. FLAMP net procedure

FLAMP fills are **operator-driven**. FLAMP decodes other stations' missing-block
reports automatically, but nothing transmits fills by itself. Someone presses Fetch
and then transmit. (Checked in the FLAMP 2.2.14 source and the FLAMP 2.2 user
manual.) hnlink does this automatically on the local net; on FLAMP nets, the
procedure below covers it.

1. **Check-in.** Net control takes check-ins and confirms the group list and window
   if either has changed.
2. **Batch announcements.** Each station gives callsign, article count and batch size
   in bytes (from `hnlink batch`). Large batches go last.
3. **Send in turn.** Net control calls each station. It adds its batch in FLAMP and
   presses **Xmit**.
4. **Reports.** After each batch, net control calls the receiving stations in turn.
   Each one selects the file in FLAMP's Receive tab and presses **Report**. That puts
   `<MISSING …>{hash}3 7 12` (or `CONFIRMED`) in fldigi's transmit buffer. The station
   transmits it on its turn.
5. **Fills.** The sending station presses **Fetch**. FLAMP merges every report it
   decoded for that file into one block list, with block 0 standing for missing header
   data. It then presses **Xmit**, which sends only those blocks. **Shift+Xmt All**
   sends fills for every queued file that has reports and skips the rest.
6. **Relays.** A station that is out of the sender's range is filled by any station
   that has the blocks. That station selects the file in its Receive tab, presses
   Fetch, then relays. Net control assigns who relays.
7. **Repeat 4–6** until every station reports `CONFIRMED`, or net control moves on.
   Anything still missing arrives in the next session's batch (the 14-day window).
8. **After the net.** Every station runs `hnlink load` and checks the counts.

FLAMP's scheduled events (Events tab) and its XML-RPC `transmit_queue` always send the
**whole** queue, never only the fills. Use them for unattended beacons of the batch,
not for fills.

## 9. Bandwidth and troubleshooting

**Batch too big for a FLAMP session:** shorten the window for that send
(`hnlink batch --days 7 …`). Every station must still send the full window often enough
that nothing falls out before it has been relayed. Keep posts short and plain text:
no quoted-reply chains and no attachments. At 1200 baud, local-net throughput across
several simplex hops is on the order of 3 MB a day.

| Symptom | Likely cause | Fix |
|---|---|---|
| `rejected` > 0, or a station is missing traffic | Most often a group not created at that station | Create it (§7). It arrives with the next exchange |
| The same article keeps going out every hour | A peer lacks its group (§7) | That station creates the group |
| hnlink page: TNC not connected | direwolf not running, or a different KISS port | `systemctl status direwolf`; check `KISSPORT` and `--kiss` |
| No stations heard | Wrong frequency or mode, audio levels, PTT | direwolf's console shows what it decodes and when it keys |
| A peer "doesn't hear us yet" | One-way path (power, antenna, noise at their end) | Normal on marginal paths; another station relays to it |
| Received FLAMP file not found | It's in `rx/YYYY_MM_DD/` | Use the `rx/*/` glob (§5.2) |
| `hnlink batch` reports 0 articles but you posted | Cancelled, or older than the window | `--days` larger; check in tin |
| A cancelled post still shows at other stations | Cancels don't travel (§7) | Ask the other operators to cancel it locally |
| tin shows no groups | `NNTPSERVER` unset, or tin run without `-r` | `NNTPSERVER=localhost tin -r` |
| hamnews down | | `systemctl status hamnews`, `journalctl -u hamnews` |

## 10. Quick reference

| Task | Command |
|---|---|
| Read and post | `NNTPSERVER=localhost tin -r` (`w` post, `f` follow-up, `Shift-D` cancel), Thunderbird, or `http://localhost:8073` |
| Local net status | `http://localhost:8073`, `journalctl -u hnlink -f` |
| FLAMP send | `hnlink batch ~/.nbems/FLAMP/tx/<call>-$(date +%Y%m%d).rnews` |
| FLAMP receive | `hnlink load ~/.nbems/FLAMP/rx/*/*.rnews*` |
| New group | add it to `/etc/hamnews/groups`, `systemctl restart hamnews` |
| Monitor the channel | `itsjustnews` (`-frames`, `-record FILE`) |
| News server status / logs | `systemctl status hamnews` / `journalctl -u hamnews` |
| Change settings | `dpkg-reconfigure hamnews hnlink`, or edit `/etc/hamnews/inn.conf`, `/etc/hnlink.conf` |
| Config | `/etc/hamnews/`, `/etc/hnlink.conf` |
| Windows | `hamnews page / read / send / receive / status / start` |

**Not done yet:** expiry is not scheduled (no `news.daily` timer). At 365-day retention
and net volumes this can wait, but it is needed before the spool and history grow
large.

# hnlink protocol, version 1

Status: in use. Reference implementation: `hnlink` 0.1 (this directory). This
revision describes what that implementation does, with the parameters it uses by
default. Where the earlier draft and the code disagreed, this follows the code.
Author: KN6FLN, revised 2026-11-07.

hnlink exchanges Usenet articles among amateur stations sharing a radio channel.
Each station runs a news server; hnlink makes the stations' article sets converge.
Everything is broadcast. There are no connections, sessions, acknowledgements or
coordinator. Every station that hears a transmission can use it.

This document is the public technical description of the code and protocol, for
47 CFR §97.309(a)(4). Nothing is encrypted. Hashes and checksums detect errors
and identify content; they hide nothing.

Keywords: "must", "must not", "may" and "should" are used in the sense of RFC 2119.
Values in `code font` are normative. Defaults are in §5.2.

## Contents

1. Link layer
2. Frame format
3. Objects and coding
4. Behaviour
5. Channel access and parameters
6. Properties and limits
7. Versioning
8. Not specified yet

## 1. Link layer

Each hnlink frame is the information field of one AX.25 UI frame:

| Field | Value |
|---|---|
| Destination | callsign `HNLINK`. The SSID and command/response bits are not checked on receive; a sender should use SSID 0 with the command bit set |
| Source | the station's callsign-SSID (e.g. `KN6FLN-5`). This is the station's protocol identity and its station identification |
| Digipeaters | none sent. Received frames with digipeater fields are accepted; the digipeater fields are skipped |
| Control | `0x03` (UI) |
| PID | `0xF0` (no layer 3) |

Frames whose destination is not `HNLINK`, or whose control or PID differ, are not
hnlink frames. They are ignored by the protocol, but they still count as channel
activity (§5.1).

Transport: the reference implementation sends and receives these through a KISS
TNC. It connects either over TCP to `HOST:PORT` (default `127.0.0.1:8001`, which is
direwolf's KISS port), or to a serial hardware TNC (`/dev/ttyUSB0`, default 9600
baud). Data frames use KISS port 0, the TNC's first channel. The implementation
sends the KISS TXDELAY command on port 0 when it connects to a serial TNC.

Callsigns in the source and in every call field are letters and digits only.

## 2. Frame format

All integers are big-endian. Every frame:

```
offset  size  field
0       1     magic 0x48 ('H')
1       1     (version << 4) | type        version = 1
2       8     object id
10      ...   body (by type)
-4      4     CRC-32 (IEEE 802.3 polynomial, as zlib crc32) of all preceding bytes
```

A **call field** is one length byte (0–16) followed by that many ASCII bytes.
Longer names are truncated to 16 bytes by senders.

The **object id** is the first 8 bytes of SHA-256 over the object (§3). It is zero
in INV frames.

### 2.0 Receiver rules (all frame types)

A receiver drops a frame without comment if any of these hold:
- it is shorter than 14 bytes;
- the magic is not `0x48`, or the version nibble is not 1;
- the CRC-32 does not match;
- the type nibble is not 1, 2, 3 or 4;
- a length or count inside the body is inconsistent with the frame's size;
- a body check in §2.1–§2.4 fails.

Reserved bits (the flag bits other than bit 0 in MANIFEST) are ignored on receive.
Senders must send them as zero.

A receiver does not check the symbol size or the symbol count against the frame's
size on receipt. Those checks happen when the symbol is added to its object (§4.4).

### 2.1 MANIFEST (type 1): describes an object

```
flags    u8      bit 0 = END (see §4.3); other bits zero
length   u32     object length in bytes
symsize  u16     symbol size in bytes, a multiple of 64
k        u16     data symbols
m        u16     repair symbols
sha256   [32]    SHA-256 of the object
content  u8      1 = rnews batch
origin   call    station that built the object
```

The body is 45 bytes plus the origin call. A manifest is valid only if all of the
following hold:
- `symsize > 0` and `symsize % 64 == 0`
- `k ≥ 1` and `m ≥ 1`
- `k + m ≤ 65536`
- `(k−1)·symsize < length ≤ k·symsize`
- the object id equals the first 8 bytes of `sha256`

Receivers do not check `content`; version 1 defines only `1`.

### 2.2 SYMBOL (type 2): one coded piece of an object

```
esi      u16     encoding symbol index: 0..k-1 data, k..k+m-1 repair
data     [symsize]
```

A symbol is valid for an object only if its data is exactly `symsize` bytes and
`esi < k + m`. The receiver checks both when it holds the manifest for that object
(§4.4). A symbol that fails the check is dropped.

### 2.3 NEED (type 3): a receiver is short of symbols

```
count    u16     distinct symbols still needed to decode (k − have)
have     u16     distinct symbols held (informational)
requester call
```

### 2.4 INV (type 4): inventory and neighbour beacon

```
gen      u16     generation; changes when the inventory or heard set changes
part     u8      0..parts-1
parts    u8      ≥ 1
origin   call
nheard   u8      number of call fields that follow (part 0 only; 0 elsewhere)
heard    call × nheard   stations the origin currently hears
hashes   [6] × n          remainder of the body
```

A receiver drops an INV whose `part ≥ parts`, or whose `parts` is 0. An INV frame
has no object id, so its id bytes are zero.

**Hashes.** Each hash is the first 6 bytes of SHA-256 over the article's Message-ID,
including the angle brackets, e.g. `<abc@host.kn6fln>`. The inventory is the union
of `hashes` over parts `0..parts-1` with the same `gen`. A receiver uses an
inventory only once it holds every part of one generation, and keeps the previous
complete one until then. A part that arrives with a different `gen` or `parts`
from the one being collected starts a new collection.

**Packing.** A sender packs the hashes in ascending byte order, with the following
counts. Let `call` be the length of the origin call field, and `budget = symsize − len(call)`.

- Let `H` be the heard list the sender sends in part 0: at most 32 calls, the first
  32 of the neighbours it currently hears.
- `per = ⌊budget / 6⌋`: hashes per frame, parts 1 and up.
- `per0 = ⌊max(0, budget − Σ(1 + len(c)) over H) / 6⌋`: hashes in part 0.
- At most 255 parts. The sender truncates its inventory to `per0 + 254·per` hashes
  and logs the truncation.

With `symsize` 192 and an 8-character origin call, `per` is 30. `per0` is 30 with
no neighbours and falls to 0 when the 32-call heard list uses the whole budget. So
an inventory holds at most 7,620 to 7,650 articles, depending on neighbours.

A part-0 frame can be longer than `symsize`, because the heard list is not counted
against the frame size. Receivers accept frames of any length.

**Heard list.** A sender lists at most 32 calls. Receivers accept up to 255.

The origin call field is informational. A station is identified by the AX.25 source.

## 3. Objects and coding

An object is an **rnews batch**: articles with LF line endings, each preceded by
`#! rnews <n>\n`, where n is the article's length in bytes. The format is the one
FLAMP nets carry and INN's rnews ingests.

To send an object of `length` bytes with symbol size `s`:

1. `k = ceil(length / s)`, at most 16384; `m = max(k, 4)`.
2. Split the object into `k` symbols of `s` bytes, zero-padding the last.
3. Compute `m` repair symbols with a systematic Reed-Solomon erasure code over the
   `k` data symbols. **Any `k` distinct symbols** (data or repair) recover the object.

The code is defined as `klauspost/reedsolomon` v1.14.2 computes it with
`reedsolomon.New(k, m)` and default options:
- `k + m ≤ 256`: GF(2^8) with a systematic Vandermonde-derived matrix.
- `k + m > 256`: Leopard-RS over GF(2^16). This is why `symsize` must be a
  multiple of 64.

An independent implementation must reproduce those encodings bit for bit. Until
someone writes a stand-alone description, the reference library is normative.

Receivers truncate the decoded data to `length` and accept it only if its SHA-256
equals the manifest's. A mismatch means a corrupted symbol got past CRC-32, so the
receiver discards every symbol it holds for that object and starts collecting again.

**Batch size.** A sender packs articles into one object while the batch plus the
next article plus 20 bytes of framing stays within `MaxObject`. The 20 bytes is an
allowance for the `#! rnews` header, not an exact count. A single article larger
than `MaxObject` is sent as an object of its own, so an object can exceed
`MaxObject` by that article's size.

## 4. Behaviour

Each station keeps:
- **local**: the hashes of articles its server holds within the exchange window
  (14 days by default).
- **heard**: each station it has received any valid hnlink frame from, with the time
  last heard. A station counts as a neighbour for `PeerTTL`.
- **peers**: each neighbour's latest complete inventory and heard set.
- **rx**: the objects it is collecting (§4.4), dropped after 30 minutes without a frame.
- **done**: objects it has decoded or sent, kept for 24 hours.
- **sentAt**: when it last sent or relayed each article, and when it last received
  one over the air, kept for `ResendHold`.

### 4.1 Inventory beacons

A station sends its INV:
- `InvDebounce` after its inventory or neighbour set changes;
- shortly after first hearing a new station (a randomised delay of about
  `InvDebounce`);
- every `InvEvery` in any case.

A beacon can be lost: a repeater's identification or another station's
transmission can cover it. If a station has sent an INV, a live peer's
inventory still differs from its own, and it has heard nothing since the
beacon went out, it sends the INV again after a randomised `BeaconRetry`,
up to `BeaconRetries` times. A station that is in step with its peers does
not retry, so a quiet net sends nothing extra.

It polls its news server every `RefreshEvery` for articles it doesn't yet know, and
treats any it finds as changes.

The generation increments on every change. Hashes are sorted (§2.4). The packing
rules are in §2.4.

### 4.2 Election: who sends what

Every station evaluates the same rule over the same beacons. For each article hash
`h` in **local**, station **me** is elected to send `h` if there exists a live peer
`L` such that all of the following hold:

- `L`'s inventory lacks `h`;
- `L` lists **me** as heard;
- no other live peer `P` with a smaller callsign (byte-wise string comparison)
  satisfies all of: `P`'s inventory has `h`, `L` lists `P` as heard, and `P`
  lists `L` as heard.

"Live" means a peer heard within `PeerTTL` whose complete inventory has been received.

That is: for each station lacking an article, the lowest-callsign holder with a
two-way link to it sends. Where everyone hears everyone, this reduces to "the
lowest holder sends once". On a chain A–B–C it routes through B.

Two exceptions skip an article:
- the station sent it within `ResendHold`;
- the station received it over the air within `ResendHold`. Every station in range
  of the original sender heard the same transmission, and repairing it is the
  sender's job.

Elected articles are packed into objects (§3) and sent one object at a time.
Election runs again when the transmitter goes idle.

### 4.3 Sending an object (one round)

```
MANIFEST
SYMBOL esi=0 .. k-1                          (first round only)
SYMBOL repair × r                            r = ceil(0.15·k) + 1 on the first round
MANIFEST(END) × 2
```

A MANIFEST is also sent after every 16th frame of the round, counting the leading
MANIFEST. Repair symbols are taken in order from `esi = k` upward. When all `m` are
used up, the sender starts again from `k`.

After the round is on the air, the sender listens for `Listen + NeedSpread`. If it
heard any NEED for the object, it sends a **fill round**:
- the MANIFEST;
- `ceil(1.25 · maxNEED) + 1` fresh repair symbols, where maxNEED is the largest
  `count` heard;
- END twice.

It sends at most `MaxRounds` fill rounds. A NEED for an object the sender isn't
currently sending is ignored. Articles still missing after that are picked up by
later elections, once `ResendHold` expires.

### 4.4 Receiving

Symbols and manifests are collected per object id. Symbols may arrive before the
manifest. A symbol that arrives before the manifest is held without a check; the
check in §2.2 runs when the manifest arrives. Once a station holds the manifest and
`k` distinct valid symbols, it decodes, verifies the hash (§3), and offers each
article to its news server, which discards Message-IDs it already has.

On a MANIFEST with END for an object it can't yet decode, a receiver:
1. waits a uniformly random delay in `[0, NeedSpread]`;
2. sends `NEED(count = k − have)`, **unless** it has heard a NEED for the same object
   with a count at least that large since END. The sender's fill will cover it too.

Received articles count as held immediately, even before the news server's own
indexes show them (up to 10 minutes). Articles received over the air are held the
same way: they are not offered back to the air until a peer lacks them after that
(a hidden terminal). Objects already decoded, and objects the station itself sent,
are ignored for 24 hours.

When the news server refuses articles for good, the object's articles are still
held. When it defers any article of an object (for example, because the group does
not exist yet), none of that object's articles are marked held, so peers offer them
again.

## 5. Channel access and parameters

### 5.1 Carrier sense at the application layer

Before each burst, a station waits until it has heard no frame of any kind for
`Quiet`, plus a random extra of up to `Quiet/2`. Winlink, APRS and anything else
on the channel count. It sends at most `Burst` frames per key-up. It then waits for
the burst's estimated airtime before doing anything that depends on it being sent,
using this estimate:

```
airtime = TxDelay + Σ (len(frame) + 23 bytes AX.25 overhead) × 8 × 1.03 / Bitrate
```

The TNC's own carrier sense and persistence still apply underneath.

### 5.2 Defaults (1200 baud AFSK)

These are the defaults of the reference implementation. Every value can be set on
the command line.

| Parameter | Default | | Parameter | Default |
|---|---|---|---|---|
| symsize | 192 | | InvEvery | 30 min |
| Bitrate | 1200 | | InvDebounce | 20 s |
| TxDelay | 300 ms | | RefreshEvery | 2 min |
| Burst | 6 | | PeerTTL | 2 h |
| Quiet | 3 s | | ResendHold | 1 h |
| BeaconRetry | 3 min | | BeaconRetries | 2 |
| Overhead | 0.15 | | Listen | 10 s |
| MaxObject | 48 KiB | | NeedSpread | 6 s |
| MaxRounds | 4 | | window | 14 days |

Fixed values, not command-line settings: the ingest grace (10 minutes), the rx idle
timeout (30 minutes), the done retention (24 hours), and the heard-list cap (32
calls sent).

Stations on one net need not share timing parameters. They must share the
protocol version, the article window (or a longer one), and the group list.

## 6. Properties and limits

- **Idempotent.** Duplicate or repeated delivery is harmless (Message-ID dedupe).
  Nothing received is ever used as a filename or executed.
- **Local knowledge.** A station knows the inventories of its live neighbours and
  their heard lists. That is two hops of knowledge. The election can route an article
  through one relay (A–B–C), not through longer chains, so a network can grow as far
  as its links connect and as far as each relay can hear both ends.
- **Delay.** Each hop adds roughly `InvDebounce + airtime`.
- **Inventory cost** grows with (articles in window × stations in range): 6 bytes per
  article per beacon. The packing limit is in §2.4.
- **Hidden terminals.** On simplex, stations that can't hear each other can collide
  at a station between them. Short bursts and the repair mechanism make this
  survivable, not free.
- **Hash width.** 6-byte article hashes collide at about 10⁻⁷ for 10,000 articles. A
  collision makes one article look held when it isn't.
- **Groups.** A station whose server lacks an article's group refuses it. That
  article is offered to it again every `ResendHold` until the group exists.

## 7. Versioning

The version nibble is 1. A station ignores frames of other versions. Incompatible
changes will use a new version number. Compatible additions will use new frame
types or reserved flag bits, which version-1 stations ignore.

## 8. Not specified yet

These are gaps in this document. The reference implementation is the only definition
for them.

- **Test vectors.** There are no byte-level examples for a frame, a manifest, an
  inventory, or an encoded object. An independent implementation would need them.
- **Article handling.** How the news server's IHAVE and the article's headers are
  processed (including the Path and Message-ID checks) is left to the news server's
  configuration.
- **Rate and fairness.** There is no limit on how often a station can take the
  channel, beyond the timers above.
- **Security.** Frames are not authenticated. Any station can send any frame with
  any source call. The protocol trusts the radio channel, not the sender. A station
  that wants to refuse an article must do so in its news server.

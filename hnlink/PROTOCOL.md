# hnlink protocol, version 1

Status: in use. Reference implementation: `hnlink` 0.1 (this directory).
Author: KN6FLN, 2026-11-05.

hnlink exchanges Usenet articles among amateur stations sharing a radio channel.
Each station runs a news server; hnlink makes the stations' article sets converge.
Everything is broadcast. There are no connections, sessions, acknowledgements or
coordinator. Every station that hears a transmission can use it.

This document is the public technical description of the code and protocol, for
47 CFR §97.309(a)(4). Nothing is encrypted. Hashes and checksums detect errors
and identify content; they hide nothing.

## 1. Link layer

Each hnlink frame is the information field of one AX.25 UI frame:

| Field | Value |
|---|---|
| Destination | `HNLINK`, SSID 0, command bit set |
| Source | the station's callsign-SSID (e.g. `KN6FLN-5`). This is the station's protocol identity and its station identification |
| Digipeaters | none sent; received frames with digipeater fields are accepted |
| Control | `0x03` (UI) |
| PID | `0xF0` (no layer 3) |

The reference implementation sends and receives these through a KISS TNC over TCP
(direwolf, or anything speaking KISS on port 0). Frames to any other destination
are ignored, but they still count as channel activity (§5.1).

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

A receiver drops a frame without comment if the magic, version or CRC is wrong,
the type is unknown, or any length is inconsistent.

The **object id** is the first 8 bytes of SHA-256 over the object (§3). It is zero
in INV frames.

A **call field** is one length byte (0–16) followed by that many ASCII bytes.

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

A manifest is valid only if all of the following hold:
- `symsize > 0` and `symsize % 64 == 0`
- `k ≥ 1` and `m ≥ 1`
- `k + m ≤ 65536`
- `(k−1)·symsize < length ≤ k·symsize`
- the object id equals the first 8 bytes of `sha256`

### 2.2 SYMBOL (type 2): one coded piece of an object

```
esi      u16     encoding symbol index: 0..k-1 data, k..k+m-1 repair
data     [symsize]
```

### 2.3 NEED (type 3): a receiver is short of symbols

```
count    u16     distinct symbols still needed to decode
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

Each hash is the first 6 bytes of SHA-256 over the article's Message-ID, including
the angle brackets, e.g. `<abc@host.kn6fln>`. The inventory is the union of
`hashes` over parts `0..parts-1` with the same `gen`. A receiver uses an
inventory only once it holds every part of one generation, and keeps the previous
complete one until then.

The origin call field is informational. A station is identified by the AX.25 source.

## 3. Objects and coding

An object is an **rnews batch**: articles with LF line endings, each preceded by
`#! rnews <n>\n`, where n is the article's length in bytes.

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

## 4. Behaviour

Each station keeps:
- **local**: the hashes of articles its server holds within the exchange window
  (14 days by default).
- **heard**: each station it has received any valid hnlink frame from, with the time
  last heard. A station counts as a neighbour for `PeerTTL`.
- **peers**: each neighbour's latest complete inventory and heard set.

### 4.1 Inventory beacons

A station sends its INV:
- `InvDebounce` after its inventory or neighbour set changes;
- shortly after first hearing a new station (a randomised delay of about
  `InvDebounce`);
- every `InvEvery` in any case.

The generation increments on every change. Hashes are sorted. Each frame carries
about `(symsize − len(call)) / 6` hashes, part 0 fewer to make room for the heard
list (capped at 32 calls). There are at most 255 parts.

### 4.2 Election: who sends what

Every station evaluates the same rule over the same beacons. For each article hash
`h` in **local**, station **me** is elected to send `h` if there exists a live peer
`L` such that all of the following hold:

- `L`'s inventory lacks `h`;
- `L` lists **me** as heard;
- no other live peer `P` with a smaller callsign (byte-wise string comparison)
  satisfies all of: `P`'s inventory has `h`, `L` lists `P` as heard, and `P`
  lists `L` as heard.

That is: for each station lacking an article, the lowest-callsign holder with a
two-way link to it sends. Where everyone hears everyone, this reduces to "the
lowest holder sends once". On a chain A–B–C it routes through B.

Two exceptions skip an article:
- the station sent it within `ResendHold`;
- the station received it over the air within `ResendHold`. Every station in range
  of the original sender heard the same transmission, and repairing it is the
  sender's job.

Elected articles are packed into objects of at most `MaxObject` bytes and sent one
object at a time.

### 4.3 Sending an object (one round)

```
MANIFEST
SYMBOL esi=0 .. k-1                          (first round only)
SYMBOL repair × r                            r = ceil(0.15·k) + 1 on the first round
MANIFEST(END) × 2
```

The MANIFEST is also repeated after every 16th frame. Repair symbols are taken in
order from `esi = k` upward. When all `m` are used up, the sender starts again
from `k`.

After the round is on the air, the sender listens for `Listen + NeedSpread`. If it
heard any NEED for the object, it sends a **fill round**:
- the MANIFEST;
- `ceil(1.25 · maxNEED) + 1` fresh repair symbols, where maxNEED is the largest
  `count` heard;
- END twice.

It sends at most `MaxRounds` fill rounds. Articles still missing after that are
picked up by later elections, once `ResendHold` expires.

### 4.4 Receiving

Symbols and manifests are collected per object id. Symbols may arrive before the
manifest. Once a station holds the manifest and `k` distinct valid symbols, it
decodes, verifies the hash (§3), and offers each article to its news server. The
server discards Message-IDs it already has.

On a MANIFEST with END for an object it can't yet decode, a receiver:
1. waits a uniformly random delay in `[0, NeedSpread]`;
2. sends `NEED(count = k − have)`, **unless** it has heard a NEED for the same object
   with a count at least that large since END. The sender's fill will cover it too.

Received articles count as held immediately, even before the news server's own
indexes show them (up to 10 minutes). Objects already decoded, and objects the
station itself sent, are ignored for 24 hours.

## 5. Channel access

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

| Parameter | Default | | Parameter | Default |
|---|---|---|---|---|
| symsize | 192 | | InvEvery | 30 min |
| Bitrate | 1200 | | InvDebounce | 20 s |
| TxDelay | 300 ms | | PeerTTL | 2 h |
| Burst | 6 | | ResendHold | 1 h |
| Quiet | 3 s | | Listen | 10 s |
| MaxObject | 48 KiB | | NeedSpread | 6 s |
| MaxRounds | 4 | | window | 14 days |

Stations on one net need not share timing parameters. They must share the
protocol version, the article window (or a longer one), and the group list.

## 6. Properties and limits

- **Idempotent.** Duplicate or repeated delivery is harmless (Message-ID dedupe).
  Nothing received is ever used as a filename or executed.
- **No global state.** A station knows only stations within two hops, so a network
  can grow as large as connectivity allows. Each hop adds roughly
  `InvDebounce + airtime` of delay.
- **Inventory cost** grows with (articles in window × stations in range): 6 bytes per
  article per beacon. A hard cap of about 7,600 articles fits in 255 parts.
- **Hidden terminals.** On simplex, stations that can't hear each other can collide
  at a station between them. Short bursts and the repair mechanism make this
  survivable, not free.
- **Hash width.** 6-byte article hashes collide at ~10⁻⁷ for 10,000 articles. A
  collision makes one article look held when it isn't.
- **Groups.** A station whose server lacks an article's group refuses it. That
  article is offered to it again every `ResendHold` until the group exists.

## 7. Versioning

The version nibble is 1. A station ignores frames of other versions. Incompatible
changes will use a new version number. Compatible additions will use new frame
types or reserved flag bits, which version-1 stations ignore.

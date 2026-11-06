// Package link is the hnlink protocol engine for one local net.
//
// Stations kibitz like this:
//
//  1. INV: every station beacons the hashes of the articles it holds in the
//     exchange window: on change (debounced), on hearing a new peer, and every
//     InvEvery.
//  2. Election: every INV also lists the stations its origin hears. For each
//     article a live peer L lacks, the candidates are the holders with a
//     two-way link to L (each lists the other as heard); the lowest callsign
//     sends it. On a repeater everyone hears everyone and this is just "lowest
//     holder"; on simplex it routes through whichever station can reach L, so
//     A - B - C relays through B. Everyone computes the same answer from the
//     same beacons, so each article goes out once with no coordinator.
//  3. Objects: the sender packs those articles into an rnews batch, RS-codes
//     it, and sends MANIFEST, the k data symbols, some repair symbols and an
//     END manifest.
//  4. Fills: a receiver that is short after END waits a random moment and sends
//     NEED(count) unless it already heard a NEED at least as large. The sender
//     answers the largest NEED with fresh repair symbols, which help every
//     receiver at once.
//  5. The receiver checks SHA-256, offers the articles to innd by IHAVE, and its
//     next INV tells everyone what it now holds.
package link

import (
	"bytes"
	"io"
	"log"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"hamnews/hnlink/internal/fec"
	"hamnews/hnlink/internal/nntp"
	"hamnews/hnlink/internal/rnews"
	"hamnews/hnlink/internal/wire"
)

type Transport interface {
	Send(frame []byte) error
	Recv() (from string, frame []byte, err error)
	LastHeard() time.Time
}

type Store interface {
	Inventory() ([]string, error)
	Articles(ids []string) (map[string][]byte, error)
	Ingest(batch []byte, log io.Writer) (nntp.Tally, error)
}

type Config struct {
	Call    string // this station as it appears on the air, e.g. KN6FLN-5
	SymSize int    // bytes per symbol, multiple of 64

	// Channel model, used to pace transmissions and know when they are done.
	Bitrate float64       // bits/s on the air
	TxDelay time.Duration // key-up delay per burst
	Burst   int           // frames per key-up
	Quiet   time.Duration // channel must be idle this long before keying

	InvEvery     time.Duration // periodic inventory beacon
	InvDebounce  time.Duration // delay after a change before beaconing
	RefreshEvery time.Duration // poll the local server for new articles
	PeerTTL      time.Duration // forget a peer not heard this long
	ResendHold   time.Duration // don't send (or relay) the same article again this soon

	Listen     time.Duration // wait for NEED after END (after the END is on the air)
	NeedSpread time.Duration // receivers pick a NEED delay in [0, NeedSpread)
	MaxRounds  int           // fill rounds per object
	Overhead   float64       // initial repair symbols as a fraction of k
	MaxObject  int           // max object (batch) size in bytes

	Logger *log.Logger
}

// Defaults suit 1200 baud AFSK through direwolf.
func Defaults(call string) Config {
	return Config{
		Call: strings.ToUpper(call), SymSize: 192,
		Bitrate: 1200, TxDelay: 300 * time.Millisecond, Burst: 6, Quiet: 3 * time.Second,
		InvEvery: 30 * time.Minute, InvDebounce: 20 * time.Second, RefreshEvery: 2 * time.Minute,
		PeerTTL: 2 * time.Hour, ResendHold: time.Hour,
		Listen: 10 * time.Second, NeedSpread: 6 * time.Second, MaxRounds: 4, Overhead: 0.15,
		MaxObject: 48 << 10,
	}
}

type peer struct {
	inv       map[wire.AHash]bool
	heard     map[string]bool // stations this peer hears (from its INV)
	have      bool            // inv is a complete generation
	gen       uint16
	pendGen   uint16
	pendParts map[uint8][]wire.AHash
	pendHeard []string
	pendN     uint8
	seen      time.Time
}

type rxObj struct {
	dec       *fec.Decoder
	last      time.Time
	needTimer *time.Timer
	heardNeed int
}

type txObj struct {
	enc     *fec.Encoded
	next    int // next repair ESI to send
	round   int
	maxNeed int
	listen  bool
	hashes  []wire.AHash
}

type Engine struct {
	cfg Config
	tr  Transport
	st  Store
	lg  *log.Logger

	ev   chan func()
	txq  chan txJob
	quit chan struct{}

	local    map[wire.AHash]string // hash -> message-id
	ingested map[wire.AHash]ingest // radio arrivals not yet visible via NEWNEWS
	gen      uint16
	invTimer *time.Timer
	peers    map[string]*peer
	heard    map[string]time.Time // any valid frame, by sender
	nbrSig   string               // heard set as last beaconed
	rx       map[wire.ObjID]*rxObj
	done     map[wire.ObjID]time.Time
	tx       *txObj
	txQueue  [][]string // message-id groups waiting to be sent
	sentAt   map[wire.AHash]time.Time
	elect    *time.Timer
	rng      *rand.Rand

	Stats Stats
}

type Stats struct {
	FramesTx, FramesRx, ObjectsSent, ObjectsDecoded, Needs int
	Ingested                                               nntp.Tally
}

type ingest struct {
	id string
	at time.Time
}

// ingestGrace covers innd's buffered history/overview: articles we just
// ingested count as held until NEWNEWS shows them or this expires.
const ingestGrace = 10 * time.Minute

type txJob struct {
	frames [][]byte
	done   func()
}

func New(cfg Config, tr Transport, st Store) *Engine {
	lg := cfg.Logger
	if lg == nil {
		lg = log.New(io.Discard, "", 0)
	}
	return &Engine{cfg: cfg, tr: tr, st: st, lg: lg,
		ev: make(chan func(), 256), txq: make(chan txJob, 64), quit: make(chan struct{}),
		local: map[wire.AHash]string{}, ingested: map[wire.AHash]ingest{}, peers: map[string]*peer{}, heard: map[string]time.Time{},
		rx: map[wire.ObjID]*rxObj{}, done: map[wire.ObjID]time.Time{}, sentAt: map[wire.AHash]time.Time{},
		rng: rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(len(cfg.Call))))}
}

// post runs f on the engine goroutine.
func (e *Engine) post(f func()) {
	select {
	case e.ev <- f:
	case <-e.quit:
	}
}

func (e *Engine) after(d time.Duration, f func()) *time.Timer {
	return time.AfterFunc(d, func() { e.post(f) })
}

// Run blocks until Stop or a transport error.
func (e *Engine) Run() error {
	errc := make(chan error, 1)
	go e.txLoop()
	go func() {
		for {
			from, f, err := e.tr.Recv()
			if err != nil {
				errc <- err
				return
			}
			e.post(func() { e.onFrame(from, f) })
		}
	}()
	e.refresh()
	e.scheduleInv(e.jitter(e.cfg.InvDebounce))
	refresh := time.NewTicker(e.cfg.RefreshEvery)
	beacon := time.NewTicker(e.cfg.InvEvery)
	gc := time.NewTicker(time.Minute)
	defer refresh.Stop()
	defer beacon.Stop()
	defer gc.Stop()
	for {
		select {
		case f := <-e.ev:
			f()
		case <-refresh.C:
			e.refresh()
		case <-beacon.C:
			e.scheduleInv(e.jitter(e.cfg.InvDebounce))
		case <-gc.C:
			e.gc()
		case err := <-errc:
			close(e.quit)
			return err
		case <-e.quit:
			return nil
		}
	}
}

func (e *Engine) Stop() {
	select {
	case <-e.quit:
	default:
		close(e.quit)
	}
}

// Do runs f on the engine goroutine and waits (for status and tests).
func (e *Engine) Do(f func(*Engine)) {
	c := make(chan struct{})
	e.post(func() { f(e); close(c) })
	select {
	case <-c:
	case <-e.quit:
	}
}

func (e *Engine) jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(e.rng.Int63n(int64(d)))
}

// ------------------------------------------------------------------ transmit

func (e *Engine) airtime(frames [][]byte) time.Duration {
	bits := 0.0
	for _, f := range frames {
		bits += float64(len(f)+16+2+2+3) * 8 * 1.03 // AX.25 addr+ctl/pid+FCS+flags, bit stuffing
	}
	return e.cfg.TxDelay + time.Duration(bits/e.cfg.Bitrate*float64(time.Second))
}

func (e *Engine) waitQuiet(rng *rand.Rand) bool {
	for {
		idle := time.Since(e.tr.LastHeard())
		if idle >= e.cfg.Quiet {
			return true
		}
		wait := e.cfg.Quiet - idle + time.Duration(rng.Int63n(int64(e.cfg.Quiet/2)+1))
		select {
		case <-time.After(wait):
		case <-e.quit:
			return false
		}
	}
}

// txLoop owns the transmitter: bursts of Burst frames, each after the channel
// has been quiet, then waits out the estimated airtime so "done" means "on air".
func (e *Engine) txLoop() {
	rng := rand.New(rand.NewSource(time.Now().UnixNano() + 1)) // e.rng belongs to the engine goroutine
	for {
		var j txJob
		select {
		case j = <-e.txq:
		case <-e.quit:
			return
		}
		for i := 0; i < len(j.frames); i += e.cfg.Burst {
			b := j.frames[i:min(i+e.cfg.Burst, len(j.frames))]
			if !e.waitQuiet(rng) {
				return
			}
			for _, f := range b {
				if err := e.tr.Send(f); err != nil {
					e.lg.Printf("send: %v", err)
				}
			}
			e.post(func() { e.Stats.FramesTx += len(b) })
			select {
			case <-time.After(e.airtime(b)):
			case <-e.quit:
				return
			}
		}
		if j.done != nil {
			e.post(j.done)
		}
	}
}

func (e *Engine) queue(frames [][]byte, done func()) {
	select {
	case e.txq <- txJob{frames, done}:
	case <-e.quit:
	}
}

// ------------------------------------------------------------------ inventory

func (e *Engine) refresh() {
	ids, err := e.st.Inventory()
	if err != nil {
		e.lg.Printf("inventory: %v", err)
		return
	}
	m := make(map[wire.AHash]string, len(ids))
	for _, id := range ids {
		m[wire.ArticleHash(id)] = id
	}
	for h, in := range e.ingested {
		if _, ok := m[h]; ok || time.Since(in.at) > ingestGrace {
			delete(e.ingested, h)
		} else {
			m[h] = in.id
		}
	}
	changed := len(m) != len(e.local)
	for h := range m {
		if _, ok := e.local[h]; !ok {
			changed = true
			break
		}
	}
	e.local = m
	if changed {
		e.gen++
		e.scheduleInv(e.cfg.InvDebounce)
		e.scheduleElect()
	}
}

func (e *Engine) scheduleInv(d time.Duration) {
	if e.invTimer != nil {
		e.invTimer.Stop()
	}
	e.invTimer = e.after(d, e.sendInv)
}

// neighbors is the sorted set of stations heard within PeerTTL.
func (e *Engine) neighbors() []string {
	var n []string
	for c, t := range e.heard {
		if time.Since(t) < e.cfg.PeerTTL {
			n = append(n, c)
		}
	}
	sort.Strings(n)
	return n
}

// checkNeighbors bumps the generation when the heard set changes, so peers
// re-route around stations that appeared or went away.
func (e *Engine) checkNeighbors() {
	if sig := strings.Join(e.neighbors(), " "); sig != e.nbrSig {
		e.nbrSig = sig
		e.gen++
		e.scheduleInv(e.cfg.InvDebounce)
		e.scheduleElect()
	}
}

func (e *Engine) invFrames() [][]byte {
	hs := make([]wire.AHash, 0, len(e.local))
	for h := range e.local {
		hs = append(hs, h)
	}
	sort.Slice(hs, func(i, j int) bool { return bytes.Compare(hs[i][:], hs[j][:]) < 0 })
	nb := e.neighbors()
	if len(nb) > 32 {
		nb = nb[:32]
	}
	nbBytes := 0
	for _, c := range nb {
		nbBytes += 1 + len(c)
	}
	budget := max(wire.HashLen, e.cfg.SymSize-len(e.cfg.Call))
	per := budget / wire.HashLen
	per0 := max(0, budget-nbBytes) / wire.HashLen
	if max := per0 + 254*per; len(hs) > max {
		e.lg.Printf("inventory of %d articles truncated to %d; shorten the window", len(hs), max)
		hs = hs[:max]
	}
	var chunks [][]wire.AHash
	n0 := min(per0, len(hs))
	chunks = append(chunks, hs[:n0])
	for r := hs[n0:]; len(r) > 0; {
		k := min(per, len(r))
		chunks = append(chunks, r[:k])
		r = r[k:]
	}
	var out [][]byte
	for p, c := range chunks {
		v := &wire.Inv{Gen: e.gen, Part: uint8(p), Parts: uint8(len(chunks)), Origin: e.cfg.Call, Hashes: c}
		if p == 0 {
			v.Heard = nb
		}
		out = append(out, v.Marshal())
	}
	return out
}

func (e *Engine) sendInv() {
	f := e.invFrames()
	e.lg.Printf("INV gen %d: %d articles in %d frame(s)", e.gen, len(e.local), len(f))
	e.queue(f, nil)
}

func (e *Engine) onInv(from string, v *wire.Inv) {
	p := e.peers[from]
	isNew := p == nil
	if isNew {
		p = &peer{}
		e.peers[from] = p
		e.lg.Printf("new peer %s", from)
		// It needs our inventory to decide what it should send us.
		e.scheduleInv(e.jitter(e.cfg.InvDebounce))
	}
	p.seen = time.Now()
	if p.pendParts == nil || p.pendGen != v.Gen || p.pendN != v.Parts {
		p.pendGen, p.pendN, p.pendParts = v.Gen, v.Parts, map[uint8][]wire.AHash{}
	}
	p.pendParts[v.Part] = v.Hashes
	if v.Part == 0 {
		p.pendHeard = v.Heard
	}
	if len(p.pendParts) < int(p.pendN) {
		return
	}
	inv := map[wire.AHash]bool{}
	for _, hs := range p.pendParts {
		for _, h := range hs {
			inv[h] = true
		}
	}
	heard := map[string]bool{}
	for _, c := range p.pendHeard {
		heard[c] = true
	}
	p.inv, p.heard, p.have, p.gen, p.pendParts, p.pendHeard = inv, heard, true, v.Gen, nil, nil
	e.lg.Printf("peer %s gen %d: %d articles", from, v.Gen, len(inv))
	e.scheduleElect()
}

// ------------------------------------------------------------------ election

// scheduleElect runs the election soon. It does not push back a pending run:
// with several peers beaconing, a resetting debounce would never fire.
func (e *Engine) scheduleElect() {
	if e.elect != nil {
		return
	}
	e.elect = e.after(e.jitter(e.cfg.InvDebounce), func() {
		e.elect = nil
		e.runElection()
	})
}

// PeerView is what the election knows about a live peer.
type PeerView struct {
	Inv   map[wire.AHash]bool
	Heard map[string]bool
}

// Choose returns the hashes this station is elected to send (exported for tests).
// For every live peer L lacking h, the candidates are holders with a two-way
// link to L: me if L lists me as heard (L is live, so I hear L), and any peer P
// holding h where L lists P and P lists L. The lowest candidate sends.
func Choose(me string, local map[wire.AHash]string, peers map[string]PeerView,
	recent func(wire.AHash) bool) []wire.AHash {
	var out []wire.AHash
	for h := range local {
		if recent(h) {
			continue
		}
		for lc, l := range peers {
			if l.Inv[h] || !l.Heard[me] {
				continue
			}
			lowest := me
			for pc, p := range peers {
				if pc != lc && pc < lowest && p.Inv[h] && l.Heard[pc] && p.Heard[lc] {
					lowest = pc
				}
			}
			if lowest == me {
				out = append(out, h)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

func (e *Engine) livePeers() map[string]PeerView {
	m := map[string]PeerView{}
	for call, p := range e.peers {
		if p.have && time.Since(p.seen) < e.cfg.PeerTTL {
			m[call] = PeerView{Inv: p.inv, Heard: p.heard}
		}
	}
	return m
}

func (e *Engine) runElection() {
	if e.tx != nil || len(e.txQueue) > 0 {
		return // re-run when the current send finishes
	}
	hs := Choose(e.cfg.Call, e.local, e.livePeers(), func(h wire.AHash) bool {
		t, ok := e.sentAt[h]
		return ok && time.Since(t) < e.cfg.ResendHold
	})
	if len(hs) == 0 {
		return
	}
	ids := make([]string, len(hs))
	for i, h := range hs {
		ids[i] = e.local[h]
	}
	e.lg.Printf("elected to send %d article(s)", len(ids))
	e.txQueue = append(e.txQueue, ids)
	e.nextObject()
}

func (e *Engine) nextObject() {
	for e.tx == nil && len(e.txQueue) > 0 {
		ids := e.txQueue[0]
		e.txQueue = e.txQueue[1:]
		arts, err := e.st.Articles(ids)
		if err != nil {
			e.lg.Printf("fetch: %v", err)
			continue
		}
		var batch []byte
		var hashes []wire.AHash
		var rest []string
		for _, id := range ids {
			a, ok := arts[id]
			if !ok {
				continue
			}
			if len(batch) > 0 && len(batch)+len(a)+20 > e.cfg.MaxObject {
				rest = append(rest, id)
				continue
			}
			batch = rnews.Append(batch, a)
			hashes = append(hashes, wire.ArticleHash(id))
		}
		if len(rest) > 0 {
			e.txQueue = append([][]string{rest}, e.txQueue...)
		}
		if len(batch) == 0 {
			continue
		}
		enc, err := fec.Encode(batch, e.cfg.SymSize, wire.ContentRnews, e.cfg.Call)
		if err != nil {
			e.lg.Printf("encode: %v", err)
			continue
		}
		e.tx = &txObj{enc: enc, next: int(enc.Manifest.K), hashes: hashes}
		e.lg.Printf("object %s: %d article(s), %d bytes, k=%d", enc.Manifest.ID, len(hashes), len(batch), enc.Manifest.K)
		e.sendRound(int(math.Ceil(float64(enc.Manifest.K)*e.cfg.Overhead))+1, true)
	}
	if e.tx == nil && len(e.txQueue) == 0 {
		e.scheduleElect()
	}
}

func (e *Engine) symbolFrame(esi int) []byte {
	return (&wire.Symbol{ID: e.tx.enc.Manifest.ID, ESI: uint16(esi), Data: e.tx.enc.Shards[esi]}).Marshal()
}

// sendRound sends (first round) all data symbols plus repair, or (fills) repair only.
func (e *Engine) sendRound(repair int, first bool) {
	t := e.tx
	m := t.enc.Manifest
	mf := m.Marshal()
	frames := [][]byte{mf}
	add := func(f []byte) {
		frames = append(frames, f)
		if len(frames)%16 == 0 {
			frames = append(frames, mf)
		}
	}
	if first {
		for i := 0; i < int(m.K); i++ {
			add(e.symbolFrame(i))
		}
	}
	n := int(m.K) + int(m.M)
	for i := 0; i < repair; i++ {
		add(e.symbolFrame(t.next))
		if t.next++; t.next >= n {
			t.next = int(m.K) // out of fresh repair; cycle
		}
	}
	end := m
	end.Flags = wire.FlagEnd
	ef := end.Marshal()
	frames = append(frames, ef, ef) // twice: a lost END would silence every NEED
	t.round++
	t.maxNeed, t.listen = 0, false
	id := m.ID
	e.queue(frames, func() {
		if e.tx == nil || e.tx.enc.Manifest.ID != id {
			return
		}
		e.tx.listen = true
		e.after(e.cfg.Listen+e.cfg.NeedSpread, func() { e.endRound(id) })
	})
}

func (e *Engine) endRound(id wire.ObjID) {
	t := e.tx
	if t == nil || t.enc.Manifest.ID != id {
		return
	}
	if t.maxNeed > 0 && t.round <= e.cfg.MaxRounds {
		n := int(math.Ceil(float64(t.maxNeed)*1.25)) + 1
		e.lg.Printf("object %s: fill round %d, %d repair symbols", id, t.round, n)
		e.sendRound(n, false)
		return
	}
	if t.maxNeed > 0 {
		e.lg.Printf("object %s: giving up after %d rounds; inventory will retry later", id, t.round)
	}
	now := time.Now()
	for _, h := range t.hashes {
		e.sentAt[h] = now
	}
	e.done[id] = now // late copies of our own object (relays, other senders) are not ours to decode
	e.Stats.ObjectsSent++
	e.tx = nil
	e.nextObject()
}

// ------------------------------------------------------------------ receive

func (e *Engine) onFrame(from string, f []byte) {
	p, err := wire.Parse(f)
	if err != nil {
		return
	}
	e.Stats.FramesRx++
	_, known := e.heard[from]
	e.heard[from] = time.Now()
	if pp := e.peers[from]; pp != nil {
		pp.seen = time.Now()
	}
	if !known {
		e.checkNeighbors()
	}
	switch v := p.(type) {
	case *wire.Inv:
		e.onInv(from, v)
	case *wire.Manifest:
		e.onManifest(v)
	case *wire.Symbol:
		e.onSymbol(v)
	case *wire.Need:
		e.onNeed(v)
	}
}

func (e *Engine) rxFor(id wire.ObjID) *rxObj {
	if _, ok := e.done[id]; ok {
		return nil
	}
	if e.tx != nil && e.tx.enc.Manifest.ID == id {
		return nil // our own object, relayed or echoed
	}
	r := e.rx[id]
	if r == nil {
		r = &rxObj{dec: fec.NewDecoder(id)}
		e.rx[id] = r
	}
	r.last = time.Now()
	return r
}

func (e *Engine) onManifest(m *wire.Manifest) {
	r := e.rxFor(m.ID)
	if r == nil {
		return
	}
	r.dec.SetManifest(m)
	if e.tryDecode(m.ID, r) {
		return
	}
	if m.Flags&wire.FlagEnd != 0 && r.needTimer == nil {
		r.heardNeed = 0
		r.needTimer = e.after(time.Duration(e.rng.Int63n(int64(e.cfg.NeedSpread)+1)), func() { e.fireNeed(m.ID) })
	}
}

func (e *Engine) onSymbol(s *wire.Symbol) {
	r := e.rxFor(s.ID)
	if r == nil {
		return
	}
	if r.dec.Add(s) {
		e.tryDecode(s.ID, r)
	}
}

func (e *Engine) fireNeed(id wire.ObjID) {
	r := e.rx[id]
	if r == nil {
		return
	}
	r.needTimer = nil
	need := r.dec.Need()
	if need <= 0 {
		return
	}
	if r.heardNeed >= need {
		e.lg.Printf("object %s: need %d, suppressed (heard NEED %d)", id, need, r.heardNeed)
		return
	}
	e.Stats.Needs++
	e.lg.Printf("object %s: NEED %d (have %d)", id, need, r.dec.Have())
	e.queue([][]byte{(&wire.Need{ID: id, Count: uint16(need), Have: uint16(r.dec.Have()), Requester: e.cfg.Call}).Marshal()}, nil)
}

func (e *Engine) onNeed(n *wire.Need) {
	if t := e.tx; t != nil && t.enc.Manifest.ID == n.ID {
		t.maxNeed = max(t.maxNeed, int(n.Count))
		return
	}
	if r := e.rx[n.ID]; r != nil {
		r.heardNeed = max(r.heardNeed, int(n.Count))
	}
}

func (e *Engine) tryDecode(id wire.ObjID, r *rxObj) bool {
	obj, err := r.dec.TryDecode()
	if err != nil {
		e.lg.Printf("object %s: %v", id, err)
		return false
	}
	if obj == nil {
		return false
	}
	if r.needTimer != nil {
		r.needTimer.Stop()
	}
	delete(e.rx, id)
	now := time.Now()
	e.done[id] = now
	e.Stats.ObjectsDecoded++
	// Everyone in range of the sender heard this object too, and repairing it is
	// the sender's job. Hold these articles like ones we sent; relay them only if
	// a peer still lacks them afterwards (a hidden terminal).
	var ids []string
	if arts, err := rnews.Split(obj); err == nil {
		for _, a := range arts {
			id := rnews.Header(a, "Message-ID")
			ids = append(ids, id)
			e.sentAt[wire.ArticleHash(id)] = now
		}
	}
	origin := r.dec.Manifest.Origin
	go func() {
		t, err := e.st.Ingest(obj, logWriter{e.lg})
		e.post(func() {
			e.Stats.Ingested.Accepted += t.Accepted
			e.Stats.Ingested.Duplicate += t.Duplicate
			e.Stats.Ingested.Rejected += t.Rejected
			e.Stats.Ingested.Deferred += t.Deferred
			if err != nil {
				e.lg.Printf("object %s from %s: ingest: %v (%v)", id, origin, err, t)
			} else {
				e.lg.Printf("object %s from %s: %v", id, origin, t)
				// Held now (accepted, duplicate, or refused for good). Deferred
				// articles stay out so peers offer them again.
				if t.Deferred == 0 {
					for _, mid := range ids {
						e.ingested[wire.ArticleHash(mid)] = ingest{mid, time.Now()}
					}
				}
			}
			e.refresh()
		})
	}()
	return true
}

func (e *Engine) gc() {
	now := time.Now()
	for id, r := range e.rx {
		if now.Sub(r.last) > 30*time.Minute {
			delete(e.rx, id)
		}
	}
	for id, t := range e.done {
		if now.Sub(t) > 24*time.Hour {
			delete(e.done, id)
		}
	}
	for h, t := range e.sentAt {
		if now.Sub(t) > e.cfg.ResendHold {
			delete(e.sentAt, h)
		}
	}
	for call, p := range e.peers {
		if now.Sub(p.seen) > e.cfg.PeerTTL {
			e.lg.Printf("peer %s expired", call)
			delete(e.peers, call)
		}
	}
	for call, t := range e.heard {
		if now.Sub(t) > e.cfg.PeerTTL {
			delete(e.heard, call)
		}
	}
	e.checkNeighbors()
}

type logWriter struct{ l *log.Logger }

func (w logWriter) Write(b []byte) (int, error) {
	w.l.Print(strings.TrimRight(string(b), "\n"))
	return len(b), nil
}

// ------------------------------------------------------------------ status

type PeerStatus struct {
	Call      string    `json:"call"`
	Articles  int       `json:"articles"`  // in its last complete inventory
	TheyLack  int       `json:"they_lack"` // we have, they don't
	WeLack    int       `json:"we_lack"`   // they have, we don't
	Heard     []string  `json:"heard"`     // stations it hears
	HearsUs   bool      `json:"hears_us"`
	LastHeard time.Time `json:"last_heard"`
}

type ObjStatus struct {
	ID     string `json:"id"`
	Origin string `json:"origin"`
	Have   int    `json:"have"`
	K      int    `json:"k"`
	Bytes  int    `json:"bytes"`
	Round  int    `json:"round,omitempty"`
	Need   int    `json:"need,omitempty"`
}

type Status struct {
	Call      string       `json:"call"`
	Local     int          `json:"local_articles"`
	Gen       uint16       `json:"gen"`
	Neighbors []string     `json:"neighbors"`
	Peers     []PeerStatus `json:"peers"`
	Sending   *ObjStatus   `json:"sending"`
	Queued    int          `json:"queued_batches"`
	Receiving []ObjStatus  `json:"receiving"`
	Stats     Stats        `json:"stats"`
}

func (e *Engine) Snapshot() Status {
	var st Status
	e.Do(func(e *Engine) {
		st = Status{Call: e.cfg.Call, Local: len(e.local), Gen: e.gen, Neighbors: e.neighbors(),
			Queued: len(e.txQueue), Stats: e.Stats}
		for call, p := range e.peers {
			ps := PeerStatus{Call: call, LastHeard: e.heard[call], HearsUs: p.heard[e.cfg.Call]}
			if p.have {
				ps.Articles = len(p.inv)
				for h := range e.local {
					if !p.inv[h] {
						ps.TheyLack++
					}
				}
				for h := range p.inv {
					if _, ok := e.local[h]; !ok {
						ps.WeLack++
					}
				}
			}
			for c := range p.heard {
				ps.Heard = append(ps.Heard, c)
			}
			sort.Strings(ps.Heard)
			st.Peers = append(st.Peers, ps)
		}
		sort.Slice(st.Peers, func(i, j int) bool { return st.Peers[i].Call < st.Peers[j].Call })
		if t := e.tx; t != nil {
			m := t.enc.Manifest
			st.Sending = &ObjStatus{ID: m.ID.String(), Origin: e.cfg.Call, K: int(m.K), Bytes: int(m.Length),
				Round: t.round, Need: t.maxNeed, Have: len(t.hashes)}
		}
		for id, r := range e.rx {
			o := ObjStatus{ID: id.String(), Have: r.dec.Have()}
			if m := r.dec.Manifest; m != nil {
				o.Origin, o.K, o.Bytes = m.Origin, int(m.K), int(m.Length)
			}
			st.Receiving = append(st.Receiving, o)
		}
		sort.Slice(st.Receiving, func(i, j int) bool { return st.Receiving[i].ID < st.Receiving[j].ID })
	})
	return st
}

// Refresh asks the engine to re-read the local server now (after a local post).
func (e *Engine) Refresh() { e.post(e.refresh) }

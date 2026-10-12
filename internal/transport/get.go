package transport

// GET + header-uplink front. Inflight, exclusive Puller, and pad hold are here.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"loft/internal/cdn"
	"loft/internal/config"
	ecrypto "loft/internal/crypto"
	"loft/internal/mux"
	"loft/internal/netconf"
	"loft/internal/wire"
)

// GET is the GET + header-uplink transport. Any CDN/proxy that only
// forwards GET can sit in front; which vendor is node config, not protocol.
type GET struct {
	prof      cdn.Profile
	cfg       config.Outbound
	token     []byte
	client    *http.Client
	base      string
	path      string
	host      string
	sess      *mux.Session
	key       []byte
	sid       string
	seq       atomic.Uint64
	started   atomic.Bool
	stop      context.CancelFunc
	done      chan struct{}
	dead      sync.Once
	ticket    []byte
	mu        sync.Mutex
	pushMu    sync.Mutex
	offer     wire.Cap
	nextSeq   uint64
	held      map[uint64][]wire.Frame
	keepAlive time.Duration
	busy      atomic.Bool
	upBusy    atomic.Bool
	catch     atomic.Int32 // extra pad GETs after useful up/down
	lastLive  atomic.Int64 // unix nano of last useful up/down
	caps      wire.Cap
	connUp    atomic.Uint64
	connDown  atomic.Uint64
	trips     getTrips
	lastDrop  atomic.Int64
}

// getTrips is per-GET HTTP timing: pad vs bulk body vs fat Cookie uplink.
// Used to split CDN delay (edge TTFB) from origin body and header budget.
type getTrips struct {
	errN      atomic.Int64
	pad       tripBucket
	bulk      tripBucket
	up        tripBucket
	maxTTFBNs atomic.Int64
}

type tripBucket struct {
	n, ttfbNs, totalNs, bytes atomic.Int64
}

func (b *tripBucket) add(ttfb, total time.Duration, n int) {
	b.n.Add(1)
	b.ttfbNs.Add(ttfb.Nanoseconds())
	b.totalNs.Add(total.Nanoseconds())
	b.bytes.Add(int64(n))
}

// TripSummary is pad/bulk/up HTTP timing for CDN vs direct GET debug.
func (g *GET) TripSummary() string {
	if g == nil {
		return ""
	}
	n := g.trips.pad.n.Load() + g.trips.bulk.n.Load() + g.trips.up.n.Load()
	if n == 0 && g.trips.errN.Load() == 0 {
		return "get trips=0"
	}
	avg := func(b *tripBucket) (ttfb, total, kb float64) {
		c := b.n.Load()
		if c == 0 {
			return 0, 0, 0
		}
		ttfb = float64(b.ttfbNs.Load()) / float64(c) / 1e6
		total = float64(b.totalNs.Load()) / float64(c) / 1e6
		kb = float64(b.bytes.Load()) / float64(c) / 1024
		return
	}
	pT, pTot, _ := avg(&g.trips.pad)
	bT, bTot, bKB := avg(&g.trips.bulk)
	uT, uTot, uKB := avg(&g.trips.up)
	return fmt.Sprintf(
		"get trips=%d err=%d max_ttfb=%.0fms | pad n=%d ttfb=%.0fms total=%.0fms | bulk n=%d ttfb=%.0fms total=%.0fms body=%.1fKiB | up n=%d ttfb=%.0fms total=%.0fms hdr=%.1fKiB",
		n, g.trips.errN.Load(), float64(g.trips.maxTTFBNs.Load())/1e6,
		g.trips.pad.n.Load(), pT, pTot,
		g.trips.bulk.n.Load(), bT, bTot, bKB,
		g.trips.up.n.Load(), uT, uTot, uKB,
	)
}

func (g *GET) noteTrip(ttfb, total time.Duration, up, down int, err error) {
	if err != nil {
		g.trips.errN.Add(1)
		return
	}
	for {
		cur := g.trips.maxTTFBNs.Load()
		if ttfb.Nanoseconds() <= cur || g.trips.maxTTFBNs.CompareAndSwap(cur, ttfb.Nanoseconds()) {
			break
		}
	}
	switch {
	case up >= 2048:
		g.trips.up.add(ttfb, total, up)
	case down >= 8192:
		g.trips.bulk.add(ttfb, total, down)
	default:
		g.trips.pad.add(ttfb, total, down)
	}
}

// NewGET builds a GET client from o. Handshake runs in Start.
func NewGET(token string, o config.Outbound) (*GET, error) {
	prof, err := cdn.Lookup(o.CDN)
	if err != nil {
		return nil, err
	}
	if prof.Front == "ws" {
		return nil, fmt.Errorf("cdn profile %q is ws, use type ws", prof.Name)
	}
	if o.Path == "" {
		o.Path = prof.Path
	}
	if o.HeaderBudget <= 0 {
		o.HeaderBudget = prof.HeaderBudget
	}
	if o.Inflight <= 0 {
		o.Inflight = prof.Inflight
	}
	cl, err := httpClient(o, token)
	if err != nil {
		return nil, err
	}
	u, err := parseURL(o.URL)
	if err != nil {
		return nil, err
	}
	base := strings.TrimRight(o.URL, "/")
	return &GET{
		prof:   prof,
		cfg:    o,
		token:  []byte(token),
		client: cl,
		base:   base,
		path:   strings.TrimRight(o.Path, "/"),
		host:   u.Host,
		sess:   mux.NewClient(),
		done:   make(chan struct{}),
		held:   map[uint64][]wire.Frame{},
	}, nil
}

// Start sends the first GET (X-Auth cookie) and starts the inflight pump.
func (g *GET) Start(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started.Load() {
		return nil
	}
	hello := newClientHello(g.offer)
	var auth []byte
	var nonce []byte
	var err error
	if len(g.ticket) == 0 {
		g.ticket = CachedTicket()
	}
	if len(g.ticket) > 0 {
		auth = append(append([]byte{}, g.ticket...), hello.Encode()...)
		_, nonce, _, err = ecrypto.OpenTicket(g.token, g.ticket)
		if err != nil {
			g.ticket = nil
			auth = nil
		}
	}
	if len(auth) == 0 {
		var proof []byte
		proof, nonce, err = ecrypto.MakeAuth(g.token)
		if err != nil {
			return err
		}
		auth = append(proof, hello.Encode()...)
	}
	key, err := ecrypto.DeriveKey(g.token, nonce)
	if err != nil {
		return err
	}
	g.key = key
	g.sid = "new"
	body, _, err := g.roundTrip(ctx, auth, nil)
	if err != nil {
		return fmt.Errorf("get handshake: %w", err)
	}
	if g.sid == "new" {
		return fmt.Errorf("get handshake: empty session")
	}
	plain, err := ecrypto.Open(g.key, body, []byte(g.sid))
	if err != nil {
		return fmt.Errorf("get handshake: status: %w", err)
	}
	st, err := wire.DecodeStatus(plain)
	if err != nil {
		return fmt.Errorf("get handshake: status: %w", err)
	}
	if !st.Code.OK() {
		return wire.Fail(st.Code, st.Program)
	}
	if len(st.Ticket) > 0 {
		g.ticket = st.Ticket
		RememberTicket(st.Ticket)
	}
	g.caps = st.Caps
	if !g.cfg.UseMux() && !st.Caps.Has(wire.CapConnect) {
		return fmt.Errorf("node requires mux")
	}
	ka := time.Duration(st.KeepaliveMS) * time.Millisecond
	if ka <= 0 {
		ka = 15 * time.Second
	}
	g.keepAlive = ka
	if addr, sni, _, err := resolveTLS(g.cfg); err == nil {
		if err := adoptDecoy(addr, sni, st, strings.TrimSpace(g.cfg.CDN) == ""); err != nil {
			return err
		}
	}
	loopCtx, stop := context.WithCancel(context.Background())
	g.stop = stop
	g.nextSeq = g.seq.Load() + 1
	g.started.Store(true)
	n := g.cfg.Inflight
	if n < 1 {
		n = 1
	}
	go g.pump(loopCtx, n)
	return nil
}

// Session returns the live mux session.
func (g *GET) Session() *mux.Session { return g.sess }

// ConnBytes returns spliced CONNECT bytes when mux is off.
func (g *GET) ConnBytes() (uint64, uint64) {
	return g.connUp.Load(), g.connDown.Load()
}

// Done is closed when the pump dies or held seqs overflow.
func (g *GET) Done() <-chan struct{} { return g.done }

// Open starts the front if needed. Mux off uses HTTP CONNECT splice.
func (g *GET) Open(target string) (net.Conn, error) {
	if !g.started.Load() {
		if err := g.Start(context.Background()); err != nil {
			return nil, err
		}
	}
	if !g.cfg.UseMux() {
		return g.openConnect(target)
	}
	return g.sess.Open(target)
}

// die closes Done once.
func (g *GET) die() {
	g.dead.Do(func() { close(g.done) })
}

// Close stops the pump, sends CmdLeave, and closes mux.
func (g *GET) Close() error {
	if g.stop != nil {
		g.stop()
	}
	g.sendLeave()
	g.sess.Close()
	g.die()
	return nil
}

// sendLeave is the last GET: CmdLeave, then the node Detach+Release.
// Independent of the loop ctx so SIGTERM can still deliver it (GUI SIGKILL is 1.5s).
func (g *GET) sendLeave() {
	if !g.started.Load() || g.key == nil || g.sid == "" || g.sid == "new" {
		return
	}
	raw, err := encodeLeave()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	_, _, _ = g.roundTrip(ctx, nil, raw)
}

// getJob is one uplink batch with its GET seq.
type getJob struct {
	seq     uint64
	payload []byte
}

// pump is the only mux Puller. Seq is assigned in Pull order, then HTTP
// workers run in parallel. Extra loops used to Pull concurrently and
// g.seq.Add in a different order than mux.out — origin TLS saw reordered
// records (bad record MAC) and Yandex upload sat at ~100 kbit/s.
func (g *GET) pump(ctx context.Context, inflight int) {
	jobs := make(chan getJob, inflight)
	var wg sync.WaitGroup
	for i := 0; i < inflight; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.worker(ctx, jobs)
		}()
	}
	defer func() {
		close(jobs)
		wg.Wait()
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		wait := g.pace()
		pullWait := wait
		queued := g.sess.OutQueued() > 0
		if queued || wait == 0 {
			pullWait = 0
		} else if g.outstanding() > 0 {
			// Warm/idle: wait for in-flight before another pad so we
			// don't stampede, and so a Data body can set lastLive.
			if !g.sleepCtx(ctx, 5*time.Millisecond) {
				return
			}
			continue
		}
		// Pads must not occupy every worker: Open/Data uplink then
		// waits a full RTT (TCP 1-byte went to ~8 RTT / 640ms).
		padMax := inflight / 4
		if padMax < 4 {
			padMax = 4
		}
		if padMax > inflight {
			padMax = inflight
		}
		if pullWait == 0 && !queued && g.outstanding() >= padMax {
			if !g.sleepCtx(ctx, 5*time.Millisecond) {
				return
			}
			continue
		}
		up := g.sess.Pull(g.uplinkBudget(), pullWait)
		if muxBusy(up) {
			g.upBusy.Store(true)
			g.markLive()
		} else {
			g.upBusy.Store(false)
			if g.outstanding() >= padMax {
				if !g.sleepCtx(ctx, 5*time.Millisecond) {
					return
				}
				continue
			}
			if wait > 0 {
				// paced pad (idle keepalive)
			} else if g.busy.Load() || g.catchingDown() {
				g.noteCatch()
			} else {
				if !g.sleepCtx(ctx, 20*time.Millisecond) {
					return
				}
				continue
			}
		}
		raw, err := wire.EncodeBatch(up)
		if err != nil {
			continue
		}
		seq := g.seq.Add(1)
		select {
		case jobs <- getJob{seq: seq, payload: raw}:
		case <-ctx.Done():
			return
		}
	}
}

// worker runs one inflight HTTP GET per job and retries on front reset.
func (g *GET) worker(ctx context.Context, jobs <-chan getJob) {
	for {
		select {
		case <-ctx.Done():
			return
		case j, ok := <-jobs:
			if !ok {
				return
			}
			t0 := time.Now()
			tries := 0
			for {
				_, _, _, err := g.roundTripAt(ctx, nil, j.payload, j.seq)
				if err == nil {
					break
				}
				if !retryableGET(err) {
					fmt.Fprintf(os.Stderr, "get front %v\n", err)
					g.die()
					return
				}
				if frontReset(err) {
					fmt.Fprintf(os.Stderr, "get front reset\n")
					g.busy.Store(false)
					g.dropIdleFront()
				}
				tries++
				if tries > 8 || time.Since(t0) > 12*time.Second {
					fmt.Fprintf(os.Stderr, "get front give up seq=%d %v\n", j.seq, err)
					g.die()
					return
				}
				wait := 50 * time.Millisecond
				if !frontReset(err) {
					wait = time.Duration(tries) * 150 * time.Millisecond
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
	}
}

// dropIdleFront drops a dead HTTP/2 conn so the next GET dials a new one.
func (g *GET) dropIdleFront() {
	if g.client == nil {
		return
	}
	now := time.Now().UnixNano()
	prev := g.lastDrop.Load()
	if prev != 0 && now-prev < int64(200*time.Millisecond) {
		return
	}
	if !g.lastDrop.CompareAndSwap(prev, now) {
		return
	}
	g.client.CloseIdleConnections()
}

// markLive records useful mux so pace stays warm for a short window.
func (g *GET) markLive() {
	g.catch.Store(8)
	g.lastLive.Store(time.Now().UnixNano())
}

// liveWindow is true for 800ms after the last useful up or down.
func (g *GET) liveWindow() bool {
	t := g.lastLive.Load()
	if t == 0 {
		return false
	}
	return time.Since(time.Unix(0, t)) < 800*time.Millisecond
}

// outstanding is seqs assigned but not yet applied by pushOrdered.
func (g *GET) outstanding() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.nextSeq == 0 {
		return int(g.seq.Load())
	}
	n := int64(g.seq.Load()) - int64(g.nextSeq) + 1
	if n < 0 {
		return 0
	}
	return int(n)
}

// catchingDown is true while extra pad GETs after useful mux remain.
func (g *GET) catchingDown() bool {
	return g.catch.Load() > 0
}

// wantFull keeps inflight packed after useful mux. A count of empty
// pads used to clear busy (inflight=8 blows past 4 in one RTT) and
// the pump slept 1s — live download fell ~10× vs an 80ms pipeline.
func (g *GET) wantFull() bool {
	return g.busy.Load() || g.catchingDown() || g.sess.OutQueued() > 0 || g.liveWindow()
}

// noteCatch consumes one catch pad after a full inflight GET.
func (g *GET) noteCatch() {
	for {
		v := g.catch.Load()
		if v <= 0 {
			return
		}
		if g.catch.CompareAndSwap(v, v-1) {
			return
		}
	}
}

// pace: idle GETs once per keepalive so :444 is not a metronome for ТСПУ.
// busy / catch / uplink → fill inflight (download). lastLive or live TCP
// warms at 20ms so a gapped download does not sleep 1s (~10×). pace=0
// on lastLive packed 8 pads and TCP 1-byte waited ~8 RTT.
func (g *GET) pace() time.Duration {
	if g.busy.Load() || g.catchingDown() || g.sess.OutQueued() > 0 {
		return 0
	}
	if g.liveWindow() || g.sess.StreamCount() > 0 {
		return 20 * time.Millisecond
	}
	d := g.keepAlive / 2
	if d < time.Second {
		d = time.Second
	}
	return d
}

// uplinkBudget is decoded mux plaintext that still fits HeaderBudget after AEAD and base64url.
func (g *GET) uplinkBudget() int {
	h := g.cfg.HeaderBudget
	if h <= 0 {
		h = 16 << 10
	}
	// X-Request-Id is base64.RawURLEncoding(nonce||ct). Keep the b64
	// value inside HeaderBudget so the GET is not 413 / truncated
	// (truncated uplink used to consume seq and drop POST Data).
	n := h*3/4 - 12 - 16 - 96
	if n < 1024 {
		n = 1024
	}
	if n > wire.MaxPayload {
		n = wire.MaxPayload
	}
	return n
}

// sleepCtx waits d or returns false if ctx is done.
func (g *GET) sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// pipelineSlot is true when fewer than Inflight seqs are still waiting
// on pushOrdered. Pump does not call it: jobs+workers over-subscribe so
// a pad is waiting to be exclusive Puller. Capping at Inflight starved a
// 4 MB GET (2.1/4 MB, 2.6s gap). Extra loops can stampede HTTP/1.1.
func (g *GET) pipelineSlot() bool {
	n := g.cfg.Inflight
	if n < 1 {
		n = 1
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.nextSeq == 0 {
		return true
	}
	ahead := int64(g.seq.Load()) - int64(g.nextSeq) + 1
	return ahead < int64(n)
}

// muxBusy is true when frames carry Open, Data, Dgram, Leave, or Close (not Pad/Ping/Pong).
func muxBusy(frames []wire.Frame) bool {
	for _, f := range frames {
		switch f.Cmd {
		case wire.CmdPad, wire.CmdPing, wire.CmdPong:
			continue
		default:
			return true
		}
	}
	return false
}

// frontReset reports a dropped HTTP connection that the worker should retry.
func frontReset(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "goaway") ||
		strings.Contains(s, "stream error") ||
		strings.Contains(s, "http2: client conn") ||
		strings.Contains(s, "unexpected eof")
}

// retryableGET is a transient front/CDN failure. 413/405/404 must not HOL-block.
func retryableGET(err error) bool {
	if err == nil {
		return false
	}
	if frontReset(err) {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "header too large") || strings.Contains(s, "method not allowed") {
		return false
	}
	if strings.Contains(s, "status 4") {
		return strings.Contains(s, "status 408") || strings.Contains(s, "status 429")
	}
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "deadline") ||
		strings.Contains(s, "status 502") ||
		strings.Contains(s, "status 503") ||
		strings.Contains(s, "status 504") ||
		strings.Contains(s, "unavailable") ||
		strings.Contains(s, "eof") ||
		strings.Contains(s, "http2:")
}

// roundTrip is roundTripAt with seq assigned inside.
func (g *GET) roundTrip(ctx context.Context, auth, uplink []byte) ([]byte, http.Header, error) {
	b, h, _, err := g.roundTripAt(ctx, auth, uplink, 0)
	return b, h, err
}

// roundTripAt issues one GET. Auth uses Cookie+X-Auth; later uplink is X-Request-Id.
func (g *GET) roundTripAt(ctx context.Context, auth, uplink []byte, seq uint64) ([]byte, http.Header, uint64, error) {
	var blob []byte
	var err error
	if len(auth) > 0 {
		blob = auth
	} else {
		payload := uplink
		if len(payload) == 0 {
			pad := make([]byte, 24+randN(80))
			_, _ = rand.Read(pad)
			payload, err = wire.EncodeBatch([]wire.Frame{{Cmd: wire.CmdPad, Payload: pad}})
			if err != nil {
				return nil, nil, seq, err
			}
		}
		blob, err = ecrypto.Seal(g.key, payload, []byte(g.sid))
		if err != nil {
			return nil, nil, seq, err
		}
	}
	if seq == 0 {
		seq = g.seq.Add(1)
	}
	ext := g.prof.Ext
	if ext == "" {
		ext = ".js"
	}
	url := fmt.Sprintf("%s%s/%s/%d%s", g.base, g.path, g.sid, seq, ext)
	// 2s was shorter than Google/Chrome TLS+HTTP2. Timed-out GETs used to
	// finishMux(empty) and skip that seq — the next DATA was a hole in the
	// TLS record stream (ERR_SSL_PROTOCOL_ERROR / LibreSSL bad decrypt).
	timeout := g.tripTimeout(len(auth) > 0)
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, seq, err
	}
	dest := g.prof.FetchDest
	if dest == "" {
		dest = "script"
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "ru-RU,ru;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("User-Agent", chromeUA)
	req.Header.Set("Sec-Fetch-Dest", dest)
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", g.base)
	req.Header.Set("Referer", g.base+"/")
	b64 := base64.RawURLEncoding.EncodeToString(blob)
	if len(auth) > 0 {
		req.Header.Set("Cookie", "session="+b64)
		req.Header.Set("X-Auth", "1")
	} else if strings.EqualFold(g.prof.UplinkHeader, "Cookie") {
		req.Header.Set("Cookie", "session="+b64)
	} else {
		req.Header.Set("X-Request-Id", b64)
	}

	t0 := time.Now()
	resp, err := g.client.Do(req)
	ttfb := time.Since(t0)
	if err != nil {
		g.noteTrip(ttfb, ttfb, len(blob), 0, err)
		// Do not finishMux(empty): that skips this seq and the next GET's
		// DATA lands in the middle of a TLS record (Chrome ERR_SSL_PROTOCOL_ERROR).
		return nil, nil, seq, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed {
		g.noteTrip(ttfb, time.Since(t0), len(blob), 0, fmt.Errorf("status"))
		return nil, nil, seq, fmt.Errorf("GET front: method not allowed (use type get)")
	}
	if resp.StatusCode == http.StatusRequestEntityTooLarge || resp.StatusCode == 413 {
		g.noteTrip(ttfb, time.Since(t0), len(blob), 0, fmt.Errorf("status"))
		return nil, nil, seq, fmt.Errorf("GET front: header too large (413): lower header_budget")
	}
	if resp.StatusCode != http.StatusOK {
		slurp, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		g.noteTrip(ttfb, time.Since(t0), len(blob), 0, fmt.Errorf("status"))
		return nil, resp.Header, seq, fmt.Errorf("GET front: status %d: %s", resp.StatusCode, slurp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	total := time.Since(t0)
	if err != nil {
		g.noteTrip(ttfb, total, len(blob), len(body), err)
		return nil, resp.Header, seq, err
	}
	g.noteTrip(ttfb, total, len(blob), len(body), nil)
	if sid := resp.Header.Get("X-Session"); sid != "" {
		g.sid = sid
	}
	if len(auth) > 0 {
		return body, resp.Header, seq, nil
	}
	var frames []wire.Frame
	if len(body) > 0 && g.key != nil && g.sid != "new" {
		plain, err := ecrypto.Open(g.key, body, []byte(g.sid))
		if err != nil {
			// Do not finishMux(empty): that punches a hole in the TLS stream.
			return nil, resp.Header, seq, err
		}
		frames, err = wire.DecodeBatch(plain)
		if err != nil {
			return nil, resp.Header, seq, err
		}
	}
	g.finishMux(seq, auth, frames)
	return body, resp.Header, seq, nil
}

func (g *GET) tripTimeout(auth bool) time.Duration {
	if auth {
		return 20 * time.Second
	}
	if d := g.prof.TripTimeout; d > 0 {
		return d
	}
	return 15 * time.Second
}

// finishMux applies downlink in GET seq order.
func (g *GET) finishMux(seq uint64, auth []byte, frames []wire.Frame) {
	if len(auth) > 0 {
		return
	}
	g.pushOrdered(seq, frames)
}

// pushOrdered applies downlink in GET seq order. Inflight>1 otherwise
// reorders CmdData and Chrome shows ERR_CONNECTION_CLOSED / TLS bad decrypt.
func (g *GET) pushOrdered(seq uint64, frames []wire.Frame) {
	g.mu.Lock()
	if !g.started.Load() || g.nextSeq == 0 {
		g.mu.Unlock()
		g.sess.Push(frames)
		return
	}
	if seq < g.nextSeq {
		g.mu.Unlock()
		return
	}
	if seq > g.nextSeq {
		if len(g.held) >= 256 {
			g.mu.Unlock()
			g.die()
			return
		}
		g.held[seq] = frames
		g.mu.Unlock()
		return
	}
	var batch [][]wire.Frame
	batch = append(batch, frames)
	g.nextSeq++
	for {
		extra, ok := g.held[g.nextSeq]
		if !ok {
			break
		}
		delete(g.held, g.nextSeq)
		batch = append(batch, extra)
		g.nextSeq++
	}
	g.mu.Unlock()
	useful := false
	g.pushMu.Lock()
	for _, fs := range batch {
		if muxBusy(fs) {
			useful = true
		}
		g.sess.Push(fs)
	}
	g.pushMu.Unlock()
	if useful {
		g.busy.Store(true)
		g.markLive()
	} else if !g.liveWindow() {
		g.busy.Store(false)
	}
}

// chromeUA is the browser User-Agent GET and WS send on the public path.
const chromeUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// randN returns a random integer in [0, n).
func randN(n int) int {
	if n <= 0 {
		return 0
	}
	var b [2]byte
	_, _ = rand.Read(b[:])
	return int(b[0]) % n
}

// GETServer is the origin for the GET front (CDN or direct).
type GETServer struct {
	Profile cdn.Profile
	Token   []byte
	Path    string
	Decoy   []string
	Info    NodeInfo
	Seen    *ecrypto.Nonces

	mu       sync.Mutex
	sessions map[string]*serverSess
}

// serverSess is one GET session: ordered uplink, exclusive downlink Puller.
type serverSess struct {
	id       string
	key      []byte
	mux      *mux.Session
	once     sync.Once
	mu       sync.Mutex
	cond     *sync.Cond
	nextSeq  uint64
	downSeq  uint64
	replied  uint64
	pulling  bool
	held     map[uint64][]wire.Frame
	skipPull map[uint64]struct{}
	replies  map[uint64][]byte
	replyQ   []uint64
}

const replyCacheN = 32

// NewGETServer builds a GET origin with the generic CDN profile.
func NewGETServer(token, path string) *GETServer {
	return NewGETServerProfile(token, path, cdn.GenericGET())
}

// NewGETServerProfile builds a GET origin with a named CDN profile (path, ext, type).
func NewGETServerProfile(token, path string, prof cdn.Profile) *GETServer {
	if path == "" {
		path = prof.Path
	}
	if prof.Ext == "" {
		prof.Ext = ".woff2"
	}
	if prof.ContentType == "" {
		prof.ContentType = "font/woff2"
	}
	return &GETServer{
		Profile:  prof,
		Token:    []byte(token),
		Path:     strings.TrimRight(path, "/"),
		Seen:     ecrypto.NewNonces(),
		sessions: map[string]*serverSess{},
	}
}

// contentType is the response Content-Type (default font/woff2).
func (s *GETServer) contentType() string {
	if s.Profile.ContentType != "" {
		return s.Profile.ContentType
	}
	return "font/woff2"
}

// Match reports whether p is under this front's asset path.
func (s *GETServer) Match(p string) bool {
	return strings.HasPrefix(p, s.Path+"/")
}

// ServeHTTP handles the first GET (auth) and later GETs (header uplink, exclusive Puller).
func (s *GETServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	setCDNHeaders(w)
	sid, seqStr := parseGETPath(s.Path, r.URL.Path)
	seq, _ := strconv.ParseUint(seqStr, 10, 64)
	rawHdr := r.Header.Get("X-Request-Id")
	if c := r.Header.Get("Cookie"); strings.HasPrefix(c, "session=") {
		rawHdr = strings.TrimPrefix(c, "session=")
	}
	blob, _ := base64.RawURLEncoding.DecodeString(rawHdr)

	var ss *serverSess
	if r.Header.Get("X-Auth") == "1" || sid == "new" {
		token, nonce, rest, err := frontAuth(s.Token, blob, s.Seen)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		key, err := ecrypto.DeriveKey(token, nonce)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ss = s.spawn(key)
		if seq > 0 {
			ss.nextSeq = seq + 1
		} else {
			ss.nextSeq = 2
		}
		ss.downSeq = ss.nextSeq
		if ss.nextSeq > 0 {
			ss.replied = ss.nextSeq - 1
		}
		attachHello(ss.mux, rest)
		w.Header().Set("X-Session", ss.id)
		sid = ss.id
		st := s.Info.Status(ss.id)
		if len(s.Info.Token) == 0 {
			st = withDecoy(netconf.DefaultSession().Status(wire.OK, "", "", ss.id, 1280, 0), s.Decoy)
		}
		finishStatus(&st, token, ss.id, rest, ss.mux, remoteAddr(r.RemoteAddr))
		sealed, err := ecrypto.Seal(ss.key, st.Encode(), []byte(ss.id))
		if err != nil {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", s.contentType())
		w.Header().Set("Content-Length", strconv.Itoa(len(sealed)))
		if r.Method != http.MethodHead {
			_, _ = io.Copy(w, bytes.NewReader(sealed))
		}
		return
	} else {
		ss = s.get(sid)
		if ss == nil {
			http.NotFound(w, r)
			return
		}
		var frames []wire.Frame
		if len(blob) > 0 {
			plain, err := ecrypto.Open(ss.key, blob, []byte(ss.id))
			if err != nil {
				// Do not ingest as empty: that advances nextSeq and the
				// client retry of the same seq is dropped (TLS bad MAC).
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
			frames, err = wire.DecodeBatch(plain)
			if err != nil {
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
		}
		// HTTP/2 waiters sit until they are Puller. Jumping downSeq on
		// a 200ms timeout skipped Pulls and live GET download lagged WS.
		// HTTP/1.1: a Data GET must not sit behind another conn.
		// Upload Data on HTTP/2 used to wait the same 5s Puller queue:
		// Yandex Cookie TTFB went 238ms (pad) → 973ms (14 KiB up) and
		// CDN upload sat at ~3 Mbit. Return empty at once if not Puller;
		// jump stays false so downSeq is not skipped.
		turn := getH2PullerWait
		jump := false
		if r.ProtoMajor < 2 {
			jump = true
			if usefulMux(frames) {
				turn = 0
			} else {
				turn = getTurnWait
			}
		} else if usefulMux(frames) {
			turn = 0
		}
		if seq > 0 {
			if cached := ss.replayReply(seq); cached != nil {
				s.writeSealed(w, r, cached)
				return
			}
		}
		down, pulled := ss.ingestAndPull(seq, frames, turn, jump)
		if ss.mux.Closed() {
			s.drop(ss.id)
		}
		if pulled {
			sealed, err := s.sealDown(ss, down)
			if err != nil {
				ss.endPull(seq)
				http.Error(w, "unavailable", http.StatusBadGateway)
				return
			}
			sealed = ss.rememberReply(seq, sealed)
			ss.endPull(seq)
			s.writeSealed(w, r, sealed)
			return
		}
		if cached := ss.replayReply(seq); cached != nil {
			s.writeSealed(w, r, cached)
			return
		}
		s.writeGET(w, r, ss, seq, down)
		return
	}
}

// writeGET seals downlink (with pad to ~400 bytes) and writes the GET body.
func (s *GETServer) writeGET(w http.ResponseWriter, r *http.Request, ss *serverSess, seq uint64, down []wire.Frame) {
	sealed, err := s.sealDown(ss, down)
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	if seq > 0 {
		sealed = ss.rememberReply(seq, sealed)
	}
	s.writeSealed(w, r, sealed)
}

func (s *GETServer) sealDown(ss *serverSess, down []wire.Frame) ([]byte, error) {
	rawSize := 0
	for _, f := range down {
		rawSize += wire.HeaderSize + len(f.Payload)
	}
	if rawSize < 400 {
		pad := make([]byte, 400-rawSize+randN(200))
		_, _ = rand.Read(pad)
		down = append(down, wire.Frame{Cmd: wire.CmdPad, Payload: pad})
	}
	payload, err := wire.EncodeBatch(down)
	if err != nil {
		return nil, err
	}
	return ecrypto.Seal(ss.key, payload, []byte(ss.id))
}

func (s *GETServer) writeSealed(w http.ResponseWriter, r *http.Request, sealed []byte) {
	w.Header().Set("Content-Type", s.contentType())
	w.Header().Set("Content-Length", strconv.Itoa(len(sealed)))
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, bytes.NewReader(sealed))
}

// spawn allocates a GET session id and starts mux Accept and datagram relay.
func (s *GETServer) spawn(key []byte) *serverSess {
	ss := &serverSess{
		id:       ecrypto.RandomID(),
		key:      key,
		mux:      mux.NewServer(),
		held:     map[uint64][]wire.Frame{},
		skipPull: map[uint64]struct{}{},
		replies:  map[uint64][]byte{},
	}
	ss.cond = sync.NewCond(&ss.mu)
	s.mu.Lock()
	s.sessions[ss.id] = ss
	s.mu.Unlock()
	ss.once.Do(func() {
		go serveMux(ss.mux)
		go serveDgrams(ss.mux)
	})
	return ss
}

// get returns the live session for id, or nil.
func (s *GETServer) get(id string) *serverSess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

// GET Puller hold. Idle pads used to sit 250ms as exclusive Puller;
// ICMP and TCP 1-byte then waited that plus an HTTP/2 waiter jump.
const (
	getDownlinkBytes = 256 << 10
	// Several streams (Speedtest ping during download). 32 KiB rotated
	// the Puller faster but on a CDN RTT (~240 ms) cut multi-stream
	// download to ~1–3 Mbit. other-stream pri already lets ping jump
	// mux.out; keep live bodies near the single-stream size.
	getDownlinkLive = 192 << 10
	getDownlinkMore = 15 * time.Millisecond
	getDownlinkFill = 3 * time.Millisecond
	// Overlay ICMP is sync (FilterDgram). Internet ICMP is async: hold
	// this GET so the echo-reply can ride the same body.
	getDgramHold = 80 * time.Millisecond
	getTurnWait  = 5 * time.Millisecond
	// HTTP/2 waiters poll until they are Puller. Safety cap only.
	getH2PullerWait = 5 * time.Second
)

// pullHold is how long the exclusive Puller waits for mux downlink.
// Empty keepalive pads return immediately so they do not stall ping/upload.
func pullHold(frames []wire.Frame, ms *mux.Session) (first, more time.Duration) {
	if ms != nil && ms.OutQueued() > 0 {
		return 0, getDownlinkFill
	}
	n := muxDataBytes(frames)
	if n >= 2048 {
		return 0, 0
	}
	if n > 0 || hasCmd(frames, wire.CmdOpen) {
		return getDownlinkMore, getDownlinkFill
	}
	if hasCmd(frames, wire.CmdDgram) || hasCmd(frames, wire.CmdPing) {
		return getDgramHold, getDownlinkFill
	}
	return 0, 0
}

// muxDataBytes is the total CmdData payload in frames (bulk-upload detector).
func muxDataBytes(frames []wire.Frame) int {
	n := 0
	for _, f := range frames {
		if f.Cmd == wire.CmdData {
			n += len(f.Payload)
		}
	}
	return n
}

// usefulMux is true when frames carry Open, Data, Dgram, Leave, or Close.
func usefulMux(frames []wire.Frame) bool {
	for _, f := range frames {
		switch f.Cmd {
		case wire.CmdPad, wire.CmdPing, wire.CmdPong:
			continue
		default:
			return true
		}
	}
	return false
}

// hasCmd reports whether any frame is cmd.
func hasCmd(frames []wire.Frame, cmd byte) bool {
	for _, f := range frames {
		if f.Cmd == cmd {
			return true
		}
	}
	return false
}

// pullDownlink holds up to first for mux, then more after useful data, up to 64KiB.
func pullDownlink(ms *mux.Session, first, more time.Duration) []wire.Frame {
	deadline := time.Now()
	if first > 0 {
		deadline = time.Now().Add(first)
	}
	max := getDownlinkBytes
	if ms != nil && ms.StreamCount() > 1 {
		max = getDownlinkLive
	}
	var down []wire.Frame
	n := 0
	for n < max {
		wait := time.Until(deadline)
		if wait < 0 {
			wait = 0
		}
		chunk := ms.Pull(max-n, wait)
		if len(chunk) == 0 {
			if usefulMux(down) || wait == 0 {
				break
			}
			continue
		}
		down = append(down, chunk...)
		for _, f := range chunk {
			n += wire.HeaderSize + len(f.Payload)
		}
		if usefulMux(down) {
			if more <= 0 {
				deadline = time.Now()
			} else {
				deadline = time.Now().Add(more)
			}
		}
	}
	return down
}

// ingestAndPull applies uplink in seq order. Only the in-order GET Pulls
// mux downlink. pullLoop assigned bodies to whatever GET was free; if that
// HTTP response was dropped, mux Data vanished (Speedtest/YouTube TLS holes).
func (ss *serverSess) ingestAndPull(seq uint64, frames []wire.Frame, waitTurn time.Duration, jump bool) ([]wire.Frame, bool) {
	if seq == 0 {
		if len(frames) > 0 {
			ss.mux.Push(frames)
		}
		first, more := pullHold(frames, ss.mux)
		return pullDownlink(ss.mux, first, more), false
	}
	ss.mu.Lock()
	ss.pushUplinkLocked(seq, frames)
	if ss.downSeq == 0 {
		ss.downSeq = seq
	}
	ss.drainSkipPullLocked()
	if seq < ss.downSeq {
		ss.mu.Unlock()
		return nil, false
	}
	deadline := time.Time{}
	if waitTurn > 0 {
		deadline = time.Now().Add(waitTurn)
	}
	for seq != ss.downSeq || ss.pulling {
		if seq < ss.downSeq {
			ss.mu.Unlock()
			return nil, false
		}
		ss.drainSkipPullLocked()
		if seq == ss.downSeq && !ss.pulling {
			break
		}
		notTurn := seq != ss.downSeq
		timedOut := (waitTurn <= 0 && notTurn) || (!deadline.IsZero() && time.Now().After(deadline))
		if timedOut {
			if jump && notTurn && seq > ss.replied {
				ss.replied = seq
			} else if notTurn {
				if ss.skipPull == nil {
					ss.skipPull = map[uint64]struct{}{}
				}
				if len(ss.skipPull) >= 256 {
					ss.mu.Unlock()
					ss.mux.Close()
					return nil, false
				}
				ss.skipPull[seq] = struct{}{}
			}
			if ss.cond != nil {
				ss.cond.Broadcast()
			}
			ss.mu.Unlock()
			return nil, false
		}
		if ss.cond == nil {
			ss.mu.Unlock()
			time.Sleep(time.Millisecond)
			ss.mu.Lock()
			continue
		}
		wait := 2 * time.Millisecond
		if !deadline.IsZero() {
			if remain := time.Until(deadline); remain < wait {
				wait = remain
			}
		}
		if wait < 0 {
			wait = 0
		}
		timer := time.AfterFunc(wait, func() {
			ss.mu.Lock()
			if ss.cond != nil {
				ss.cond.Broadcast()
			}
			ss.mu.Unlock()
		})
		ss.cond.Wait()
		timer.Stop()
	}
	if seq < ss.downSeq {
		ss.mu.Unlock()
		return nil, false
	}
	ss.pulling = true
	ss.mu.Unlock()
	first, more := pullHold(frames, ss.mux)
	down := pullDownlink(ss.mux, first, more)
	return down, true
}

func (ss *serverSess) endPull(seq uint64) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if !ss.pulling {
		return
	}
	ss.pulling = false
	if seq > ss.replied {
		ss.replied = seq
	}
	ss.downSeq = ss.replied + 1
	ss.drainSkipPullLocked()
	if ss.cond != nil {
		ss.cond.Broadcast()
	}
}

func (ss *serverSess) rememberReply(seq uint64, body []byte) []byte {
	if seq == 0 || len(body) == 0 {
		return body
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.replies == nil {
		ss.replies = map[uint64][]byte{}
	}
	if prev, ok := ss.replies[seq]; ok {
		return prev
	}
	ss.replies[seq] = body
	ss.replyQ = append(ss.replyQ, seq)
	for len(ss.replyQ) > replyCacheN {
		old := ss.replyQ[0]
		ss.replyQ = ss.replyQ[1:]
		delete(ss.replies, old)
	}
	return body
}

func (ss *serverSess) replayReply(seq uint64) []byte {
	if seq == 0 {
		return nil
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if ss.replies == nil {
		return nil
	}
	return ss.replies[seq]
}

// drainSkipPullLocked advances downSeq over Data GETs that already
// returned empty (HTTP/2 upload workers). ss.mu must be held.
func (ss *serverSess) drainSkipPullLocked() {
	if ss.skipPull == nil {
		return
	}
	for {
		if _, ok := ss.skipPull[ss.downSeq]; !ok {
			return
		}
		delete(ss.skipPull, ss.downSeq)
		ss.downSeq++
		if ss.replied < ss.downSeq-1 {
			ss.replied = ss.downSeq - 1
		}
	}
}

// pushUplink applies uplink in seq order, holding out-of-order GETs.
func (ss *serverSess) pushUplink(seq uint64, frames []wire.Frame) {
	if seq == 0 {
		ss.mux.Push(frames)
		return
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.pushUplinkLocked(seq, frames)
}

// pushUplinkLocked is pushUplink with ss.mu already held.
func (ss *serverSess) pushUplinkLocked(seq uint64, frames []wire.Frame) {
	if ss.nextSeq == 0 {
		ss.nextSeq = seq
	}
	if seq < ss.nextSeq {
		return
	}
	if seq > ss.nextSeq {
		// Inflight 48 plus the next wave (Speedtest download→upload)
		// used to hit 64 and Close the mux: download looked fine, upload died.
		if len(ss.held) >= 256 {
			ss.mux.Close()
			return
		}
		ss.held[seq] = frames
		return
	}
	ss.mux.Push(frames)
	ss.nextSeq++
	for {
		extra, ok := ss.held[ss.nextSeq]
		if !ok {
			break
		}
		delete(ss.held, ss.nextSeq)
		ss.mux.Push(extra)
		ss.nextSeq++
	}
}

// drop forgets id and closes its mux (Detach+Release follow from serveMux).
func (s *GETServer) drop(id string) {
	s.mu.Lock()
	ss := s.sessions[id]
	delete(s.sessions, id)
	s.mu.Unlock()
	if ss != nil {
		ss.mux.Close()
	}
}

// parseGETPath splits {base}/{session}/{seq}.ext into sid and seq.
func parseGETPath(prefix, p string) (sid string, seq string) {
	rest := strings.TrimPrefix(p, prefix+"/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 {
		return "", ""
	}
	sid = parts[0]
	if len(parts) > 1 {
		seq = strings.TrimSuffix(parts[1], filepath.Ext(parts[1]))
	}
	return sid, seq
}

// setCDNHeaders disables caching so a CDN does not store AEAD bodies.
func setCDNHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("CDN-Cache-Control", "no-store")
	w.Header().Set("X-Accel-Expires", "0")
	w.Header().Set("Vary", "Cookie, X-Request-Id")
}

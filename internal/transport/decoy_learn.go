package transport

// Decoy learning caches node identity, DPI policy, replicas, and roster.

import (
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"

	"loft/internal/config"
	"loft/internal/wire"
)

// learned is this process's view of each node's decoy identity, keyed by
// outbound host:port. The node is the source of truth (Status.Decoy); a
// probe of the IP (no HMAC) is only a bootstrap so the first ClientHello
// already uses the right SNI.
var learned sync.Map // string -> []string; empty slice = decoy off

// RememberDecoy stores origins for addr. Empty slice means decoy is off.
func RememberDecoy(addr string, origins []string) {
	if origins == nil {
		origins = []string{}
	}
	learned.Store(addr, append([]string(nil), origins...))
}

// CachedDecoy returns a copy of stored origins for addr.
func CachedDecoy(addr string) (origins []string, known bool) {
	v, ok := learned.Load(addr)
	if !ok {
		return nil, false
	}
	return append([]string(nil), v.([]string)...), true
}

// clearDecoyCache drops every learned decoy entry.
func clearDecoyCache() { learned.Range(func(k, _ any) bool { learned.Delete(k); return true }) }

// dpiPol and replicaMap cache Status dpi and unsigned replicas per addr.
var dpiPol sync.Map     // addr -> DPIPolicy
var replicaMap sync.Map // addr -> []string

// RememberDPI stores Status dpi_flags and dpi_pad for addr.
func RememberDPI(addr string, flags byte, pad uint16) {
	dpiPol.Store(addr, DPIPolicy{Fat: flags&1 != 0, Split: flags&2 != 0, Pad: pad})
}

// CachedDPI returns the stored policy, or Fat ClientHello by default.
func CachedDPI(addr string) DPIPolicy {
	v, ok := dpiPol.Load(addr)
	if !ok {
		return DPIPolicy{Fat: true}
	}
	return v.(DPIPolicy)
}

// RememberReplicas stores unsigned replica hosts from Status.
func RememberReplicas(addr string, hosts []string) {
	replicaMap.Store(addr, append([]string(nil), hosts...))
}

// rosterMu guards heldRoster and pinnedPub.
var rosterMu sync.Mutex
var heldRoster wire.Roster
var pinnedPub []byte

// PinControllerPub rejects later rosters signed by a different key.
func PinControllerPub(pub []byte) {
	rosterMu.Lock()
	pinnedPub = append([]byte(nil), pub...)
	rosterMu.Unlock()
}

// RememberRoster stores a verified roster with a non-decreasing serial.
func RememberRoster(raw []byte) {
	if len(raw) == 0 {
		return
	}
	r, err := wire.DecodeRoster(raw)
	if err != nil {
		return
	}
	if err := r.Verify(); err != nil {
		return
	}
	rosterMu.Lock()
	defer rosterMu.Unlock()
	if len(pinnedPub) > 0 && string(pinnedPub) != string(r.Pub) {
		return
	}
	if len(heldRoster.Pub) > 0 && string(heldRoster.Pub) != string(r.Pub) {
		return
	}
	if r.Serial < heldRoster.Serial {
		return
	}
	heldRoster = r
}

// CachedRoster returns live (non-drain) relays from the held roster.
func CachedRoster() []wire.RelayInfo {
	rosterMu.Lock()
	defer rosterMu.Unlock()
	out := make([]wire.RelayInfo, 0, len(heldRoster.Relays))
	for _, e := range heldRoster.Relays {
		if e.Drain || e.URL == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

// CachedReplicas returns a copy of replica hosts for addr.
func CachedReplicas(addr string) []string {
	v, ok := replicaMap.Load(addr)
	if !ok {
		return nil
	}
	return append([]string(nil), v.([]string)...)
}

// DPIPolicy is Status dpi_flags (bit 0 fat ClientHello, bit 1 split) and dpi_pad.
type DPIPolicy struct {
	Fat   bool
	Split bool
	Pad   uint16
}

// AdaptError: ClientHello SNI did not match the node's identity. Start retries
// once with the cached origin.
type AdaptError struct {
	Addr string
	SNI  string
}

// Error implements error.
func (e *AdaptError) Error() string {
	return "decoy: node identity is " + e.SNI
}

// outboundAddr is host:port of o.URL.
func outboundAddr(o config.Outbound) (string, error) {
	u, err := parseURL(o.URL)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http", "ws":
			port = "80"
		default:
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

// resolveTLS picks ClientHello SNI. CDN fronts keep the URL host. Direct
// fronts use the node's advertised (or probed) decoy hostname and skip
// verify, because the loft path presents the node's cert under that SNI.
func resolveTLS(o config.Outbound) (addr, sni string, insecure bool, err error) {
	u, err := parseURL(o.URL)
	if err != nil {
		return "", "", false, err
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http", "ws":
			port = "80"
		default:
			port = "443"
		}
	}
	addr = net.JoinHostPort(u.Hostname(), port)
	sni = o.SNI
	if sni == "" {
		sni = u.Hostname()
	}
	insecure = o.Insecure || u.Scheme == "http" || u.Scheme == "ws"
	if origins, known := CachedDecoy(addr); known {
		if o.CDN != "" {
			return addr, sni, insecure, nil
		}
		if len(origins) > 0 {
			return addr, decoyHost(origins[0]), true, nil
		}
		return addr, sni, insecure, nil
	}
	if o.CDN != "" || u.Scheme == "http" || u.Scheme == "ws" {
		return addr, sni, insecure, nil
	}
	if o.Decoy != "" {
		return addr, decoyHost(o.Decoy), true, nil
	}
	// DiscoverDecoy splices without session HMAC. A mask node holds that
	// probe; Status + AdaptError already teach the decoy name. Skip it.
	return addr, sni, insecure, nil
}

// adoptDecoy caches Status identity fields and returns AdaptError on SNI mismatch.
func adoptDecoy(addr, usedSNI string, st wire.Status, apply bool) error {
	hadMask := len(CachedMaskPub(addr)) == 32
	RememberDecoy(addr, st.Decoy)
	RememberFronts(addr, st.Caps)
	RememberMaskPub(addr, st.MaskPub)
	RememberReplicas(addr, st.Replicas)
	RememberDPI(addr, st.DPIFlags, st.DPIPad)
	RememberRoster(st.Roster)
	maybeRefreshLists(st)
	RememberVis(st)
	if apply && !hadMask && len(st.MaskPub) == 32 {
		return &AdaptError{Addr: addr, SNI: usedSNI}
	}
	if len(st.Decoy) == 0 {
		return nil
	}
	want := decoyHost(st.Decoy[0])
	if strings.EqualFold(usedSNI, want) {
		return nil
	}
	if !apply {
		return nil
	}
	return &AdaptError{Addr: addr, SNI: want}
}

// DiscoverDecoy dials addr without a session HMAC so the node splices to its
// origin (or presents its own cert if decoy is off). Returns the name a
// stranger would see, and whether that name is not the URL host.
func DiscoverDecoy(addr, hint, fingerprint string) (name string, foreign bool, err error) {
	raw, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return "", false, err
	}
	cfg := &utls.Config{
		ServerName:         hint,
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2", "http/1.1"},
	}
	u := utls.UClient(raw, cfg, helloID(fingerprint))
	if err := u.Handshake(); err != nil {
		raw.Close()
		return "", false, err
	}
	state := u.ConnectionState()
	_ = u.Close()
	return peerName(state.PeerCertificates, hint)
}

// peerName picks a certificate DNS name; foreign is true when it is not hint.
func peerName(certs []*x509.Certificate, hint string) (string, bool, error) {
	if len(certs) == 0 {
		return hint, false, fmt.Errorf("decoy: no peer certificate")
	}
	c := certs[0]
	names := append([]string{}, c.DNSNames...)
	if c.Subject.CommonName != "" {
		names = append(names, c.Subject.CommonName)
	}
	hint = strings.ToLower(strings.TrimSpace(hint))
	for _, n := range names {
		if matchHost(n, hint) {
			return hint, false, nil
		}
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0], true, nil
	}
	if c.Subject.CommonName != "" {
		return c.Subject.CommonName, true, nil
	}
	return hint, false, nil
}

// matchHost reports whether host matches pattern, including a single-label *.
func matchHost(pattern, host string) bool {
	pattern = strings.ToLower(pattern)
	host = strings.ToLower(host)
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:]) {
		rest := strings.TrimSuffix(host, pattern[1:])
		return rest != "" && !strings.Contains(rest, ".")
	}
	return false
}

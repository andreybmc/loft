package transport

// Decoy splice forwards non-loft ClientHellos to a configured HTTPS origin.

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// prefixConn replays peeked ClientHello bytes before reading the conn.
type prefixConn struct {
	net.Conn
	prefix []byte
}

// Read drains prefix then the inner conn.
func (p *prefixConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}

// decoyListener implements decoy splice. Own SNI (advertise / certificate)
// is always the node certificate — CDN origin and a client that dials the
// advertise URL. HMAC on a decoy SNI is loft (mask when ready). Anything
// else is forwarded to a configured origin so a probe sees that origin.
//
// The splice destination is server config (`decoy:`), never a client field.
// First entry is this node's identity (one node may be a government portal,
// another a bank). A matching ClientHello SNI selects among the node's list;
// anything else splices to the primary. Hosts are not protocol constants.
type decoyListener struct {
	ln      net.Listener
	token   []byte
	primary string
	origins map[string]string // sni -> host:port
	own     []string          // advertise / cert names: do not splice
	mask    *Mask
	handler http.Handler
	onLoft  func(net.Conn, clientHello)
	mu      sync.Mutex
}

// WrapDecoy wraps ln. Empty origins: decoy is off, Accept is plain.
func WrapDecoy(ln net.Listener, token []byte, origins []string) net.Listener {
	return WrapDecoyMask(ln, token, origins, nil, nil)
}

// WrapDecoyMask wraps ln with decoy splice and optional borrowed-cert mask.
// own is advertise and certificate hostnames: a ClientHello with that SNI and
// no loft session HMAC is served with the node certificate (CDN origin pull).
func WrapDecoyMask(ln net.Listener, token []byte, origins []string, mask *Mask, h http.Handler, own ...string) net.Listener {
	if len(origins) == 0 && mask == nil {
		return ln
	}
	m := map[string]string{}
	var primary string
	for i, d := range origins {
		host, addr := splitDecoy(d)
		m[strings.ToLower(host)] = addr
		if i == 0 {
			primary = addr
		}
	}
	return &decoyListener{ln: ln, token: token, primary: primary, origins: m, own: ownSNIList(own), mask: mask, handler: h}
}

// PublicSNIs is advertise hostname plus DNS names on the node certificate.
// CDN origin Host/SNI must be one of these so the node does not splice the edge.
func PublicSNIs(advertise, certFile string) []string {
	var out []string
	if u, err := url.Parse(strings.TrimSpace(advertise)); err == nil {
		if h := normSNI(u.Hostname()); h != "" {
			out = append(out, h)
		}
	}
	body, err := os.ReadFile(certFile)
	if err != nil || certFile == "" {
		return out
	}
	for rest := body; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if cn := normSNI(cert.Subject.CommonName); cn != "" {
			out = append(out, cn)
		}
		for _, n := range cert.DNSNames {
			if n = normSNI(n); n != "" {
				out = append(out, n)
			}
		}
		break
	}
	return ownSNIList(out)
}

func normSNI(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

func ownSNIList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range in {
		n = normSNI(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// sniIsOwn reports whether sni is this node's public name (exact or one-label wildcard).
func sniIsOwn(sni string, names []string) bool {
	sni = normSNI(sni)
	if sni == "" {
		return false
	}
	for _, n := range names {
		if n == sni {
			return true
		}
		if strings.HasPrefix(n, "*.") {
			suf := n[1:]
			if strings.HasSuffix(sni, suf) && !strings.Contains(strings.TrimSuffix(sni, suf), ".") {
				return true
			}
		}
	}
	return false
}

// splitDecoy returns hostname and host:port, defaulting port 443.
func splitDecoy(d string) (host, addr string) {
	host, _, err := net.SplitHostPort(d)
	if err != nil {
		return d, d + ":443"
	}
	return host, d
}

// decoyHost strips a port from d.
func decoyHost(d string) string {
	host, _, err := net.SplitHostPort(d)
	if err != nil {
		return d
	}
	return host
}

// Addr returns the inner listener address.
func (d *decoyListener) Addr() net.Addr { return d.ln.Addr() }

// Close closes the inner listener.
func (d *decoyListener) Close() error { return d.ln.Close() }

// Accept peeks ClientHello:
//  1. SNI is this node's public name → node certificate (CDN origin, and a
//     client that dials the advertise URL). HMAC does not change this: mask
//     wears the decoy cert, so it must not run on own SNI.
//  2. session_id HMAC → loft; mask when ready and SNI is a decoy.
//  3. anything else → splice.
func (d *decoyListener) Accept() (net.Conn, error) {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			return nil, err
		}
		_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
		raw, hello, err := readClientHello(c)
		_ = c.SetReadDeadline(time.Time{})
		if err != nil {
			c.Close()
			continue
		}
		d.mu.Lock()
		own := d.own
		d.mu.Unlock()
		if sniIsOwn(hello.SNI, own) {
			return &prefixConn{Conn: c, prefix: raw}, nil
		}
		if verifyAny(d.token, hello) {
			if d.mask != nil && d.mask.Ready() {
				if d.onLoft != nil {
					go d.maskThen(c, raw, hello, d.onLoft)
					continue
				}
				if d.handler != nil {
					go d.serveMask(c, raw, hello)
					continue
				}
			}
			return &prefixConn{Conn: c, prefix: raw}, nil
		}
		go spliceDecoy(c, raw, d.pick(hello.SNI))
	}
}

// SetOrigins replaces the splice destination list. First entry is primary.
func (d *decoyListener) SetOrigins(origins []string) {
	m := map[string]string{}
	var primary string
	for i, o := range origins {
		host, addr := splitDecoy(o)
		m[strings.ToLower(host)] = addr
		if i == 0 {
			primary = addr
		}
	}
	d.mu.Lock()
	d.primary = primary
	d.origins = m
	d.mu.Unlock()
}

// pick selects the origin for sni, or primary if unmatched.
func (d *decoyListener) pick(sni string) string {
	sni = strings.ToLower(sni)
	d.mu.Lock()
	defer d.mu.Unlock()
	if a, ok := d.origins[sni]; ok {
		return a
	}
	return d.primary
}

// maskThen completes mask TLS then calls fn with the masked conn.
func (d *decoyListener) maskThen(c net.Conn, raw []byte, hello clientHello, fn func(net.Conn, clientHello)) {
	mc, err := d.mask.Handshake(c, raw, hello)
	if err != nil {
		c.Close()
		return
	}
	fn(mc, hello)
}

// serveMask completes mask TLS and serves HTTP/2 on the masked conn.
func (d *decoyListener) serveMask(c net.Conn, raw []byte, hello clientHello) {
	mc, err := d.mask.Handshake(c, raw, hello)
	if err != nil {
		c.Close()
		return
	}
	alpn := ""
	if cs, ok := mc.(interface{ ConnectionState() tls.ConnectionState }); ok {
		alpn = cs.ConnectionState().NegotiatedProtocol
	}
	if alpn == "http/1.1" {
		srv := &http.Server{Handler: d.handler, ReadHeaderTimeout: 10 * time.Second}
		ln := &onceLn{c: mc, ch: make(chan struct{})}
		_ = srv.Serve(ln)
		return
	}
	h2s := H2HTTPServer()
	h2s.ServeConn(mc, &http2.ServeConnOpts{Handler: d.handler})
}

// spliceDecoy writes hello to origin and copies both directions.
func spliceDecoy(c net.Conn, hello []byte, origin string) {
	defer c.Close()
	if origin == "" {
		return
	}
	up, err := net.DialTimeout("tcp", origin, 8*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	if _, err := up.Write(hello); err != nil {
		return
	}
	errc := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, c); errc <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); errc <- struct{}{} }()
	<-errc
}

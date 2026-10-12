package transport

// uTLS dial, session_id HMAC inject, ClientHello split, and HTTP/2 client.

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"loft/internal/config"
)

// tlsToken is the secret for mask + session_id HMAC. A CDN hop is ordinary
// TLS to the edge: no loft ClientHello, no borrowed-cert mask.
func tlsToken(o config.Outbound, token []byte) []byte {
	if strings.TrimSpace(o.CDN) != "" {
		return nil
	}
	return token
}

// helloID maps a fingerprint name to a uTLS ClientHelloID (default Chrome).
func helloID(fp string) utls.ClientHelloID {
	switch fp {
	case "firefox":
		return utls.HelloFirefox_Auto
	case "go":
		return utls.HelloGolang
	default:
		return utls.HelloChrome_Auto
	}
}

// tlsSess is the uTLS session cache.
var tlsSess = utls.NewLRUClientSessionCache(64)

// tcpBuf is the front/origin socket buffer. 256 KiB is ~16 Mbit of BDP at 128 ms;
// 512 KiB–2 MiB queued a 0.5–2 s ping under load (speedtest loaded latency).
const tcpBuf = 256 << 10

// tuneTCP turns on TCP_NODELAY and grows the kernel buffers on the TCP conn
// under c (TLS, prefixConn, and other wrappers are unwrapped).
func tuneTCP(c net.Conn) {
	for i := 0; i < 8 && c != nil; i++ {
		if t, ok := c.(*net.TCPConn); ok {
			_ = t.SetNoDelay(true)
			_ = t.SetReadBuffer(tcpBuf)
			_ = t.SetWriteBuffer(tcpBuf)
			return
		}
		switch u := c.(type) {
		case *prefixConn:
			c = u.Conn
		case interface{ NetConn() net.Conn }:
			c = u.NetConn()
		default:
			return
		}
	}
}

// tcpTuneListener calls tuneTCP on every Accept.
type tcpTuneListener struct{ net.Listener }

// Accept returns the next conn with TCP_NODELAY and large buffers.
func (l tcpTuneListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tuneTCP(c)
	return c, nil
}

// TuneListener wraps ln so Accept sets TCP_NODELAY and 2 MiB buffers.
func TuneListener(ln net.Listener) net.Listener {
	if ln == nil {
		return nil
	}
	return tcpTuneListener{ln}
}

// dialTLS dials TCP, injects session HMAC, and completes uTLS. Mask is tried when advertised.
func dialTLS(addr, sni, fingerprint string, insecure bool, token []byte) (net.Conn, error) {
	return dialTLSALPN(addr, sni, fingerprint, insecure, token, []string{"h2", "http/1.1"})
}

// dialTLSALPN is dialTLS with an explicit ALPN list. WebSocket must offer
// only http/1.1: the node prefers h2, and an h2 socket cannot HTTP Upgrade.
func dialTLSALPN(addr, sni, fingerprint string, insecure bool, token []byte, alpn []string) (net.Conn, error) {
	if len(alpn) == 0 {
		alpn = []string{"h2", "http/1.1"}
	}
	if len(token) > 0 {
		if pub := CachedMaskPub(addr); len(pub) == 32 {
			return dialMaskALPN(addr, sni, fingerprint, token, pub, alpn)
		}
		// Do not derive the mask key from the member key. The node seeds
		// mask from LOFT_TOKEN; Status.MaskPub + AdaptError teach it.
	}
	raw, err := net.DialTimeout("tcp4", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	tuneTCP(raw)
	pol := CachedDPI(addr)
	if pol.Fat && fingerprint == "go" {
		fingerprint = "chrome"
	}
	if pol.Split {
		raw = &splitHelloConn{Conn: raw, at: 1}
	}
	cfg := &utls.Config{
		ServerName:         sni,
		InsecureSkipVerify: insecure,
		NextProtos:         append([]string(nil), alpn...),
		ClientSessionCache: tlsSess,
	}
	u := utls.UClient(raw, cfg, helloID(fingerprint))
	if err := finishClientHello(u, token, sni, alpn); err != nil {
		raw.Close()
		return nil, err
	}
	if err := u.Handshake(); err != nil {
		raw.Close()
		if maskHandshake(err) {
			return dialMaskALPN(addr, sni, fingerprint, token, nil, alpn)
		}
		return nil, err
	}
	return u, nil
}

// maskHandshake reports whether err looks like a mask CertificateVerify failure.
func maskHandshake(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "Ed25519") ||
		strings.Contains(s, "invalid signature") ||
		strings.Contains(s, "certificate verify") ||
		strings.Contains(s, "expected an Ed25519")
}

// finishClientHello applies ALPN and session_id HMAC, then re-marshals the hello.
func finishClientHello(u *utls.UConn, token []byte, sni string, alpn []string) error {
	if err := u.BuildHandshakeState(); err != nil {
		return err
	}
	if len(alpn) > 0 {
		for _, ext := range u.Extensions {
			if a, ok := ext.(*utls.ALPNExtension); ok {
				a.AlpnProtocols = append([]string(nil), alpn...)
			}
		}
		u.HandshakeState.Hello.AlpnProtocols = append([]string(nil), alpn...)
	}
	if len(token) > 0 {
		rnd := u.HandshakeState.Hello.Random
		if len(rnd) == 32 {
			h := clientHello{Random: rnd, SNI: sni, KeyShare: utlsShare(u)}
			u.HandshakeState.Hello.SessionId = sessionHMAC(token, h)
		}
	}
	return u.MarshalClientHello()
}

// utlsShare returns the X25519 public from uTLS key_share, or nil.
func utlsShare(u *utls.UConn) []byte {
	ks := u.HandshakeState.State13.KeyShareKeys
	if ks == nil {
		return nil
	}
	if ks.Ecdhe != nil {
		return ks.Ecdhe.PublicKey().Bytes()
	}
	if ks.MlkemEcdhe != nil {
		return ks.MlkemEcdhe.PublicKey().Bytes()
	}
	return nil
}

// splitHelloConn writes the first TLS handshake record as two records so a
// middlebox must reassemble ClientHello. Optional; server readClientHello concatenates.
type splitHelloConn struct {
	net.Conn
	at   int
	used bool
}

// Write splits the first handshake record at s.at, then writes later records whole.
func (s *splitHelloConn) Write(p []byte) (int, error) {
	if s.used || s.at <= 0 || len(p) < 8 || p[0] != 0x16 {
		return s.Conn.Write(p)
	}
	s.used = true
	payload := p[5:]
	if s.at >= len(payload) {
		return s.Conn.Write(p)
	}
	if err := writeTLSRecord(s.Conn, 0x16, payload[:s.at]); err != nil {
		return 0, err
	}
	if err := writeTLSRecord(s.Conn, 0x16, payload[s.at:]); err != nil {
		return 0, err
	}
	return len(p), nil
}

// httpClient is the HTTP/2 client GET uses. ReadIdleTimeout is 0 so idle ping cannot kill the conn.
func httpClient(o config.Outbound, token string) (*http.Client, error) {
	u, err := parseURL(o.URL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "http" {
		return &http.Client{Timeout: 0, Transport: &http.Transport{
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 32,
			MaxConnsPerHost:     32,
		}}, nil
	}
	addr, sni, insecure, err := resolveTLS(o)
	if err != nil {
		return nil, err
	}
	tok := tlsToken(o, []byte(token))
	idlePing := time.Duration(0)
	if strings.EqualFold(strings.TrimSpace(o.CDN), "yandex") {
		// Direct GET: 5s ping killed a 1s-RTT h2 conn (TLS hole on retry).
		// Yandex CDN GOAWAY/RST after a quiet or long-lived h2 conn; a 20s
		// ping keeps the session off that cliff without the 5s false death.
		idlePing = 20 * time.Second
	}
	t2 := &http2.Transport{
		AllowHTTP: false,
		ReadIdleTimeout:  idlePing,
		MaxReadFrameSize: 1 << 20,
		DialTLS: func(network, _ string, _ *tls.Config) (net.Conn, error) {
			return dialTLS(addr, sni, o.Fingerprint, insecure, tok)
		},
	}
	return &http.Client{Transport: t2}, nil
}

// parseURL parses a front URL. Empty raw is an error.
func parseURL(raw string) (*httpURL, error) {
	if raw == "" {
		return nil, fmt.Errorf("empty url")
	}
	u, err := httpParse(raw)
	if err != nil {
		return nil, err
	}
	return u, nil
}

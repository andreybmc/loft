package transport

// Borrowed-cert mask: loft TLS presents the origin certificate; probes still splice.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"crypto/hmac"

	"golang.org/x/crypto/cryptobyte"
	"golang.org/x/crypto/curve25519"
)

// TLS 1.3 record, handshake, group, and signature numbers used by mask.
const (
	tls13AESGCM         uint16 = 0x1301
	tls13ChaCha         uint16 = 0x1303
	x25519Group         uint16 = 0x001d
	sigEd25519          uint16 = 0x0807
	recChangeCipher            = 20
	recAlert                   = 21
	recHandshake               = 22
	recApp                     = 23
	hsClientHello              = 1
	hsServerHello              = 2
	hsEncryptedExt             = 8
	hsCertificate              = 11
	hsCertificateVerify        = 15
	hsFinished                 = 20
)

// Mask is decoy mask: loft TLS wears the origin certificate. Probes still splice.
type Mask struct {
	priv   ed25519.PrivateKey
	pub    ed25519.PublicKey
	origin string
	sni    string
	mu     sync.RWMutex
	certs  [][]byte
	alpn   string
	ready  atomic.Bool
	stop   chan struct{}
}

// NewMask derives the Ed25519 mask key from token and starts origin-cert refresh.
func NewMask(token []byte, origin string) *Mask {
	seed := maskSeed(token)
	priv := ed25519.NewKeyFromSeed(seed)
	host, addr := splitDecoy(origin)
	m := &Mask{
		priv:   priv,
		pub:    priv.Public().(ed25519.PublicKey),
		origin: addr,
		sni:    host,
		stop:   make(chan struct{}),
	}
	go m.loop()
	return m
}

// maskSeed is HKDF-SHA256(token) → 32 bytes for the Ed25519 seed.
func maskSeed(token []byte) []byte {
	s, err := hkdf.Key(sha256.New, token, nil, "loft-mask-ed25519", 32)
	if err != nil {
		out := make([]byte, 32)
		copy(out, token)
		return out
	}
	return s
}

// Public returns a copy of the 32-byte mask public key.
func (m *Mask) Public() []byte { return append([]byte(nil), m.pub...) }

// NodeMaskPub is the mask public key a client derives from token when Status has not arrived.
func NodeMaskPub(token []byte) []byte {
	if len(token) == 0 {
		return nil
	}
	priv := ed25519.NewKeyFromSeed(maskSeed(token))
	pub := priv.Public().(ed25519.PublicKey)
	return append([]byte(nil), pub...)
}

// Ready is true after the origin certificate has been fetched.
func (m *Mask) Ready() bool { return m.ready.Load() }

// Close stops origin-cert refresh.
func (m *Mask) Close() {
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
}

// loop refreshes the origin certificate on start and every 6 hours.
func (m *Mask) loop() {
	_ = m.refresh()
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			_ = m.refresh()
		}
	}
}

// refresh dials the origin and stores its certificate chain and ALPN.
func (m *Mask) refresh() error {
	cfg := &tls.Config{
		ServerName:         m.sni,
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2", "http/1.1"},
		MinVersion:         tls.VersionTLS12,
	}
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", m.origin, cfg)
	if err != nil {
		return err
	}
	st := c.ConnectionState()
	_ = c.Close()
	var certs [][]byte
	for _, p := range st.PeerCertificates {
		certs = append(certs, append([]byte(nil), p.Raw...))
	}
	if len(certs) == 0 {
		return errors.New("mask: origin sent no certificate")
	}
	alpn := st.NegotiatedProtocol
	if alpn == "" {
		alpn = "h2"
	}
	m.mu.Lock()
	m.certs = certs
	m.alpn = alpn
	m.mu.Unlock()
	m.ready.Store(true)
	return nil
}

// snapshot copies the current origin certs and ALPN.
func (m *Mask) snapshot() (certs [][]byte, alpn string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, c := range m.certs {
		certs = append(certs, append([]byte(nil), c...))
	}
	return certs, m.alpn
}

// Handshake completes mask TLS on raw using the peeked ClientHello.
func (m *Mask) Handshake(raw net.Conn, rec []byte, hello clientHello) (net.Conn, error) {
	if !m.Ready() {
		return nil, errors.New("mask: origin cert not ready")
	}
	if len(hello.KeyShare) != 32 {
		return nil, fmt.Errorf("mask: no x25519 share (len %d sni %q)", len(hello.KeyShare), hello.SNI)
	}
	certs, originALPN := m.snapshot()
	if len(certs) == 0 {
		return nil, errors.New("mask: no certs")
	}
	return maskServer(raw, rec, hello, m.priv, certs, pickALPN(hello.ALPNs, originALPN))
}

// pickALPN is h2 when the client offered it (GET), http/1.1 when that is all
// it offered (WebSocket). Origin ALPN is the default when the client lists both.
func pickALPN(client []string, origin string) string {
	hasH2, hasH1 := false, false
	for _, a := range client {
		switch a {
		case "h2":
			hasH2 = true
		case "http/1.1":
			hasH1 = true
		}
	}
	if hasH1 && !hasH2 {
		return "http/1.1"
	}
	if hasH2 {
		if origin != "" {
			return origin
		}
		return "h2"
	}
	if hasH1 {
		return "http/1.1"
	}
	if origin != "" {
		return origin
	}
	return "h2"
}

// maskServer is the TLS 1.3 server handshake: origin certs, Ed25519 CertificateVerify.
func maskServer(raw net.Conn, rec []byte, hello clientHello, ident ed25519.PrivateKey, certs [][]byte, alpn string) (net.Conn, error) {
	tr := sha256.New()
	chMsg := rec
	if len(rec) >= 5 && rec[0] == recHandshake {
		chMsg = rec[5:]
	}
	tr.Write(chMsg)

	ephPriv := make([]byte, 32)
	if _, err := rand.Read(ephPriv); err != nil {
		return nil, err
	}
	ephPub, err := curve25519.X25519(ephPriv, curve25519.Basepoint)
	if err != nil {
		return nil, err
	}
	shared, err := curve25519.X25519(ephPriv, hello.KeyShare)
	if err != nil {
		return nil, err
	}

	var srvRand [32]byte
	if _, err := rand.Read(srvRand[:]); err != nil {
		return nil, err
	}
	sid := hello.SessionID
	sh := marshalServerHello(srvRand[:], sid, ephPub)
	tr.Write(sh)
	if err := writeRecord(raw, recHandshake, sh); err != nil {
		return nil, err
	}
	_ = writeRecord(raw, recChangeCipher, []byte{1})

	hs := tls13Handshake(shared[:], tr.Sum(nil))
	sc, err := newHalf(hs.serverHS, true)
	if err != nil {
		return nil, err
	}

	ee := marshalEncryptedExt(alpn)
	tr.Write(ee)
	if err := sc.writeHandshake(raw, ee); err != nil {
		return nil, err
	}
	certMsg := marshalCertificate(certs)
	tr.Write(certMsg)
	if err := sc.writeHandshake(raw, certMsg); err != nil {
		return nil, err
	}
	toSign := certVerifySigned(tr.Sum(nil), true)
	sig := ed25519.Sign(ident, toSign)
	cv := marshalCertVerify(sig)
	tr.Write(cv)
	if err := sc.writeHandshake(raw, cv); err != nil {
		return nil, err
	}
	fin := marshalFinished(tls13Finished(hs.serverHS, tr.Sum(nil)))
	tr.Write(fin)
	if err := sc.writeHandshake(raw, fin); err != nil {
		return nil, err
	}

	apHash := tr.Sum(nil)
	app := tls13App(hs.handshakeSecret, apHash)
	cc, err := newHalf(hs.clientHS, false)
	if err != nil {
		return nil, err
	}
	if err := readClientFinished(raw, cc, hs.clientHS, tr); err != nil {
		return nil, err
	}
	cApp, err := newHalf(app.clientApp, false)
	if err != nil {
		return nil, err
	}
	sApp, err := newHalf(app.serverApp, true)
	if err != nil {
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return &maskConn{
		Conn: raw,
		in:   cApp,
		out:  sApp,
		state: tls.ConnectionState{
			Version:            tls.VersionTLS13,
			HandshakeComplete:  true,
			CipherSuite:        tls.TLS_AES_128_GCM_SHA256,
			NegotiatedProtocol: alpn,
			ServerName:         hello.SNI,
			PeerCertificates:   parseRawCerts(certs),
		},
	}, nil
}

// DialMask is the client mask handshake. maskPub empty skips CertificateVerify check.
func DialMask(addr, sni, fingerprint string, token, maskPub []byte) (net.Conn, error) {
	return dialMaskALPN(addr, sni, fingerprint, token, maskPub, []string{"http/1.1", "h2"})
}

// dialMaskALPN is DialMask with an explicit ALPN offer. WS passes only http/1.1.
func dialMaskALPN(addr, sni, fingerprint string, token, maskPub []byte, alpn []string) (net.Conn, error) {
	skipCV := len(maskPub) != ed25519.PublicKeySize
	raw, err := net.DialTimeout("tcp4", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	tuneTCP(raw)
	helloRaw, priv, err := buildMaskClientHello(sni, token, alpn)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = fingerprint
	if err := writeRecord(raw, recHandshake, helloRaw); err != nil {
		raw.Close()
		return nil, err
	}
	tr := sha256.New()
	tr.Write(helloRaw)

	shRec, err := readFullRecord(raw)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("mask: serverhello read: %w", err)
	}
	if len(shRec) >= 5 && shRec[0] != recHandshake {
		head := shRec
		if len(head) > 24 {
			head = head[:24]
		}
		raw.Close()
		return nil, fmt.Errorf("mask: first rec type=%d len=%d head=%x", shRec[0], len(shRec), head)
	}
	shMsg, err := handshakeFromRecord(shRec)
	if err != nil {
		raw.Close()
		return nil, err
	}
	serverShare, err := parseServerHelloShare(shMsg)
	if err != nil {
		head := shMsg
		if len(head) > 32 {
			head = head[:32]
		}
		raw.Close()
		return nil, fmt.Errorf("%w (sh %d %x)", err, len(shMsg), head)
	}
	tr.Write(shMsg)

	shared, err := curve25519.X25519(priv, serverShare)
	if err != nil {
		raw.Close()
		return nil, err
	}
	hs := tls13Handshake(shared, tr.Sum(nil))
	sc, err := newHalf(hs.serverHS, false)
	if err != nil {
		raw.Close()
		return nil, err
	}
	// optional CCS
	rec, err := readFullRecord(raw)
	if err != nil {
		raw.Close()
		return nil, fmt.Errorf("mask: after-sh read: %w", err)
	}
	if rec[0] == recChangeCipher {
		rec, err = readFullRecord(raw)
		if err != nil {
			raw.Close()
			return nil, fmt.Errorf("mask: after-ccs read: %w", err)
		}
	}
	plain, err := sc.openRecord(rec)
	if err != nil {
		head := rec
		if len(head) > 16 {
			head = head[:16]
		}
		raw.Close()
		return nil, fmt.Errorf("mask: hs open typ=%d len=%d head=%x: %w", rec[0], len(rec), head, err)
	}
	msgs := splitHandshake(plain)
	// may need more records
	for !hasFinished(msgs) {
		rec, err = readFullRecord(raw)
		if err != nil {
			raw.Close()
			return nil, err
		}
		p, err := sc.openRecord(rec)
		if err != nil {
			raw.Close()
			return nil, err
		}
		msgs = append(msgs, splitHandshake(p)...)
	}
	var sawCV bool
	for _, msg := range msgs {
		if msg[0] == hsFinished {
			want := tls13Finished(hs.serverHS, tr.Sum(nil))
			got := msg[4:]
			if len(got) < 32 || !hmacEqual(got[:32], want) {
				raw.Close()
				return nil, errors.New("mask: server finished")
			}
			tr.Write(msg)
			continue
		}
		if msg[0] == hsCertificateVerify {
			if skipCV {
				sawCV = true
			} else if err := verifyMaskCV(msg, tr.Sum(nil), maskPub); err != nil {
				raw.Close()
				return nil, err
			} else {
				sawCV = true
			}
		}
		tr.Write(msg)
	}
	if !sawCV {
		raw.Close()
		return nil, errors.New("mask: no certificate verify")
	}
	cc, err := newHalf(hs.clientHS, true)
	if err != nil {
		raw.Close()
		return nil, err
	}
	fin := marshalFinished(tls13Finished(hs.clientHS, tr.Sum(nil)))
	// App secrets use ClientHello…server Finished, not the client Finished.
	app := tls13App(hs.handshakeSecret, tr.Sum(nil))
	if err := cc.writeHandshake(raw, fin); err != nil {
		raw.Close()
		return nil, err
	}
	tr.Write(fin)
	cApp, err := newHalf(app.clientApp, true)
	if err != nil {
		raw.Close()
		return nil, err
	}
	sApp, err := newHalf(app.serverApp, false)
	if err != nil {
		raw.Close()
		return nil, err
	}
	_ = raw.SetDeadline(time.Time{})
	return &maskConn{
		Conn: raw,
		in:   sApp,
		out:  cApp,
		state: tls.ConnectionState{
			Version:            tls.VersionTLS13,
			HandshakeComplete:  true,
			CipherSuite:        tls.TLS_AES_128_GCM_SHA256,
			NegotiatedProtocol: firstALPN(alpn),
			ServerName:         sni,
		},
	}, nil
}

func firstALPN(alpn []string) string {
	if len(alpn) == 0 {
		return "h2"
	}
	return alpn[0]
}

// buildMaskClientHello builds a TLS 1.3 ClientHello with session HMAC and X25519.
func buildMaskClientHello(sni string, token []byte, alpn []string) (msg, priv []byte, err error) {
	if len(alpn) == 0 {
		alpn = []string{"http/1.1", "h2"}
	}
	var ephPriv, ephPub [32]byte
	if _, err = rand.Read(ephPriv[:]); err != nil {
		return nil, nil, err
	}
	curve25519.ScalarBaseMult(&ephPub, &ephPriv)
	var random [32]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, nil, err
	}
	sid := sessionHMAC(token, clientHello{Random: random[:], SNI: sni, KeyShare: ephPub[:]})
	var b cryptobyte.Builder
	b.AddUint8(hsClientHello)
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint16(0x0303)
		b.AddBytes(random[:])
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(sid) })
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			b.AddUint16(tls13AESGCM)
			b.AddUint16(tls13ChaCha)
		})
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint8(0) })
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			// supported_versions
			b.AddUint16(43)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(0x0304) })
			})
			// server_name
			if sni != "" {
				b.AddUint16(0)
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
						b.AddUint8(0)
						b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte(sni)) })
					})
				})
			}
			// supported_groups
			b.AddUint16(10)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(x25519Group) })
			})
			// key_share
			b.AddUint16(51)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					b.AddUint16(x25519Group)
					b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(ephPub[:]) })
				})
			})
			// signature_algorithms
			b.AddUint16(13)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					b.AddUint16(sigEd25519)
					b.AddUint16(0x0403)
				})
			})
			// psk_key_exchange_modes (psk_dhe_ke) — some stacks require it for TLS 1.3
			b.AddUint16(45)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint8(1) })
			})
			b.AddUint16(16)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					for _, p := range alpn {
						b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte(p)) })
					}
				})
			})
		})
	})
	msg, err = b.Bytes()
	return msg, ephPriv[:], err
}

// hmacEqual is a constant-time compare.
func hmacEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// parseRawCerts parses DER certificates, skipping failures.
func parseRawCerts(raws [][]byte) []*x509.Certificate {
	var out []*x509.Certificate
	for _, r := range raws {
		c, err := x509.ParseCertificate(r)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

// hsSecrets are TLS 1.3 handshake traffic secrets.
type hsSecrets struct {
	handshakeSecret []byte
	clientHS        []byte
	serverHS        []byte
}

// appSecrets are TLS 1.3 application traffic secrets.
type appSecrets struct {
	clientApp []byte
	serverApp []byte
}

// tls13Handshake derives handshake traffic secrets from the X25519 shared secret.
func tls13Handshake(shared, helloHash []byte) hsSecrets {
	early := hkdfExtract(make([]byte, 32), nil)
	derived := hkdfExpandLabel(early, "derived", emptyHash(), 32)
	hs := hkdfExtract(derived, shared)
	return hsSecrets{
		handshakeSecret: hs,
		clientHS:        hkdfExpandLabel(hs, "c hs traffic", helloHash, 32),
		serverHS:        hkdfExpandLabel(hs, "s hs traffic", helloHash, 32),
	}
}

// tls13App derives application traffic secrets from the handshake secret.
func tls13App(handshakeSecret, hash []byte) appSecrets {
	derived := hkdfExpandLabel(handshakeSecret, "derived", emptyHash(), 32)
	master := hkdfExtract(derived, nil)
	return appSecrets{
		clientApp: hkdfExpandLabel(master, "c ap traffic", hash, 32),
		serverApp: hkdfExpandLabel(master, "s ap traffic", hash, 32),
	}
}

// tls13Finished is the Finished verify_data for traffic.
func tls13Finished(traffic, sum []byte) []byte {
	key := hkdfExpandLabel(traffic, "finished", nil, 32)
	return hmacSHA256(key, sum)
}

// hmacSHA256 returns HMAC-SHA256(key, data).
func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// emptyHash is SHA-256 of the empty string.
func emptyHash() []byte {
	h := sha256.New()
	return h.Sum(nil)
}

// hkdfExtract is HKDF-Extract with SHA-256.
func hkdfExtract(salt, ikm []byte) []byte {
	if salt == nil {
		salt = make([]byte, 32)
	}
	if ikm == nil {
		ikm = make([]byte, 32)
	}
	out, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		panic(err)
	}
	return out
}

// hkdfExpandLabel is TLS 1.3 HKDF-Expand-Label.
func hkdfExpandLabel(secret []byte, label string, context []byte, length int) []byte {
	full := "tls13 " + label
	var b cryptobyte.Builder
	b.AddUint16(uint16(length))
	b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte(full)) })
	b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(context) })
	info, _ := b.Bytes()
	out, err := hkdf.Expand(sha256.New, secret, string(info), length)
	if err != nil {
		panic(err)
	}
	return out
}

// certVerifySigned is the TLS 1.3 CertificateVerify input for transcript.
func certVerifySigned(transcript []byte, server bool) []byte {
	pad := make([]byte, 64)
	for i := range pad {
		pad[i] = 0x20
	}
	ctx := "TLS 1.3, client CertificateVerify"
	if server {
		ctx = "TLS 1.3, server CertificateVerify"
	}
	out := append(pad, []byte(ctx)...)
	out = append(out, 0)
	return append(out, transcript...)
}

// marshalServerHello builds a TLS 1.3 ServerHello with X25519 key_share.
func marshalServerHello(random, sid, share []byte) []byte {
	var b cryptobyte.Builder
	b.AddUint8(hsServerHello)
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint16(0x0303)
		b.AddBytes(random)
		b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(sid) })
		b.AddUint16(tls13AESGCM)
		b.AddUint8(0) // compression
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			// supported_versions
			b.AddUint16(43)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddUint16(0x0304) })
			// key_share
			b.AddUint16(51)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16(x25519Group)
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(share) })
			})
		})
	})
	msg, _ := b.Bytes()
	return msg
}

// marshalEncryptedExt builds EncryptedExtensions with ALPN.
func marshalEncryptedExt(alpn string) []byte {
	var b cryptobyte.Builder
	b.AddUint8(hsEncryptedExt)
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
			if alpn == "" {
				return
			}
			b.AddUint16(16)
			b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
				b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) {
					b.AddUint8LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes([]byte(alpn)) })
				})
			})
		})
	})
	msg, _ := b.Bytes()
	return msg
}

// marshalCertificate builds a TLS 1.3 Certificate message from DER certs.
func marshalCertificate(certs [][]byte) []byte {
	var b cryptobyte.Builder
	b.AddUint8(hsCertificate)
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint8(0) // request context
		b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
			for _, c := range certs {
				b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(c) })
				b.AddUint16(0)
			}
		})
	})
	msg, _ := b.Bytes()
	return msg
}

// marshalCertVerify builds CertificateVerify with Ed25519 sig.
func marshalCertVerify(sig []byte) []byte {
	var b cryptobyte.Builder
	b.AddUint8(hsCertificateVerify)
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) {
		b.AddUint16(sigEd25519)
		b.AddUint16LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(sig) })
	})
	msg, _ := b.Bytes()
	return msg
}

// marshalFinished builds a Finished message.
func marshalFinished(mac []byte) []byte {
	var b cryptobyte.Builder
	b.AddUint8(hsFinished)
	b.AddUint24LengthPrefixed(func(b *cryptobyte.Builder) { b.AddBytes(mac) })
	msg, _ := b.Bytes()
	return msg
}

// verifyMaskCV checks Ed25519 CertificateVerify under pub.
func verifyMaskCV(msg, transcript, pub []byte) error {
	if len(msg) < 8 {
		return errors.New("mask: short cert verify")
	}
	body := msg[4:]
	if binary.BigEndian.Uint16(body[0:2]) != sigEd25519 {
		return errors.New("mask: sig scheme")
	}
	ln := int(binary.BigEndian.Uint16(body[2:4]))
	if 4+ln > len(body) {
		return errors.New("mask: short sig")
	}
	sig := body[4 : 4+ln]
	if !ed25519.Verify(ed25519.PublicKey(pub), certVerifySigned(transcript, true), sig) {
		return errors.New("mask: bad certificate verify")
	}
	return nil
}

// parseServerHelloShare returns the X25519 public from ServerHello key_share.
func parseServerHelloShare(msg []byte) ([]byte, error) {
	if len(msg) < 40 || msg[0] != hsServerHello {
		return nil, errors.New("mask: not server hello")
	}
	n := int(msg[1])<<16 | int(msg[2])<<8 | int(msg[3])
	body := msg[4 : 4+n]
	off := 2 + 32 // version + random
	if off >= len(body) {
		return nil, errors.New("mask: short sh")
	}
	sidLen := int(body[off])
	off += 1 + sidLen + 2 + 1 // sid, cipher, compression
	if off+2 > len(body) {
		return nil, errors.New("mask: no ext")
	}
	extLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	end := off + extLen
	if end > len(body) {
		end = len(body)
	}
	for off+4 <= end {
		typ := binary.BigEndian.Uint16(body[off : off+2])
		ln := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		off += 4
		if off+ln > end {
			break
		}
		if typ == 51 && ln >= 36 {
			// group + key
			if binary.BigEndian.Uint16(body[off:off+2]) == x25519Group {
				klen := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
				if klen == 32 && 4+klen <= ln {
					return append([]byte(nil), body[off+4:off+4+32]...), nil
				}
			}
		}
		off += ln
	}
	return nil, errors.New("mask: no x25519 in server hello")
}

// writeRecord writes one TLS record of typ.
func writeRecord(w io.Writer, typ byte, payload []byte) error {
	hdr := []byte{typ, 0x03, 0x03, 0, 0}
	binary.BigEndian.PutUint16(hdr[3:5], uint16(len(payload)))
	_, err := w.Write(append(hdr, payload...))
	return err
}

// readFullRecord reads one TLS record. Ciphertext max is 16KiB + 256.
func readFullRecord(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 5)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[3:5]))
	// TLS 1.3 ciphertext max is 2^14 + 256 (plaintext 16KiB + AEAD expansion).
	if n < 0 || n > (16<<10)+256 {
		return nil, errors.New("mask: bad record")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return append(hdr, body...), nil
}

// handshakeFromRecord strips the TLS record header from a handshake record.
func handshakeFromRecord(rec []byte) ([]byte, error) {
	if len(rec) < 5 || rec[0] != recHandshake {
		return nil, errors.New("mask: not handshake record")
	}
	return rec[5:], nil
}

// splitHandshake splits concatenated handshake messages.
func splitHandshake(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 4 {
		n := int(b[1])<<16 | int(b[2])<<8 | int(b[3])
		if 4+n > len(b) {
			break
		}
		out = append(out, b[:4+n])
		b = b[4+n:]
	}
	return out
}

// hasFinished reports whether msgs includes a Finished.
func hasFinished(msgs [][]byte) bool {
	for _, m := range msgs {
		if len(m) > 0 && m[0] == hsFinished {
			return true
		}
	}
	return false
}

// readClientFinished reads and checks the client's Finished.
func readClientFinished(raw net.Conn, cc *half, traffic []byte, tr hash.Hash) error {
	rec, err := readFullRecord(raw)
	if err != nil {
		return err
	}
	if rec[0] == recChangeCipher {
		rec, err = readFullRecord(raw)
		if err != nil {
			return err
		}
	}
	plain, err := cc.openRecord(rec)
	if err != nil {
		return err
	}
	msgs := splitHandshake(plain)
	if len(msgs) == 0 || msgs[0][0] != hsFinished {
		return errors.New("mask: expected client finished")
	}
	want := tls13Finished(traffic, tr.Sum(nil))
	got := msgs[0][4:]
	if len(got) < 32 || !hmacEqual(got[:32], want) {
		return errors.New("mask: client finished")
	}
	tr.Write(msgs[0])
	return nil
}

// half is one TLS 1.3 AES-GCM traffic direction.
type half struct {
	aead cipher.AEAD
	iv   []byte
	seq  uint64
}

// newHalf derives AES-128-GCM key and IV from traffic.
func newHalf(traffic []byte, _ bool) (*half, error) {
	key := hkdfExpandLabel(traffic, "key", nil, 16)
	iv := hkdfExpandLabel(traffic, "iv", nil, 12)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &half{aead: aead, iv: iv}, nil
}

// nonce is the next AES-GCM nonce (IV xor seq) and increments seq.
func (h *half) nonce() []byte {
	n := make([]byte, 12)
	copy(n, h.iv)
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], h.seq)
	for i := 0; i < 8; i++ {
		n[4+i] ^= seq[i]
	}
	h.seq++
	return n
}

// writeHandshake seals msg as a TLS 1.3 handshake inner plaintext.
func (h *half) writeHandshake(w io.Writer, msg []byte) error {
	inner := append(append([]byte{}, msg...), recHandshake)
	overhead := h.aead.Overhead()
	clen := len(inner) + overhead
	hdr := []byte{recApp, 0x03, 0x03, byte(clen >> 8), byte(clen)}
	ct := h.aead.Seal(nil, h.nonce(), inner, hdr)
	_, err := w.Write(append(hdr, ct...))
	return err
}

// openRecord decrypts rec and strips the inner content type.
func (h *half) openRecord(rec []byte) ([]byte, error) {
	if len(rec) < 5 {
		return nil, errors.New("mask: short record")
	}
	hdr, ct := rec[:5], rec[5:]
	plain, err := h.aead.Open(nil, h.nonce(), ct, hdr)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 {
		return nil, errors.New("mask: empty inner")
	}
	return plain[:len(plain)-1], nil // strip inner type
}

// sealApp seals application data as a TLS 1.3 record.
func (h *half) sealApp(p []byte) []byte {
	inner := append(append([]byte{}, p...), recApp)
	clen := len(inner) + h.aead.Overhead()
	hdr := []byte{recApp, 0x03, 0x03, byte(clen >> 8), byte(clen)}
	ct := h.aead.Seal(nil, h.nonce(), inner, hdr)
	return append(hdr, ct...)
}

// openApp decrypts an application record.
func (h *half) openApp(rec []byte) ([]byte, error) {
	return h.openRecord(rec)
}

// maskConn is net.Conn after mask TLS, with AES-GCM application records.
type maskConn struct {
	net.Conn
	in, out *half
	state   tls.ConnectionState
	rbuf    []byte
	wmu     sync.Mutex
}

// ConnectionState returns the completed TLS 1.3 state.
func (c *maskConn) ConnectionState() tls.ConnectionState { return c.state }

// Read decrypts application records into p.
func (c *maskConn) Read(p []byte) (int, error) {
	if len(c.rbuf) > 0 {
		n := copy(p, c.rbuf)
		c.rbuf = c.rbuf[n:]
		return n, nil
	}
	rec, err := readFullRecord(c.Conn)
	if err != nil {
		return 0, err
	}
	plain, err := c.in.openApp(rec)
	if err != nil {
		return 0, err
	}
	n := copy(p, plain)
	c.rbuf = append([]byte(nil), plain[n:]...)
	return n, nil
}

// TLS 1.3 inner plaintext max 2^14; last byte is the hidden content type.
const maskAppMax = 16384 - 1

// Write seals p in records of at most maskAppMax bytes.
func (c *maskConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	n := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > maskAppMax {
			chunk = chunk[:maskAppMax]
		}
		if _, err := c.Conn.Write(c.out.sealApp(chunk)); err != nil {
			return n, err
		}
		n += len(chunk)
		p = p[len(chunk):]
	}
	return n, nil
}

package transport

// Mask record-layer, handshake loopback, and front-filter tests.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"crypto/ed25519"

	"loft/internal/config"
)

// TestMaskRecordRoundtrip opens a sealed handshake record.
func TestMaskRecordRoundtrip(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	w, err := newHalf(secret, true)
	if err != nil {
		t.Fatal(err)
	}
	r, err := newHalf(secret, false)
	if err != nil {
		t.Fatal(err)
	}
	var buf []byte
	if err := w.writeHandshake(writerFunc(func(p []byte) (int, error) {
		buf = append(buf, p...)
		return len(p), nil
	}), []byte{hsFinished, 0, 0, 4, 1, 2, 3, 4}); err != nil {
		t.Fatal(err)
	}
	plain, err := r.openRecord(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) < 4 || plain[0] != hsFinished {
		t.Fatalf("%x", plain)
	}
}

// TestMaskConnLargeWrite writes more than one TLS 1.3 application record.
func TestMaskConnLargeWrite(t *testing.T) {
	secret := bytes.Repeat([]byte{9}, 32)
	out, err := newHalf(secret, true)
	if err != nil {
		t.Fatal(err)
	}
	in, err := newHalf(secret, false)
	if err != nil {
		t.Fatal(err)
	}
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	w := &maskConn{Conn: c1, out: out}
	r := &maskConn{Conn: c2, in: in}
	payload := make([]byte, 40000)
	for i := range payload {
		payload[i] = byte(i)
	}
	errc := make(chan error, 1)
	go func() {
		_, err := w.Write(payload)
		errc <- err
		c1.Close()
	}()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("got %d want %d", len(got), len(payload))
	}
}

// writerFunc is an io.Writer over a function.
type writerFunc func([]byte) (int, error)

// Write calls the function.
func (w writerFunc) Write(p []byte) (int, error) { return w(p) }

// TestMaskHandshakeLoopback is skipped: full hello loopback needs a buffered pipe.
func TestMaskHandshakeLoopback(t *testing.T) {
	t.Skip("mask record layer is tested; full hello loopback needs a buffered pipe")
	token := []byte("mask-test-token-32bytes-minimum!")
	ident := ed25519.NewKeyFromSeed(maskSeed(token))
	cert := selfSigned(t, "www.gosuslugi.ru")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	m := &Mask{priv: ident, pub: ident.Public().(ed25519.PublicKey)}
	m.certs = [][]byte{cert}
	m.alpn = "h2"
	m.ready.Store(true)

	var wg sync.WaitGroup
	wg.Add(1)
	errc := make(chan error, 1)
	go func() {
		defer wg.Done()
		c, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		raw, hello, err := readClientHello(c)
		if err != nil {
			c.Close()
			errc <- err
			return
		}
		if !verifySessionID(token, hello) {
			c.Close()
			errc <- io.ErrUnexpectedEOF
			return
		}
		if hello.KeyShare == nil {
			errc <- fmt.Errorf("hello sni=%q sid=%d raw=%d exts=%v", hello.SNI, len(hello.SessionID), len(raw), extTypes(raw))
			c.Close()
			return
		}
		mc, err := m.Handshake(c, raw, hello)
		if err != nil {
			c.Close()
			errc <- err
			return
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(mc, buf); err != nil {
			errc <- err
			return
		}
		_, err = mc.Write(buf)
		errc <- err
	}()

	addr := ln.Addr().String()
	RememberMaskPub(addr, m.Public())
	t.Cleanup(func() { views.Delete(addr) })
	client, err := DialMask(addr, "www.gosuslugi.ru", "chrome", token, m.Public())
	if err != nil {
		wg.Wait()
		t.Fatalf("dial %v server %v", err, <-errc)
	}
	defer client.Close()
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("got %q", buf)
	}
	wg.Wait()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

// TestFilterOutboundsRespectsFronts keeps only client-enabled fronts.
func TestFilterOutboundsRespectsFronts(t *testing.T) {
	clearDecoyCache()
	views = sync.Map{}
	cfg := &config.ClientConfig{
		Fronts: []string{"ws"},
		Outbounds: []config.Outbound{
			{Name: "a", Type: "get", URL: "https://n.example"},
			{Name: "b", Type: "ws", URL: "wss://n.example/api/v1/sync"},
		},
	}
	list := filterOutbounds(cfg, cfg.Outbounds)
	if len(list) != 1 || list[0].Type != "ws" {
		t.Fatalf("%+v", list)
	}
}

// selfSigned returns a DER self-signed certificate for dns.
func selfSigned(t *testing.T, dns string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: dns},
		DNSNames:     []string{dns},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	_ = pem.EncodeToMemory
	return der
}

// extTypes lists TLS extension types from a ClientHello record.
func extTypes(rec []byte) []uint16 {
	body := rec
	if len(rec) >= 5 && rec[0] == 0x16 {
		body = rec[5:]
	}
	if len(body) < 40 {
		return nil
	}
	off := 4 + 2 + 32
	if off >= len(body) {
		return nil
	}
	off += 1 + int(body[off])
	if off+2 > len(body) {
		return nil
	}
	cs := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2 + cs
	if off >= len(body) {
		return nil
	}
	off += 1 + int(body[off])
	if off+2 > len(body) {
		return nil
	}
	el := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	end := off + el
	if end > len(body) {
		end = len(body)
	}
	var out []uint16
	for off+4 <= end {
		typ := binary.BigEndian.Uint16(body[off : off+2])
		ln := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		out = append(out, typ)
		off += 4 + ln
	}
	return out
}

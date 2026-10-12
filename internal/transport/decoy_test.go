package transport

// Decoy splice pick, cache, resolveTLS, and DiscoverDecoy tests.

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"loft/internal/config"
	"loft/internal/wire"
)

// TestDecoyPickIsServerPrimary splices unmatched SNI to the first origin.
func TestDecoyPickIsServerPrimary(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	d, ok := WrapDecoy(ln, []byte("tok"), []string{"www.gosuslugi.ru:443", "online.sberbank.ru:443"}).(*decoyListener)
	if !ok {
		t.Fatal("wrap")
	}
	if d.primary != "www.gosuslugi.ru:443" {
		t.Fatalf("primary %q", d.primary)
	}
	if got := d.pick("evil.example"); got != d.primary {
		t.Fatalf("unmatched SNI must splice to primary, got %q", got)
	}
	if got := d.pick("online.sberbank.ru"); got != "online.sberbank.ru:443" {
		t.Fatalf("matched SNI %q", got)
	}
	if got := d.pick("WWW.GOSUSLUGI.RU"); got != "www.gosuslugi.ru:443" {
		t.Fatalf("case %q", got)
	}
}

func TestSNIIsOwn(t *testing.T) {
	names := []string{"loft.iqos.help"}
	if !sniIsOwn("loft.iqos.help", names) {
		t.Fatal("exact")
	}
	if !sniIsOwn("Loft.IQOS.Help.", names) {
		t.Fatal("case")
	}
	if sniIsOwn("cdn.iqos.help", names) {
		t.Fatal("cdn hostname is not the node")
	}
	if sniIsOwn("origin.example", names) {
		t.Fatal("decoy sni")
	}
	if sniIsOwn("", names) {
		t.Fatal("empty")
	}
	if !sniIsOwn("loft.iqos.help", []string{"*.iqos.help"}) {
		t.Fatal("wildcard")
	}
	if sniIsOwn("a.b.iqos.help", []string{"*.iqos.help"}) {
		t.Fatal("too many labels")
	}
}

func TestPublicSNIsFromAdvertise(t *testing.T) {
	got := PublicSNIs("https://loft.iqos.help:444", "")
	if len(got) != 1 || got[0] != "loft.iqos.help" {
		t.Fatalf("%v", got)
	}
}

func TestOwnSNIDoesNotSplice(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	w := WrapDecoyMask(inner, []byte("tok"), []string{"origin.example:443"}, nil, nil, "node.example")
	got := make(chan error, 1)
	go func() {
		c, err := w.Accept()
		if err != nil {
			got <- err
			return
		}
		c.Close()
		got <- nil
	}()
	cli, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	go func() {
		_ = tls.Client(cli, &tls.Config{
			ServerName: "node.example", InsecureSkipVerify: true, NextProtos: []string{"http/1.1"},
		}).Handshake()
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("public SNI was spliced")
	}
}

func TestOwnSNIWithHMACSkipsMask(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	mask := &Mask{}
	mask.ready.Store(true)
	hanged := make(chan struct{})
	w := WrapDecoyMask(inner, []byte("tok"), []string{"origin.example:443"}, mask, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(hanged)
	}), "node.example")
	got := make(chan error, 1)
	go func() {
		c, err := w.Accept()
		if err != nil {
			got <- err
			return
		}
		c.Close()
		got <- nil
	}()
	cli, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	raw, _, err := buildMaskClientHello("node.example", []byte("tok"), []string{"h2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeRecord(cli, recHandshake, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-hanged:
		t.Fatal("own SNI with HMAC entered serveMask")
	case <-time.After(2 * time.Second):
		t.Fatal("Accept hung (mask or splice)")
	}
}

func TestForeignSNIStillSplices(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()
	w := WrapDecoyMask(inner, []byte("tok"), []string{"origin.example:443"}, nil, nil, "node.example")
	got := make(chan error, 1)
	go func() {
		c, err := w.Accept()
		if err != nil {
			got <- err
			return
		}
		c.Close()
		got <- nil
	}()
	cli, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	go func() {
		_ = tls.Client(cli, &tls.Config{
			ServerName: "probe.example", InsecureSkipVerify: true,
		}).Handshake()
	}()
	select {
	case err := <-got:
		if err == nil {
			t.Fatal("foreign SNI returned from Accept; must splice")
		}
	case <-time.After(400 * time.Millisecond):
		// Accept kept waiting: hello was spliced, not returned. That is the point.
	}
}

// TestWrapDecoyOff leaves the listener unchanged when origins are empty.
func TestWrapDecoyOff(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if WrapDecoy(ln, []byte("tok"), nil) != ln {
		t.Fatal("empty decoy must leave listener unchanged")
	}
}

// TestPeerNameForeign treats a foreign cert DNS name as foreign.
func TestPeerNameForeign(t *testing.T) {
	cert := &x509.Certificate{
		DNSNames: []string{"www.gosuslugi.ru", "*.gosuslugi.ru"},
		Subject:  pkix.Name{CommonName: "www.gosuslugi.ru"},
	}
	name, foreign, err := peerName([]*x509.Certificate{cert}, "node.example.com")
	if err != nil || !foreign || name != "www.gosuslugi.ru" {
		t.Fatalf("name=%q foreign=%v err=%v", name, foreign, err)
	}
	name, foreign, err = peerName([]*x509.Certificate{cert}, "www.gosuslugi.ru")
	if err != nil || foreign || name != "www.gosuslugi.ru" {
		t.Fatalf("match name=%q foreign=%v err=%v", name, foreign, err)
	}
	name, foreign, err = peerName([]*x509.Certificate{cert}, "lk.gosuslugi.ru")
	if err != nil || foreign {
		t.Fatalf("wildcard name=%q foreign=%v err=%v", name, foreign, err)
	}
}

// TestAdoptDecoyFromStatus caches Status.Decoy and AdaptError retries SNI.
func TestAdoptDecoyFromStatus(t *testing.T) {
	clearDecoyCache()
	t.Cleanup(clearDecoyCache)
	addr := "10.0.0.1:443"
	err := adoptDecoy(addr, "node.example.com", wire.Status{Decoy: []string{"online.sberbank.ru:443"}}, true)
	ae, ok := err.(*AdaptError)
	if !ok || ae.SNI != "online.sberbank.ru" {
		t.Fatalf("adapt %v", err)
	}
	origins, known := CachedDecoy(addr)
	if !known || len(origins) != 1 || origins[0] != "online.sberbank.ru:443" {
		t.Fatalf("cache %+v known=%v", origins, known)
	}
	if err := adoptDecoy(addr, "online.sberbank.ru", wire.Status{Decoy: []string{"online.sberbank.ru:443"}}, true); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptDecoyCDNKeepsEdgeSNI(t *testing.T) {
	clearDecoyCache()
	t.Cleanup(clearDecoyCache)
	st := wire.Status{Decoy: []string{"gosuslugi.ru:443"}, MaskPub: make([]byte, 32)}
	if err := adoptDecoy("cdn.example:443", "cdn.example", st, false); err != nil {
		t.Fatalf("cdn hop: %v", err)
	}
	origins, known := CachedDecoy("cdn.example:443")
	if !known || len(origins) == 0 || origins[0] != "gosuslugi.ru:443" {
		t.Fatalf("cache %+v known=%v", origins, known)
	}
}

// TestResolveTLSUsesNodeCache uses cached decoy SNI on a direct front.
func TestResolveTLSUsesNodeCache(t *testing.T) {
	clearDecoyCache()
	t.Cleanup(clearDecoyCache)
	o := config.Outbound{URL: "https://10.1.2.3", Type: "ws"}
	RememberDecoy("10.1.2.3:443", []string{"www.gosuslugi.ru:443"})
	addr, sni, insecure, err := resolveTLS(o)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.1.2.3:443" || sni != "www.gosuslugi.ru" || !insecure {
		t.Fatalf("addr=%s sni=%s insecure=%v", addr, sni, insecure)
	}
}

// TestDiscoverDecoyReadsCert reads the origin cert name without a session HMAC.
func TestDiscoverDecoyReadsCert(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer ts.Close()
	name, foreign, err := DiscoverDecoy(ts.Listener.Addr().String(), "127.0.0.1", "go")
	if err != nil {
		t.Fatal(err)
	}
	if !foreign || name == "" || name == "127.0.0.1" {
		t.Fatalf("name=%q foreign=%v", name, foreign)
	}
}

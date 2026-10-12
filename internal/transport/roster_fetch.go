package transport

// Client roster fetch: GET public manifest, verify Ed25519, pin controller_pub.

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"loft/internal/config"
	"loft/internal/wire"
)

// ParseControllerPub decodes a 32-byte controller Ed25519 key from hex or base64.
func ParseControllerPub(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, fmt.Errorf("controller_pub: want 32 bytes")
}

// FetchRoster GETs a public roster JSON manifest, verifies it, and stores the roster.
func FetchRoster(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", chromeUA)
	cl := &http.Client{Timeout: 8 * time.Second}
	res, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("roster fetch: %s", res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return err
	}
	r, err := wire.ParseManifest(b)
	if err != nil {
		return err
	}
	if err := r.Verify(); err != nil {
		return err
	}
	RememberRoster(r.Encode())
	return nil
}

// BootstrapRoster pins controller_pub then fetches from bootstrap and CDN manifest URLs.
func BootstrapRoster(ctx context.Context, cfg *config.ClientConfig) {
	if pub, err := ParseControllerPub(cfg.ControllerPub); err == nil && len(pub) == 32 {
		PinControllerPub(pub)
	}
	urls := []string{}
	if cfg.Bootstrap != "" {
		urls = append(urls, cfg.Bootstrap)
	}
	path := "/static/v3/manifest.json"
	for _, o := range cfg.Outbounds {
		if o.CDN != "" && o.URL != "" {
			u := strings.TrimRight(o.URL, "/") + path
			urls = append(urls, u)
		}
	}
	for _, u := range urls {
		cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		err := FetchRoster(cctx, u)
		cancel()
		if err == nil {
			return
		}
	}
}

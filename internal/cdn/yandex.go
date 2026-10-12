package cdn

import "time"

// Yandex Cloud CDN edge profile: GET only, 12 KiB header budget.

// Yandex is the Yandex Cloud CDN edge: POST and WebSocket die (405 / ticket),
// headers above ~12 KiB return 413, origin must answer within 5s.
// Transport remains protocol `get`; this file only pins those limits.
func Yandex() Profile {
	p := GenericGET()
	p.Name = "yandex"
	p.HeaderBudget = 12 << 10
	// Edge origin-fetch concurrency is ~10–12 HTTP/1.1 conns. Inflight 32
	// queued Cookie GETs (TTFB 900ms) and sat upload at ~3 Mbit. 16 matches
	// the pool: pad TTFB ~220ms, more payload per RTT.
	p.Inflight = 16
	// Edge often drops X-Request-Id; Cookie already made the handshake.
	p.UplinkHeader = "Cookie"
	// Origin fetch dies at 5s (504). Waiting 15s HOL-blocks mux on a dead GET.
	p.TripTimeout = 6 * time.Second
	return p
}

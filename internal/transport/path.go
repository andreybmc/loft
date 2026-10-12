package transport

// Path watch: default egress IPv4 change (Wi-Fi / LTE / VPN) closes the
// live front immediately so Supervise redials instead of waiting for TCP idle.

import (
	"context"
	"net"
	"time"
)

// pathProbe is the local IPv4 used as the egress key. Tests replace it.
var pathProbe = egressAddr

// pathTick is how often WatchPath samples egress. Tests shorten it.
var pathTick = 250 * time.Millisecond

// hostOnly is the IP of host:port; addr without a port is returned as-is.
func hostOnly(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// egressAddr is the source IPv4 of a UDP datagram toward 1.1.1.1.
// The ephemeral port is stripped: a new socket every tick would look like a path change.
func egressAddr() string {
	d := net.Dialer{Timeout: 400 * time.Millisecond}
	c, err := d.Dial("udp4", "1.1.1.1:53")
	if err != nil {
		c, err = d.Dial("udp4", "8.8.8.8:53")
		if err != nil {
			return ""
		}
	}
	addr := hostOnly(c.LocalAddr().String())
	_ = c.Close()
	return addr
}

// WatchPath sends on the returned channel when the default IPv4 egress changes.
func WatchPath(ctx context.Context) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		last := pathProbe()
		t := time.NewTicker(pathTick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				now := pathProbe()
				if now == "" {
					continue
				}
				if last == "" {
					last = now
					continue
				}
				if now == last {
					continue
				}
				last = now
				select {
				case ch <- struct{}{}:
				default:
				}
			}
		}
	}()
	return ch
}

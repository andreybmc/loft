package transport

// Path watch: IP-only egress key, fire on address change, ignore ephemeral port.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestHostOnlyStripsPort keeps WatchPath keyed on IPv4, not the UDP source port.
func TestHostOnlyStripsPort(t *testing.T) {
	if hostOnly("10.0.0.1:54321") != "10.0.0.1" {
		t.Fatal(hostOnly("10.0.0.1:54321"))
	}
	if hostOnly("10.0.0.1") != "10.0.0.1" {
		t.Fatal(hostOnly("10.0.0.1"))
	}
}

// TestWatchPathFiresOnIPChange ignores a stable IP and signals a new one.
func TestWatchPathFiresOnIPChange(t *testing.T) {
	var n atomic.Int32
	oldProbe, oldTick := pathProbe, pathTick
	t.Cleanup(func() {
		pathProbe = oldProbe
		pathTick = oldTick
	})
	pathTick = 15 * time.Millisecond
	pathProbe = func() string {
		if n.Load() >= 6 {
			return "10.9.0.2"
		}
		n.Add(1)
		return "10.9.0.1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch := WatchPath(ctx)
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal("no path event")
	}
	select {
	case <-ch:
		t.Fatal("second event on a stable IP")
	case <-time.After(80 * time.Millisecond):
	}
}

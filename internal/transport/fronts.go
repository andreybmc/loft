// Package transport implements Loft 1.0 fronts, TLS camouflage, decoy splice,
// borrowed-cert mask, CONNECT splice, and client dialing.
package transport

import (
	"sync"
	"sync/atomic"

	"loft/internal/config"
	"loft/internal/wire"
)

// nodeView is the last Status fronts and mask_pub for one node address.
type nodeView struct {
	fronts  []string
	maskPub []byte
}

// views caches Status fronts and mask_pub; rotateN rotates filterOutbounds.
var views sync.Map // addr -> nodeView
var rotateN atomic.Uint64

// RememberFronts stores Status caps fronts for addr.
func RememberFronts(addr string, caps wire.Cap) {
	fronts := caps.Fronts()
	v, _ := views.LoadOrStore(addr, nodeView{})
	cur := v.(nodeView)
	cur.fronts = append([]string(nil), fronts...)
	views.Store(addr, cur)
}

// RememberMaskPub stores Status mask_pub for addr.
func RememberMaskPub(addr string, pub []byte) {
	v, _ := views.LoadOrStore(addr, nodeView{})
	cur := v.(nodeView)
	cur.maskPub = append([]byte(nil), pub...)
	views.Store(addr, cur)
}

// CachedFronts returns a copy of stored fronts for addr.
func CachedFronts(addr string) ([]string, bool) {
	v, ok := views.Load(addr)
	if !ok {
		return nil, false
	}
	f := v.(nodeView).fronts
	if f == nil {
		return nil, false
	}
	return append([]string(nil), f...), true
}

// CachedMaskPub returns a copy of stored mask_pub for addr.
func CachedMaskPub(addr string) []byte {
	v, ok := views.Load(addr)
	if !ok {
		return nil
	}
	return append([]byte(nil), v.(nodeView).maskPub...)
}

// filterOutbounds keeps cfg-allowed fronts that the node advertised, then rotates.
func filterOutbounds(cfg *config.ClientConfig, list []config.Outbound) []config.Outbound {
	addr := ""
	if len(list) > 0 {
		addr, _ = outboundAddr(list[0])
	}
	server, known := CachedFronts(addr)
	var out []config.Outbound
	for _, o := range list {
		if !cfg.Allows(o.Type) {
			continue
		}
		if known {
			ok := false
			for _, f := range server {
				if f == o.Type {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, o)
	}
	if len(out) == 0 {
		return nil
	}
	n := int(rotateN.Load()) % len(out)
	if n == 0 {
		return out
	}
	return append(append([]config.Outbound{}, out[n:]...), out[:n]...)
}

// helloCaps is Hello caps: enabled fronts, extra caps, and optional P2P.
func helloCaps(cfg *config.ClientConfig) wire.Cap {
	fronts, err := wire.ParseFronts(cfg.EnabledFronts())
	if err != nil {
		fronts = cfg.EnabledFronts()
	}
	c := wire.CapsWithFronts(wire.NodeCaps, fronts)
	c |= wire.Cap(cfg.ExtraCaps)
	if cfg.P2P == nil || *cfg.P2P {
		c |= wire.CapP2P
	}
	return c
}

// ServerCaps is Status caps from server config, decoy, mask, roster, and lists.
func ServerCaps(cfg *config.ServerConfig, maskOn bool) wire.Cap {
	fronts, err := wire.ParseFronts(cfg.EnabledFronts())
	if err != nil {
		fronts = cfg.EnabledFronts()
	}
	c := wire.CapsWithFronts(wire.NodeCaps, fronts)
	if len(cfg.Decoy) == 0 {
		c &^= wire.CapDecoy
	}
	if !maskOn {
		c &^= wire.CapMask
	}
	if len(cfg.Relays) == 0 && cfg.Advertise == "" && cfg.Enrol != "open" {
		c &^= wire.CapRoster
	}
	if len(cfg.Lists) == 0 {
		c &^= wire.CapLists
	}
	return c
}

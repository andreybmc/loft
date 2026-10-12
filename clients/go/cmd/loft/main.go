// Command loft is the CLI client: config-driven GET/WS/QUIC outbound,
// SOCKS5, and optional TUN with kill-switch.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"loft/clients/go/inbound"
	"loft/internal/config"
	"loft/internal/transport"
	"loft/internal/version"
)

// main loads the client profile, supervises the outbound, and serves SOCKS5/TUN.
func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("loft: ")
	version.ExitIfAsked("loft", os.Args[1:])
	fs := flag.NewFlagSet("loft", flag.ExitOnError)
	cfgPath := fs.String("c", "clients/go/configs/client.yaml", "config file")
	showVer := fs.Bool("version", false, "print program and protocol versions")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if *showVer {
		version.Print("loft")
		return
	}
	cfg, err := config.LoadClient(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	base := strings.TrimSuffix(*cfgPath, filepath.Ext(*cfgPath))
	transport.SetInstanceFile(base + ".instance")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h := &transport.Holder{}
	exclude := outboundHosts(cfg)
	var (
		tunMu  sync.Mutex
		tunDev *inbound.Device
	)
	defer func() { _ = tunDev.Close() }()
	bringTUN := func() {
		if cfg.Listen.TUN == "" {
			return
		}
		ip := transport.OverlayIP()
		if ip == "" {
			return
		}
		cidr := inbound.OverlayCIDR(ip, cfg.Listen.TunAddr)
		tunMu.Lock()
		defer tunMu.Unlock()
		if tunDev != nil {
			tunDev.Attach(h)
			return
		}
		c, err := inbound.StartTUN(cfg.Listen.TUN, cidr, cfg.Listen.KillSwitch, h, exclude)
		if err != nil {
			log.Printf("tun: %v", err)
			return
		}
		tunDev = c
		log.Printf("tun up overlay=%s kill_switch=%v", cidr, cfg.Listen.KillSwitch)
	}
	go func() {
		_ = transport.Supervise(ctx, cfg, func(d transport.Dialer, o config.Outbound) {
			h.Set(d)
			log.Printf("%s outbound %s (%s) up overlay=%s", version.Line("loft"), o.Name, o.Type, transport.OverlayIP())
			bringTUN()
		})
	}()

	ln, err := inbound.ListenSOCKS5(cfg.Listen.SOCKS5, h)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	log.Printf("socks5 %s", cfg.Listen.SOCKS5)

	<-ctx.Done()
}

// outboundHosts resolves front hostnames to IPs excluded from TUN split routes.
func outboundHosts(cfg *config.ClientConfig) []string {
	var out []string
	for _, o := range cfg.Outbounds {
		u, err := url.Parse(o.URL)
		if err != nil {
			continue
		}
		host := u.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			out = append(out, host)
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil {
			continue
		}
		for _, ip := range ips {
			out = append(out, ip.String())
		}
	}
	return out
}

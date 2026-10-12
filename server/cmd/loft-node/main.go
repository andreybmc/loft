// Command loft-node is the overlay node daemon: controller or relay.
// Config is YAML or switch-style; overlay state lives in -dir.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"loft/internal/config"
	ecrypto "loft/internal/crypto"
	"loft/internal/identity"
	"loft/internal/netconf"
	"loft/internal/transport"
	"loft/internal/version"
	"loft/server/origin"
)

// main loads overlay and server config, then serves TCP HTTPS and UDP QUIC/H3.
func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("loft-node: ")
	version.ExitIfAsked("loft-node", os.Args[1:])
	dir := flag.String("dir", "/var/lib/loft", "data directory")
	cfgPath := flag.String("c", "", "server config (role, relays, decoy)")
	listen := flag.String("listen", "", "override listen address")
	cert := flag.String("cert", "", "TLS cert pem")
	key := flag.String("key", "", "TLS key pem")
	showVer := flag.Bool("version", false, "print program and protocol versions")
	printPub := flag.Bool("print-pub", false, "print controller Ed25519 public key (64 hex) and exit")
	flag.Parse()
	if *showVer {
		version.Print("loft-node")
		return
	}
	if *printPub {
		if err := printControllerPub(*dir, *cfgPath); err != nil {
			log.Fatal(err)
		}
		return
	}

	n, err := netconf.LoadDir(*dir)
	if err != nil {
		log.Fatal(err)
	}
	if !n.Enabled {
		log.Fatal("network disabled")
	}
	addr := *listen
	if addr == "" && len(n.Listen) > 0 {
		ip := n.Listen[0].IP
		if ip == "" {
			ip = "127.0.0.1"
		}
		addr = fmt.Sprintf("%s:%d", ip, n.Listen[0].Port)
	}
	if addr == "" {
		addr = "127.0.0.1:7643"
	}
	var cfg *config.ServerConfig
	if *cfgPath != "" {
		var err error
		cfg, err = config.LoadServer(*cfgPath)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		cfg = &config.ServerConfig{
			Listen:  addr,
			GetPath: "/static/v3/build",
			WSPath:  "/api/v1/sync",
			Role:    "controller",
		}
	}
	if *listen != "" {
		cfg.Listen = *listen
	} else if cfg.Listen == "" {
		cfg.Listen = addr
	}
	if *cert != "" {
		cfg.CertFile = *cert
	}
	if *key != "" {
		cfg.KeyFile = *key
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("LOFT_TOKEN")
	}
	ring, err := identity.Load(filepath.Join(*dir, "members.keys"))
	if err != nil {
		log.Fatal(err)
	}
	want := map[string]string{}
	for _, m := range n.Members {
		if m.KeySHA256 != "" {
			want[m.ID] = m.KeySHA256
		}
	}
	ring.Filter(want)
	transport.AuthKeys = ring
	if cfg.Token == "" && ring.Len() == 0 {
		log.Fatal("LOFT_TOKEN or member keys required")
	}
	if ring.Len() > 0 {
		log.Printf("member keys: %d", ring.Len())
	}
	if cfg.ControllerKey == "" {
		cfg.ControllerKey = filepath.Join(*dir, "controller.seed")
	}
	h, err := origin.New(cfg, n)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("%s network %s role=%s peers=%s listen %s", version.Line("loft-node"), n.ID, cfg.Role, n.Peers, cfg.Listen)
	transport.EnableHTTP2ExtendedConnect()
	if err := origin.ListenAll(cfg, h); err != nil {
		log.Fatal(err)
	}
}

// printControllerPub writes the controller Ed25519 public key as 64 hex digits.
func printControllerPub(dir, cfgPath string) error {
	token := os.Getenv("LOFT_TOKEN")
	seed := filepath.Join(dir, "controller.seed")
	if cfgPath != "" {
		cfg, err := config.LoadServer(cfgPath)
		if err != nil {
			return err
		}
		if cfg.Token != "" {
			token = cfg.Token
		}
		if cfg.ControllerKey != "" {
			seed = cfg.ControllerKey
		}
	}
	if _, err := os.Stat(seed); err != nil && token == "" {
		return fmt.Errorf("controller.seed missing and LOFT_TOKEN empty")
	}
	priv, err := ecrypto.LoadControllerKey(seed, token)
	if err != nil {
		return err
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return fmt.Errorf("controller key: not ed25519")
	}
	fmt.Println(hex.EncodeToString(pub))
	return nil
}

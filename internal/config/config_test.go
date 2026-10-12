package config

// Node.conf command parse, YAML fallback, and RenderCommands round-trip.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadServerTokenFromEnv leaves token empty (filled later from LOFT_TOKEN).
func TestLoadServerTokenFromEnv(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node.conf")
	if err := os.WriteFile(p, []byte("listen :443\nrole controller\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "" {
		t.Fatalf("token %q, want empty (filled later from LOFT_TOKEN)", c.Token)
	}
	if c.Listen != ":443" {
		t.Fatalf("listen %q", c.Listen)
	}
	if c.Role != "controller" {
		t.Fatalf("role %q", c.Role)
	}
}

// TestLoadServerYAMLFallback still loads a legacy node.yaml mapping.
func TestLoadServerYAMLFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(p, []byte("listen: \":443\"\nrole: controller\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":443" || c.Role != "controller" {
		t.Fatalf("%+v", c)
	}
}

// TestRenderCommandsRoundTrip parses what RenderCommands writes.
func TestRenderCommandsRoundTrip(t *testing.T) {
	in := ServerConfig{
		Role: "controller", Listen: ":444", Advertise: "https://de1.example.com",
		Decoy: []string{"origin.example:443"}, Mask: true,
		Fronts: []string{"get", "ws", "h2"}, Enrol: "list",
		DPI: DPIConfig{SplitHello: false, Pad: 32},
	}
	on := true
	in.DPI.FatHello = &on
	got, err := ParseCommands(RenderCommands(in, true))
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":444" || got.Advertise != in.Advertise || !got.Mask {
		t.Fatalf("%+v", got)
	}
	if strings.Join(got.Fronts, ",") != "get,ws,h2" {
		t.Fatalf("fronts %v", got.Fronts)
	}
}

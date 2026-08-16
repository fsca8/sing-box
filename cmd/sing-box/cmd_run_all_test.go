//go:build with_netbird

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBuildNetbirdConfigKeepsExposePorts guards against the run-all config
// merge silently dropping expose_ports (regression: only 7 whitelisted fields
// were copied; ExposePorts was lost, so Windows bridges never started while
// the libbox path — full ParseConfig unmarshal — kept working).
func TestBuildNetbirdConfigKeepsExposePorts(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sing-box-config.json")
	nbPath := filepath.Join(dir, "netbird-config.json")
	if err := os.WriteFile(nbPath, []byte(`{
		"setup_key": "k",
		"management_url": "https://mgmt.example",
		"device_name": "test",
		"expose_ports": [{"port": 1000, "target": "127.0.0.1:1000"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := buildNetbirdConfig(cfgPath)
	if len(cfg.ExposePorts) != 1 {
		t.Fatalf("ExposePorts lost in run-all merge: got %+v", cfg.ExposePorts)
	}
	ep := cfg.ExposePorts[0]
	if ep.Port != 1000 || ep.Target != "127.0.0.1:1000" {
		t.Fatalf("unexpected expose port: %+v", ep)
	}
}

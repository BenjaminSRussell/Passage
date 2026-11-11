package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passage.yaml")
	if err := os.WriteFile(path, []byte("listen: 0.0.0.0:9443\npgDsn: postgres://a:b@db/x\nfeatures:\n  node: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, map[string]string{"PG_DSN": "postgres://c:d@db2/y", "OIDC_ISSUER": "https://id/realms/p", "FEATURE_PYTHON": "1", "REQUIRE_FIPS": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:9443" || cfg.DatabaseDSN != "postgres://c:d@db2/y" || cfg.OIDCIssuer != "https://id/realms/p" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !cfg.Features["node"] || !cfg.Features["python"] || !cfg.RequireFIPS {
		t.Fatalf("features/fips = %+v", cfg)
	}
	if cfg.OIDCAudience != "passage-api" {
		t.Fatalf("default audience lost: %q", cfg.OIDCAudience)
	}
}

func TestLoadConfigMissingFileUsesDefaults(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "none.yaml"), nil)
	if err != nil || cfg.Listen != defaultListen {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestValidateRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"half tls":   func(c *Config) { c.TLS.CertFile = "x" },
		"bad issuer": func(c *Config) { c.OIDCIssuer = "not a url" },
		"dup":        func(c *Config) { c.Services = []ServiceSpec{{Name: "a"}, {Name: "a"}} },
		"port":       func(c *Config) { c.BasePort = 0 },
	} {
		c := DefaultConfig()
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestResolveDiscoversServicesAndAssignsPorts(t *testing.T) {
	c := DefaultConfig()
	c.InstallDir = filepath.Join("C:", "Passage")
	c.DataDir = "data"
	c.AppDSN = "postgres://passage_app:pw@db:5432/postgres?sslmode=require"
	c.KafkaBootstrap = "k:9092"
	c.Services = []ServiceSpec{{Name: "ledger", Env: map[string]string{"X": "1"}}, {Name: "fx", Disabled: true}, {Name: "agents", Feature: "node", Exe: "node.exe"}}
	list := func(string) ([]string, error) {
		return []string{"purchasing.exe", "ledger.exe", "fx.exe", "passage-server.exe", "readme.txt"}, nil
	}
	_, procs, err := c.Resolve(func(string) string { return "" }, list)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 2 {
		t.Fatalf("procs = %+v", procs)
	}
	if procs[0].Name != "ledger" || procs[0].Port != 18100 || procs[0].AdminPort != 19100 {
		t.Fatalf("ledger = %+v", procs[0])
	}
	if procs[1].Name != "purchasing" || procs[1].Route != "procurement" || procs[1].Port != 18101 {
		t.Fatalf("purchasing = %+v", procs[1])
	}
	env := procs[0].Env
	if env["DATABASE_URL"] != "postgres://passage_app:pw@db:5432/passage_ledger?sslmode=require" || env["X"] != "1" || env["KAFKA_BOOTSTRAP"] != "k:9092" {
		t.Fatalf("env = %v", env)
	}
	if env["HTTP_ADDR"] != "127.0.0.1:18100" || env["MIGRATE_ON_START"] != "true" {
		t.Fatalf("env = %v", env)
	}
	if !strings.HasSuffix(filepath.ToSlash(procs[0].Exe), "bin/ledger.exe") {
		t.Fatalf("exe = %s", procs[0].Exe)
	}
}

func TestResolveFeatureGate(t *testing.T) {
	c := DefaultConfig()
	c.InstallDir, c.DataDir = "i", "d"
	c.Features = map[string]bool{"node": true}
	c.Services = []ServiceSpec{{Name: "agents-ts", Feature: "node", Exe: "{install}/runtime/node/node.exe", Args: []string{"{data}/x.js"}, NoDB: true}}
	_, procs, err := c.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil })
	if err != nil || len(procs) != 1 {
		t.Fatalf("procs=%v err=%v", procs, err)
	}
	if _, ok := procs[0].Env["DATABASE_URL"]; ok {
		t.Fatal("noDb service got a DATABASE_URL")
	}
	if !strings.Contains(procs[0].Exe, "node.exe") {
		t.Fatalf("exe = %s", procs[0].Exe)
	}
	c.Features = nil
	_, procs, _ = c.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil })
	if len(procs) != 0 {
		t.Fatal("feature off must drop the service")
	}
}

func TestConfDMergesFeatureServices(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o750); err != nil {
		t.Fatal(err)
	}
	frag := "services:\n  - name: workflow-ts\n    exe: C:/Passage/runtime/node/node.exe\n    feature: node\n    noDb: true\n"
	if err := os.WriteFile(filepath.Join(dir, "conf.d", "node.yaml"), []byte(frag), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(filepath.Join(dir, "passage.yaml"), map[string]string{"FEATURE_NODE": "1"})
	if err != nil || len(cfg.Services) != 1 || cfg.Services[0].Name != "workflow-ts" || !cfg.Features["node"] {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

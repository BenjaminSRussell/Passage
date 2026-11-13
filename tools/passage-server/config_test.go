package main

import (
	"gopkg.in/yaml.v3"

	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLoadConfigDefaultsAndOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "passage.yaml")
	if err := os.WriteFile(path, []byte("listen: 0.0.0.0:9443\npgDsn: postgres://a:b@db/x\nfeatures:\n  node: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, map[string]string{"PG_DSN": "postgres://c:d@db2/y", "OIDC_ISSUER": "https://id/realms/p", "ALLOW_PLAINTEXT": "1", "FEATURE_PYTHON": "1", "REQUIRE_FIPS": "true"})
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
	c.DatabaseDSN = "postgres://owner:pw@db:5432/postgres?sslmode=require"
	c.KafkaBootstrap = "k:9092"
	c.TemporalHost = "t:7233"
	c.Services = []ServiceSpec{{Name: "ledger", Env: map[string]string{"X": "1"}}, {Name: "fx", Disabled: true}, {Name: "agents", Feature: "node", Exe: "node.exe"}}
	list := func(string) ([]string, error) {
		return []string{"purchasing.exe", "ledger.exe", "fx.exe", "passage-server.exe", "readme.txt"}, nil
	}
	_, procs, err := c.Resolve(func(string) string { return "" }, list, ModeServe)
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
	if env["DATABASE_URL"] != "postgres://passage_app:pw@db:5432/passage_ledger?sslmode=require" || env["X"] != "1" || env["KAFKA_BROKERS"] != "k:9092" || env["TEMPORAL_ADDRESS"] != "t:7233" {
		t.Fatalf("env = %v", env)
	}
	if env["HTTP_ADDR"] != "127.0.0.1:18100" || env["MIGRATE_ON_START"] != "false" || env["MIGRATE_DATABASE_URL"] != "" {
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
	_, procs, err := c.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil }, ModeServe)
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
	_, procs, _ = c.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil }, ModeServe)
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

func resolveWith(t *testing.T, c Config, mode Mode, files ...string) []Process {
	t.Helper()
	c.InstallDir, c.DataDir = "i", "d"
	_, procs, err := c.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return files, nil }, mode)
	if err != nil {
		t.Fatal(err)
	}
	return procs
}

func TestRolesPerMode(t *testing.T) {
	c := DefaultConfig()
	c.DatabaseDSN = "postgres://owner:o@db/postgres"
	c.AppDSN = "postgres://passage_app:a@db/postgres"
	serve := resolveWith(t, c, ModeServe, "ledger.exe")[0].Env
	if serve["DATABASE_URL"] != "postgres://passage_app:a@db/passage_ledger" || serve["MIGRATE_ON_START"] != "false" || serve["MIGRATE_DATABASE_URL"] != "" {
		t.Fatalf("serve env = %v", serve)
	}
	mig := resolveWith(t, c, ModeMigrate, "ledger.exe")[0].Env
	if mig["DATABASE_URL"] != "postgres://owner:o@db/passage_ledger" || mig["MIGRATE_DATABASE_URL"] != mig["DATABASE_URL"] || mig["MIGRATE_ON_START"] != "true" {
		t.Fatalf("migrate env = %v", mig)
	}
}

func TestChildrenKnowTheirPeers(t *testing.T) {
	procs := resolveWith(t, DefaultConfig(), ModeServe, "ledger.exe", "party.exe", "supplier-portal-ts.exe")
	byName := map[string]map[string]string{}
	for _, p := range procs {
		byName[p.Name] = p.Env
	}
	if byName["party"]["LEDGER_URL"] != "http://127.0.0.1:18100" || byName["ledger"]["PARTY_URL"] != "http://127.0.0.1:18101" {
		t.Fatalf("peer urls: %v / %v", byName["party"], byName["ledger"])
	}
	if byName["ledger"]["SUPPLIER_PORTAL_TS_URL"] != "http://127.0.0.1:18102" {
		t.Fatalf("dashes must become underscores: %v", byName["ledger"])
	}
	if _, self := byName["ledger"]["LEDGER_URL"]; self {
		t.Fatal("a service must not get a URL for itself")
	}
}

func TestPlaintextNonLoopbackIsRefused(t *testing.T) {
	c := DefaultConfig()
	c.Listen = "0.0.0.0:8443"
	if err := c.Validate(); err == nil {
		t.Fatal("non-loopback without TLS must be refused")
	}
	c.AllowPlaintext = true
	if err := c.Validate(); err != nil {
		t.Fatalf("explicit override: %v", err)
	}
	c = DefaultConfig()
	c.Listen = "[::1]:8443"
	if err := c.Validate(); err != nil {
		t.Fatalf("ipv6 loopback: %v", err)
	}
}

func TestDuplicateRoutesAreRefused(t *testing.T) {
	c := DefaultConfig()
	c.InstallDir, c.DataDir = "i", "d"
	c.Services = []ServiceSpec{{Name: "a", Exe: "a.exe", Route: "x"}, {Name: "b", Exe: "b.exe", Route: "x"}}
	if _, _, err := c.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil }, ModeServe); err == nil {
		t.Fatal("two services on one route must be refused")
	}
}

// The conf.d files the MSI features install must resolve to the start commands Build-ServiceBundles.ps1 publishes and
// Test-ServiceBundles.ps1 tests: the bundle paths, the port in the arguments or env, one port for API and readiness.
func TestShippedFeatureFilesResolve(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"node.yaml", "python.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "windows", "features", f))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "conf.d", f), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadConfig(filepath.Join(dir, "passage.yaml"), map[string]string{"FEATURE_NODE": "1", "FEATURE_PYTHON": "1", "PG_DSN": "postgres://o:p@db:5432/postgres"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.InstallDir, cfg.DataDir = dir, filepath.Join(dir, "data")
	_, procs, err := cfg.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil }, ModeServe)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Process{}
	for _, p := range procs {
		byName[p.Name] = p
	}
	wf := byName["workflow-ts"]
	if len(wf.Args) != 1 || wf.Args[0] != dir+"/runtime/node/apps/workflow-ts/dist/worker.js" {
		t.Fatalf("workflow-ts args = %v", wf.Args)
	}
	if wf.Env["HTTP_PORT"] != strconv.Itoa(wf.Port) || wf.AdminPort != wf.Port {
		t.Fatalf("workflow-ts port %d admin %d env %v", wf.Port, wf.AdminPort, wf.Env["HTTP_PORT"])
	}
	ag := byName["agents-ts"]
	if ag.Args[0] != dir+"/runtime/node/apps/agents-ts/dist/main.js" || ag.Env["DATABASE_URL"] == "" || ag.AdminPort == ag.Port {
		t.Fatalf("agents-ts = %+v", ag)
	}
	for _, name := range []string{"routing-py", "scheduler-py"} {
		p := byName[name]
		if p.Exe != dir+"/runtime/python/python.exe" || p.ReadyPath != "/healthz" || p.AdminPort != p.Port {
			t.Fatalf("%s = %+v", name, p)
		}
		if p.Args[len(p.Args)-1] != strconv.Itoa(p.Port) || p.Args[0] != "-m" || p.Args[1] != "uvicorn" {
			t.Fatalf("%s args = %v", name, p.Args)
		}
	}
	// A second resolve starts again from the templates: the first one must not have written ports into the specs.
	_, again, err := cfg.Resolve(func(string) string { return "" }, func(string) ([]string, error) { return nil, nil }, ModeServe)
	if err != nil || len(again) != len(procs) || cfg.Services[0].Env["HTTP_PORT"] != "{port}" {
		t.Fatalf("second resolve: %v %v", err, cfg.Services[0].Env)
	}
}

func TestArm64NodeFeatureFileHasNoTemporalWorker(t *testing.T) {
	var file struct {
		Services []ServiceSpec `yaml:"services"`
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "windows", "features", "node-arm64.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Services) != 1 || file.Services[0].Name != "agents-ts" || file.Services[0].Feature != "node" {
		t.Fatalf("arm64 node services = %+v", file.Services)
	}
}

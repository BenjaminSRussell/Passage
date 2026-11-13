package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs", "a.log")
	r := &RotatingFile{Path: p, MaxBytes: 10, Keep: 2}
	for i := 0; i < 5; i++ {
		if _, err := r.Write([]byte("123456\n")); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Close()
	for _, f := range []string{p, p + ".1", p + ".2"} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Fatal("kept more files than configured")
	}
}

func TestKeysPersistAndPeersGetPublicKeys(t *testing.T) {
	dir := t.TempDir()
	k1, err := LoadOrCreateKeys(dir, []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := LoadOrCreateKeys(dir, []string{"a", "b"})
	if !bytes.Equal(k1["a"], k2["a"]) {
		t.Fatal("seed changed between loads")
	}
	procs := []Process{{Name: "a"}, {Name: "b"}}
	k1.Apply(procs)
	pubB := base64.StdEncoding.EncodeToString(ed25519.NewKeyFromSeed(k1["b"]).Public().(ed25519.PublicKey))
	if procs[0].Env["PASSAGE_SERVICE_PUBLIC_KEYS"] != "b="+pubB {
		t.Fatalf("a's peers = %q", procs[0].Env["PASSAGE_SERVICE_PUBLIC_KEYS"])
	}
	if strings.Contains(procs[0].Env["PASSAGE_SERVICE_PUBLIC_KEYS"], "a=") {
		t.Fatal("a service must not trust its own key as a peer")
	}
}

func TestVersionAndUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := execute([]string{"version"}, &out, &errb, func(string) string { return "" }); code != 0 || !strings.HasPrefix(out.String(), "passage-server ") {
		t.Fatalf("version = %d %q", code, out.String())
	}
	if code := execute(nil, &out, &errb, func(string) string { return "" }); code != 2 {
		t.Fatalf("no args = %d", code)
	}
	if code := execute([]string{"bogus"}, &out, &errb, func(string) string { return "" }); code != 2 {
		t.Fatalf("bogus = %d", code)
	}
}

func TestFIPSStartupHook(t *testing.T) {
	c := DefaultConfig()
	env := func(string) string { return "" }
	if err := checkFIPSStartup(c, env); err != nil {
		t.Fatalf("ordinary build must start: %v", err)
	}
	c.RequireFIPS = true
	if fipsEnabled() {
		t.Skip("FIPS mode is on in this environment")
	}
	if err := checkFIPSStartup(c, env); err == nil {
		t.Fatal("requireFips without FIPS mode must refuse to start")
	}
}

func TestDatabaseURLRejectsGarbage(t *testing.T) {
	if _, err := databaseURL("not-a-url", "x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestGatewaySecretIsRandomAndApplied(t *testing.T) {
	a, err := NewGatewaySecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewGatewaySecret()
	if a == b || len(a) < 32 {
		t.Fatalf("secrets: %q %q", a, b)
	}
	procs := []Process{{Name: "x"}}
	ApplyGatewaySecret(procs, a)
	if procs[0].Env["PASSAGE_GATEWAY_SECRET"] != a {
		t.Fatal("secret not applied")
	}
}

type fakeProv struct {
	dbs    []string
	grants []string
}

func (f *fakeProv) EnsureDatabase(_ context.Context, n string) error {
	f.dbs = append(f.dbs, n)
	return nil
}
func (f *fakeProv) GrantApp(_ context.Context, db, role string) error {
	f.grants = append(f.grants, role+"@"+db)
	return nil
}

func TestProvisionDatabases(t *testing.T) {
	c := DefaultConfig()
	c.AppDSN = "postgres://passage_app:a@db/postgres"
	procs := []Process{
		{Name: "ledger", Env: map[string]string{"DATABASE_URL": "postgres://owner:o@db/passage_ledger"}},
		{Name: "agents", Env: map[string]string{}},
	}
	f := &fakeProv{}
	if err := ProvisionDatabases(context.Background(), c, procs, f, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if len(f.dbs) != 1 || f.dbs[0] != "passage_ledger" || len(f.grants) != 1 || f.grants[0] != "passage_app@passage_ledger" {
		t.Fatalf("dbs=%v grants=%v", f.dbs, f.grants)
	}
}

func TestServicesCommandListsResolvedChildrenWithoutDatabaseURLs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "windows", "features", "python.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf.d", "python.yaml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := "installDir: " + filepath.ToSlash(dir) + "\npgDsn: postgres://owner:secret@db:5432/postgres\nfeatures:\n  python: true\nservices:\n  - name: ledger\n    exe: ledger.exe\n"
	if err := os.WriteFile(filepath.Join(dir, "passage.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"PASSAGE_CONFIG": filepath.Join(dir, "passage.yaml"), "PASSAGE_DATA_DIR": dir}
	var out, errb bytes.Buffer
	if code := execute([]string{"services"}, &out, &errb, func(k string) string { return env[k] }); code != 0 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatalf("services printed a database password: %s", out.String())
	}
	for _, want := range []string{`"name": "routing-py"`, `"readyPath": "/healthz"`, `"name": "ledger"`, `"readyPath": "/readyz"`, `"uvicorn"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("services output lacks %s: %s", want, out.String())
		}
	}
}

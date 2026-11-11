package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
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

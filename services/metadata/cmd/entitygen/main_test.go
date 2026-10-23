package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const referenceDir = "../../testdata/entities/_reference"

func TestRun_CheckFailsOnDrift(t *testing.T) {
	out := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-in", referenceDir, "-out", out}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-in", referenceDir, "-out", out, "-check"}, &stdout, &stderr); err != nil {
		t.Fatalf("fresh output must pass -check: %v", err)
	}
	if err := os.WriteFile(filepath.Join(out, "Role.sql"), []byte("drift"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-in", referenceDir, "-out", out, "-check"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "Role.sql") {
		t.Fatalf("drift must fail naming Role.sql, got %v", err)
	}
}

func TestRun_CommittedGoldenMatches(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run([]string{"-in", referenceDir, "-out", "../../testdata/golden", "-check"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestRun_MissingInputDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{"-out", out}, {"-in", t.TempDir(), "-out", out}, {"-in", "/nonexistent", "-out", out}} {
		if err := run(args, &stdout, &stderr); err == nil {
			t.Errorf("args %v must fail", args)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("failed run must not create the output directory")
	}
}

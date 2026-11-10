package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func manifestArgs(extra ...string) []string {
	return append([]string{"scratch-manifest", "--name", "passage-pg-restore", "--target-time", "2026-11-09T10:30:00+02:00"}, extra...)
}

func TestScratchManifestRecoversToTheTargetTime(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), manifestArgs(), &out, os.Getenv, fixedNow); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: passage-pg-restore",
		`targetTime: "2026-11-09 08:30:00+00"`,
		"barmanObjectName: passage-pg-backup",
		"serverName: passage-pg",
		"name: barman-cloud.cloudnative-pg.io",
		"namespace: passage-data",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("manifest lacks %q:\n%s", want, out.String())
		}
	}
}

func TestScratchManifestRejectsUnsafeOrNonsenseInput(t *testing.T) {
	for name, args := range map[string][]string{
		"future target":        {"scratch-manifest", "--name", "x", "--target-time", "2026-11-10T00:00:00Z"},
		"bad time":             {"scratch-manifest", "--name", "x", "--target-time", "yesterday"},
		"name injection":       manifestArgs("--namespace", "a\nkind: Secret"),
		"overwrites source":    {"scratch-manifest", "--name", "passage-pg", "--target-time", "2026-11-09T00:00:00Z"},
		"image injection":      manifestArgs("--image", "x\n  foo: bar"),
		"storage not a volume": manifestArgs("--storage", "lots"),
	} {
		var out bytes.Buffer
		if err := run(context.Background(), args, &out, os.Getenv, fixedNow); err == nil {
			t.Errorf("%s was accepted:\n%s", name, out.String())
		}
	}
}

func TestRunRejectsUnknownCommands(t *testing.T) {
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"drop"}} {
		if err := run(context.Background(), args, &out, os.Getenv, fixedNow); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

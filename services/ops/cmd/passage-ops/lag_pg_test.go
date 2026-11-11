package main

import (
	"os"
	"strings"
	"testing"
)

// The real queries run against a plain (non-replicated) Postgres: they must be valid SQL, and they must not report an
// unreplicated server as caught up.
func TestPgLagSourceRefusesAServerWithoutReplication(t *testing.T) {
	url := os.Getenv("PASSAGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PASSAGE_TEST_DATABASE_URL is not set")
	}
	for mode, want := range map[string]string{ModeStandby: "not a streaming standby", ModePrimary: "no standby is connected"} {
		env := map[string]string{"OPS_LAG_DATABASE_URL": url, "OPS_LAG_MODE": mode}
		src, err := pgLagSource(t.Context(), func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		_, lerr := src.Lag(t.Context())
		_ = src.Close(t.Context())
		if lerr == nil || !strings.Contains(lerr.Error(), want) {
			t.Fatalf("%s: err %v, want %q", mode, lerr, want)
		}
	}
	if _, err := pgLagSource(t.Context(), func(string) string { return "" }); err == nil {
		t.Fatal("no URL must fail")
	}
	if _, err := pgLagSource(t.Context(), func(k string) string {
		if k == "OPS_LAG_MODE" {
			return "sideways"
		}
		return url
	}); err == nil {
		t.Fatal("a bad mode must fail")
	}
}

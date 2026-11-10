package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// WMS_LOCK_TIMEOUT_MIN is the PRD lock_timeout_min parameter: 5 to 240, default 30.
func TestLockTimeout(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 30 * time.Minute, true},
		{"5", 5 * time.Minute, true},
		{"240", 240 * time.Minute, true},
		{"4", 0, false},
		{"241", 0, false},
		{"thirty", 0, false},
	} {
		got, err := lockTimeout(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("lockTimeout(%q) = %v, %v; want %v ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestRunRefusesServiceTokensWithoutTheSharedReplayCache(t *testing.T) {
	pool := testkit.DB(t)
	var schema string
	if err := pool.QueryRow(context.Background(), "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	dbURL, err := url.Parse(os.Getenv(testkit.EnvURL))
	if err != nil {
		t.Fatal(err)
	}
	q := dbURL.Query()
	q.Set("search_path", schema)
	dbURL.RawQuery = q.Encode()
	env := map[string]string{
		"DATABASE_URL":                  dbURL.String(),
		"HTTP_ADDR":                     "127.0.0.1:0",
		"ADMIN_ADDR":                    "127.0.0.1:0",
		tenant.EnvServiceName:           "warehouse",
		tenant.EnvServicePublicKeys:     "mobile=" + base64.StdEncoding.EncodeToString(pub),
		"MIGRATE_ON_START":              "",
		"WMS_LOCK_TIMEOUT_MIN":          "",
		"PASSAGE_SERVICE_REPLAY_SIZE":   "",
		"PASSAGE_INVENTORY_URL":         "",
		"PASSAGE_INVENTORY_SERVICE_KEY": "",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = run(ctx, func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "service_token_replay") {
		t.Fatalf("a verifying service without the replay table must refuse to start, got %v", err)
	}
}

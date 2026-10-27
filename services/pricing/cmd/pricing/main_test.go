package main

import (
	"testing"
	"time"
)

func TestConfig_DefaultsAndBounds(t *testing.T) {
	none := func(string) string { return "" }
	cfg, err := config(none)
	if err != nil || cfg.MaxLines != 500 || cfg.ExplainMaxBytes != 65536 {
		t.Fatalf("defaults = %+v, %v; want 500 lines and 65536 bytes", cfg, err)
	}
	env := map[string]string{"PRICING_MAX_LINES": "2001"}
	if _, err := config(func(k string) string { return env[k] }); err == nil {
		t.Fatal("PRICING_MAX_LINES above 2000 must be refused")
	}
	env = map[string]string{"PRICING_EXPLAIN_MAX_BYTES": "100"}
	if _, err := config(func(k string) string { return env[k] }); err == nil {
		t.Fatal("PRICING_EXPLAIN_MAX_BYTES below 8192 must be refused")
	}
}

func TestCacheTTL_DefaultsFormsAndBounds(t *testing.T) {
	cases := []struct {
		raw     string
		want    time.Duration
		refused bool
	}{
		{"", 60 * time.Second, false},
		{"60s", 60 * time.Second, false},
		{"2m", 2 * time.Minute, false},
		{"600", 600 * time.Second, false},
		{"0", 0, false},
		{"5s", 0, true},
		{"11m", 0, true},
		{"soon", 0, true},
	}
	for _, c := range cases {
		got, err := cacheTTL(func(string) string { return c.raw })
		if c.refused {
			if err == nil {
				t.Errorf("PRICING_CACHE_TTL=%q = %s; want refused", c.raw, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("PRICING_CACHE_TTL=%q = %s, %v; want %s", c.raw, got, err, c.want)
		}
	}
}

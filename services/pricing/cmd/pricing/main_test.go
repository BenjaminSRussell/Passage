package main

import "testing"

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

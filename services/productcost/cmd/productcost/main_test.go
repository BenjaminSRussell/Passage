package main

import (
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestConfig_DefaultsAndOverrides(t *testing.T) {
	cfg, err := config(env(nil))
	if err != nil || cfg.MaxLevels != 20 || cfg.Workers != 4 {
		t.Fatalf("defaults: %+v %v", cfg, err)
	}
	cfg, err = config(env(map[string]string{"COSTING_MAX_LEVELS": "12", "COSTING_ROLL_WORKERS": "8"}))
	if err != nil || cfg.MaxLevels != 12 || cfg.Workers != 8 {
		t.Fatalf("overrides: %+v %v", cfg, err)
	}
}

func TestConfig_RejectsOutOfRange(t *testing.T) {
	for _, bad := range []map[string]string{
		{"COSTING_MAX_LEVELS": "41"},
		{"COSTING_MAX_LEVELS": "x"},
		{"COSTING_ROLL_WORKERS": "0"},
	} {
		if _, err := config(env(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

package main

import (
	"strings"
	"testing"
)

func env(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

func TestLoadConfig_Validation(t *testing.T) {
	base := map[string]string{"EDGE_SITE": "plant-1", "EDGE_ID": "edge-7"}
	tests := []struct {
		name     string
		override map[string]string
		wantErr  string
		hours    int
	}{
		{"defaults", nil, "", defaultBufferHrs},
		{"missing site", map[string]string{"EDGE_SITE": ""}, "EDGE_SITE is required", 0},
		{"missing edge id", map[string]string{"EDGE_ID": ""}, "EDGE_ID is required", 0},
		{"admin not loopback", map[string]string{"EDGE_ADMIN_ADDR": "0.0.0.0:8081"}, "loopback", 0},
		{"buffer hours too high", map[string]string{"IOT_BUFFER_HOURS": "169"}, "IOT_BUFFER_HOURS", 0},
		{"buffer hours not a number", map[string]string{"IOT_BUFFER_HOURS": "a day"}, "IOT_BUFFER_HOURS", 0},
		{"buffer hours set", map[string]string{"IOT_BUFFER_HOURS": "48"}, "", 48},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vals := map[string]string{}
			for k, v := range base {
				vals[k] = v
			}
			for k, v := range tc.override {
				vals[k] = v
			}
			cfg, err := loadConfig(env(vals))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.BufferHours != tc.hours {
					t.Fatalf("buffer hours %d, want %d", cfg.BufferHours, tc.hours)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

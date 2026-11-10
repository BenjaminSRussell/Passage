package main

import (
	"context"
	"testing"
	"time"
)

func TestIdleTimeout(t *testing.T) {
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
		{"soon", 0, false},
	} {
		got, err := idleTimeout(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("idleTimeout(%q) = %v, %v; want %v ok=%v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestRunRefusesToStartWithoutItsDependencies(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "postgres://x"}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("a missing WAREHOUSE_URL must stop the service")
	}
	env = map[string]string{"WAREHOUSE_URL": "http://warehouse"}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("a missing DATABASE_URL must stop the service")
	}
	env = map[string]string{"DATABASE_URL": "postgres://x", "WAREHOUSE_URL": "http://warehouse"}
	if err := run(context.Background(), func(k string) string { return env[k] }); err == nil {
		t.Fatal("a missing signing key must stop the service")
	}
}

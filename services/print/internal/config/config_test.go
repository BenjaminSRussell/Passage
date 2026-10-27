package config

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_DefaultsKeepTwoAttemptsPerDevice(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AttemptsPerDevice() != domain.FailoverAttempts || cfg.RetentionDays != domain.DefaultRetentionDays {
		t.Fatalf("defaults = %+v (attempts %d)", cfg, cfg.AttemptsPerDevice())
	}
}

func TestLoad_ReadsValidOverrides(t *testing.T) {
	cases := []struct {
		env      map[string]string
		attempts int
		days     int
	}{
		{map[string]string{"PRINT_RETRY": "0"}, 1, 30},
		{map[string]string{"PRINT_RETRY": "3", "PRINT_JOB_RETENTION_DAYS": "90"}, 4, 90},
		{map[string]string{"PRINT_RETRY": "10", "PRINT_JOB_RETENTION_DAYS": "365"}, 11, 365},
	}
	for _, c := range cases {
		cfg, err := Load(env(c.env))
		if err != nil {
			t.Fatalf("Load(%v): %v", c.env, err)
		}
		if cfg.AttemptsPerDevice() != c.attempts || cfg.RetentionDays != c.days {
			t.Fatalf("Load(%v) = %+v, want %d attempts and %d days", c.env, cfg, c.attempts, c.days)
		}
	}
}

func TestLoad_RejectsOutOfRangeAndNonNumeric(t *testing.T) {
	cases := []map[string]string{
		{"PRINT_RETRY": "-1"},
		{"PRINT_RETRY": "11"},
		{"PRINT_RETRY": "three"},
		{"PRINT_JOB_RETENTION_DAYS": "0"},
		{"PRINT_JOB_RETENTION_DAYS": "366"},
		{"PRINT_JOB_RETENTION_DAYS": "30d"},
	}
	for _, m := range cases {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("Load(%v) accepted an invalid setting", m)
		}
	}
}

func TestLoad_PurgeTenants(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil || cfg.PurgeTenantIDs != nil {
		t.Fatalf("default purge tenants = %v, %v; want none", cfg.PurgeTenantIDs, err)
	}
	cfg, err = Load(env(map[string]string{
		"PRINT_PURGE_TENANT_IDS": "0190a1b2-0000-7000-8000-000000000001, 0190A1B2-0000-7000-8000-000000000002",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"0190a1b2-0000-7000-8000-000000000001", "0190a1b2-0000-7000-8000-000000000002"}
	if len(cfg.PurgeTenantIDs) != 2 || cfg.PurgeTenantIDs[0] != want[0] || cfg.PurgeTenantIDs[1] != want[1] {
		t.Fatalf("purge tenants = %v, want %v", cfg.PurgeTenantIDs, want)
	}
}

func TestLoad_RejectsBadPurgeTenants(t *testing.T) {
	for _, raw := range []string{
		"not-a-uuid",
		"0190a1b2-0000-7000-8000-000000000001,",
		"0190a1b2-0000-7000-8000-000000000001,0190a1b2-0000-7000-8000-000000000001",
	} {
		if _, err := Load(env(map[string]string{"PRINT_PURGE_TENANT_IDS": raw})); err == nil {
			t.Errorf("PRINT_PURGE_TENANT_IDS=%q accepted", raw)
		}
	}
}

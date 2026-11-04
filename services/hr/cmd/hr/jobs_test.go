package main

import (
	"context"
	"strings"
	"testing"
)

func TestTenantJobsValidateBeforeConnecting(t *testing.T) {
	tenantID := "00000000-0000-4000-8000-000000000001"
	tests := []struct {
		name    string
		job     tenantJob
		env     map[string]string
		args    []string
		wantErr string
	}{
		{"no tenants", accrueJob, map[string]string{"DATABASE_URL": "postgres://x"}, nil, "usage: hr accrue"},
		{"bad tenant", carryOverExpiryJob, map[string]string{"DATABASE_URL": "postgres://x"}, []string{"acme"}, `tenant id "acme"`},
		{"missing database url", retentionPurgeJob, nil, []string{tenantID}, "DATABASE_URL is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := runTenantJob(context.Background(), envOf(tc.env), tc.job, tc.args); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLegalHoldsFromEnv(t *testing.T) {
	if _, err := legalHoldsFromEnv(envOf(map[string]string{"HR_RETENTION_PURGE_ENABLED": "false", "COMPLIANCE_URL": "http://c"})); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("kill switch: %v", err)
	}
	if _, err := legalHoldsFromEnv(envOf(nil)); err == nil || !strings.Contains(err.Error(), "COMPLIANCE_URL is required") {
		t.Fatalf("missing url: %v", err)
	}
	if holds, err := legalHoldsFromEnv(envOf(map[string]string{"COMPLIANCE_URL": "http://c"})); err != nil || holds == nil {
		t.Fatalf("configured: %v %v", holds, err)
	}
}

func TestJobByCommand(t *testing.T) {
	for _, command := range []string{"accrue", "carryover-expiry", "retention-purge"} {
		if _, ok := jobByCommand(command); !ok {
			t.Errorf("%s is not a job command", command)
		}
	}
	if _, ok := jobByCommand("reseal-pii"); ok {
		t.Error("reseal-pii has its own handler")
	}
}

package main

import (
	"testing"
)

const (
	reconTenant = "00000000-0000-0000-0000-000000000001"
	reconEntity = "11111111-1111-4111-8111-111111111111"
	reconLedger = "22222222-2222-4222-8222-222222222222"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func fullEnv() map[string]string {
	return map[string]string{
		"ANALYTICS_RECON_TENANT_ID": reconTenant, "CUBE_URL": "http://cube", "CUBE_API_TOKEN": "t", "LEDGER_URL": "http://ledger",
		"ANALYTICS_RECON_LEDGERS": reconEntity + "=" + reconLedger, "ANALYTICS_RECON_REVENUE_ACCOUNTS": "4000-4999",
		"PASSAGE_SERVICE_NAME": "analytics", "PASSAGE_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
	}
}

func TestNewReconJob_DisabledWithoutATenant(t *testing.T) {
	job, err := newReconJob(env(nil), nil)
	if job != nil || err != nil {
		t.Fatalf("job %v err %v", job, err)
	}
}

func TestNewReconJob_BuildsFromFullConfig(t *testing.T) {
	m := fullEnv()
	m["ANALYTICS_RECON_TZ"] = "America/New_York"
	job, err := newReconJob(env(m), nil)
	if err != nil || job == nil || job.Location.String() != "America/New_York" || job.Principal.TenantID != reconTenant {
		t.Fatalf("job %+v err %v", job, err)
	}
}

func TestNewReconJob_RejectsIncompleteOrMalformedConfig(t *testing.T) {
	for _, key := range []string{"CUBE_URL", "CUBE_API_TOKEN", "LEDGER_URL", "ANALYTICS_RECON_LEDGERS", "ANALYTICS_RECON_REVENUE_ACCOUNTS", "PASSAGE_SERVICE_SIGNING_KEY"} {
		m := fullEnv()
		delete(m, key)
		if _, err := newReconJob(env(m), nil); err == nil {
			t.Fatalf("missing %s must fail", key)
		}
	}
	for key, bad := range map[string]string{
		"ANALYTICS_RECON_TENANT_ID": "nope", "ANALYTICS_RECON_TZ": "Mars/Base", "ANALYTICS_RECON_LEDGERS": "x=y",
		"ANALYTICS_RECON_REVENUE_ACCOUNTS": "4000",
	} {
		m := fullEnv()
		m[key] = bad
		if _, err := newReconJob(env(m), nil); err == nil {
			t.Fatalf("%s=%q must fail", key, bad)
		}
	}
}

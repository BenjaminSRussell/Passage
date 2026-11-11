package main

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

type fakeRetainer struct {
	entities  []string
	offloaded []tenant.Principal
	purged    []time.Duration
}

func (f *fakeRetainer) CaptureLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	if !p.HasDuty(tenant.DutyAllScope) || p.LegalEntityID != "" {
		return nil, context.Canceled
	}
	return f.entities, nil
}

func (f *fakeRetainer) OffloadCaptures(ctx context.Context, _ int) (int, error) {
	p, _ := tenant.FromContext(ctx)
	f.offloaded = append(f.offloaded, p)
	return 1, nil
}

func (f *fakeRetainer) PurgeCaptures(_ context.Context, _ time.Time, retention time.Duration, _ int) (store.CaptureRetentionResult, error) {
	f.purged = append(f.purged, retention)
	return store.CaptureRetentionResult{Purged: 1}, nil
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNewCaptureJob_Config(t *testing.T) {
	f := &fakeRetainer{}
	if j, err := newCaptureJob(envOf(nil), f); j != nil || err != nil {
		t.Fatalf("unset tenants disable the job: %v %v", j, err)
	}
	tenantID := "00000000-0000-0000-0000-000000000001"
	for _, bad := range []string{"-1", "abc", "9999"} {
		if _, err := newCaptureJob(envOf(map[string]string{"FIELDSERVICE_CAPTURE_JOB_TENANT_IDS": tenantID, "FIELDSERVICE_CAPTURE_RETENTION_DAYS": bad}), f); err == nil {
			t.Errorf("retention %q was accepted", bad)
		}
	}
	j, err := newCaptureJob(envOf(map[string]string{"FIELDSERVICE_CAPTURE_JOB_TENANT_IDS": tenantID, "FIELDSERVICE_CAPTURE_RETENTION_DAYS": "365"}), f)
	if err != nil || j.retention != 365*24*time.Hour || j.sched.interval != time.Hour {
		t.Fatalf("job %+v %v", j, err)
	}
	if j, err = newCaptureJob(envOf(map[string]string{"FIELDSERVICE_CAPTURE_JOB_TENANT_IDS": tenantID}), f); err != nil || j.retention != 0 {
		t.Fatalf("no retention means never purge: %+v %v", j, err)
	}
}

func TestCaptureJob_RunsEachLegalEntityUnderItsOwnScope(t *testing.T) {
	f := &fakeRetainer{entities: []string{"00000000-0000-0000-0000-0000000000a1", "00000000-0000-0000-0000-0000000000a2"}}
	j := &captureJob{tenants: []string{"00000000-0000-0000-0000-000000000001"}, retention: 48 * time.Hour, store: f, now: time.Now}
	j.once(context.Background())
	if len(f.offloaded) != 2 || len(f.purged) != 2 || f.purged[0] != 48*time.Hour {
		t.Fatalf("offloaded %d purged %v", len(f.offloaded), f.purged)
	}
	for i, p := range f.offloaded {
		if p.LegalEntityID != f.entities[i] || p.HasDuty(tenant.DutyAllScope) || p.UserID != captureJobActor {
			t.Fatalf("principal %d %+v", i, p)
		}
	}
}

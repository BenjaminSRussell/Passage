package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const overrideBody = `{"resourceId":"` + resID + `","start":"2026-01-05T18:00:00Z","overrideConstraints":true}`

func TestMove_OverrideNeedsTheOverrideDuty(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", overrideBody, allDuties, "o1")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "SCHED-0141") || f.lastMove.JobID != "" {
		t.Fatalf("status %d body %s, store reached %v", rec.Code, rec.Body, f.lastMove.JobID != "")
	}
	// A preview asking for the override is refused the same way.
	preview := strings.Replace(overrideBody, `"overrideConstraints"`, `"dryRun":true,"overrideConstraints"`, 1)
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", preview, allDuties, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("preview status %d", rec.Code)
	}
	// A move that does not ask for it is unaffected by the missing duty.
	plain := `{"resourceId":"` + resID + `","start":"2026-01-05T09:00:00Z"}`
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", plain, allDuties, "o2"); rec.Code != http.StatusOK {
		t.Fatalf("plain status %d", rec.Code)
	}
}

func TestMove_OverrideReportsWhatItCommittedOver(t *testing.T) {
	f := &fakeStore{overridden: []store.Violation{{Code: "SCHED-0002", Detail: "start is outside open capacity"}}}
	h := newRouter(f)
	rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", overrideBody, allDuties+",scheduling.constraints.override", "o3")
	if rec.Code != http.StatusOK || !f.lastMove.Override {
		t.Fatalf("status %d body %s, override reached store = %v", rec.Code, rec.Body, f.lastMove.Override)
	}
	var out moveResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Feasible || !out.Overridden || len(out.Violations) != 1 || out.Violations[0].Code != "SCHED-0002" {
		t.Fatalf("response = %+v", out)
	}
	// Idempotency still applies to a committed override.
	if rec := do(h, http.MethodPost, "/v1/scheduling/jobs/"+jobID+":move", overrideBody, allDuties+",scheduling.constraints.override", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key status %d", rec.Code)
	}
}

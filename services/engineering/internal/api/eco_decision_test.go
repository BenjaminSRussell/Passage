package api_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const workflowDuty = "engineering.eco.workflow"

func decide(h http.Handler, action, duties, user string) (int, string) {
	rec := do(h, http.MethodPost, ecos+"/"+ecoID+":"+action, "", duties, true, "X-Passage-User", user)
	return rec.Code, rec.Body.String()
}

func TestDecideEco_RecordsTheDecisionWithoutReleasing(t *testing.T) {
	tests := []struct {
		action string
		want   domain.EcoState
	}{{"approve", domain.EcoApproved}, {"reject", domain.EcoRejected}, {"cancel", domain.EcoCancelled}}
	for _, tc := range tests {
		t.Run(tc.action, func(t *testing.T) {
			f := &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}
			r, _ := newRouter(f, nil)
			code, body := decide(r, tc.action, workflowDuty, "carol")
			if code != http.StatusOK || !strings.Contains(body, `"state":"`+string(tc.want)+`"`) || !strings.Contains(body, `"decidedBy":"carol"`) {
				t.Fatalf("%s = %d %s", tc.action, code, body)
			}
			if len(f.released) != 1 || f.released[0].prev != domain.EcoSubmitted || f.released[0].eco.State != tc.want || len(f.released[0].plan.Activate) != 0 || len(f.released[0].plan.Expire) != 0 {
				t.Fatalf("store writes = %+v", f.released)
			}
		})
	}
}

func TestDecideEco_RepeatIsIdempotent(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoApproved, time.Time{})}
	r, _ := newRouter(f, nil)
	if code, body := decide(r, "approve", workflowDuty, "carol"); code != http.StatusOK || !strings.Contains(body, `"state":"approved"`) || len(f.released) != 0 {
		t.Fatalf("repeat approve = %d %s %+v", code, body, f.released)
	}
}

func TestDecideEco_Refusals(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		action     string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"released eco cannot be rejected", &fakeStore{record: releasableRecord(domain.EcoReleased, time.Time{})}, "reject", workflowDuty, http.StatusConflict, domain.CodeEcoState},
		{"approved eco cannot be cancelled by the workflow", &fakeStore{record: releasableRecord(domain.EcoApproved, time.Time{})}, "cancel", workflowDuty, http.StatusConflict, domain.CodeEcoState},
		{"read duty only", &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{})}, "approve", "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"unknown eco", &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeEcoNotFound, "missing")}, "approve", workflowDuty, http.StatusNotFound, store.CodeEcoNotFound},
		{"concurrent change", &fakeStore{record: releasableRecord(domain.EcoSubmitted, time.Time{}), relErr: httpx.NewProblem(http.StatusConflict, domain.CodeEcoState, "changed concurrently")}, "approve", workflowDuty, http.StatusConflict, domain.CodeEcoState},
		{"store unavailable", &fakeStore{err: errors.New("boom")}, "approve", workflowDuty, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store, nil)
			code, body := decide(r, tc.action, tc.duties, "carol")
			if code != tc.wantStatus || !strings.Contains(body, tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", code, body, tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestDecideEco_RejectsBadId(t *testing.T) {
	r, _ := newRouter(&fakeStore{}, nil)
	if rec := do(r, http.MethodPost, ecos+"/not-a-uuid:approve", "", workflowDuty, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-UUID id = %d %s", rec.Code, rec.Body)
	}
}

func TestDecideEco_ThenReleaseNeedsNoApprover(t *testing.T) {
	f := &fakeStore{record: releasableRecord(domain.EcoApproved, time.Time{})}
	r, _ := newRouter(f, nil)
	if code, body := decide(r, "release", workflowDuty, "carol"); code != http.StatusOK || !strings.Contains(body, `"state":"released"`) || len(f.released) != 1 || len(f.released[0].plan.Activate) != 1 {
		t.Fatalf("release of an approved eco = %d %s %+v", code, body, f.released)
	}
}

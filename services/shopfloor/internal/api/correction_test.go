package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

const correctDuty = "shopfloor.time.correct,shopfloor.pay.revise,shopfloor.view"

func TestCorrectRegistration_DutyValidationAndSuccess(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/registrations/" + regID + ":correct"
	body := `{"startedAt":"2026-10-20T06:30:00Z","reason":"  forgot to clock in  "}`
	if rec := call(t, h, "POST", path, body, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	if rec := call(t, h, "POST", path, `{"startedAt":"2026-10-20T06:30:00Z"}`, correctDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, "POST", path, `{"reason":"x"}`, correctDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("no times: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/v1/shopfloor/registrations/not-a-uuid:correct", body, correctDuty); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := callWith(t, h, "POST", path, body, correctDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
	rec := call(t, h, "POST", path, body, correctDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct: %d %s", rec.Code, rec.Body)
	}
	if len(f.corrected) != 1 || f.corrected[0].Reason != "forgot to clock in" {
		t.Fatalf("store got %+v", f.corrected)
	}
}

func TestCorrectRegistration_RuleErrorKeepsItsCode(t *testing.T) {
	f := &fakeStore{correctErr: httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeRegistrationNotCorrectable, "bundled")}
	rec := call(t, newRouter(f), "POST", "/v1/shopfloor/registrations/"+regID+":correct", `{"startedAt":"2026-10-20T06:30:00Z","reason":"x"}`, correctDuty)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "SF-0127") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestRevisePay_RecalculatesAndPassesReason(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"workerId":"` + workerID + `","from":"2026-10-20","to":"2026-10-20","reason":"missed punch"}`
	if rec := call(t, h, "POST", "/v1/shopfloor/pay:revise", body, "shopfloor.pay.approve"); rec.Code != http.StatusForbidden {
		t.Fatalf("approve duty must not revise: %d", rec.Code)
	}
	if rec := call(t, h, "POST", "/v1/shopfloor/pay:revise", `{"workerId":"`+workerID+`","from":"2026-10-20","to":"2026-10-20"}`, correctDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d", rec.Code)
	}
	rec := call(t, h, "POST", "/v1/shopfloor/pay:revise", body, correctDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("revise: %d %s", rec.Code, rec.Body)
	}
	if f.revised == nil || f.revised.WorkerID != workerID || f.revisedWhy != "missed punch" {
		t.Fatalf("store got %+v %q", f.revised, f.revisedWhy)
	}
	if !strings.Contains(rec.Body.String(), `"revision":1`) {
		t.Fatalf("body %s", rec.Body)
	}
}

func TestListCorrections_NeedsRange(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, "GET", "/v1/shopfloor/registration-corrections", "", "shopfloor.view"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no range: %d", rec.Code)
	}
	if rec := call(t, h, "GET", "/v1/shopfloor/registration-corrections?from=2026-10-01&to=2026-10-31", "", "shopfloor.view"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
}

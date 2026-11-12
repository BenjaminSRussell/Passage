package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/phiaudit"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

func healthBenefits(workerID string) store.WorkerBenefits {
	return store.WorkerBenefits{
		WorkerID:    workerID,
		Enrollments: []store.BenefitEnrollment{{ID: "e1", WorkerID: workerID, PlanCode: "MED-GOLD", PlanType: "MEDICAL", OptionCode: "FAMILY", CoverageLevel: "Family", DependentCount: 2}},
		LifeEvents:  []store.LifeEvent{{ID: "l1", WorkerID: workerID, Kind: "BIRTH", EventDate: "2026-10-01"}},
	}
}

// serveBenefits runs GET /v1/hr/workers/{id}/benefits with the read stubbed, so no database is needed.
func serveBenefits(t *testing.T, a *API, workerID, query string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	p, _ := tenant.FromContext(testkit.Ctx(tid(), duties...))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	a.Register(r)
	req := httptest.NewRequest(http.MethodGet, "/v1/hr/workers/"+workerID+"/benefits"+query, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func apiWith(reader store.WorkerBenefits, rec phiaudit.Recorder) *API {
	a := New(nil)
	a.listWorkerBenefits = func(context.Context, string, bool) (store.WorkerBenefits, error) { return reader, nil }
	if rec != nil {
		a.WithPHIReadAudit(rec)
	}
	return a
}

func TestBenefitsReadWritesExactlyOneAuditEntryWithWhoWhatWhenWhy(t *testing.T) {
	worker := testkit.TenantID(9)
	fake := &phiaudit.Fake{}
	rec := serveBenefits(t, apiWith(healthBenefits(worker), fake), worker, "?purpose=open+enrollment+review", dutyEssBenefits, dutyWorkerInquire)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "MED-GOLD") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	got := fake.Reads()
	if len(got) != 1 {
		t.Fatalf("reads recorded = %d, want exactly 1: %+v", len(got), got)
	}
	read := got[0]
	if read.UserID == "" || read.TenantID != tid() {
		t.Errorf("who: %+v", read)
	}
	if read.Entity != "hr.worker.benefits" || read.RecordID != worker {
		t.Errorf("what record: %+v", read)
	}
	if read.Purpose != "open enrollment review" {
		t.Errorf("purpose: %q", read.Purpose)
	}
	for _, f := range []string{"enrollments.optionCode", "enrollments.coverageLevel", "lifeEvents.kind"} {
		found := false
		for _, g := range read.Fields {
			found = found || g == f
		}
		if !found {
			t.Errorf("phi field %s not recorded: %v", f, read.Fields)
		}
	}
	if read.OccurredAt.IsZero() || time.Since(read.OccurredAt) > time.Minute {
		t.Errorf("when: %v", read.OccurredAt)
	}
}

func TestBenefitsReadWithoutPHIFieldsWritesNoEntry(t *testing.T) {
	worker := testkit.TenantID(9)
	for name, benefits := range map[string]store.WorkerBenefits{
		"nothing on file":      {WorkerID: worker, Enrollments: []store.BenefitEnrollment{}, LifeEvents: []store.LifeEvent{}},
		"retirement plan only": {WorkerID: worker, Enrollments: []store.BenefitEnrollment{{PlanCode: "401K", PlanType: "RETIREMENT_401K", OptionCode: "6PCT"}}, LifeEvents: []store.LifeEvent{}},
	} {
		fake := &phiaudit.Fake{}
		rec := serveBenefits(t, apiWith(benefits, fake), worker, "", dutyEssBenefits, dutyWorkerInquire)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", name, rec.Code, rec.Body.String())
		}
		if n := len(fake.Reads()); n != 0 {
			t.Errorf("%s: %d entries written for a response with no phi fields", name, n)
		}
	}
}

func TestBenefitsReadWithAuditOffWritesNothingAndNeedsNoPurpose(t *testing.T) {
	worker := testkit.TenantID(9)
	rec := serveBenefits(t, apiWith(healthBenefits(worker), nil), worker, "", dutyEssBenefits, dutyWorkerInquire)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBenefitsReadNeedsAPurposeFromSomeoneElseAndRecordsNothingWithout(t *testing.T) {
	worker := testkit.TenantID(9)
	fake := &phiaudit.Fake{}
	a := apiWith(healthBenefits(worker), fake)
	for _, q := range []string{"", "?purpose=", "?purpose=%20%20", "?purpose=" + strings.Repeat("x", 501)} {
		rec := serveBenefits(t, a, worker, q, dutyEssBenefits, dutyWorkerInquire)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"purpose"`) {
			t.Errorf("%q: status %d, want 400 naming purpose: %s", q, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "MED-GOLD") {
			t.Errorf("%q: the data must not be returned without a purpose", q)
		}
	}
	if n := len(fake.Reads()); n != 0 {
		t.Errorf("refused reads wrote %d entries", n)
	}
}

func TestBenefitsSelfReadDefaultsThePurpose(t *testing.T) {
	worker := testkit.TenantID(9)
	fake := &phiaudit.Fake{}
	// An employee with only hr.benefits.self reads their own benefits.
	rec := serveBenefits(t, apiWith(healthBenefits(worker), fake), worker, "", dutyEssBenefits)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := fake.Reads(); len(got) != 1 || got[0].Purpose != "self-service" {
		t.Fatalf("reads %+v", got)
	}
}

func TestBenefitsReadFailsClosedWhenTheAuditCannotBeWritten(t *testing.T) {
	worker := testkit.TenantID(9)
	a := apiWith(healthBenefits(worker), &phiaudit.Fake{Err: errors.New("audit down")})
	rec := serveBenefits(t, a, worker, "?purpose=review", dutyEssBenefits, dutyWorkerInquire)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "HCM-PHI-AUDIT-UNAVAILABLE") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "MED-GOLD") || strings.Contains(rec.Body.String(), "audit down") {
		t.Fatalf("the data and the audit error must stay out of the response: %s", rec.Body.String())
	}
}

// TestBreakGlassReadsAnyoneOnlyWhenAuditedAndSaysSo: a caller without hr.worker.inquire who holds the break-glass duty
// reads another worker's benefits only with the BREAK-GLASS reason code, the read is recorded with that purpose (the
// audit service queues it for review), and with read auditing off the access is refused rather than left unlogged.
func TestBreakGlassReadsAnyoneOnlyWhenAuditedAndSaysSo(t *testing.T) {
	worker := testkit.TenantID(9)
	var ownOnlySeen []bool
	withSpy := func(rec phiaudit.Recorder) *API {
		a := apiWith(healthBenefits(worker), rec)
		a.listWorkerBenefits = func(_ context.Context, _ string, ownOnly bool) (store.WorkerBenefits, error) {
			ownOnlySeen = append(ownOnlySeen, ownOnly)
			return healthBenefits(worker), nil
		}
		return a
	}

	fake := &phiaudit.Fake{}
	rec := serveBenefits(t, withSpy(fake), worker, "?purpose=BREAK-GLASS%3A+member+in+ER", dutyBenefitsBreakGlass)
	if rec.Code != http.StatusOK || ownOnlySeen[len(ownOnlySeen)-1] {
		t.Fatalf("break-glass read: %d ownOnly=%v %s", rec.Code, ownOnlySeen, rec.Body.String())
	}
	if reads := fake.Reads(); len(reads) != 1 || reads[0].Purpose != "BREAK-GLASS: member in ER" {
		t.Fatalf("recorded %+v", reads)
	}

	// Without the reason code the duty grants nothing beyond the caller's own record.
	serveBenefits(t, withSpy(&phiaudit.Fake{}), worker, "?purpose=curious", dutyBenefitsBreakGlass)
	if !ownOnlySeen[len(ownOnlySeen)-1] {
		t.Fatal("a read without BREAK-GLASS must stay own-only")
	}
	// The reason code without the duty grants nothing either.
	serveBenefits(t, withSpy(&phiaudit.Fake{}), worker, "?purpose=BREAK-GLASS", dutyEssBenefits)
	if !ownOnlySeen[len(ownOnlySeen)-1] {
		t.Fatal("BREAK-GLASS without the duty must stay own-only")
	}

	calls := len(ownOnlySeen)
	rec = serveBenefits(t, withSpy(nil), worker, "?purpose=BREAK-GLASS", dutyBenefitsBreakGlass)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), codeBreakGlassUnaudited) || len(ownOnlySeen) != calls {
		t.Fatalf("break-glass with auditing off must be refused before the read: %d %s", rec.Code, rec.Body.String())
	}

	failing := &phiaudit.Fake{Err: errors.New("audit down")}
	rec = serveBenefits(t, withSpy(failing), worker, "?purpose=BREAK-GLASS", dutyBenefitsBreakGlass)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "MED-GOLD") {
		t.Fatalf("an unrecorded break-glass read must not return data: %d %s", rec.Code, rec.Body.String())
	}
}

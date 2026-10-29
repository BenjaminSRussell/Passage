package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

const (
	allDuties = "MigrationRun,MigrationView,CutoverManager,CutoverStepOwner"
	tenantA   = "00000000-0000-7000-8000-00000000000a"
	tenantB   = "00000000-0000-7000-8000-00000000000b"
	runsPath  = "/api/v1/migration/packs/open_items/runs"
)

// unreachableStore fails the test if a handler reaches the store: the requests sent to it must
// be rejected by routing, duties or validation first.
type unreachableStore struct{ t *testing.T }

func (u unreachableStore) fail() { u.t.Helper(); u.t.Fatal("request reached the store") }
func (u unreachableStore) Ping(context.Context) error {
	u.fail()
	return nil
}
func (u unreachableStore) RunOpenItems(context.Context, store.Idem, store.OpenItemsRun) (domain.PackRun, bool, error) {
	u.fail()
	return domain.PackRun{}, false, nil
}
func (u unreachableStore) GetRun(context.Context, string) (domain.PackRun, error) {
	u.fail()
	return domain.PackRun{}, nil
}
func (u unreachableStore) ListRuns(context.Context, store.RunFilter) ([]domain.PackRun, error) {
	u.fail()
	return nil, nil
}
func (u unreachableStore) CreatePlan(context.Context, store.Idem, domain.Plan, string) (domain.Plan, bool, error) {
	u.fail()
	return domain.Plan{}, false, nil
}
func (u unreachableStore) GetPlan(context.Context, string) (domain.Plan, error) {
	u.fail()
	return domain.Plan{}, nil
}
func (u unreachableStore) ListPlans(context.Context, store.PlanFilter) ([]domain.Plan, error) {
	u.fail()
	return nil, nil
}
func (u unreachableStore) CompleteStep(context.Context, store.Idem, store.StepCompletion) (domain.Plan, bool, error) {
	u.fail()
	return domain.Plan{}, false, nil
}
func (u unreachableStore) Rollback(context.Context, store.Idem, store.RollbackRequest) (domain.Plan, bool, error) {
	u.fail()
	return domain.Plan{}, false, nil
}

type request struct {
	method, path, tenant, duties, body, key, user string
}

func do(t *testing.T, h http.Handler, rq request) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(rq.method, rq.path, bytes.NewBufferString(rq.body))
	req.Header.Set("Content-Type", "application/json")
	if rq.tenant != "" {
		req.Header.Set("X-Passage-Tenant", rq.tenant)
	}
	if rq.duties != "" {
		req.Header.Set("X-Passage-Duties", rq.duties)
	}
	if rq.key != "" {
		req.Header.Set("Idempotency-Key", rq.key)
	}
	if rq.user != "" {
		req.Header.Set("X-Passage-User", rq.user)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// openItemsBody builds an open_items extract of n clean USD records plus extra records.
func openItemsBody(n int, extra ...string) string {
	var recs []string
	for i := 0; i < n; i++ {
		recs = append(recs, fmt.Sprintf(`{"legacyId":"OI-%05d","party":"V001","account":"2100","currency":"USD","amount":"10.25"}`, i))
	}
	recs = append(recs, extra...)
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","parties":["V001"],"records":[%s]}`, strings.Join(recs, ","))
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("problem body %q: %v", rec.Body, err)
	}
	return p.Code
}

func TestRequestsRejectedBeforeTheStore(t *testing.T) {
	h := New(unreachableStore{t}).Routes()
	late := strings.Replace(openItemsBody(1), "2026-12-31T00:00:00Z", "2027-01-02T00:00:00Z", 1)
	tooMany := `{"legacyId":"B1","party":"V999","account":"2100","currency":"USD","amount":"1"}`
	planPath := "/api/v1/cutover/plans/00000000-0000-7000-8000-000000000001"
	cases := []struct {
		name   string
		rq     request
		status int
		code   string
		detail string
	}{
		{"pack run needs MigrationRun", request{method: "POST", path: runsPath, tenant: tenantA, duties: "MigrationView", body: openItemsBody(1)}, 403, "AUTH-0003", ""},
		{"no tenant", request{method: "POST", path: runsPath, duties: allDuties, body: openItemsBody(1)}, 401, "", ""},
		{"late cutoff MIG-1501", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: late}, 422, CodeInvalid, "MIG-1501"},
		{"pack not in slice 1", request{method: "POST", path: "/api/v1/migration/packs/gl_balances/runs", tenant: tenantA, duties: allDuties, body: openItemsBody(1)}, 422, CodeUnsupported, ""},
		{"unknown pack", request{method: "POST", path: "/api/v1/migration/packs/nonsense/runs", tenant: tenantA, duties: allDuties, body: openItemsBody(1)}, 422, CodeUnsupported, ""},
		{"unknown source system", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), "AX2012", "AX4", 1)}, 422, CodeInvalid, ""},
		{"blank company", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), `"USMF"`, `" "`, 1)}, 422, CodeInvalid, "sourceCompany"},
		{"missing freeze", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), `,"freezeTs":"2026-12-31T06:00:00Z"`, "", 1)}, 422, CodeInvalid, "freezeTs"},
		{"empty extract", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(0)}, 422, CodeInvalid, "at least one"},
		{"bad amount", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(0, `{"legacyId":"X","party":"V001","account":"2100","currency":"USD","amount":"1,5"}`)}, 422, CodeInvalid, "not a decimal"},
		{"quarantine pct above 10", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), `"parties"`, `"quarantineMaxPct":"11","parties"`, 1)}, 422, CodeInvalid, "0 to 10"},
		{"quarantine share MIG-1504", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(10, tooMany)}, 422, CodeInvalid, "MIG-1504"},
		{"unknown field", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: `{"surprise":1}`}, 400, "COM-0001", ""},
		{"run id not a UUID", request{method: "GET", path: "/api/v1/migration/runs/nope", tenant: tenantA, duties: allDuties}, 422, CodeInvalid, "UUID"},
		{"list runs bad status", request{method: "GET", path: "/api/v1/migration/runs?status=DONE", tenant: tenantA, duties: allDuties}, 422, CodeInvalid, ""},
		{"list runs bad limit", request{method: "GET", path: "/api/v1/migration/runs?limit=500", tenant: tenantA, duties: allDuties}, 422, CodeInvalid, ""},
		{"list runs needs MigrationView", request{method: "GET", path: "/api/v1/migration/runs", tenant: tenantA, duties: "MigrationRun"}, 403, "AUTH-0003", ""},
		{"generate needs CutoverManager", request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "MigrationRun", body: `{}`}, 403, "AUTH-0003", ""},
		{"generate without t0", request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: allDuties, body: `{"packs":["open_items"],"entities":["USMF"]}`}, 422, CodeInvalid, "t0"},
		{"generate duplicate pack", request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: allDuties, body: `{"t0":"2027-01-01T00:00:00Z","packs":["open_items","open_items"],"entities":["USMF"]}`}, 422, CodeInvalid, "twice"},
		{"generate abort offset out of range", request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: allDuties, body: `{"t0":"2027-01-01T00:00:00Z","packs":["open_items"],"entities":["USMF"],"abortOffsetHours":80}`}, 422, CodeInvalid, "12-72"},
		{"complete needs CutoverStepOwner", request{method: "POST", path: planPath + "/steps/1/complete", tenant: tenantA, duties: "CutoverManager", body: `{}`}, 403, "AUTH-0003", ""},
		{"complete bad seq", request{method: "POST", path: planPath + "/steps/0/complete", tenant: tenantA, duties: allDuties, body: `{}`}, 422, CodeInvalid, "positive"},
		{"rollback needs CutoverManager", request{method: "POST", path: planPath + "/rollback", tenant: tenantA, duties: "CutoverStepOwner", body: `{}`}, 403, "AUTH-0003", ""},
		{"long idempotency key", request{method: "POST", path: planPath + "/rollback", tenant: tenantA, duties: allDuties, body: `{}`, key: strings.Repeat("k", 201)}, 422, CodeInvalid, "Idempotency-Key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, h, c.rq)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if c.code != "" {
				if got := problemCode(t, rec); got != c.code {
					t.Fatalf("code %q, want %q: %s", got, c.code, rec.Body)
				}
			}
			if c.detail != "" && !strings.Contains(rec.Body.String(), c.detail) {
				t.Fatalf("body %s does not mention %q", rec.Body, c.detail)
			}
		})
	}
}

func TestPlanDTO_ReportsAbortWarningAndDeadline(t *testing.T) {
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan, err := domain.GeneratePlan(domain.PlanInput{PlanID: "p", T0: t0, Packs: []domain.Pack{domain.PackOpenItems}, Entities: []string{"USMF"}})
	if err != nil {
		t.Fatal(err)
	}
	at := func(h int) planDTO {
		s := New(unreachableStore{t}).WithClock(func() time.Time { return t0.Add(time.Duration(h) * time.Hour) })
		return s.toPlanDTO(plan, true)
	}
	if d := at(10); d.AbortDue || d.AbortWarning != "" {
		t.Fatalf("T+10h: no warning expected, got %+v", d)
	}
	if d := at(35); d.AbortDue || !strings.Contains(d.AbortWarning, "CUT-1604") {
		t.Fatalf("T+35h: CUT-1604 warning expected, got due=%v warn=%q", d.AbortDue, d.AbortWarning)
	}
	if d := at(37); !d.AbortDue {
		t.Fatal("T+37h with open go/no-go items: abort must be due")
	}
	if d := at(37); len(d.OpenGoNoGo) != 2 || d.StepsTotal != len(plan.Steps) || d.StepsDone != 0 {
		t.Fatalf("summary counts %+v", d)
	}
	if err := plan.Rollback("alice", "bob"); err != nil {
		t.Fatal(err)
	}
	if d := at(37); d.AbortDue {
		t.Fatal("a rolled-back plan has no abort deadline left")
	}
}

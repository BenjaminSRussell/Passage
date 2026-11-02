package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	allDuties = "MigrationRun,MigrationView,MigrationReview,CutoverManager,CutoverStepOwner"
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
func (u unreachableStore) GetReadiness(context.Context, string) (domain.ReadinessFacts, error) {
	u.fail()
	return domain.ReadinessFacts{}, nil
}
func (u unreachableStore) RecordDecision(context.Context, store.Idem, store.DecisionRequest) (store.DecisionResult, bool, error) {
	u.fail()
	return store.DecisionResult{}, false, nil
}
func (u unreachableStore) StartStep(context.Context, store.Idem, store.StepStart) (domain.Plan, bool, error) {
	u.fail()
	return domain.Plan{}, false, nil
}
func (u unreachableStore) CompleteStep(context.Context, store.Idem, store.StepCompletion) (domain.Plan, bool, error) {
	u.fail()
	return domain.Plan{}, false, nil
}
func (u unreachableStore) Rollback(context.Context, store.Idem, store.RollbackRequest) (store.RollbackResult, bool, error) {
	u.fail()
	return store.RollbackResult{}, false, nil
}

func (u unreachableStore) RekeyLines(context.Context, string, domain.PostingKind) ([]domain.RekeyPosting, error) {
	u.fail()
	return nil, nil
}

func (u unreachableStore) ReleaseQuarantined(context.Context, store.Idem, store.Release) (domain.PackRun, bool, error) {
	u.fail()
	return domain.PackRun{}, false, nil
}
func (u unreachableStore) LookupXref(context.Context, store.XrefQuery) ([]domain.Xref, error) {
	u.fail()
	return nil, nil
}
func (u unreachableStore) StartParallelRun(context.Context, store.Idem, domain.ParallelRun) (domain.ParallelRun, bool, error) {
	u.fail()
	return domain.ParallelRun{}, false, nil
}
func (u unreachableStore) GetParallelRun(context.Context, string) (domain.ParallelRun, error) {
	u.fail()
	return domain.ParallelRun{}, nil
}
func (u unreachableStore) ListParallelRuns(context.Context, store.ParallelFilter) ([]domain.ParallelRun, error) {
	u.fail()
	return nil, nil
}
func (u unreachableStore) ExplainVariance(context.Context, store.Idem, store.ExplainVariance) (domain.ParallelRun, bool, error) {
	u.fail()
	return domain.ParallelRun{}, false, nil
}
func (u unreachableStore) SignOffParallelRun(context.Context, store.Idem, store.SignOff) (domain.ParallelRun, bool, error) {
	u.fail()
	return domain.ParallelRun{}, false, nil
}
func (u unreachableStore) RunProjectWIP(context.Context, store.Idem, store.ProjectWIPRun) (domain.PackRun, bool, error) {
	u.fail()
	return domain.PackRun{}, false, nil
}
func (u unreachableStore) RunGLBalances(context.Context, store.Idem, store.GLBalancesRun) (domain.PackRun, bool, error) {
	u.fail()
	return domain.PackRun{}, false, nil
}
func (u unreachableStore) GetProjectOpening(context.Context, store.OpeningFilter) (store.ProjectOpening, error) {
	u.fail()
	return store.ProjectOpening{}, nil
}

type request struct {
	method, path, tenant, duties, body, key, user, le string
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
	if rq.le != "" {
		req.Header.Set("X-Passage-Legal-Entity", rq.le)
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
	runPath := "/api/v1/migration/runs/00000000-0000-7000-8000-000000000002"
	parallelPath := "/api/v1/migration/parallel-runs"
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
		{"pack not built yet", request{method: "POST", path: "/api/v1/migration/packs/onhand/runs", tenant: tenantA, duties: allDuties, body: openItemsBody(1)}, 422, CodeUnsupported, ""},
		{"unknown pack", request{method: "POST", path: "/api/v1/migration/packs/nonsense/runs", tenant: tenantA, duties: allDuties, body: openItemsBody(1)}, 422, CodeUnsupported, ""},
		{"unknown source system", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), "AX2012", "AX4", 1)}, 422, CodeInvalid, ""},
		{"blank company", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), `"USMF"`, `" "`, 1)}, 422, CodeInvalid, "sourceCompany"},
		{"missing freeze", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: strings.Replace(openItemsBody(1), `,"freezeTs":"2026-12-31T06:00:00Z"`, "", 1)}, 422, CodeInvalid, "freezeTs"},
		{"empty extract", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(0)}, 422, CodeInvalid, "at least one"},
		{"bad amount", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(0, `{"legacyId":"X","party":"V001","account":"2100","currency":"USD","amount":"1,5"}`)}, 422, CodeInvalid, "not a decimal"},
		{"amount in exponent notation", request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, body: openItemsBody(0, `{"legacyId":"X","party":"V001","account":"2100","currency":"USD","amount":"9e9999999"}`)}, 422, CodeInvalid, "not a decimal"},
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
		// Slice 2: quarantine release, xref, parallel runs, project beginning balances.
		{"release needs MigrationRun", request{method: "POST", path: runPath + "/quarantine/1/release", tenant: tenantA, duties: "MigrationView", body: `{}`}, 403, "AUTH-0003", ""},
		{"release bad record seq", request{method: "POST", path: runPath + "/quarantine/x/release", tenant: tenantA, duties: allDuties, body: `{}`}, 422, CodeInvalid, "rec"},
		{"release negative version", request{method: "POST", path: runPath + "/quarantine/1/release", tenant: tenantA, duties: allDuties, body: `{"version":-1}`}, 422, CodeInvalid, "version"},
		{"release unknown field", request{method: "POST", path: runPath + "/quarantine/1/release", tenant: tenantA, duties: allDuties, body: `{"amount":"1"}`}, 400, "COM-0001", ""},
		{"xref needs a tenant", request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&table=open_items&id=1"}, 401, "", ""},
		{"xref unknown system", request{method: "GET", path: "/api/v1/migration/xref?system=AX4&table=open_items&id=1", tenant: tenantA}, 422, CodeInvalid, ""},
		{"xref without id", request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&table=open_items", tenant: tenantA}, 422, CodeInvalid, "required"},
		{"xref both directions", request{method: "GET", path: "/api/v1/migration/xref?system=AX2012&passageEntity=open_item&passageId=" + tenantB, tenant: tenantA}, 422, CodeInvalid, "either"},
		{"xref passage id not a UUID", request{method: "GET", path: "/api/v1/migration/xref?passageEntity=open_item&passageId=7", tenant: tenantA}, 422, CodeInvalid, "UUID"},
		{"parallel start needs MigrationRun", request{method: "POST", path: parallelPath, tenant: tenantA, duties: "MigrationReview", body: `{}`}, 403, "AUTH-0003", ""},
		{"parallel bad period", request{method: "POST", path: parallelPath, tenant: tenantA, duties: allDuties, body: `{"period":"2026-13","sourceSystem":"AX2012","revenue":"1","legacy":[{"area":"TB","key":"k","amount":"1"}]}`}, 422, CodeInvalid, "YYYY-MM"},
		{"parallel bad area", request{method: "POST", path: parallelPath, tenant: tenantA, duties: allDuties, body: `{"period":"2026-11","sourceSystem":"AX2012","revenue":"1","legacy":[{"area":"GL","key":"k","amount":"1"}]}`}, 422, CodeInvalid, "area"},
		{"parallel bad amount", request{method: "POST", path: parallelPath, tenant: tenantA, duties: allDuties, body: `{"period":"2026-11","sourceSystem":"AX2012","revenue":"1","passage":[{"area":"TB","key":"k","amount":"1e"}]}`}, 422, CodeInvalid, "decimal"},
		{"parallel bad revenue", request{method: "POST", path: parallelPath, tenant: tenantA, duties: allDuties, body: `{"period":"2026-11","sourceSystem":"AX2012","revenue":"","legacy":[{"area":"TB","key":"k","amount":"1"}]}`}, 422, CodeInvalid, "revenue"},
		{"parallel explained pct above 1", request{method: "POST", path: parallelPath, tenant: tenantA, duties: allDuties, body: `{"period":"2026-11","sourceSystem":"AX2012","revenue":"1","explainedMaxPct":"2","legacy":[{"area":"TB","key":"k","amount":"1"}]}`}, 422, CodeInvalid, "explainedMaxPct"},
		{"parallel list bad state", request{method: "GET", path: parallelPath + "?state=DONE", tenant: tenantA, duties: allDuties}, 422, CodeInvalid, "state"},
		{"parallel list needs MigrationView", request{method: "GET", path: parallelPath, tenant: tenantA, duties: "MigrationReview"}, 403, "AUTH-0003", ""},
		{"explain needs MigrationReview", request{method: "POST", path: parallelPath + "/" + tenantB + "/variances/" + tenantB + "/explain", tenant: tenantA, duties: "MigrationRun,MigrationView", body: `{"reason":"x"}`}, 403, "AUTH-0003", ""},
		{"explain variance id not a UUID", request{method: "POST", path: parallelPath + "/" + tenantB + "/variances/v1/explain", tenant: tenantA, duties: allDuties, body: `{"reason":"x"}`}, 422, CodeInvalid, "UUID"},
		{"sign-off run id not a UUID", request{method: "POST", path: parallelPath + "/p1/sign-off", tenant: tenantA, duties: allDuties, body: `{}`}, 422, CodeInvalid, "UUID"},
		{"project run without lines", request{method: "POST", path: "/api/v1/migration/packs/project_wip/runs", tenant: tenantA, duties: allDuties, body: `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","projects":[{"projectId":"P1","legacyTotals":[],"lines":[]}]}`}, 422, CodeInvalid, "at least one"},
		{"project run duplicate project", request{method: "POST", path: "/api/v1/migration/packs/project_wip/runs", tenant: tenantA, duties: allDuties, body: `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","projects":[{"projectId":"P1","legacyTotals":[],"lines":[]},{"projectId":"P1","legacyTotals":[],"lines":[]}]}`}, 422, CodeInvalid, "twice"},
		{"project run bad category", request{method: "POST", path: "/api/v1/migration/packs/project_wip/runs", tenant: tenantA, duties: allDuties, body: `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","projects":[{"projectId":"P1","legacyTotals":[],"lines":[{"legacyId":"L1","category":"TRAVEL","transactionDate":"2026-12-01","costAmount":"1","salesAmount":"1","currency":"USD","billingStatus":"BILLED"}]}]}`}, 422, CodeInvalid, "category"},
		{"project run bad date", request{method: "POST", path: "/api/v1/migration/packs/project_wip/runs", tenant: tenantA, duties: allDuties, body: `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","projects":[{"projectId":"P1","legacyTotals":[],"lines":[{"legacyId":"L1","category":"HOUR","transactionDate":"01.12.2026","costAmount":"1","salesAmount":"1","currency":"USD","billingStatus":"BILLED"}]}]}`}, 422, CodeInvalid, "YYYY-MM-DD"},
		{"project run late cutoff", request{method: "POST", path: "/api/v1/migration/packs/project_wip/runs", tenant: tenantA, duties: allDuties, body: `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2027-01-02T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","projects":[]}`}, 422, CodeInvalid, "MIG-1501"},
		{"opening needs MigrationView", request{method: "GET", path: runPath + "/projects/P1/opening", tenant: tenantA, duties: "MigrationRun"}, 403, "AUTH-0003", ""},
		// Slice 3: the rollback re-keying export.
		{"rollback posting bad kind", request{method: "POST", path: planPath + "/rollback", tenant: tenantA, duties: allDuties, body: `{"postings":[{"kind":"TRANSFER","entity":"USMF","documentRef":"JV-1","postedAt":"2027-01-02T00:00:00Z","account":"1100","currency":"USD","amount":"1"}]}`}, 422, CodeInvalid, "JOURNAL or DOCUMENT"},
		{"rollback posting bad amount", request{method: "POST", path: planPath + "/rollback", tenant: tenantA, duties: allDuties, body: `{"postings":[{"kind":"JOURNAL","entity":"USMF","documentRef":"JV-1","postedAt":"2027-01-02T00:00:00Z","account":"1100","currency":"USD","amount":"ten"}]}`}, 422, CodeInvalid, "not a decimal"},
		{"rollback posting unknown field", request{method: "POST", path: planPath + "/rollback", tenant: tenantA, duties: allDuties, body: `{"postings":[{"kind":"JOURNAL","debit":"1"}]}`}, 400, "COM-0001", ""},
		{"rekeying file needs CutoverManager", request{method: "GET", path: planPath + "/rekeying/journals.csv", tenant: tenantA, duties: "MigrationView"}, 403, "AUTH-0003", ""},
		{"rekeying unknown file", request{method: "GET", path: planPath + "/rekeying/ledger.csv", tenant: tenantA, duties: allDuties}, 422, CodeInvalid, "journals.csv or documents.csv"},
		{"rekeying plan id not a UUID", request{method: "GET", path: "/api/v1/cutover/plans/p1/rekeying/journals.csv", tenant: tenantA, duties: allDuties}, 422, CodeInvalid, "UUID"},
		// Slice 4: step start, the pack catalog.
		{"start needs CutoverStepOwner", request{method: "POST", path: planPath + "/steps/1/start", tenant: tenantA, duties: "CutoverManager", body: `{}`}, 403, "AUTH-0003", ""},
		{"start bad seq", request{method: "POST", path: planPath + "/steps/x/start", tenant: tenantA, duties: allDuties, body: `{}`}, 422, CodeInvalid, "positive"},
		{"start plan id not a UUID", request{method: "POST", path: "/api/v1/cutover/plans/p1/steps/1/start", tenant: tenantA, duties: allDuties, body: `{}`}, 422, CodeInvalid, "UUID"},
		{"start negative version", request{method: "POST", path: planPath + "/steps/1/start", tenant: tenantA, duties: allDuties, body: `{"version":-1}`}, 422, CodeInvalid, "version"},
		{"start unknown field", request{method: "POST", path: planPath + "/steps/1/start", tenant: tenantA, duties: allDuties, body: `{"startedAt":"2027-01-01T00:00:00Z"}`}, 400, "COM-0001", ""},
		{"catalog needs MigrationView", request{method: "GET", path: "/api/v1/migration/packs", tenant: tenantA, duties: "MigrationRun,CutoverManager"}, 403, "AUTH-0003", ""},
		{"catalog needs a tenant", request{method: "GET", path: "/api/v1/migration/packs", duties: "MigrationView"}, 401, "", ""},
		// Slice 5: the gl_balances pack.
		{"gl run needs MigrationRun", request{method: "POST", path: glPath, tenant: tenantA, duties: "MigrationView", body: glBody("USMF", "", cleanGL...)}, 403, "AUTH-0003", ""},
		{"gl run unbalanced extract", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", glRec("A", "1100", "", "10"), glRec("B", "3000", "", "-9.99"))}, 422, CodeInvalid, "not balanced in USD"},
		{"gl run without chart of accounts", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: strings.Replace(glBody("USMF", "", cleanGL...), `"accounts":["1100","2100","3000"]`, `"accounts":[" "]`, 1)}, 422, CodeInvalid, "chart of accounts"},
		{"gl run without balances", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "")}, 422, CodeInvalid, "at least one balance"},
		{"gl run bad balance", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", glRec("A", "1100", "", "1.000,00"))}, 422, CodeInvalid, "not a decimal"},
		{"gl run balance finer than numeric(28,6)", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", glRec("A", "1100", "", "0.0000001"), glRec("B", "3000", "", "-0.0000001"))}, 422, CodeInvalid, "more than 6 decimal places"},
		{"gl run balance in exponent notation", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", glRec("A", "1100", "", "1e2147483647"))}, 422, CodeInvalid, "not a decimal"},
		{"gl run late cutoff", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: strings.Replace(glBody("USMF", "", cleanGL...), "2026-12-31T00:00:00Z", "2027-01-02T00:00:00Z", 1)}, 422, CodeInvalid, "MIG-1501"},
		{"gl run blank company", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody(" ", "", cleanGL...)}, 422, CodeInvalid, "sourceCompany"},
		{"gl run open items run not a UUID", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", `,"openItemsRunId":"OI-RUN"`, cleanGL...)}, 422, CodeInvalid, "UUID"},
		{"gl run quarantine share MIG-1504", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: glBody("USMF", "", glRec("A", "1100", "", "10"), glRec("B", "9999", "", "-10"))}, 422, CodeInvalid, "MIG-1504"},
		{"gl run open-items shaped records", request{method: "POST", path: glPath, tenant: tenantA, duties: allDuties, body: openItemsBody(1)}, 400, "COM-0001", ""},
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

func TestParseMoney(t *testing.T) {
	for _, raw := range []string{"9e9999999", "1e2147483647", "1E5", "+5", "1,5", "", "0x10", "1" + strings.Repeat("0", 22), "-" + strings.Repeat("9", 23) + ".5"} {
		if _, err := parseMoney(raw); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	if _, err := parseMoney("0.0000001"); !errors.Is(err, errTooManyDecs) {
		t.Errorf("seven decimals: %v", err)
	}
	for raw, want := range map[string]string{"12.5": "12.5", " -0.000001 ": "-0.000001", strings.Repeat("9", 22): strings.Repeat("9", 22), "7": "7"} {
		got, err := parseMoney(raw)
		if err != nil || got.String() != want {
			t.Errorf("%q: %v, %v", raw, got, err)
		}
	}
	started := time.Now()
	if _, err := toLines([]tupleDTO{{Area: "GL", Key: "k", Amount: "1e2147483647"}}, "legacy"); err == nil {
		t.Error("toLines accepted a huge exponent")
	}
	if _, err := toPostings([]postingDTO{{Kind: "JOURNAL", Amount: "9e9999999"}}); err == nil {
		t.Error("toPostings accepted a huge exponent")
	}
	_, _ = parseMoney("9e9999999")
	if _, err := decimals("line", [2]string{"qty", "1e2147483647"}); err == nil {
		t.Error("decimals accepted a huge exponent")
	}
	if time.Since(started) > time.Second {
		t.Errorf("huge exponent took %s", time.Since(started))
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

func (u unreachableStore) GetTargetLoad(context.Context, string) (domain.TargetLoad, error) {
	u.fail()
	return domain.TargetLoad{}, nil
}
func (u unreachableStore) ListTargetRows(context.Context, string, store.TargetRowFilter) ([]domain.TargetRowView, error) {
	u.fail()
	return nil, nil
}
func (u unreachableStore) ClaimTargetLoad(context.Context, string, string, time.Duration) (domain.TargetLoad, []store.PendingRow, error) {
	u.fail()
	return domain.TargetLoad{}, nil, nil
}
func (u unreachableStore) RecordTargetChunk(context.Context, string, time.Duration, []store.RowOutcome) error {
	u.fail()
	return nil
}
func (u unreachableStore) FinishTargetLoad(context.Context, string, store.TargetFinish) (domain.TargetLoad, []domain.ControlTotal, error) {
	u.fail()
	return domain.TargetLoad{}, nil, nil
}

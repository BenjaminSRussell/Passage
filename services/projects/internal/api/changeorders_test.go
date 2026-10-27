package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// userCtx is a tenant-1 principal named user holding the given duties.
func userCtx(user string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

var (
	manager    = userCtx("pm-1", DutyMaintain, DutyInquire, DutyPlan)
	controller = userCtx("controller-1", DutyChangeOrderApprove, DutyInquire)
)

func coOf(t *testing.T, rec *httptest.ResponseRecorder) domain.ChangeOrder {
	t.Helper()
	var co domain.ChangeOrder
	if err := json.Unmarshal(rec.Body.Bytes(), &co); err != nil {
		t.Fatalf("decode change order: %v (%s)", err, rec.Body.String())
	}
	return co
}

func etag(v int) map[string]string { return map[string]string{"If-Match": `"` + strconv.Itoa(v) + `"`} }

func key(k string) map[string]string { return map[string]string{"Idempotency-Key": k} }

func postCO(t *testing.T, h http.Handler, ctx context.Context, body, k string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, ctx, http.MethodPost, "/v1/projects/change-orders", body, key(k))
}

// fieldErrors returns the problem's field errors.
func fieldErrors(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body struct {
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode problem: %v (%s)", err, rec.Body.String())
	}
	return body.Errors
}

// baselineProject seeds a fixed-price project with contract value 500,000.00,
// a WORKING plan with WBS 1 (summary), 1.1 and 1.3, and a BASELINE copy. It
// returns the working plan's node id of 1.3 and the baseline id.
func baselineProject(t *testing.T, f *fakeStore, h http.Handler, id string) (string, string) {
	t.Helper()
	p := sampleProject(id)
	p.ContractValue = "500000"
	if _, _, err := f.CreateProject(context.Background(), p, ""); err != nil {
		t.Fatal(err)
	}
	working := planOf(t, createPlan(t, h, id, `{"kind":"WORKING"}`, id+"-w"))
	wbs := `{"nodes":[
		{"wbsCode":"1","name":"Build","durationDays":0},
		{"wbsCode":"1.1","parentWbsCode":"1","name":"Design","effortHours":"80.00","durationDays":5},
		{"wbsCode":"1.3","parentWbsCode":"1","name":"Install","effortHours":"120.00","durationDays":4}],
		"dependencies":[{"predecessorWbsCode":"1.1","successorWbsCode":"1.3","type":"FS","lagDays":0}]}`
	rec := as(t, h, []string{DutyPlan}, http.MethodPut, "/v1/projects/"+id+"/plans/"+working.ID+"/wbs", wbs, etag(1))
	if rec.Code != http.StatusOK {
		t.Fatalf("seed wbs: %d %s", rec.Code, rec.Body.String())
	}
	var view wbsView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	var node13 string
	for _, n := range view.Nodes {
		if n.WBSCode == "1.3" {
			node13 = n.ID
		}
	}
	baseline := planOf(t, createPlan(t, h, id, `{"kind":"BASELINE","copyFromVersionId":"`+working.ID+`"}`, id+"-b"))
	return node13, baseline.ID
}

func scopeChange(project, node string) string {
	return `{"projectId":"` + project + `","type":"SCOPE_CHANGE","title":"Extra panel","lines":[
		{"wbsNodeId":"` + node + `","costImpact":"30000","revenueImpact":"50000.004","effortHoursDelta":"40","scheduleDaysDelta":2}]}`
}

func TestChangeOrder_CreateValidatesKeysAndNumbers(t *testing.T) {
	f := newFake()
	h := router(f)
	node, _ := baselineProject(t, f, h, "P-CO")

	if rec := do(t, h, manager, http.MethodPost, "/v1/projects/change-orders", scopeChange("P-CO", node), nil); rec.Code != http.StatusBadRequest || fieldErrors(t, rec)["Idempotency-Key"] == "" {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postCO(t, h, userCtx("", DutyMaintain), scopeChange("P-CO", node), "k0"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no user identity: %d", rec.Code)
	}
	if rec := postCO(t, h, controller, scopeChange("P-CO", node), "k0"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without ProjMaintain: %d", rec.Code)
	}
	bad := `{"projectId":"p-co","type":"CLAIM_VENDOR","title":"","lines":[
		{"costImpact":"12,5","revenueImpact":"1e3","effortHoursDelta":"8","scheduleDaysDelta":0}]}`
	rec := postCO(t, h, manager, bad, "k1")
	errs := fieldErrors(t, rec)
	for _, field := range []string{"projectId", "title", "vendorPartyId", "lines[0].costImpact", "lines[0].revenueImpact", "lines[0].wbsNodeId"} {
		if errs[field] == "" {
			t.Errorf("expected a field error on %s, got %v", field, errs)
		}
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", rec.Code)
	}
	if rec := postCO(t, h, manager, `{"projectId":"P-CO","type":"INTERNAL","title":"x","lines":[]}`, "k2"); rec.Code != http.StatusBadRequest || fieldErrors(t, rec)["lines"] == "" {
		t.Fatalf("no lines: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postCO(t, h, manager, scopeChange("NOPE", node), "k3"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", rec.Code)
	}
	if rec := postCO(t, h, manager, scopeChange("P-CO", ids.New()), "k4"); rec.Code != http.StatusUnprocessableEntity || fieldErrors(t, rec)["lines[0].wbsNodeId"] == "" {
		t.Fatalf("node of no plan: %d %s", rec.Code, rec.Body.String())
	}

	rec = postCO(t, h, manager, scopeChange("P-CO", node), "k5")
	first := coOf(t, rec)
	if rec.Code != http.StatusCreated || first.Number != "CO-0001" || first.Status != domain.COStatusDraft || first.CreatedBy != "pm-1" || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if l := first.Lines[0]; l.RevenueImpact != "50000.00" || l.CostImpact != "30000.00" || l.EffortHoursDelta != "40.00" || l.WBSCode == nil || *l.WBSCode != "1.3" {
		t.Fatalf("line not rounded to USD or WBS code not resolved: %+v", l)
	}
	if first.RevenueImpact != "50000.00" || first.CostImpact != "30000.00" {
		t.Fatalf("totals: %s / %s", first.RevenueImpact, first.CostImpact)
	}
	replay := postCO(t, h, manager, scopeChange("P-CO", node), "k5")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || coOf(t, replay).ID != first.ID {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	other := strings.Replace(scopeChange("P-CO", node), "Extra panel", "Other", 1)
	if rec := postCO(t, h, manager, other, "k5"); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != store.CodeIdempotencyReuse {
		t.Fatalf("key reused: %d %s", rec.Code, rec.Body.String())
	}
	if second := coOf(t, postCO(t, h, manager, other, "k6")); second.Number != "CO-0002" {
		t.Fatalf("second number: %s", second.Number)
	}

	rec = do(t, h, controller, http.MethodGet, "/v1/projects/change-orders?projectId=P-CO", "", nil)
	var list changeOrderList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 2 || list.Items[0].Number != "CO-0001" {
		t.Fatalf("list: %v %s", err, rec.Body.String())
	}
	if rec := do(t, h, controller, http.MethodGet, "/v1/projects/change-orders", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("list without projectId: %d", rec.Code)
	}
	if rec := do(t, h, controller, http.MethodGet, "/v1/projects/change-orders?projectId=P-CO&status=OPEN", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("list with bad status: %d", rec.Code)
	}
	if rec := do(t, h, controller, http.MethodGet, "/v1/projects/change-orders/not-a-uuid", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("get bad id: %d", rec.Code)
	}
	if rec := do(t, h, controller, http.MethodGet, "/v1/projects/change-orders/"+first.ID, "", nil); rec.Code != http.StatusOK || len(coOf(t, rec).Lines) != 1 {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
}

func move(t *testing.T, h http.Handler, ctx context.Context, id, action string, version int, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, ctx, http.MethodPost, "/v1/projects/change-orders/"+id+"/"+action, body, etag(version))
}

func TestChangeOrder_LifecycleAndSegregationOfDuties(t *testing.T) {
	f := newFake()
	h := router(f)
	node, _ := baselineProject(t, f, h, "P-LIFE")
	co := coOf(t, postCO(t, h, manager, scopeChange("P-LIFE", node), "l1"))

	if rec := do(t, h, manager, http.MethodPost, "/v1/projects/change-orders/"+co.ID+"/submit", "", nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("submit without If-Match: %d", rec.Code)
	}
	if rec := move(t, h, manager, co.ID, "approve", 1, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("approve without ProjChangeOrderApprove: %d", rec.Code)
	}
	if rec := move(t, h, controller, co.ID, "approve", 1, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeChangeOrderState {
		t.Fatalf("approve a DRAFT: %d %s", rec.Code, rec.Body.String())
	}
	if rec := move(t, h, manager, co.ID, "submit", 7, ""); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale submit: %d", rec.Code)
	}
	rec := move(t, h, manager, co.ID, "submit", 1, "")
	if rec.Code != http.StatusOK || coOf(t, rec).Status != domain.COStatusSubmitted {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	// The creator holds both duties but still cannot approve their own change order.
	both := userCtx("pm-1", DutyMaintain, DutyChangeOrderApprove)
	if rec := move(t, h, both, co.ID, "approve", 2, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeSelfApproval {
		t.Fatalf("self-approval: %d %s", rec.Code, rec.Body.String())
	}
	rec = move(t, h, controller, co.ID, "approve", 2, "")
	approved := coOf(t, rec)
	if rec.Code != http.StatusOK || approved.Status != domain.COStatusApproved || approved.DecidedBy == nil || *approved.DecidedBy != "controller-1" {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	if rec := move(t, h, controller, co.ID, "reject", 3, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeChangeOrderState {
		t.Fatalf("reject an APPROVED: %d", rec.Code)
	}

	rejected := coOf(t, postCO(t, h, manager, strings.Replace(scopeChange("P-LIFE", node), "Extra", "Rejected", 1), "l2"))
	move(t, h, manager, rejected.ID, "submit", 1, "")
	if rec := move(t, h, controller, rejected.ID, "reject", 2, ""); rec.Code != http.StatusOK || coOf(t, rec).Status != domain.COStatusRejected {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body.String())
	}
	if rec := move(t, h, controller, rejected.ID, "apply", 3, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeChangeOrderState {
		t.Fatalf("apply a REJECTED: %d", rec.Code)
	}
}

func TestChangeOrder_CustomerClaimNeedsApprovalReference(t *testing.T) {
	f := newFake()
	h := router(f)
	baselineProject(t, f, h, "P-CLAIM")
	body := `{"projectId":"P-CLAIM","type":"CLAIM_CUSTOMER","title":"Delay claim","lines":[{"costImpact":"0","revenueImpact":"12000"}]}`
	co := coOf(t, postCO(t, h, manager, body, "c1"))
	move(t, h, manager, co.ID, "submit", 1, "")
	if rec := move(t, h, controller, co.ID, "approve", 2, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeCustomerApproval {
		t.Fatalf("approve without evidence: %d %s", rec.Code, rec.Body.String())
	}
	if rec := move(t, h, controller, co.ID, "approve", 2, `{"customerApprovalRef":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank evidence: %d", rec.Code)
	}
	rec := move(t, h, controller, co.ID, "approve", 2, `{"customerApprovalRef":"Signed letter 2026-10-20"}`)
	if out := coOf(t, rec); rec.Code != http.StatusOK || out.CustomerApprovalRef == nil || *out.CustomerApprovalRef != "Signed letter 2026-10-20" {
		t.Fatalf("approve with evidence: %d %s", rec.Code, rec.Body.String())
	}
}

// TestChangeOrder_ApplyAC4112 is PRD AC-41.12: an APPROVED SCOPE_CHANGE with
// +50,000.00 revenue and +30,000.00 cost on WBS 1.3 raises the contract value
// by 50,000.00, reports a 30,000.00 budget delta, creates a new BASELINE and a
// second apply returns PROJ-CO-001.
func TestChangeOrder_ApplyAC4112(t *testing.T) {
	f := newFake()
	h := router(f)
	node, oldBaseline := baselineProject(t, f, h, "P-APPLY")
	co := coOf(t, postCO(t, h, manager, scopeChange("P-APPLY", node), "a1"))
	if rec := move(t, h, controller, co.ID, "apply", 1, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeChangeOrderState {
		t.Fatalf("apply a DRAFT: %d", rec.Code)
	}
	move(t, h, manager, co.ID, "submit", 1, "")
	move(t, h, controller, co.ID, "approve", 2, "")
	if rec := move(t, h, manager, co.ID, "apply", 3, ""); rec.Code != http.StatusForbidden {
		t.Fatalf("apply without ProjChangeOrderApprove: %d", rec.Code)
	}

	rec := move(t, h, controller, co.ID, "apply", 3, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	var out applyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Effect.ContractValueDelta != "50000.00" || out.Effect.BudgetDelta != "30000.00" || out.Effect.ContractValue != "550000.00" {
		t.Fatalf("effect: %+v", out.Effect)
	}
	if out.ChangeOrder.Status != domain.COStatusApplied || out.ChangeOrder.AppliedBaselineVersionID == nil || *out.ChangeOrder.AppliedBaselineVersionID != out.Baseline.ID {
		t.Fatalf("applied change order: %+v", out.ChangeOrder)
	}
	if out.Baseline.Kind != domain.KindBaseline || !out.Baseline.Locked || out.Baseline.ID == oldBaseline ||
		out.Baseline.CopiedFromVersionID == nil || *out.Baseline.CopiedFromVersionID != oldBaseline {
		t.Fatalf("new baseline: %+v", out.Baseline)
	}
	if p := f.projects["P-APPLY"]; p.ContractValue != "550000.00" || p.Version != 2 {
		t.Fatalf("project after apply: %+v", p)
	}
	if f.plans[oldBaseline].SupersededByVersionID == nil || *f.plans[oldBaseline].SupersededByVersionID != out.Baseline.ID {
		t.Fatalf("old baseline not superseded: %+v", f.plans[oldBaseline])
	}
	for _, n := range f.nodes[out.Baseline.ID] {
		if n.WBSCode == "1.3" && (n.DurationDays != 6 || n.EffortHours == nil || *n.EffortHours != "160.00") {
			t.Fatalf("WBS 1.3 in the new baseline: duration %d effort %v", n.DurationDays, n.EffortHours)
		}
	}
	for _, v := range []int{3, 4} {
		if rec := move(t, h, controller, co.ID, "apply", v, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeChangeOrderApplied {
			t.Fatalf("second apply with version %d: %d %s", v, rec.Code, rec.Body.String())
		}
	}
	if rec := move(t, h, manager, co.ID, "submit", 4, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeChangeOrderApplied {
		t.Fatalf("an APPLIED change order is immutable: %d", rec.Code)
	}
	// The plans list now has the superseded baseline and the new one, and a
	// further baseline copy is refused while the new one is current.
	rec = as(t, h, []string{DutyInquire}, http.MethodGet, "/v1/projects/P-APPLY/plans", "", nil)
	if !strings.Contains(rec.Body.String(), `"supersededByVersionId":"`+out.Baseline.ID+`"`) {
		t.Fatalf("plan list does not show the superseded baseline: %s", rec.Body.String())
	}
}

func TestChangeOrder_ApplyNeedsBaseline(t *testing.T) {
	f := newFake()
	h := router(f)
	seedProject(t, f, "P-NOBASE")
	co := coOf(t, postCO(t, h, manager, `{"projectId":"P-NOBASE","type":"INTERNAL","title":"Rework","lines":[{"costImpact":"500","revenueImpact":"0"}]}`, "n1"))
	move(t, h, manager, co.ID, "submit", 1, "")
	move(t, h, controller, co.ID, "approve", 2, "")
	if rec := move(t, h, controller, co.ID, "apply", 3, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeNoBaseline {
		t.Fatalf("apply without baseline: %d %s", rec.Code, rec.Body.String())
	}
	if f.projects["P-NOBASE"].Version != 1 {
		t.Fatal("a refused apply changed the project")
	}
}

func TestChangeOrder_ApplyerMustNotBeCreator(t *testing.T) {
	f := newFake()
	h := router(f)
	node, _ := baselineProject(t, f, h, "P-SOD")
	both := userCtx("pm-1", DutyMaintain, DutyChangeOrderApprove)
	co := coOf(t, postCO(t, h, both, scopeChange("P-SOD", node), "s1"))
	move(t, h, both, co.ID, "submit", 1, "")
	move(t, h, controller, co.ID, "approve", 2, "")
	if rec := move(t, h, both, co.ID, "apply", 3, ""); rec.Code != http.StatusUnprocessableEntity || errorCode(t, rec) != domain.CodeSelfApproval {
		t.Fatalf("creator applies: %d %s", rec.Code, rec.Body.String())
	}
}

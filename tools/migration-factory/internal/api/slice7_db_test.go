package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func insertPackRun(t *testing.T, admin *pgxpool.Pool, tenantID, pack, company, status string, at time.Time) string {
	t.Helper()
	id := ids.New()
	if _, err := admin.Exec(context.Background(), `insert into pack_run (tenant_id, run_id, pack, source_system, source_company, cutoff_ts, input_hash, status,
		records_in, records_loaded, records_quarantined, created_at) values ($1, $2::uuid, $3, 'AX2012', $4, $5, $2::text, $6, 1, 1, 0, $5)`,
		tenantID, id, pack, company, at, status); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertParallelRun(t *testing.T, admin *pgxpool.Pool, tenantID, period, system, state string) string {
	t.Helper()
	signed := ""
	if state == "SIGNED_OFF" {
		signed = ", signed_off_by, signed_off_at"
	}
	vals := ""
	if signed != "" {
		vals = ", 'controller', now()"
	}
	id := ids.New()
	if _, err := admin.Exec(context.Background(), fmt.Sprintf(`insert into parallel_run (tenant_id, parallel_run_id, period, source_system, documents_replayed, revenue,
		explained_max_pct, input_hash, state, unexplained_count, unexplained_amount, explained_amount%s)
		values ($1, $2::uuid, $3, $5, 1, 100, 0.1, $2::text, $4, 0, 0, 0%s)`, signed, vals), tenantID, id, period, state, system); err != nil {
		t.Fatal(err)
	}
	return id
}

func attestations(codes ...string) string {
	if len(codes) == 0 {
		codes = []string{"opening_tb_signed", "sod_clear", "country_packs", "load_test", "dr_game_day", "backup_verified", "security_clear",
			"integrations_smoke", "uat_signed", "support_rota", "monitoring_green", "legacy_freeze_rollback"}
	}
	var parts []string
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf(`{"code":%q,"confirmed":true,"reference":"https://evidence.example/%s"}`, code, code))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// readyPlan generates a plan (packs open_items and gl_balances, entity USMF, T-0 2027-01-01) and loads
// production runs for it: both packs RECONCILED after the legacy freeze and December signed off.
func readyPlan(t *testing.T, h http.Handler, admin *pgxpool.Pool) (planDTO, time.Time) {
	t.Helper()
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "CutoverManager", body: generateBody(t0, 36), user: "manager"}))
	insertPackRun(t, admin, tenantA, "open_items", "USMF", "RECONCILED", t0.Add(2*time.Hour))
	insertPackRun(t, admin, tenantA, "gl_balances", "USMF", "RECONCILED", t0.Add(2*time.Hour))
	insertParallelRun(t, admin, tenantA, "2026-12", "AX2012", "SIGNED_OFF")
	return plan, t0
}

type decisionCall struct {
	t    *testing.T
	h    http.Handler
	base string
}

func (c decisionCall) post(user, body, key string) *httptest.ResponseRecorder {
	c.t.Helper()
	return do(c.t, c.h, request{method: "POST", path: c.base, tenant: tenantA, duties: "CutoverManager", body: body, key: key, user: user})
}

func (c decisionCall) get(t *testing.T) readinessDTO {
	t.Helper()
	r := do(t, c.h, request{method: "GET", path: c.base, tenant: tenantA, duties: "CutoverManager", user: "viewer"})
	if r.Code != http.StatusOK {
		t.Fatalf("readiness: %d %s", r.Code, r.Body)
	}
	return decode[readinessDTO](t, r)
}

// FR-49.6 / 9.49.5: the checklist is evaluated from the factory's own data. Only production runs
// count: the latest run of each plan pack and entity started at or after the legacy freeze, and every
// parallel run of the month before T-0.
func TestGoNoGo_ChecklistFromFactoryData(t *testing.T) {
	h, admin := dbServer(t)
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "CutoverManager", body: generateBody(t0, 36), user: "manager"}))
	c := decisionCall{t, h, "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go"}

	rd := c.get(t)
	if rd.SystemReady || len(rd.Items) != 14 || rd.Items[0].Code != "packs_reconciled" || rd.Items[0].Status != "OPEN" || rd.Items[1].Status != "OPEN" ||
		len(rd.Decisions) != 0 || rd.PendingGo != nil || rd.Items[2].Status != "UNCONFIRMED" {
		t.Fatalf("readiness before any run: %+v", rd)
	}
	if !strings.Contains(rd.Items[0].Detail, "no run for gl_balances/USMF") || !strings.Contains(rd.Items[1].Detail, "No parallel run exists for 2026-12") {
		t.Fatalf("details %q %q", rd.Items[0].Detail, rd.Items[1].Detail)
	}

	// A RECONCILED dry run from before the freeze does not count; a later LOADED run supersedes a RECONCILED one.
	insertPackRun(t, admin, tenantA, "gl_balances", "USMF", "RECONCILED", t0.Add(-14*24*time.Hour))
	insertPackRun(t, admin, tenantA, "open_items", "USMF", "RECONCILED", t0.Add(time.Hour))
	later := insertPackRun(t, admin, tenantA, "open_items", "USMF", "LOADED", t0.Add(2*time.Hour))
	insertPackRun(t, admin, tenantA, "open_items", "DEMF", "RECONCILED", t0.Add(2*time.Hour))  // not a plan entity
	insertPackRun(t, admin, tenantB, "gl_balances", "USMF", "RECONCILED", t0.Add(2*time.Hour)) // another tenant
	rd = c.get(t)
	if d := rd.Items[0].Detail; rd.Items[0].Status != "OPEN" || !strings.Contains(d, "before the legacy freeze 2026-12-31T18:00:00Z for gl_balances/USMF") ||
		!strings.Contains(d, "open_items/USMF is LOADED") || !contains(rd.Items[0].References, later) || len(rd.Items[0].References) != 2 {
		t.Fatalf("packs item: %+v", rd.Items[0])
	}
	insertPackRun(t, admin, tenantA, "open_items", "USMF", "RECONCILED", t0.Add(3*time.Hour))
	insertPackRun(t, admin, tenantA, "gl_balances", "USMF", "RECONCILED", t0.Add(3*time.Hour))

	// The parallel run must be the month before T-0, and every legacy system's run of it signed off.
	insertParallelRun(t, admin, tenantA, "2026-11", "AX2012", "SIGNED_OFF")
	insertParallelRun(t, admin, tenantA, "2027-01", "AX2012", "SIGNED_OFF")
	rd = c.get(t)
	if rd.Items[0].Status != "MET" || rd.Items[1].Status != "OPEN" || rd.SystemReady {
		t.Fatalf("parallel runs of other months: %+v", rd.Items[:2])
	}
	insertParallelRun(t, admin, tenantA, "2026-12", "AX2012", "SIGNED_OFF")
	sap := insertParallelRun(t, admin, tenantA, "2026-12", "SAPECC", "REVIEW")
	if rd = c.get(t); rd.Items[1].Status != "OPEN" || !strings.Contains(rd.Items[1].Detail, "SAPECC is REVIEW") {
		t.Fatalf("one legacy system in review: %+v", rd.Items[1])
	}
	if _, err := admin.Exec(context.Background(), `update parallel_run set state = 'SIGNED_OFF', signed_off_by = 'controller', signed_off_at = now() where parallel_run_id = $1`, sap); err != nil {
		t.Fatal(err)
	}
	if rd = c.get(t); !rd.SystemReady || rd.Items[1].Status != "MET" || len(rd.Items[1].References) != 2 || len(rd.OpenItems) != 12 {
		t.Fatalf("system ready: %+v", rd)
	}
}

// CUT-1605: approvers are authenticated users. A NO_GO is approved by the user recording it. A GO
// is approved by one CutoverManager and recorded when a second, different one approves the same
// attestations; nobody can name other people as approvers.
func TestGoNoGo_TwoAuthenticatedApprovers(t *testing.T) {
	h, admin := dbServer(t)
	plan, _ := readyPlan(t, h, admin)
	c := decisionCall{t, h, "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go"}
	goBody := func(version int, att string) string {
		return fmt.Sprintf(`{"decision":"GO","version":%d,"attestations":%s}`, version, att)
	}

	// Approvers cannot be named in the request.
	if r := c.post("alice", `{"decision":"GO","approvers":["cfo","sponsor"],"attestations":`+attestations()+`}`, ""); r.Code != http.StatusBadRequest {
		t.Fatalf("approvers in the body: %d %s", r.Code, r.Body)
	}
	// Without the attestations the GO is refused (CUT-1607) and nothing is stored.
	if r := c.post("alice", `{"decision":"GO"}`, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "CUT-1607") {
		t.Fatalf("unattested: %d %s", r.Code, r.Body)
	}
	for name, cs := range map[string]struct{ body, want string }{
		"no reference":  {`{"decision":"GO","attestations":[{"code":"load_test","confirmed":true}]}`, "CUT-1606"},
		"system attest": {`{"decision":"GO","attestations":[{"code":"packs_reconciled","confirmed":true,"reference":"x"}]}`, "cannot be attested"},
		"unknown":       {`{"decision":"MAYBE"}`, "GO or NO_GO"},
	} {
		if r := c.post("alice", cs.body, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), cs.want) {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	if r := c.post("", goBody(1, attestations()), ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "identified person") {
		t.Fatalf("anonymous: %d %s", r.Code, r.Body)
	}
	if n := count(t, admin, `select (select count(*) from cutover_decision) + (select count(*) from cutover_go_approval)`); n != 0 {
		t.Fatalf("rows after refused requests = %d", n)
	}

	// Alice's GO is her approval: 202, no decision, no event, the plan version moves.
	first := c.post("alice", goBody(1, attestations()), "go-alice")
	if first.Code != http.StatusAccepted {
		t.Fatalf("first approval: %d %s", first.Code, first.Body)
	}
	fr := decode[decisionResponse](t, first)
	if fr.Decision != nil || fr.PendingGo == nil || fr.PendingGo.Approver != "alice" || fr.PendingGo.Round != 1 || len(fr.PendingGo.Attestations) != 12 || fr.Plan.Version != 2 || fr.Plan.Decision != nil {
		t.Fatalf("first approval result %+v", fr)
	}
	if r := c.post("alice", goBody(1, attestations()), "go-alice"); r.Code != http.StatusAccepted || r.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %s", r.Code, r.Body)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.decision.recorded.v1'`); n != 0 {
		t.Fatalf("events after one approval = %d", n)
	}
	rd := c.get(t)
	if rd.PendingGo == nil || rd.PendingGo.Approver != "alice" || len(rd.OpenItems) != 0 || len(rd.Decisions) != 0 || rd.Version != 2 {
		t.Fatalf("readiness with a pending GO: %+v", rd)
	}

	// Alice cannot approve twice, even under another name or key.
	if r := c.post("alice", goBody(2, attestations()), ""); r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "CUT-1605") {
		t.Fatalf("alice again: %d %s", r.Code, r.Body)
	}
	// Bob approves what Alice approved: other attestations are refused, a stale version too.
	if r := c.post("bob", goBody(2, attestations("load_test")), ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "CUT-1605") {
		t.Fatalf("bob with other attestations: %d %s", r.Code, r.Body)
	}
	if r := c.post("bob", `{"decision":"GO","version":1}`, ""); r.Code != http.StatusConflict || problemCode(t, r) != CodeVersion {
		t.Fatalf("bob stale: %d %s", r.Code, r.Body)
	}
	done := c.post("bob", `{"decision":"GO","version":2}`, "go-bob")
	if done.Code != http.StatusCreated {
		t.Fatalf("second approval: %d %s", done.Code, done.Body)
	}
	g := decode[decisionResponse](t, done)
	if g.Decision == nil || g.Decision.Seq != 1 || g.Decision.Decision != "GO" || strings.Join(g.Decision.Approvers, ",") != "alice,bob" || g.Decision.DecidedBy != "bob" ||
		g.Decision.PlanVersion != 3 || g.Plan.Version != 3 || g.Plan.Decision == nil || g.Plan.Decision.Decision != "GO" {
		t.Fatalf("go result %+v", g)
	}
	for _, it := range g.Decision.Checklist {
		if it.Status != "MET" {
			t.Fatalf("checklist item %+v kept as evidence", it)
		}
	}
	var payload []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.cutover.decision.recorded.v1' and aggregate_id = $1`, plan.PlanID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data struct {
			Decision  string   `json:"decision"`
			Approvers []string `json:"approvers"`
			OpenItems []string `json:"open_items"`
			DecidedBy string   `json:"decided_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.Data.Decision != "GO" || strings.Join(evt.Data.Approvers, ",") != "alice,bob" || len(evt.Data.OpenItems) != 0 || evt.Data.DecidedBy != "bob" {
		t.Fatalf("event %s %v", payload, err)
	}

	// A GO is recorded once; the plan carries it in the read and the list.
	if r := c.post("carol", `{"decision":"NO_GO","reason":"late"}`, ""); r.Code != http.StatusConflict || problemCode(t, r) != CodeTransition {
		t.Fatalf("decision after GO: %d %s", r.Code, r.Body)
	}
	got := decode[planDTO](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID, tenant: tenantA, duties: "CutoverManager"}))
	if got.Decision == nil || got.Decision.Decision != "GO" || got.Version != 3 {
		t.Fatalf("plan read %+v", got)
	}
	list := decode[struct{ Items []planDTO }](t, do(t, h, request{method: "GET", path: "/api/v1/cutover/plans", tenant: tenantA, duties: "CutoverManager"}))
	if len(list.Items) != 1 || list.Items[0].Decision == nil || list.Items[0].Decision.Seq != 1 {
		t.Fatalf("plan list %+v", list.Items)
	}
}

// A NO_GO is recorded at once with its recorder as approver, and voids a pending GO approval: the
// next GO starts a new round and needs two approvals again.
func TestGoNoGo_NoGoVoidsAPendingApproval(t *testing.T) {
	h, admin := dbServer(t)
	plan, _ := readyPlan(t, h, admin)
	c := decisionCall{t, h, "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go"}

	if r := c.post("alice", `{"decision":"GO","attestations":`+attestations()+`}`, ""); r.Code != http.StatusAccepted {
		t.Fatalf("alice: %d %s", r.Code, r.Body)
	}
	if r := c.post("bob", `{"decision":"NO_GO","reason":" "}`, ""); r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), "reason") {
		t.Fatalf("no-go without reason: %d %s", r.Code, r.Body)
	}
	noGo := c.post("bob", `{"decision":"NO_GO","reason":"Bank smoke test failed","version":2}`, "")
	if noGo.Code != http.StatusCreated {
		t.Fatalf("no-go: %d %s", noGo.Code, noGo.Body)
	}
	res := decode[decisionResponse](t, noGo)
	if res.Decision == nil || res.Decision.Decision != "NO_GO" || strings.Join(res.Decision.Approvers, ",") != "bob" || res.Decision.DecidedBy != "bob" || res.Plan.Version != 3 ||
		len(res.Decision.Checklist) != 14 || res.Decision.Checklist[2].Status != "UNCONFIRMED" {
		t.Fatalf("no-go result %+v", res)
	}
	if rd := c.get(t); rd.PendingGo != nil || len(rd.Decisions) != 1 || rd.Items[2].Status != "UNCONFIRMED" {
		t.Fatalf("readiness after the no-go: %+v", rd)
	}
	// Bob's GO now is a first approval of round 2, not the second approval of Alice's.
	r := c.post("bob", `{"decision":"GO","attestations":`+attestations()+`}`, "")
	if r.Code != http.StatusAccepted || decode[decisionResponse](t, r).PendingGo.Round != 2 {
		t.Fatalf("bob after the no-go: %d %s", r.Code, r.Body)
	}
	if r := c.post("alice", `{"decision":"GO"}`, ""); r.Code != http.StatusCreated || decode[decisionResponse](t, r).Decision.Seq != 2 {
		t.Fatalf("alice completes round 2: %d %s", r.Code, r.Body)
	}
	if n := count(t, admin, `select count(*) from outbox where type = 'dev.passage.cutover.decision.recorded.v1'`); n != 2 {
		t.Fatalf("decision events = %d, want 2", n)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// A rolled-back plan takes no decision; other tenants and callers without the duty see nothing.
func TestGoNoGo_RolledBackPlanDutyAndTenant(t *testing.T) {
	h, _ := dbServer(t)
	t0 := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	plan := decode[planDTO](t, do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "CutoverManager", body: generateBody(t0, 36), user: "manager"}))
	base := "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go"
	noGo := `{"decision":"NO_GO","reason":"x"}`

	for _, c := range []struct {
		name, method, tenant, duties string
		want                         int
	}{
		{"read without the duty", "GET", tenantA, "MigrationView", http.StatusForbidden},
		{"record without the duty", "POST", tenantA, "CutoverStepOwner,MigrationRun", http.StatusForbidden},
		{"read another tenant's plan", "GET", tenantB, "CutoverManager", http.StatusNotFound},
		{"record on another tenant's plan", "POST", tenantB, "CutoverManager", http.StatusNotFound},
	} {
		if r := do(t, h, request{method: c.method, path: base, tenant: c.tenant, duties: c.duties, body: noGo, user: "manager"}); r.Code != c.want {
			t.Errorf("%s: %d %s", c.name, r.Code, r.Body)
		}
	}
	if r := do(t, h, request{method: "GET", path: "/api/v1/cutover/plans/not-a-uuid/go-no-go", tenant: tenantA, duties: "CutoverManager"}); r.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad id: %d", r.Code)
	}

	rb := fmt.Sprintf(`{"approvers":["alice","bob"],"postings":%s}`, rekeyPostingsBody())
	if r := do(t, h, request{method: "POST", path: "/api/v1/cutover/plans/" + plan.PlanID + "/rollback", tenant: tenantA, duties: "CutoverManager", body: rb, user: "manager"}); r.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", r.Code, r.Body)
	}
	if r := do(t, h, request{method: "POST", path: base, tenant: tenantA, duties: "CutoverManager", body: noGo, user: "manager"}); r.Code != http.StatusConflict || problemCode(t, r) != CodeTransition {
		t.Fatalf("decision on a rolled-back plan: %d %s", r.Code, r.Body)
	}
	rd := decode[readinessDTO](t, do(t, h, request{method: "GET", path: base, tenant: tenantA, duties: "CutoverManager"}))
	if !rd.RolledBack || rd.SystemReady {
		t.Fatalf("rolled-back readiness %+v", rd)
	}
}

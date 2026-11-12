package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

func stepBody(order int, region, status string) string {
	now := time.Now().UTC().Truncate(time.Second)
	b, _ := json.Marshal(map[string]any{"order": order, "region": region, "status": status, "detail": "ok", "startedAt": now, "finishedAt": now.Add(time.Second)})
	return string(b)
}

func TestFailoverStepsAreRecordedAndTheFailoverCompletes(t *testing.T) {
	e := newEnv(t, "us-east")
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-s", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatalf("failover: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PUT", "/v1/ops/failovers/evt-s/steps/fence-old-primary", "alice", DutyView, "", stepBody(1, "us-east", "DONE")); rec.Code != http.StatusForbidden {
		t.Fatalf("recording needs the failover duty: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/failovers/evt-s/steps/format-disk", "alice", DutyFailover, "", stepBody(1, "us-east", "DONE")); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/failovers/evt-s/steps/fence-old-primary", "alice", DutyFailover, "", stepBody(1, "us-east", "SKIPPED")); rec.Code != http.StatusBadRequest {
		t.Fatalf("a step is DONE or FAILED: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/failovers/nope/steps/fence-old-primary", "alice", DutyFailover, "", stepBody(1, "us-east", "DONE")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown failover: %d %s", rec.Code, rec.Body)
	}
	actions := []string{"fence-old-primary", "promote-cnpg", "switch-mirrormaker", "activate-temporal", "flip-dns", "verify-ledger-hash-chain"}
	for i, a := range actions {
		region := "eu-west"
		if i == 0 {
			region = "us-east"
		}
		status := "DONE"
		if i == 2 {
			status = "FAILED" // the mirror step fails first, then succeeds on the retry
		}
		rec := e.do("PUT", "/v1/ops/failovers/evt-s/steps/"+a, "alice", DutyFailover, "", stepBody(i+1, region, status))
		if rec.Code != http.StatusOK {
			t.Fatalf("step %s: %d %s", a, rec.Code, rec.Body)
		}
		var d store.FailoverDetail
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		if d.Complete && i < len(actions)-1 {
			t.Fatalf("not complete after step %d", i)
		}
		if i == 2 {
			if rec := e.do("PUT", "/v1/ops/failovers/evt-s/steps/"+a, "alice", DutyFailover, "", stepBody(i+1, region, "DONE")); rec.Code != http.StatusOK {
				t.Fatalf("retry: %d %s", rec.Code, rec.Body)
			}
		}
	}
	rec := e.do("GET", "/v1/ops/failovers/evt-s", "bob", DutyView, "", "")
	var d store.FailoverDetail
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if rec.Code != http.StatusOK || !d.Complete || len(d.Steps) != 6 || d.Steps[2].Status != "DONE" || d.Steps[0].ReportedBy != "alice" {
		t.Fatalf("detail: %d %s", rec.Code, rec.Body)
	}
	// a DONE step is not overwritten by a late FAILED report
	e.do("PUT", "/v1/ops/failovers/evt-s/steps/flip-dns", "alice", DutyFailover, "", stepBody(5, "eu-west", "FAILED"))
	rec = e.do("GET", "/v1/ops/failovers/evt-s", "bob", DutyView, "", "")
	if !strings.Contains(rec.Body.String(), `"complete":true`) {
		t.Fatalf("a DONE step was overwritten: %s", rec.Body)
	}
}

func TestMetricsReportFencedRegionsAndFailoversInProgress(t *testing.T) {
	e := newEnv(t, "us-east")
	a := New(store.New(e.pool), Config{Region: "us-east", OperatorTenantID: e.tenant})
	metrics := func() string {
		rec := httpGet(a.Metrics())
		return rec
	}
	if m := metrics(); !strings.Contains(m, "passage_ops_region_fenced 0") || !strings.Contains(m, "passage_ops_failover_in_progress 0") {
		t.Fatalf("idle: %s", m)
	}
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-m", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	if m := metrics(); !strings.Contains(m, "passage_ops_region_fenced 1") || !strings.Contains(m, "passage_ops_failover_in_progress 1") {
		t.Fatalf("after failover: %s", m)
	}
}

func TestBreakGlassWindowAuthorisesFailoverRoutesAndIsLoggedAndReviewed(t *testing.T) {
	e := newEnv(t, "us-east")
	const open = `{"reasonCode":"REGION_LOSS","ticketRef":"INC-42","minutes":30}`
	// carol holds only the request duty: the failover route refuses her until a window is open.
	if rec := e.do("POST", "/v1/ops/failover", "carol", DutyBreakGlassRequest, "evt-b1", `{"to":"eu-west"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("no window: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/break-glass", "carol", DutyView, "", open); rec.Code != http.StatusForbidden {
		t.Fatalf("opening needs the request duty: %d", rec.Code)
	}
	for _, bad := range []string{`{"reasonCode":"BORED","ticketRef":"x"}`, `{"reasonCode":"REGION_LOSS"}`, `{"reasonCode":"REGION_LOSS","ticketRef":"x","minutes":500}`} {
		if rec := e.do("POST", "/v1/ops/break-glass", "carol", DutyBreakGlassRequest, "", bad); rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeBreakGlassInvalid {
			t.Fatalf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	rec := e.do("POST", "/v1/ops/break-glass", "carol", DutyBreakGlassRequest, "", open)
	if rec.Code != http.StatusCreated {
		t.Fatalf("open: %d %s", rec.Code, rec.Body)
	}
	var g store.BreakGlass
	_ = json.Unmarshal(rec.Body.Bytes(), &g)
	if g.UserID != "carol" || !g.ExpiresAt.After(time.Now().Add(25*time.Minute)) || !g.ExpiresAt.Before(time.Now().Add(35*time.Minute)) {
		t.Fatalf("window %+v", g)
	}
	if rec := e.do("POST", "/v1/ops/break-glass", "carol", DutyBreakGlassRequest, "", open); rec.Code != http.StatusConflict || code(t, rec) != domain.CodeBreakGlassActive {
		t.Fatalf("second window: %d %s", rec.Code, rec.Body)
	}
	// the window admits carol, and only carol
	if rec := e.do("POST", "/v1/ops/failover", "dave", DutyBreakGlassRequest, "evt-b2", `{"to":"eu-west"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("another operator: %d", rec.Code)
	}
	if rec := e.do("POST", "/v1/ops/failover", "carol", DutyBreakGlassRequest, "evt-b1", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatalf("inside the window: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/v1/ops/break-glass?unreviewed=true", "bob", DutyView, "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"uses":1`) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	// review: not while open, not by carol, needs the review duty
	reviewURL := "/v1/ops/break-glass/" + g.ID + ":review"
	if rec := e.do("POST", reviewURL, "bob", DutyView, "", `{"outcome":"APPROVED"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("review needs its duty: %d", rec.Code)
	}
	if rec := e.do("POST", reviewURL, "bob", DutyBreakGlassReview, "", `{"outcome":"APPROVED"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "still open") {
		t.Fatalf("review while open: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/break-glass/"+g.ID+":revoke", "bob", DutyBreakGlassRequest, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("only the holder revokes: %d", rec.Code)
	}
	if rec := e.do("POST", "/v1/ops/break-glass/"+g.ID+":revoke", "carol", DutyBreakGlassRequest, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover", "carol", DutyBreakGlassRequest, "evt-b3", `{"to":"us-east"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("after revoke: %d", rec.Code)
	}
	if rec := e.do("POST", reviewURL, "carol", DutyBreakGlassReview, "", `{"outcome":"APPROVED"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "cannot review") {
		t.Fatalf("self review: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", reviewURL, "bob", DutyBreakGlassReview, "", `{"outcome":"FLAGGED"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("flag without a note: %d", rec.Code)
	}
	if rec := e.do("POST", reviewURL, "bob", DutyBreakGlassReview, "", `{"outcome":"APPROVED","note":"real region loss"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"reviewedBy":"bob"`) {
		t.Fatalf("review: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", reviewURL, "erin", DutyBreakGlassReview, "", `{"outcome":"APPROVED"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "already reviewed") {
		t.Fatalf("second review: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/break-glass/"+ids0()+":review", "bob", DutyBreakGlassReview, "", `{"outcome":"APPROVED"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown window: %d", rec.Code)
	}
}

func TestBreakGlassExpires(t *testing.T) {
	e := newEnv(t, "us-east")
	rec := e.do("POST", "/v1/ops/break-glass", "carol", DutyBreakGlassRequest, "", `{"reasonCode":"INCIDENT_RESPONSE","ticketRef":"INC-1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	// move the whole window into the past; the CHECK keeps its length valid
	if _, err := e.admin.Exec(context.Background(), `update ops_break_glass set granted_at = now() - interval '2 hours', expires_at = now() - interval '1 hour', review_due_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("POST", "/v1/ops/failover", "carol", DutyBreakGlassRequest, "evt-x", `{"to":"eu-west"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("expired window: %d", rec.Code)
	}
	a := New(store.New(e.pool), Config{OperatorTenantID: e.tenant})
	if m := httpGet(a.Metrics()); !strings.Contains(m, "ops_break_glass_review_overdue 1") || !strings.Contains(m, "ops_break_glass_active 0") {
		t.Fatalf("metrics: %s", m)
	}
	// an expired window can be reviewed, and a new one can be opened
	if rec := e.do("POST", "/v1/ops/break-glass", "carol", DutyBreakGlassRequest, "", `{"reasonCode":"INCIDENT_RESPONSE","ticketRef":"INC-2"}`); rec.Code != http.StatusCreated {
		t.Fatalf("new window: %d %s", rec.Code, rec.Body)
	}
}

func httpGet(h http.Handler) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func ids0() string { return ids.New() }

func TestForcedFenceNeedsASecondOperatorsApprovalAndIsAuditedInTheScorecard(t *testing.T) {
	e := newEnv(t, "us-east")
	// lag over the limit: the failover is a request until a second operator approves
	if rec := e.do("PUT", "/v1/ops/regions/eu-west/lag", "mon", DutyReport, "", `{"lagSeconds":900}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	forced := func(order int, region, status, detail string, forcedFlag bool) string {
		now := time.Now().UTC().Truncate(time.Second)
		b, _ := json.Marshal(map[string]any{"order": order, "region": region, "status": status, "detail": detail, "forced": forcedFlag, "startedAt": now, "finishedAt": now.Add(time.Second)})
		return string(b)
	}
	// an unapproved, no-loss failover cannot carry a forced fence: use a second failover on a fresh env for that
	rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-lost", `{"to":"eu-west","acceptDataLoss":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var created struct {
		Request store.Request `json:"request"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rec := e.do("POST", "/v1/ops/failover-requests/"+created.Request.ID+":approve", "bob", DutyFailover, "", ""); rec.Code != http.StatusCreated {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	path := "/v1/ops/failovers/evt-lost/steps/fence-old-primary"
	for name, body := range map[string]string{
		"no reason":         forced(1, "us-east", "DONE", "", true),
		"forced and FAILED": forced(1, "us-east", "FAILED", "r", true),
	} {
		if rec := e.do("PUT", path, "alice", DutyFailover, "", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := e.do("PUT", "/v1/ops/failovers/evt-lost/steps/flip-dns", "alice", DutyFailover, "", forced(5, "eu-west", "DONE", "r", true)); rec.Code != http.StatusBadRequest {
		t.Errorf("only the fence step can be forced: %d", rec.Code)
	}
	if rec := e.do("PUT", path, "alice", DutyFailover, "", forced(1, "us-east", "DONE", "region lost, INC-9", true)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"forced":true`) {
		t.Fatalf("approved failover: %d %s", rec.Code, rec.Body)
	}
	sc := e.get("/v1/ops/failovers/evt-lost/scorecard", "bob", DutyView)
	if !strings.Contains(sc.Body.String(), `"fenceForced":true`) || !strings.Contains(sc.Body.String(), `"passed":false`) {
		t.Fatalf("scorecard: %s", sc.Body)
	}

	// a failover nobody else approved refuses the forced fence
	e2 := newEnv(t, "us-east")
	if rec := e2.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-plain", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	rec = e2.do("PUT", "/v1/ops/failovers/evt-plain/steps/fence-old-primary", "alice", DutyFailover, "", forced(1, "us-east", "DONE", "region lost", true))
	if rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeApprovalRequired {
		t.Fatalf("unapproved forced fence: %d %s", rec.Code, rec.Body)
	}
}

func policyEnv(t *testing.T, p store.LagPolicy) *env {
	t.Helper()
	e := newEnv(t, "us-east")
	r := httpx.NewRouterWithoutTenant()
	New(store.New(e.pool).WithLagPolicy(p), Config{Region: "us-east", MaxLagSeconds: 300, OperatorTenantID: e.tenant}).Register(r)
	e.router = r
	return e
}

func TestAStaleLagReadingIsUnknownAndBlocksAFailover(t *testing.T) {
	e := policyEnv(t, store.LagPolicy{StaleAfter: 90 * time.Second})
	if rec := e.do("PUT", "/v1/ops/regions/eu-west/lag", "mon", DutyReport, "", `{"lagSeconds":5}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/regions", "alice", DutyView, "", ""); strings.Contains(rec.Body.String(), "lagStale") || !strings.Contains(rec.Body.String(), `"lagSeconds":5`) {
		t.Fatalf("a fresh reading is used as it is: %s", rec.Body)
	}
	// the reporter stops: the reading ages past three intervals
	if _, err := e.admin.Exec(context.Background(), `update ops_region set lag_reported_at = now() - interval '10 minutes' where name = 'eu-west'`); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/v1/ops/regions", "alice", DutyView, "", "")
	if !strings.Contains(rec.Body.String(), `"lagStale":true`) || !strings.Contains(rec.Body.String(), `"lagSeconds":1073741824`) {
		t.Fatalf("a stale reading is unknown: %s", rec.Body)
	}
	rec = e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-stale", `{"to":"eu-west"}`)
	if rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeLagExceeded || !strings.Contains(rec.Body.String(), "unknown") {
		t.Fatalf("stale lag must refuse the failover: %d %s", rec.Code, rec.Body)
	}
	// a fresh report makes it usable again
	if rec := e.do("PUT", "/v1/ops/regions/eu-west/lag", "mon", DutyReport, "", `{"lagSeconds":7}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-fresh", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatalf("a fresh reading: %d %s", rec.Code, rec.Body)
	}
}

func TestNeverReportedLagIsUnknownOnlyWhenReportsAreRequired(t *testing.T) {
	lenient := policyEnv(t, store.LagPolicy{StaleAfter: 90 * time.Second})
	if rec := lenient.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatalf("without a reporter the seeded lag stands: %d %s", rec.Code, rec.Body)
	}
	strict := policyEnv(t, store.LagPolicy{StaleAfter: 90 * time.Second, RequireReports: true})
	if rec := strict.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west"}`); rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeLagExceeded {
		t.Fatalf("with a reporter required a region that never reported is unknown: %d %s", rec.Code, rec.Body)
	}
	off := policyEnv(t, store.LagPolicy{})
	if _, err := off.admin.Exec(context.Background(), `update ops_region set lag_reported_at = now() - interval '1 day'`); err != nil {
		t.Fatal(err)
	}
	if rec := off.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west"}`); rec.Code != http.StatusCreated {
		t.Fatalf("StaleAfter 0 turns the check off: %d %s", rec.Code, rec.Body)
	}
}

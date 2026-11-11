package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

type env struct {
	router http.Handler
	tenant string
	pool   *pgxpool.Pool
}

func newEnv(t *testing.T, region string) *env {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	if err := st.Seed(context.Background(), []domain.Region{{Name: "us-east", Role: domain.Primary}, {Name: "eu-west", Role: domain.Passive}}); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouterWithoutTenant()
	operator := ids.New()
	New(st, Config{Region: region, MaxLagSeconds: 300, OperatorTenantID: operator}).Register(r)
	return &env{router: r, tenant: operator, pool: pool}
}

func (e *env) do(method, path, user, duties, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", e.tenant)
	if user != "" {
		req.Header.Set("X-Passage-User", user)
	}
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

type httptestRecorder = httptest.ResponseRecorder

// doWith is do with extra request headers.
func (e *env) doWith(method, path, user, duties string, headers map[string]string, body string) *httptestRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", e.tenant)
	if user != "" {
		req.Header.Set("X-Passage-User", user)
	}
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *env) get(path, user, duties string) *httptestRecorder {
	return e.doWith("GET", path, user, duties, nil, "")
}

func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p.Code
}

func TestRoutesRequireTheirDuty(t *testing.T) {
	e := newEnv(t, "us-east")
	for _, c := range []struct {
		method, path, duty string
		want               int
	}{
		{"GET", "/v1/ops/regions", "", http.StatusForbidden},
		{"GET", "/v1/ops/regions", DutyFailover, http.StatusForbidden},
		{"GET", "/v1/ops/regions", DutyView, http.StatusOK},
		{"GET", "/v1/ops/capacity/estimate", DutyReport, http.StatusForbidden},
		{"PUT", "/v1/ops/regions/eu-west/lag", DutyView, http.StatusForbidden},
		{"POST", "/v1/ops/failover", DutyView, http.StatusForbidden},
		{"POST", "/v1/ops/regions/us-east:rejoin", DutyView, http.StatusForbidden},
	} {
		if rec := e.do(c.method, c.path, "alice", c.duty, "k", `{}`); rec.Code != c.want {
			t.Errorf("%s %s with %q: %d, want %d: %s", c.method, c.path, c.duty, rec.Code, c.want, rec.Body)
		}
	}
	req := httptest.NewRequest("GET", "/v1/ops/regions", nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no tenant: %d", rec.Code)
	}
}

func TestFailoverEndToEnd(t *testing.T) {
	e := newEnv(t, "eu-west")
	// eu-west is PASSIVE: DNS must not route to it.
	if rec := e.do("GET", "/healthz/region", "", "", "", ""); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("passive health: %d %v", rec.Code, rec.Header())
	}
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "", `{"to":"eu-west"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west","extra":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", rec.Code)
	}
	rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("failover: %d %s", rec.Code, rec.Body)
	}
	var got store.Failover
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Plan.From != "us-east" || got.Plan.To != "eu-west" || got.RequestedBy != "alice" || len(got.Plan.Steps) != 6 {
		t.Fatalf("body = %s", rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west"}`); rec.Code != http.StatusOK {
		t.Fatalf("replay: %d", rec.Code)
	}
	if rec := e.do("GET", "/healthz/region", "", "", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("new primary health: %d", rec.Code)
	}
	// The old primary is FENCED; a second failover back to it is refused until it rejoins.
	rec = e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-2", `{"to":"us-east"}`)
	if rec.Code != http.StatusConflict || code(t, rec) != domain.CodeTransitionInvalid {
		t.Fatalf("fenced target: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/v1/ops/regions/us-east:rejoin", "alice", DutyFailover, "", `{"resyncVerified":false}`)
	if rec.Code != http.StatusConflict || code(t, rec) != domain.CodeResyncUnverified {
		t.Fatalf("unverified rejoin: %d %s", rec.Code, rec.Body)
	}
	if rec = e.do("POST", "/v1/ops/regions/us-east:rejoin", "alice", DutyFailover, "", `{"resyncVerified":true}`); rec.Code != http.StatusOK {
		t.Fatalf("rejoin: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("GET", "/v1/ops/regions", "alice", DutyView, "", "")
	var list struct {
		Items         []domain.Region `json:"items"`
		MaxLagSeconds int             `json:"maxLagSeconds"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 2 || list.Items[0].Role != domain.Primary || list.Items[1].Role != domain.Passive || list.MaxLagSeconds != 300 {
		t.Fatalf("regions = %s", rec.Body)
	}
}

func TestLagGuardOverHTTP(t *testing.T) {
	e := newEnv(t, "us-east")
	if rec := e.do("PUT", "/v1/ops/regions/eu-west/lag", "mon", DutyReport, "", `{"lagSeconds":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative lag: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/regions/nowhere/lag", "mon", DutyReport, "", `{"lagSeconds":5}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown region: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/regions/eu-west/lag", "mon", DutyReport, "", `{"lagSeconds":900}`); rec.Code != http.StatusOK {
		t.Fatalf("report: %d", rec.Code)
	}
	rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-1", `{"to":"eu-west"}`)
	if rec.Code != http.StatusUnprocessableEntity || code(t, rec) != domain.CodeLagExceeded {
		t.Fatalf("lag: %d %s", rec.Code, rec.Body)
	}
	// The approver never comes from the body.
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-2", `{"to":"eu-west","acceptDataLoss":true,"approvedBy":"bob"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("approvedBy in body: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-3", `{"to":"eu-west","acceptDataLoss":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Request store.Request `json:"request"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created.Request.ID
	if !ids.Valid(id) || created.Request.Status != "PENDING" || created.Request.RequestedBy != "alice" {
		t.Fatalf("request body = %s", rec.Body)
	}
	if rec := e.do("GET", "/healthz/region", "", "", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("a pending request changed the topology: %d", rec.Code)
	}
	if rec := e.do("GET", "/v1/ops/failover-requests/"+id, "bob", DutyView, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"PENDING"`) {
		t.Fatalf("read request: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover-requests/"+id+":approve", "bob", DutyView, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("approve without the failover duty: %d", rec.Code)
	}
	rec = e.do("POST", "/v1/ops/failover-requests/"+id+":approve", "alice", DutyFailover, "", "")
	if rec.Code != http.StatusForbidden || code(t, rec) != domain.CodeApprovalRequired {
		t.Fatalf("self-approval: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover-requests/"+ids.New()+":approve", "bob", DutyFailover, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown request: %d", rec.Code)
	}
	rec = e.do("POST", "/v1/ops/failover-requests/"+id+":approve", "bob", DutyFailover, "", "")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"approvedBy":"bob"`) || !strings.Contains(rec.Body.String(), `"requestedBy":"alice"`) {
		t.Fatalf("approved: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/failover-requests/"+id+":approve", "carol", DutyFailover, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("approval replay: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/healthz/region", "", "", "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("us-east still primary after the approved failover: %d", rec.Code)
	}
}

func TestOpsRoutesServeTheOperatorTenantOnly(t *testing.T) {
	e := newEnv(t, "us-east")
	all := DutyView + "," + DutyFailover + "," + DutyReport
	operator := e.tenant
	e.tenant = ids.New()
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/ops/regions", ""},
		{"GET", "/v1/ops/capacity/estimate", ""},
		{"PUT", "/v1/ops/regions/eu-west/lag", `{"lagSeconds":1}`},
		{"POST", "/v1/ops/failover", `{"to":"eu-west"}`},
		{"POST", "/v1/ops/regions/us-east:rejoin", `{"resyncVerified":true}`},
		{"GET", "/v1/ops/failover-requests/" + ids.New(), ""},
		{"POST", "/v1/ops/failover-requests/" + ids.New() + ":approve", ""},
	} {
		rec := e.do(c.method, c.path, "mallory", all, "evt-x", c.body)
		if rec.Code != http.StatusForbidden || code(t, rec) != domain.CodeOperatorOnly {
			t.Errorf("%s %s from another tenant: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	e.tenant = operator
	if rec := e.do("GET", "/v1/ops/regions", "olivia", all, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("operator: %d %s", rec.Code, rec.Body)
	}
	// No operator tenant configured serves no one.
	r := httpx.NewRouterWithoutTenant()
	New(nil, Config{Region: "us-east"}).Register(r)
	req := httptest.NewRequest("GET", "/v1/ops/regions", nil)
	req.Header.Set("X-Passage-Tenant", operator)
	req.Header.Set("X-Passage-User", "olivia")
	req.Header.Set("X-Passage-Duties", all)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unconfigured operator tenant: %d", rec.Code)
	}
}

func TestCapacityEndpoint(t *testing.T) {
	e := newEnv(t, "us-east")
	rec := e.do("GET", "/v1/ops/capacity/estimate?nodeUsdMonth=610.00&pgGibUsdMonth=0.115&brokerUsdMonth=420.00", "alice", DutyView, "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"costUsdMonth":"16637.60"`) {
		t.Fatalf("reference: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/capacity/estimate?users=500&journalLinesPerHour=50000&orderLinesPerHour=5000&scanners=20", "alice", DutyView, "", ""); !strings.Contains(rec.Body.String(), `"primary":19`) {
		t.Fatalf("small: %s", rec.Body)
	}
	for _, q := range []string{"users=0", "users=abc", "years=-3", "nodeUsdMonth=-1", "pgGibUsdMonth=x"} {
		if rec := e.do("GET", "/v1/ops/capacity/estimate?"+q, "alice", DutyView, "", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestRegionHealthIsPublicAndFailsClosedForUnknownRegions(t *testing.T) {
	e := newEnv(t, "mars-north")
	req := httptest.NewRequest("GET", "/healthz/region", nil)
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "UNKNOWN") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

var now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

// fakeStore records calls and applies the domain state machine to one
// in-memory conflict, so handler tests can check wiring without Postgres.
type fakeStore struct {
	pingErr   error
	known     map[string]string
	conflict  *store.ConflictDetail
	lastCap   int
	lastKey   string
	lastList  store.Filter
	evaluated []domain.Role
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) Evaluate(_ context.Context, userID string, roles []domain.Role, _ time.Time) ([]store.Conflict, error) {
	f.evaluated = roles
	out := []store.Conflict{}
	for _, c := range domain.Detect(userID, domain.SeedRules(), roles) {
		out = append(out, store.Conflict{ConflictID: ids.New(), UserID: userID, RuleID: c.RuleID, Severity: c.Severity, State: c.State, Version: 1})
	}
	return out, nil
}

func (f *fakeStore) KnownStates(context.Context, string, time.Time) (map[string]string, error) {
	return f.known, nil
}

func (f *fakeStore) List(_ context.Context, fl store.Filter, _ time.Time) (store.Page, error) {
	f.lastList = fl
	if f.conflict == nil {
		return store.Page{Items: []store.Conflict{}}, nil
	}
	return store.Page{Items: []store.Conflict{f.conflict.Conflict}}, nil
}

func (f *fakeStore) get(id string) (*store.ConflictDetail, error) {
	if f.conflict == nil || f.conflict.ConflictID != id {
		return nil, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "no SoD conflict with that id")
	}
	return f.conflict, nil
}

func (f *fakeStore) Get(_ context.Context, id string, _ time.Time) (store.ConflictDetail, error) {
	c, err := f.get(id)
	if err != nil {
		return store.ConflictDetail{}, err
	}
	return *c, nil
}

func (f *fakeStore) Mitigate(ctx context.Context, id string, expected int, in store.MitigationInput, capDays int, key string, at time.Time) (store.ConflictDetail, error) {
	f.lastCap, f.lastKey = capDays, key
	c, err := f.get(id)
	if err != nil {
		return store.ConflictDetail{}, err
	}
	if c.Version != expected {
		return store.ConflictDetail{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	p, _ := tenant.FromContext(ctx)
	rule, _ := domain.RuleByID(domain.SeedRules(), c.RuleID)
	dc := domain.Conflict{UserID: c.UserID, RuleID: c.RuleID, Severity: c.Severity, State: c.State}
	if err := dc.Mitigate(rule, domain.Mitigation{MitigationID: ids.New(), ControlRef: in.ControlRef, Reason: in.Reason, ApproverID: p.UserID, ExpiresAt: in.ExpiresAt}, at, capDays); err != nil {
		return store.ConflictDetail{}, err
	}
	c.State, c.MitigationID, c.ExpiresAt, c.Version = dc.State, dc.MitigationID, dc.ExpiresAt, c.Version+1
	return *c, nil
}

func (f *fakeStore) AcceptRisk(_ context.Context, id string, expected int, reason string, _ time.Time) (store.ConflictDetail, error) {
	c, err := f.get(id)
	if err != nil {
		return store.ConflictDetail{}, err
	}
	if c.Version != expected {
		return store.ConflictDetail{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	}
	dc := domain.Conflict{RuleID: c.RuleID, Severity: c.Severity, State: c.State}
	if err := dc.AcceptRisk(reason); err != nil {
		return store.ConflictDetail{}, err
	}
	c.State, c.Version = dc.State, c.Version+1
	return *c, nil
}

func newAPI(f *fakeStore, blockHigh bool) *API {
	a := New(f, Config{BlockHigh: blockHigh, MaxMitigationDays: 365})
	a.now = func() time.Time { return now }
	return a
}

func router(a *API) http.Handler {
	r := chi.NewRouter()
	a.Register(r)
	return r
}

type call struct {
	method, path string
	body         any
	user         string
	duties       []string
	headers      map[string]string
}

func serve(t *testing.T, h http.Handler, c call) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if c.body != nil {
		if err := json.NewEncoder(&buf).Encode(c.body); err != nil {
			t.Fatal(err)
		}
	}
	p := tenant.Principal{TenantID: ids.New(), UserID: c.user, Duties: map[string]struct{}{}}
	for _, d := range c.duties {
		p.Duties[d] = struct{}{}
	}
	req := httptest.NewRequest(c.method, c.path, &buf).WithContext(tenant.With(context.Background(), p))
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem body: %s", rec.Body.String())
	}
	return p.Code
}

func TestReadyz(t *testing.T) {
	if rec := serve(t, router(newAPI(&fakeStore{}, true)), call{method: http.MethodGet, path: "/readyz"}); rec.Code != http.StatusOK {
		t.Fatalf("ready: %d", rec.Code)
	}
	if rec := serve(t, router(newAPI(&fakeStore{pingErr: errors.New("down")}, true)), call{method: http.MethodGet, path: "/readyz"}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unready: %d", rec.Code)
	}
}

func TestDutiesAreEnforced(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	id := ids.New()
	for _, tc := range []struct {
		method, path, duty string
	}{
		{http.MethodGet, "/v1/sod/rules", DutySoDReview},
		{http.MethodPost, "/v1/sod/simulate", DutySecurityAdmin},
		{http.MethodPost, "/v1/sod/evaluations", DutySecurityAdmin},
		{http.MethodGet, "/v1/sod/conflicts", DutySoDReview},
		{http.MethodGet, "/v1/sod/conflicts/" + id, DutySoDReview},
		{http.MethodPost, "/v1/sod/conflicts/" + id + "/mitigations", DutySoDApprove},
		{http.MethodPost, "/v1/sod/conflicts/" + id + "/accept-risk", DutySoDAdmin},
		{http.MethodPost, "/v1/security/incidents:breach-clocks", DutyIncidentManage},
		{http.MethodPost, "/v1/compliance/audit-policies:evaluate", DutyAuditPolicyRun},
		{http.MethodPost, "/v1/compliance/audit-cases:group", DutyAuditCaseManage},
		{http.MethodPost, "/v1/compliance/audit-cases:transition", DutyAuditCaseManage},
		{http.MethodPost, "/v1/security/elevated-sessions:plan", DutyBreakGlassRequest},
	} {
		// A neighbouring duty is not enough.
		rec := serve(t, h, call{method: tc.method, path: tc.path, duties: []string{"ComplianceView"}})
		if rec.Code != http.StatusForbidden || problemCode(t, rec) != "AUTH-0003" {
			t.Errorf("%s %s without %s: %d", tc.method, tc.path, tc.duty, rec.Code)
		}
	}
}

func TestListRules(t *testing.T) {
	rec := serve(t, router(newAPI(&fakeStore{}, true)), call{method: http.MethodGet, path: "/v1/sod/rules", duties: []string{DutySoDReview}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Items []domain.Rule `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 11 || body.Items[0].RuleID != "P2P-001" {
		t.Fatalf("want the 11 seed rules, got %+v (%v)", body.Items, err)
	}
}

func simulateReq(user string) SimulateRequest {
	return SimulateRequest{
		UserID:        user,
		CurrentRoles:  []domain.Role{{Name: "Maint", Duties: []string{"MaintainVendors"}}},
		ProposedRoles: []domain.Role{{Name: "Pay", Duties: []string{"ProcessVendorPayments"}}},
	}
}

func decodeSim(t *testing.T, rec *httptest.ResponseRecorder) SimulateResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res SimulateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestSimulateBlocksHighConflict(t *testing.T) {
	user := ids.New()
	sim := call{method: http.MethodPost, path: "/v1/sod/simulate", body: simulateReq(user), duties: []string{DutySecurityAdmin}}
	res := decodeSim(t, serve(t, router(newAPI(&fakeStore{}, true)), sim))
	if !res.Blocked || res.Code != domain.CodeBlocked || len(res.BlockingRuleIDs) != 1 || res.BlockingRuleIDs[0] != "P2P-001" {
		t.Fatalf("want SOD-1101 on P2P-001, got %+v", res)
	}
	if res.NewConflicts[0].ExpiresAt != nil {
		t.Errorf("an OPEN conflict carries no expiry: %+v", res.NewConflicts[0])
	}

	res = decodeSim(t, serve(t, router(newAPI(&fakeStore{}, false)), sim))
	if res.Blocked || res.Code != "" || len(res.NewConflicts) != 1 {
		t.Fatalf("block_high=false should report without blocking: %+v", res)
	}

	// A mitigation the service recorded exempts the conflict.
	res = decodeSim(t, serve(t, router(newAPI(&fakeStore{known: map[string]string{"P2P-001": domain.StateMitigated}}, true)), sim))
	if res.Blocked {
		t.Fatalf("recorded mitigation must not block: %+v", res)
	}
}

// A caller cannot exempt a conflict by claiming it is mitigated: the request
// field the draft accepted is gone, and an unknown field is refused.
func TestSimulateRejectsClientClaimedMitigation(t *testing.T) {
	body := map[string]any{"userId": ids.New(), "currentRoles": []any{}, "proposedRoles": []any{}, "mitigatedRuleIds": []string{"P2P-001"}}
	rec := serve(t, router(newAPI(&fakeStore{}, true)), call{method: http.MethodPost, path: "/v1/sod/simulate", body: body, duties: []string{DutySecurityAdmin}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestSimulateValidation(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	for name, body := range map[string]SimulateRequest{
		"bad user":     {UserID: "nope"},
		"unnamed role": {UserID: ids.New(), ProposedRoles: []domain.Role{{Duties: []string{"A"}}}},
	} {
		rec := serve(t, h, call{method: http.MethodPost, path: "/v1/sod/simulate", body: body, duties: []string{DutySecurityAdmin}})
		if rec.Code != http.StatusBadRequest || problemCode(t, rec) != "COM-0002" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestEvaluateRecordsConflicts(t *testing.T) {
	f := &fakeStore{}
	user := strings.ToUpper(ids.New())
	roles := []domain.Role{{Name: "GL", Duties: []string{"PostGeneralJournals", "ApproveGeneralJournals"}}}
	rec := serve(t, router(newAPI(f, true)), call{method: http.MethodPost, path: "/v1/sod/evaluations", body: EvaluateRequest{UserID: user, Roles: roles}, duties: []string{DutySecurityAdmin}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res EvaluateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.UserID != strings.ToLower(user) || len(res.Conflicts) != 1 || res.Conflicts[0].RuleID != "GL-001" || len(f.evaluated) != 1 {
		t.Fatalf("want GL-001 for the lower-cased user, got %+v", res)
	}
}

func TestListConflictsValidatesFilters(t *testing.T) {
	f := &fakeStore{}
	h := router(newAPI(f, true))
	for _, q := range []string{"state=CLOSED", "severity=CRITICAL", "userId=x", "limit=0", "limit=201", "cursor=bm90LWpzb24"} {
		rec := serve(t, h, call{method: http.MethodGet, path: "/v1/sod/conflicts?" + q, duties: []string{DutySoDReview}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
	user := ids.New()
	rec := serve(t, h, call{method: http.MethodGet, path: "/v1/sod/conflicts?state=OPEN&severity=HIGH&limit=25&userId=" + user, duties: []string{DutySoDReview}})
	if rec.Code != http.StatusOK || f.lastList.State != "OPEN" || f.lastList.Severity != "HIGH" || f.lastList.Limit != 25 || f.lastList.UserID != user {
		t.Fatalf("filters not passed: %d %+v", rec.Code, f.lastList)
	}
}

func openConflict(user string) *store.ConflictDetail {
	return &store.ConflictDetail{Conflict: store.Conflict{ConflictID: ids.New(), UserID: user, RuleID: "P2P-001", Severity: domain.SeverityHigh,
		State: domain.StateOpen, Version: 1}, Mitigations: []store.Mitigation{}}
}

func mitigation() store.MitigationInput {
	return store.MitigationInput{ControlRef: "CTL-DUAL-PAY", Reason: "payments need a second approver", ExpiresAt: now.AddDate(0, 0, 180)}
}

func TestGetConflictSetsETag(t *testing.T) {
	f := &fakeStore{conflict: openConflict(ids.New())}
	h := router(newAPI(f, true))
	rec := serve(t, h, call{method: http.MethodGet, path: "/v1/sod/conflicts/" + f.conflict.ConflictID, duties: []string{DutySoDReview}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d etag %q", rec.Code, rec.Header().Get("ETag"))
	}
	rec = serve(t, h, call{method: http.MethodGet, path: "/v1/sod/conflicts/" + ids.New(), duties: []string{DutySoDReview}})
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeNotFound {
		t.Fatalf("unknown id: %d", rec.Code)
	}
	if rec := serve(t, h, call{method: http.MethodGet, path: "/v1/sod/conflicts/not-a-uuid", duties: []string{DutySoDReview}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed id: %d", rec.Code)
	}
}

// AC-48.4: a non-conflicted approver with a reason and a 180-day expiry
// mitigates the conflict; the response is 201 with the new ETag.
func TestMitigateWorkflow(t *testing.T) {
	user := ids.New()
	f := &fakeStore{conflict: openConflict(user)}
	h := router(newAPI(f, true))
	path := "/v1/sod/conflicts/" + f.conflict.ConflictID + "/mitigations"
	ok := map[string]string{"If-Match": `"1"`, "Idempotency-Key": "k-1"}

	for name, tc := range map[string]struct {
		headers map[string]string
		body    any
		status  int
		code    string
	}{
		"missing If-Match":        {map[string]string{"Idempotency-Key": "k"}, mitigation(), http.StatusPreconditionRequired, store.CodeVersionConflict},
		"malformed If-Match":      {map[string]string{"If-Match": "abc", "Idempotency-Key": "k"}, mitigation(), http.StatusBadRequest, "COM-0002"},
		"missing Idempotency-Key": {map[string]string{"If-Match": `"1"`}, mitigation(), http.StatusBadRequest, "COM-0002"},
		"missing reason":          {ok, store.MitigationInput{ControlRef: "C", ExpiresAt: now.AddDate(0, 0, 10)}, http.StatusBadRequest, "COM-0002"},
		"stale version":           {map[string]string{"If-Match": `"7"`, "Idempotency-Key": "k"}, mitigation(), http.StatusPreconditionFailed, store.CodeVersionConflict},
	} {
		rec := serve(t, h, call{method: http.MethodPost, path: path, body: tc.body, user: ids.New(), duties: []string{DutySoDApprove}, headers: tc.headers})
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// The conflicted user cannot approve their own mitigation.
	rec := serve(t, h, call{method: http.MethodPost, path: path, body: mitigation(), user: user, duties: []string{DutySoDApprove}, headers: ok})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeSelfApproval {
		t.Fatalf("self approval: %d %s", rec.Code, rec.Body.String())
	}
	// Past the cap.
	long := mitigation()
	long.ExpiresAt = now.AddDate(0, 0, 400)
	rec = serve(t, h, call{method: http.MethodPost, path: path, body: long, user: ids.New(), duties: []string{DutySoDApprove}, headers: ok})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeExpiryTooLong {
		t.Fatalf("expiry cap: %d %s", rec.Code, rec.Body.String())
	}

	rec = serve(t, h, call{method: http.MethodPost, path: path, body: mitigation(), user: ids.New(), duties: []string{DutySoDApprove}, headers: ok})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body.String())
	}
	var got store.ConflictDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.State != domain.StateMitigated || got.ExpiresAt == nil {
		t.Fatalf("want MITIGATED with expiry, got %+v %v", got, err)
	}
	if f.lastCap != 365 || f.lastKey != "k-1" {
		t.Errorf("cap %d key %q not passed to the store", f.lastCap, f.lastKey)
	}
}

func TestAcceptRisk(t *testing.T) {
	f := &fakeStore{conflict: openConflict(ids.New())}
	h := router(newAPI(f, true))
	path := "/v1/sod/conflicts/" + f.conflict.ConflictID + "/accept-risk"
	hdr := map[string]string{"If-Match": `"1"`}
	rec := serve(t, h, call{method: http.MethodPost, path: path, body: AcceptRiskRequest{Reason: "x"}, user: ids.New(), duties: []string{DutySoDAdmin}, headers: hdr})
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != domain.CodeAcceptRisk {
		t.Fatalf("HIGH accept: %d %s", rec.Code, rec.Body.String())
	}
	if rec := serve(t, h, call{method: http.MethodPost, path: path, body: AcceptRiskRequest{}, user: ids.New(), duties: []string{DutySoDAdmin}, headers: hdr}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing reason: %d", rec.Code)
	}
	f.conflict.Severity = domain.SeverityMedium
	rec = serve(t, h, call{method: http.MethodPost, path: path, body: AcceptRiskRequest{Reason: "monthly review"}, user: ids.New(), duties: []string{DutySoDAdmin}, headers: hdr})
	if rec.Code != http.StatusOK || f.conflict.State != domain.StateAcceptedRisk {
		t.Fatalf("MEDIUM accept: %d %s", rec.Code, rec.Body.String())
	}
}

func breachCall(body any, duties ...string) call {
	return call{method: http.MethodPost, path: "/v1/security/incidents:breach-clocks", body: body, duties: duties}
}

func TestBreachClocks(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	// AC-48.19: aware 2026-11-02T09:00Z; the server clock reads 13 h later.
	aware := now.Add(-13 * time.Hour)
	body := map[string]any{"awareAt": aware.Format(time.RFC3339), "personalDataBreach": true, "regimes": []string{"CERT_IN", "GDPR"}}
	rec := serve(t, h, breachCall(body, DutyIncidentManage))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got BreachClocksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.AsOf.Equal(now) || len(got.Items) != 3 {
		t.Fatalf("want 3 clocks as of the server time, got %+v", got)
	}
	gdpr, subjects, certIn := got.Items[0], got.Items[1], got.Items[2]
	if gdpr.Regime != domain.RegimeGDPR || gdpr.DeadlineAt == nil || !gdpr.DeadlineAt.Equal(aware.Add(72*time.Hour)) || gdpr.Status != domain.ClockNotDue {
		t.Fatalf("GDPR authority clock: %+v", gdpr)
	}
	if subjects.DeadlineAt != nil || subjects.Status != domain.ClockNoFixedDeadline {
		t.Fatalf("GDPR Art. 34 has no fixed hour: %+v", subjects)
	}
	if certIn.Regime != domain.RegimeCERTIn || certIn.Status != domain.ClockOverdue {
		t.Fatalf("CERT-In 6 h clock is overdue at +13 h: %+v", certIn)
	}
}

func TestBreachClocksRefusals(t *testing.T) {
	h := router(newAPI(&fakeStore{}, true))
	aware := now.Add(-time.Hour).Format(time.RFC3339)
	for _, tc := range []struct {
		name   string
		body   any
		status int
		code   string
		field  string
	}{
		{"not a confirmed breach", map[string]any{"awareAt": aware, "personalDataBreach": false, "regimes": []string{"GDPR"}}, http.StatusUnprocessableEntity, domain.CodeBreachNotConfirmed, ""},
		{"missing awareAt", map[string]any{"personalDataBreach": true, "regimes": []string{"GDPR"}}, http.StatusBadRequest, codeInvalid, "awareAt"},
		{"awareAt in the future", map[string]any{"awareAt": now.Add(time.Hour).Format(time.RFC3339), "personalDataBreach": true, "regimes": []string{"GDPR"}}, http.StatusBadRequest, codeInvalid, "awareAt"},
		{"no regimes", map[string]any{"awareAt": aware, "personalDataBreach": true, "regimes": []string{}}, http.StatusBadRequest, codeInvalid, "regimes"},
		{"unknown regime", map[string]any{"awareAt": aware, "personalDataBreach": true, "regimes": []string{"NOWHERE"}}, http.StatusBadRequest, codeInvalid, "regimes"},
		{"US state window out of range", map[string]any{"awareAt": aware, "personalDataBreach": true, "regimes": []string{"US_STATE"}, "usStateWindowDays": 90}, http.StatusUnprocessableEntity, domain.CodeBreachStateDays, ""},
		{"unknown field", map[string]any{"awareAt": aware, "personalDataBreach": true, "regimes": []string{"GDPR"}, "now": aware}, http.StatusBadRequest, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serve(t, h, breachCall(tc.body, DutyIncidentManage))
			if rec.Code != tc.status {
				t.Fatalf("want %d, got %d: %s", tc.status, rec.Code, rec.Body.String())
			}
			if tc.code == "" {
				return
			}
			var p httpx.Problem
			if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Code != tc.code {
				t.Fatalf("want %s, got %s", tc.code, rec.Body.String())
			}
			if tc.field != "" && p.Errors[tc.field] == "" {
				t.Fatalf("want a %s field error, got %+v", tc.field, p.Errors)
			}
		})
	}
}

func TestMetricsCountsSimulations(t *testing.T) {
	a := newAPI(&fakeStore{}, true)
	serve(t, router(a), call{method: http.MethodPost, path: "/v1/sod/simulate", body: simulateReq(ids.New()), duties: []string{DutySecurityAdmin}})
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"compliance_sod_simulations_total 1", "compliance_sod_blocked_total 1"} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Errorf("metrics missing %q: %s", want, rec.Body.String())
		}
	}
}

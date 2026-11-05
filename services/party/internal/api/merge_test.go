package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	loserID        = "00000000-0000-0000-0000-0000000000cc"
	mergeRequestID = "00000000-0000-0000-0000-0000000000dd"
)

// fakeMerges records each call with the principal that reached it.
type fakeMerges struct {
	requests  []store.MergeRequestInput
	decisions []string
	callers   []tenant.Principal
	reasons   []string
	replayed  bool
	created   bool
	err       error
}

func (f *fakeMerges) record(ctx context.Context) {
	p, _ := tenant.FromContext(ctx)
	f.callers = append(f.callers, p)
}

func (f *fakeMerges) RequestMerge(ctx context.Context, in store.MergeRequestInput) (domain.MergeRequest, bool, error) {
	f.record(ctx)
	f.requests = append(f.requests, in)
	if f.err != nil {
		return domain.MergeRequest{}, false, f.err
	}
	return domain.MergeRequest{ID: mergeRequestID, SurvivorPartyID: in.SurvivorID, MergedPartyID: in.MergedID, Rules: in.Rules, Status: domain.MergePending, RequestedBy: "rita"}, !f.replayed, nil
}

func (f *fakeMerges) GetMergeRequest(ctx context.Context, id string) (domain.MergeRequest, error) {
	f.record(ctx)
	return domain.MergeRequest{ID: id, Status: domain.MergePending, RequestedBy: "rita"}, f.err
}

func (f *fakeMerges) DecideMerge(ctx context.Context, id string, approve bool, reason string) (domain.MergeRequest, error) {
	f.record(ctx)
	f.decisions = append(f.decisions, id)
	f.reasons = append(f.reasons, reason)
	if f.err != nil {
		return domain.MergeRequest{}, f.err
	}
	status := domain.MergeRejected
	if approve {
		status = domain.MergeApproved
	}
	return domain.MergeRequest{ID: id, Status: status, RequestedBy: "rita", Replayed: f.replayed}, nil
}

func mergeRouter(f *fakeMerges) (http.Handler, *api.API) {
	r := httpx.NewRouter()
	a := api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithMerge(f)
	a.Register(r)
	return r, a
}

func userCall(r http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "rita")
	req.Header.Set("X-Passage-Duties", "party.merge,tenant.administer")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func mergeCall(r http.Handler, id, body string, headers map[string]string) (int, string) {
	rec := userCall(r, http.MethodPost, "/v1/party/parties/"+id+":merge", body, headers)
	return rec.Code, rec.Body.String()
}

func TestMergeRoute_RecordsARequestForTheCallingUser(t *testing.T) {
	f := &fakeMerges{}
	r, _ := mergeRouter(f)
	code, body := mergeCall(r, knownID, `{"mergedPartyId":"`+loserID+`","rules":{"name":"newest"}}`, map[string]string{"Idempotency-Key": "k1"})
	if code != http.StatusAccepted || !strings.Contains(body, `"id":"`+mergeRequestID+`"`) || !strings.Contains(body, `"status":"pending"`) {
		t.Fatalf("got %d %s", code, body)
	}
	if len(f.requests) != 1 || f.requests[0].SurvivorID != knownID || f.requests[0].MergedID != loserID || f.requests[0].Rules["name"] != domain.Newest || f.requests[0].Key != "k1" || f.requests[0].Hash == "" {
		t.Fatalf("store got %+v", f.requests)
	}
	if p := f.callers[0]; p.UserID != "rita" || p.LegalEntityID != "" || !p.HasDuty(store.DutyMerge) || !tenant.IsTenantAdmin(p) || p.Service != "" {
		t.Fatalf("principal reaching the store = %+v", p)
	}
	mergeCall(r, knownID, `{"mergedPartyId":"`+loserID+`","rules":{"name":"newest"}}`, map[string]string{"Idempotency-Key": "k1"})
	mergeCall(r, knownID, `{"mergedPartyId":"`+loserID+`","rules":{"name":"keepLoser"}}`, map[string]string{"Idempotency-Key": "k1"})
	if f.requests[1].Hash != f.requests[0].Hash || f.requests[2].Hash == f.requests[0].Hash {
		t.Fatalf("hashes %q %q %q: same body same hash, other rules another hash", f.requests[0].Hash, f.requests[1].Hash, f.requests[2].Hash)
	}
}

func TestMergeRoute_Rejections(t *testing.T) {
	pending := httpx.NewProblem(http.StatusConflict, store.CodeMergePending, "pending")
	tests := []struct {
		name     string
		id       string
		body     string
		duties   string
		storeErr error
		want     int
		wantCode string
	}{
		{"self merge", knownID, `{"mergedPartyId":"` + knownID + `"}`, "", nil, 409, "PTY-MERGE-CONFLICT"},
		{"missing merged id", knownID, `{}`, "", nil, 422, "PTY-PARTY-INVALID"},
		{"malformed merged id", knownID, `{"mergedPartyId":"nope"}`, "", nil, 422, "PTY-PARTY-INVALID"},
		{"malformed survivor id", "nope", `{"mergedPartyId":"` + loserID + `"}`, "", nil, 422, "PTY-PARTY-INVALID"},
		{"unknown field in body", knownID, `{"mergedPartyId":"` + loserID + `","tenantId":"x"}`, "", nil, 400, ""},
		{"duty missing", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.write,tenant.administer", nil, 403, "AUTH-0003"},
		{"approver duty is not the request duty", knownID, `{"mergedPartyId":"` + loserID + `"}`, "party.merge.approve,tenant.administer", nil, 403, "AUTH-0003"},
		{"unknown survivor", knownID, `{"mergedPartyId":"` + loserID + `"}`, "", store.ErrNotFound, 404, "PTY-PARTY-NOT-FOUND"},
		{"unmergeable", knownID, `{"mergedPartyId":"` + loserID + `"}`, "", store.ErrNotMergeable, 409, "PTY-MERGE-CONFLICT"},
		{"role conflict", knownID, `{"mergedPartyId":"` + loserID + `"}`, "", store.ErrRoleConflict, 409, "PTY-MERGE-ROLE-CONFLICT"},
		{"already pending", knownID, `{"mergedPartyId":"` + loserID + `"}`, "", pending, 409, store.CodeMergePending},
		{"unknown rule", knownID, `{"mergedPartyId":"` + loserID + `","rules":{"name":"bogus"}}`, "", &domain.RuleError{Field: "name", Reason: "unknown rule"}, 422, "PTY-PARTY-INVALID"},
		{"legal-entity scoped caller", knownID, `{"mergedPartyId":"` + loserID + `"}`, "", store.ErrScopedPrincipal, 403, "AUTH-0003"},
		{"not tenant wide", knownID, `{"mergedPartyId":"` + loserID + `"}`, "", dbx.ErrNotTenantWide, 403, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMerges{err: tc.storeErr}
			r, _ := mergeRouter(f)
			headers := map[string]string{}
			if tc.duties != "" {
				headers["X-Passage-Duties"] = tc.duties
			}
			code, body := mergeCall(r, tc.id, tc.body, headers)
			if code != tc.want || !strings.Contains(body, tc.wantCode) {
				t.Fatalf("got %d %s, want %d %s", code, body, tc.want, tc.wantCode)
			}
			if tc.storeErr == nil && len(f.requests) != 0 {
				t.Fatalf("store reached on a request that must fail first: %+v", f.requests)
			}
		})
	}
}

func TestMergeRoute_ReadsARequest(t *testing.T) {
	f := &fakeMerges{}
	r, _ := mergeRouter(f)
	if rec := userCall(r, http.MethodGet, "/v1/party/merge-requests/"+mergeRequestID, "", map[string]string{"X-Passage-Duties": "party.read"}); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"requestedBy":"rita"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec := userCall(r, http.MethodGet, "/v1/party/merge-requests/"+mergeRequestID, "", map[string]string{"X-Passage-Duties": "party.merge"}); rec.Code != http.StatusForbidden {
		t.Fatalf("read without party.read: %d", rec.Code)
	}
}

type mergeRig struct {
	handler  http.Handler
	api      *api.API
	store    *fakeMerges
	workflow *tenant.ServiceSigner
}

func newMergeRig(t *testing.T, f *fakeMerges) *mergeRig {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("workflow-ts", key)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("party", map[string]ed25519.PublicKey{"workflow-ts": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	h, a := mergeRouter(f)
	return &mergeRig{handler: h, api: a, store: f, workflow: signer}
}

func (g *mergeRig) mint(t *testing.T, actor string) string {
	t.Helper()
	admin := tenant.Principal{TenantID: testkit.TenantID(1), UserID: actor, Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}}}
	token, err := g.workflow.MintDuties("party", admin, "", []string{store.DutyMergeExecute})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func (g *mergeRig) decide(token, action, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/party/merge-requests/"+mergeRequestID+":"+action, strings.NewReader(body))
	req.Header.Set(tenant.HeaderServiceToken, token)
	rec := httptest.NewRecorder()
	g.handler.ServeHTTP(rec, req)
	return rec
}

func TestMergeDecision_WorkflowTokenReachesTheStoreAsTheApprover(t *testing.T) {
	g := newMergeRig(t, &fakeMerges{})
	if rec := g.decide(g.mint(t, "ada"), "approve", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"approved"`) {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if rec := g.decide(g.mint(t, "ada"), "reject", `{"reason":"different supplier"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"rejected"`) {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	for _, p := range g.store.callers {
		if p.UserID != "ada" || p.Service != "workflow-ts" || p.LegalEntityID != "" || !p.HasDuty(store.DutyMergeExecute) || len(p.Duties) != 1 {
			t.Fatalf("principal reaching the store = %+v", p)
		}
	}
	if g.store.reasons[1] != "different supplier" {
		t.Fatalf("reasons %q", g.store.reasons)
	}
	metrics := httptest.NewRecorder()
	g.api.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"party_merges_total 1", `party_merge_requests_total{outcome="approved"} 1`, `party_merge_requests_total{outcome="rejected"} 1`} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Errorf("metrics lack %s", want)
		}
	}
}

func TestMergeDecision_ReplaysAreNotCountedAndTokensAreSingleUse(t *testing.T) {
	g := newMergeRig(t, &fakeMerges{replayed: true})
	token := g.mint(t, "ada")
	if rec := g.decide(token, "approve", ""); rec.Code != http.StatusOK {
		t.Fatalf("replayed decision: %d %s", rec.Code, rec.Body)
	}
	if rec := g.decide(token, "approve", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the same token twice: %d, want 401", rec.Code)
	}
	if len(g.store.decisions) != 1 {
		t.Fatalf("store reached %d times, want 1", len(g.store.decisions))
	}
	metrics := httptest.NewRecorder()
	g.api.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "party_merges_total 0") {
		t.Fatalf("a replay was counted as a merge")
	}
}

func TestMergeDecision_RefusesEveryoneButTheWorkflow(t *testing.T) {
	g := newMergeRig(t, &fakeMerges{})
	for name, headers := range map[string]map[string]string{
		"a person holding the execute duty": {"X-Passage-Duties": "party.merge.execute,tenant.administer"},
		"a merge approver":                  {"X-Passage-Duties": "party.merge.approve,tenant.administer"},
		"a party change approver":           {"X-Passage-Duties": "party.approve"},
	} {
		if rec := userCall(g.handler, http.MethodPost, "/v1/party/merge-requests/"+mergeRequestID+":approve", "", headers); rec.Code != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, rec.Code)
		}
	}
	scoped, err := g.workflow.Mint("party", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testkit.TenantID(9), UserID: "ada"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec := g.decide(scoped, "approve", ""); rec.Code != http.StatusForbidden {
		t.Errorf("an entity-scoped party.approve token: %d, want 403", rec.Code)
	}
	if len(g.store.decisions) != 0 {
		t.Fatalf("store reached by %+v", g.store.callers)
	}
}

func TestMergeDecision_StoreRefusalsPassThrough(t *testing.T) {
	for code, status := range map[string]int{store.CodeMergeSelfApproval: 403, store.CodeMergeDecided: 409, store.CodeMergeStale: 409, store.CodeMergeRequestNotFound: 404} {
		g := newMergeRig(t, &fakeMerges{err: httpx.NewProblem(status, code, "refused")})
		if rec := g.decide(g.mint(t, "rita"), "approve", ""); rec.Code != status || !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s: %d %s", code, rec.Code, rec.Body)
		}
	}
}

func TestMergeRoute_NotRegisteredWithoutStore(t *testing.T) {
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).Register(r)
	if code, _ := mergeCall(r, knownID, `{"mergedPartyId":"`+loserID+`"}`, nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d", code)
	}
}

package optimizer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

type fixture struct {
	client   *Client
	verifier *tenant.ServiceVerifier
	ctx      context.Context
	le       string
}

func newFixture(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, claims tenant.ServiceClaims)) fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("fieldservice", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("dispatch", map[string]ed25519.PublicKey{"fieldservice": pub})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		handler(w, r, claims)
	}))
	t.Cleanup(srv.Close)
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), UserID: "dispatcher", LegalEntityID: le})
	return fixture{client: New(srv.URL+"/", signer), verifier: verifier, ctx: ctx, le: le}
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func TestClient_SubmitCarriesOnlyTheCreateDuty(t *testing.T) {
	var seen tenant.ServiceClaims
	var key string
	var sent Request
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request, claims tenant.ServiceClaims) {
		seen, key = claims, r.Header.Get("Idempotency-Key")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/dispatch/runs" {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&sent)
		reply(w, http.StatusAccepted, map[string]any{"id": "run-1", "status": Queued})
	})
	run, err := f.client.Submit(f.ctx, f.le, "opt-1:0", Request{Technicians: []Technician{{ID: "t1", Home: 0}}, Travel: [][]int{{0}}, TimeLimitSeconds: 7})
	if err != nil || run.ID != "run-1" || run.Status != Queued {
		t.Fatalf("submit: %+v %v", run, err)
	}
	if seen.Aud != "dispatch" || seen.Iss != "fieldservice" || seen.Le != f.le || !slices.Equal(seen.Duties, []string{"dispatch.run.create"}) {
		t.Fatalf("claims %+v", seen)
	}
	if key != "opt-1:0" || len(sent.Technicians) != 1 || sent.TimeLimitSeconds != 7 {
		t.Fatalf("key %q body %+v", key, sent)
	}
}

func TestClient_GetCarriesOnlyTheReadDutyAndDecodesThePlan(t *testing.T) {
	var seen tenant.ServiceClaims
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request, claims tenant.ServiceClaims) {
		seen = claims
		if r.URL.Path != "/v1/dispatch/runs/run-1" {
			t.Errorf("path %s", r.URL.Path)
		}
		reply(w, http.StatusOK, map[string]any{"id": "run-1", "status": Succeeded, "result": map[string]any{
			"assignments": []map[string]any{{"workOrderId": "w1", "technicianId": "t1", "start": 540, "end": 600, "sequence": 1}},
			"unassigned":  []string{"w2"}, "solver": "greedy", "cpsatStatus": "UNKNOWN", "fallbackReason": "timeout", "elapsedSeconds": 1.5,
			"cost": map[string]any{"total": 42, "travelMinutes": 7},
		}})
	})
	run, err := f.client.Get(f.ctx, f.le, "run-1")
	if err != nil || run.Status != Succeeded || run.Result == nil {
		t.Fatalf("get: %+v %v", run, err)
	}
	r := run.Result
	if len(r.Assignments) != 1 || r.Assignments[0].Start != 540 || r.Unassigned[0] != "w2" || r.FallbackReason == nil || *r.FallbackReason != "timeout" || r.Cost.Total != 42 {
		t.Fatalf("result %+v", r)
	}
	if !slices.Equal(seen.Duties, []string{"dispatch.run.read"}) {
		t.Fatalf("duties %v", seen.Duties)
	}
}

func TestClient_FailuresAreClassified(t *testing.T) {
	status := http.StatusOK
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request, _ tenant.ServiceClaims) {
		reply(w, status, map[string]any{"code": "SVC-DSP-001", "detail": "pinned assignments are infeasible"})
	})
	for _, tc := range []struct {
		status    int
		permanent bool
	}{{http.StatusUnprocessableEntity, true}, {http.StatusBadRequest, true}, {http.StatusTooManyRequests, false}, {http.StatusBadGateway, false}, {http.StatusUnauthorized, false}} {
		status = tc.status
		_, err := f.client.Submit(f.ctx, f.le, "k", Request{})
		var refusal *Error
		if !errors.As(err, &refusal) || refusal.Status != tc.status || refusal.Permanent() != tc.permanent {
			t.Fatalf("status %d: %v", tc.status, err)
		}
		if tc.status == http.StatusUnprocessableEntity && refusal.Detail != "SVC-DSP-001 pinned assignments are infeasible" {
			t.Fatalf("detail %q", refusal.Detail)
		}
	}
	status = http.StatusNotFound
	if _, err := f.client.Get(f.ctx, f.le, "gone"); !errors.Is(err, ErrRunLost) {
		t.Fatalf("lost run: %v", err)
	}
	status = http.StatusOK
	if _, err := f.client.Get(f.ctx, f.le, "gone"); err == nil {
		t.Fatal("a body without a run id was accepted")
	}
}

func TestClient_UnconfiguredAndUnreachable(t *testing.T) {
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), UserID: "u", LegalEntityID: ids.New()})
	var none *Client
	if none.Configured() || New("", nil).Configured() {
		t.Fatal("a client without a URL or key reports itself configured")
	}
	if _, err := New("http://127.0.0.1:1", nil).Submit(ctx, ids.New(), "k", Request{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no signer: %v", err)
	}
	f := newFixture(t, func(http.ResponseWriter, *http.Request, tenant.ServiceClaims) {})
	down := New("http://127.0.0.1:1", f.client.signer)
	if _, err := down.Get(f.ctx, f.le, "x"); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unreachable service: %v", err)
	}
	var refusal *Error
	if _, err := f.client.Get(f.ctx, ids.New(), "x"); !errors.As(err, &refusal) || refusal.Status != http.StatusForbidden || !refusal.Permanent() {
		t.Fatalf("a legal entity outside the caller's scope: %v", err)
	}
	if _, err := f.client.Get(context.Background(), f.le, "x"); err == nil {
		t.Fatal("a call without a principal was sent")
	}
}

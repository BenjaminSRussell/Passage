package shopfloor

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	tenantID = "0195f0a0-0000-7000-8000-000000000001"
	entityID = "0195f0a0-0000-7000-8000-0000000000e1"
)

func signer(t *testing.T) (*tenant.ServiceSigner, *tenant.ServiceVerifier) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("processmfg", priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, tenant.NewServiceVerifierKids("shopfloor", map[string]map[string]ed25519.PublicKey{"processmfg": {"": pub}})
}

// The call carries the releasing user as act, the one create duty and the order's legal entity, and the
// idempotency key; a 200 (replay) is as good as a 201.
func TestCreateRecipeSignsAsTheReleasingUser(t *testing.T) {
	s, v := signer(t)
	for _, status := range []int{http.StatusCreated, http.StatusOK} {
		srv := httptest.NewServer(tenant.MiddlewareWith(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok || p.TenantID != tenantID || p.UserID != "planner-1" || !p.HasDuty(DutyCreate) || p.LegalEntityID != entityID ||
				r.Header.Get("Idempotency-Key") != "batch-order-recipe-o1" || r.URL.Path != "/v1/shopfloor/control-recipes" {
				t.Errorf("principal %+v ok=%v key %q path %s", p, ok, r.Header.Get("Idempotency-Key"), r.URL.Path)
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"id":"0195f0a0-0000-7000-8000-0000000000c1"}`))
		})))
		c, err := New(srv.URL+"/", s, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "planner-1", LegalEntityID: entityID})
		id, err := c.CreateRecipe(ctx, entityID, "batch-order-recipe-o1", RecipeRequest{OrderID: "o1", Destination: "kiosk", Recipe: map[string]any{}})
		srv.Close()
		if err != nil || id != "0195f0a0-0000-7000-8000-0000000000c1" {
			t.Fatalf("status %d: %q %v", status, id, err)
		}
	}
}

func TestCreateRecipeReportsRefusalsAndMissingPieces(t *testing.T) {
	s, _ := signer(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"SF-0105","detail":"the resource is unknown"}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, s, nil)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "planner-1", LegalEntityID: entityID})
	if _, err := c.CreateRecipe(ctx, entityID, "k", RecipeRequest{}); err == nil || !strings.Contains(err.Error(), "the resource is unknown") {
		t.Fatalf("refusal: %v", err)
	}
	if _, err := c.CreateRecipe(context.Background(), entityID, "k", RecipeRequest{}); err == nil {
		t.Fatal("a call with no principal was sent")
	}
	if _, err := New("", s, nil); err == nil {
		t.Fatal("a client with no URL")
	}
	if _, err := New("http://x", nil, nil); err == nil {
		t.Fatal("a client with no signer")
	}
}

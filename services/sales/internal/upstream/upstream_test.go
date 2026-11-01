package upstream

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

type harness struct {
	client *Client
	pub    ed25519.PublicKey
	ctx    context.Context
	le     string
}

func newHarness(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, h *harness)) *harness {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("sales", priv)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{le: ids.New(), pub: pub}
	h.ctx = tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "rep", LegalEntityID: h.le, Duties: testkit.ScopeDuties(h.le)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler(w, r, h) }))
	t.Cleanup(srv.Close)
	h.client = New(srv.URL, srv.URL, signer)
	return h
}

func (h *harness) claims(t *testing.T, r *http.Request, audience string) tenant.ServiceClaims {
	t.Helper()
	c, err := tenant.NewServiceVerifier(audience, map[string]ed25519.PublicKey{"sales": h.pub}).Verify(r.Header.Get(tenant.HeaderServiceToken))
	if err != nil {
		t.Fatalf("service token: %v", err)
	}
	return c
}

func TestCheck_AsksProductWithOnlyTheReadDutyAndMapsTheAnswer(t *testing.T) {
	product, configuration := ids.New(), ids.New()
	tests := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"valid", http.StatusOK, `{"status":"valid","belongsToProduct":true}`, nil},
		{"another product", http.StatusOK, `{"status":"valid","belongsToProduct":false}`, ErrConfigurationInvalid},
		{"superseded", http.StatusOK, `{"status":"superseded","belongsToProduct":true}`, ErrConfigurationInvalid},
		{"unknown", http.StatusNotFound, `{}`, ErrConfigurationInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen tenant.ServiceClaims
			var path string
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
				seen, path = h.claims(t, r, "product"), r.URL.RequestURI()
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			err := h.client.Check(h.ctx, h.le, configuration, product)
			if !errors.Is(err, tt.want) || (tt.want == nil && err != nil) {
				t.Fatalf("got %v want %v", err, tt.want)
			}
			if len(seen.Duties) != 1 || seen.Duties[0] != "product.configuration.read" || seen.Le != h.le || seen.Aud != "product" ||
				path != "/v1/product/saved-configurations/"+configuration+"?productId="+product {
				t.Fatalf("call: claims=%+v path=%s", seen, path)
			}
		})
	}
}

func TestCheck_UnavailableIsNotInvalid(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request, _ *harness) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	if err := h.client.Check(h.ctx, h.le, ids.New(), ids.New()); err == nil || errors.Is(err, ErrConfigurationInvalid) {
		t.Fatalf("a product outage must not read as an invalid configuration: %v", err)
	}
	var unconfigured *Client
	if err := unconfigured.Check(context.Background(), "", "", ""); err == nil {
		t.Fatal("an unconfigured client must refuse")
	}
}

func TestCreateForLine_IsKeyedByTheLineAndMapsTheAnswer(t *testing.T) {
	line, orderID := ids.New(), ids.New()
	tests := []struct {
		name        string
		status      int
		wantCreated bool
		wantErr     error
	}{
		{"created", http.StatusCreated, true, nil},
		{"already built for the line", http.StatusOK, false, nil},
		{"line built for another configuration", http.StatusConflict, false, ErrOriginConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen tenant.ServiceClaims
			var key string
			h := newHarness(t, func(w http.ResponseWriter, r *http.Request, h *harness) {
				seen, key = h.claims(t, r, "production"), r.Header.Get("Idempotency-Key")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"id":"` + orderID + `"}`))
			})
			id, created, err := h.client.CreateForLine(h.ctx, ProductionRequest{LegalEntityID: h.le, OriginID: line, ConfigurationID: ids.New()})
			if !errors.Is(err, tt.wantErr) || created != tt.wantCreated || (tt.wantErr == nil && id != orderID) {
				t.Fatalf("id=%s created=%v err=%v", id, created, err)
			}
			if key != "sales-line-"+line || len(seen.Duties) != 1 || seen.Duties[0] != "production.order.create-from-sales" || seen.Le != h.le {
				t.Fatalf("key=%s claims=%+v", key, seen)
			}
		})
	}
}

func TestCreateForLine_ServerErrorIsAnErrorNotAnOrder(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, _ *http.Request, _ *harness) { w.WriteHeader(http.StatusBadGateway) })
	if _, _, err := h.client.CreateForLine(h.ctx, ProductionRequest{LegalEntityID: h.le, OriginID: ids.New()}); err == nil || errors.Is(err, ErrOriginConflict) {
		t.Fatalf("got %v", err)
	}
	h.client.signer = nil
	if _, _, err := h.client.CreateForLine(h.ctx, ProductionRequest{LegalEntityID: h.le, OriginID: ids.New()}); err == nil {
		t.Fatal("an unsigned call must be refused")
	}
}

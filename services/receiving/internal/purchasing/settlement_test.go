package purchasing

import (
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func serveBody(t *testing.T, status int) (*Client, *http.Request, *string) {
	t.Helper()
	var got http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = *r.Clone(r.Context())
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, nil).WithSigner(signerFor(t)), &got, &body
}

func signerFor(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestMarkInvoiced_SignsWithTheServiceGrantAndSendsTheSettlement(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("receiving", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("purchasing", map[string]ed25519.PublicKey{"receiving": pub})
	client, got, body := serveBody(t, http.StatusOK)
	client.WithSigner(signer)

	err = client.MarkInvoiced(testCtx(), legalEntityID, orderID, 3, 2, "settlement-9", decimal.RequireFromString("1000.50"))

	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPut || got.URL.Path != "/v1/procurement/purchase-orders/"+orderID+"/lines/3/invoicing-plan/rows/2/settlement" {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	if *body != `{"settlementRef":"settlement-9","amount":"1000.5"}` {
		t.Fatalf("body %s", *body)
	}
	for _, h := range []string{"X-Passage-Duties", "X-Passage-User", "X-Passage-Tenant", "X-Passage-Legal-Entity"} {
		if got.Header.Get(h) != "" {
			t.Fatalf("%s must not be sent with a service token", h)
		}
	}
	claims, err := verifier.Verify(got.Header.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Aud != "purchasing" || claims.Act != userID || claims.Le != legalEntityID || len(claims.Duties) != 1 || claims.Duties[0] != "procurement.invoicing-plan.settle" {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func TestMarkInvoiced_WithoutASignerSendsNothing(t *testing.T) {
	client, got, _ := serveBody(t, http.StatusOK)
	client.signer = nil

	err := client.MarkInvoiced(testCtx(), legalEntityID, orderID, 1, 1, "s", decimal.NewFromInt(1))

	if err == nil || errors.Is(err, domain.ErrPlanRowMarkRejected) || got.Method != "" {
		t.Fatalf("an unsigned call must fail locally and never forward the user: err %v, method %q", err, got.Method)
	}
}

func TestMarkInvoiced_SeparatesRefusalsFromOutages(t *testing.T) {
	tests := []struct {
		status   int
		rejected bool
	}{
		{http.StatusConflict, true}, {http.StatusNotFound, true}, {http.StatusForbidden, true}, {http.StatusUnprocessableEntity, true},
		{http.StatusInternalServerError, false}, {http.StatusServiceUnavailable, false}, {http.StatusTooManyRequests, false}, {http.StatusRequestTimeout, false},
	}
	for _, tc := range tests {
		client, _, _ := serveBody(t, tc.status)

		err := client.MarkInvoiced(testCtx(), legalEntityID, orderID, 1, 1, "s", decimal.NewFromInt(1))

		if err == nil || errors.Is(err, domain.ErrPlanRowMarkRejected) != tc.rejected {
			t.Errorf("status %d: err %v, want rejected=%v", tc.status, err, tc.rejected)
		}
	}
}

func TestMarkInvoiced_TransportFailureAndMissingPrincipal(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	err := New(url, nil).WithSigner(signerFor(t)).MarkInvoiced(testCtx(), legalEntityID, orderID, 1, 1, "s", decimal.NewFromInt(1))
	if err == nil || errors.Is(err, domain.ErrPlanRowMarkRejected) {
		t.Fatalf("unreachable purchasing = %v", err)
	}
	client, got, _ := serveBody(t, http.StatusOK)
	if err := client.MarkInvoiced(t.Context(), legalEntityID, orderID, 1, 1, "s", decimal.NewFromInt(1)); err == nil || got.Method != "" {
		t.Fatalf("a call without a principal must not reach purchasing: %v", err)
	}
}

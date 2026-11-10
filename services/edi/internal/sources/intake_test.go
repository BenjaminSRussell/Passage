package sources

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
)

func TestPlaceOrder_SignsOnlyTheIntakeDutyAndSendsTheMessageIdAsKey(t *testing.T) {
	signer, verifier := signerAndVerifier(t, "sales")
	var claims tenant.ServiceClaims
	var sent domain.SalesOrderRequest
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		if claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil || r.URL.Path != "/v1/sales/orders" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key = r.Header.Get("Idempotency-Key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + id + `","version":1}`))
	}))
	defer srv.Close()
	c := New(signer, srv.URL, srv.URL, srv.URL, nil)
	order := domain.SalesOrderRequest{LegalEntityID: le, Channel: "EDI", ExternalRef: "p:PO1", Lines: []domain.SalesOrderLine{{ProductID: id, Quantity: "1"}}}
	ref, err := c.PlaceOrder(principal(le), le, "msg-1", order)
	if err != nil || ref.ID != id {
		t.Fatalf("place: %v %+v", err, ref)
	}
	if !slices.Equal(claims.Duties, []string{DutyOrderIntake}) || claims.Le != le || claims.Tid != tid || claims.Act != "user-1" {
		t.Errorf("token must carry only %s for the profile legal entity: %+v", DutyOrderIntake, claims)
	}
	if key != "msg-1" || sent.ExternalRef != "p:PO1" || sent.Channel != "EDI" || len(sent.Lines) != 1 {
		t.Errorf("request: key=%q body=%+v", key, sent)
	}
}

func TestPlaceOrder_SeparatesRefusalsFromOutages(t *testing.T) {
	status, body := http.StatusOK, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	signer, _ := signerAndVerifier(t, "sales")
	c := New(signer, srv.URL, srv.URL, srv.URL, nil)
	place := func() error { _, err := c.PlaceOrder(principal(le), le, "k", domain.SalesOrderRequest{}); return err }

	status, body = http.StatusUnprocessableEntity, `{"code":"SLS-0007","detail":"payload differs","errors":{"lines[0].unit":"must be 1 to 10 characters"}}`
	var refused *RefusedError
	if err := place(); !errors.As(err, &refused) || refused.Status != 422 || refused.Code != "SLS-0007" ||
		refused.Detail != "payload differs; lines[0].unit must be 1 to 10 characters" {
		t.Errorf("a 422 is a refusal to park: %v", err)
	}
	status, body = http.StatusForbidden, `{}`
	if err := place(); !errors.Is(err, ErrForbidden) || errors.As(err, &refused) && refused.Status == 403 {
		t.Errorf("a 403 is a grant problem, not a refusal: %v", err)
	}
	for _, s := range []int{http.StatusInternalServerError, http.StatusTooManyRequests, http.StatusBadGateway} {
		status, body = s, `{}`
		if err := place(); err == nil || errors.As(err, &refused) || errors.Is(err, ErrForbidden) {
			t.Errorf("%d must be retryable, not a refusal: %v", s, err)
		}
	}
	status, body = http.StatusCreated, `{"version":1}`
	if err := place(); err == nil {
		t.Error("an answer without an order id is an error")
	}
}

func TestPlaceOrder_RefusesUnconfiguredSalesAndMissingPrincipal(t *testing.T) {
	signer, _ := signerAndVerifier(t, "sales")
	if _, err := New(signer, "", "", "", nil).PlaceOrder(principal(le), le, "k", domain.SalesOrderRequest{}); err == nil {
		t.Error("no sales URL")
	}
	if _, err := New(signer, "", "", "http://sales", nil).PlaceOrder(t.Context(), le, "k", domain.SalesOrderRequest{}); err == nil {
		t.Error("no principal")
	}
}

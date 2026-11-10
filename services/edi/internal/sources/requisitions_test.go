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
)

func TestCreateRequisition_SignsOnlyTheRequesterDutyAsTheRequesterAndKeysTheSession(t *testing.T) {
	signer, verifier := signerAndVerifier(t, "purchasing")
	var claims tenant.ServiceClaims
	var sent RequisitionRequest
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		if claims, err = verifier.Verify(r.Header.Get(tenant.HeaderServiceToken)); err != nil || r.URL.Path != "/v1/procurement/requisitions" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		key = r.Header.Get("Idempotency-Key")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"` + id + `","status":"DRAFT"}`))
	}))
	defer srv.Close()
	c := New(signer, srv.URL, srv.URL, srv.URL, nil).WithPurchasing(srv.URL + "/")
	body := RequisitionRequest{LegalEntityID: le, Currency: "USD", Lines: []RequisitionLine{{Description: "Seal kit", Category: "31161500", Qty: "2", Unit: "C62", EstimatedUnitPrice: "25", RequestedDate: "2026-11-20"}}}
	ref, err := c.CreateRequisition(principal(le), le, "punchout:s1", body)
	if err != nil || ref.ID != id || ref.Status != "DRAFT" {
		t.Fatalf("create: %v %+v", err, ref)
	}
	if !slices.Equal(claims.Duties, []string{DutyRequisitionCreate}) || claims.Le != le || claims.Act != "user-1" || claims.Aud != "purchasing" {
		t.Errorf("the token carries only the requester's duty, for the requester: %+v", claims)
	}
	if key != "punchout:s1" || len(sent.Lines) != 1 || sent.Lines[0].Description != "Seal kit" || sent.Currency != "USD" {
		t.Errorf("request: key=%q body=%+v", key, sent)
	}
}

func TestCreateRequisition_SeparatesRefusalsFromOutages(t *testing.T) {
	status, body := http.StatusOK, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	signer, _ := signerAndVerifier(t, "purchasing")
	c := New(signer, srv.URL, srv.URL, srv.URL, nil).WithPurchasing(srv.URL)
	create := func() error { _, err := c.CreateRequisition(principal(le), le, "k", RequisitionRequest{}); return err }

	status, body = http.StatusUnprocessableEntity, `{"code":"PRC-REQ-001","detail":"invalid","errors":{"lines[0].unit":"is required"}}`
	var refused *RequisitionRefusedError
	if err := create(); !errors.As(err, &refused) || refused.Code != "PRC-REQ-001" || refused.Detail != "invalid; lines[0].unit is required" {
		t.Errorf("a 422 is a refusal: %v", err)
	}
	status, body = http.StatusForbidden, `{}`
	if err := create(); !errors.Is(err, ErrForbidden) {
		t.Errorf("a 403 is a grant problem: %v", err)
	}
	for _, s := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		status, body = s, `{}`
		if err := create(); err == nil || errors.As(err, &refused) || errors.Is(err, ErrForbidden) {
			t.Errorf("%d must be retryable: %v", s, err)
		}
	}
	// Without a purchasing URL the call is not configured.
	if _, err := New(signer, srv.URL, srv.URL, srv.URL, nil).CreateRequisition(principal(le), le, "k", RequisitionRequest{}); err == nil {
		t.Error("an unconfigured purchasing must be an error")
	}
}

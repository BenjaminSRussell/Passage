package billingclient

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func signedEnv(url string) func(string) string {
	seed := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize))
	m := map[string]string{"BILLING_URL": url, tenant.EnvServiceName: "receivables", tenant.EnvServiceSigningKey: seed}
	return func(k string) string { return m[k] }
}

func TestTaxBasis_SumsBilledLessCreditedTaxPerCodeWithTheReadDuty(t *testing.T) {
	var path, token string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, token = r.URL.Path, r.Header.Get(tenant.HeaderServiceToken)
		_, _ = w.Write([]byte(`{"number":"INV-1","legalEntityId":"le","status":"POSTED","type":"SHIPMENT","lines":[
			{"taxCode":"DE19","tax":"190.00","creditedTax":"38.00"},{"taxCode":"DE19","tax":"10.00"},{"taxCode":"DE7","tax":"7.00"},{"tax":"0"}]}`))
	}))
	defer srv.Close()
	c, err := FromEnv(signedEnv(srv.URL))
	if err != nil || c == nil {
		t.Fatalf("client: %v %v", c, err)
	}
	le, id := ids.New(), ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	b, err := c.TaxBasis(ctx, le, id)
	if err != nil || path != "/v1/billing/invoices/"+id || token == "" || b.Number != "INV-1" ||
		!b.Remaining["DE19"].Equal(decimal.RequireFromString("162.00")) || !b.Remaining["DE7"].Equal(decimal.RequireFromString("7.00")) || len(b.Remaining) != 2 {
		t.Fatalf("basis %+v path %s token %q err %v", b, path, token, err)
	}
}

func TestTaxBasis_MissingCreditNoteAndErrorsAreDistinguished(t *testing.T) {
	status, body := http.StatusNotFound, `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, _ := FromEnv(signedEnv(srv.URL))
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	if _, err := c.TaxBasis(ctx, le, ids.New()); err != ErrNotFound {
		t.Fatalf("404: %v", err)
	}
	status, body = http.StatusOK, `{"number":"CN-1","type":"CREDIT_NOTE","lines":[]}`
	if _, err := c.TaxBasis(ctx, le, ids.New()); err != ErrNotFound {
		t.Fatalf("a credit note is not an invoice: %v", err)
	}
	status = http.StatusBadGateway
	if _, err := c.TaxBasis(ctx, le, ids.New()); err == nil || err == ErrNotFound {
		t.Fatalf("502: %v", err)
	}
}

func TestFromEnv_UnsetIsNilAndURLWithoutKeyIsRefused(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if c, err := FromEnv(func(k string) string {
		if k == "BILLING_URL" {
			return "http://billing"
		}
		return ""
	}); c != nil || err == nil {
		t.Fatalf("no signing key: %v %v", c, err)
	}
}

func TestReservations_UseTheWriteDutyKeyAndPathsAndMapBillingsRefusals(t *testing.T) {
	var method, path, key, body string
	status, reply := http.StatusCreated, `{"id":"r1","status":"RESERVED"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, key = r.Method, r.URL.Path, r.Header.Get("Idempotency-Key")
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		if r.Header.Get(tenant.HeaderServiceToken) == "" {
			t.Errorf("unsigned call")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	c, _ := FromEnv(signedEnv(srv.URL))
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "ar-clerk"})
	res, err := c.Reserve(ctx, le, "inv-1", "rma-credit:k", "RMA-1", []Line{{LineNo: 3, Qty: decimal.RequireFromString("1.5")}})
	if err != nil || res.ID != "r1" || method != http.MethodPost || path != "/v1/billing/invoices/inv-1/credit-reservations" || key != "rma-credit:k" ||
		body != `{"lines":[{"lineNo":3,"qty":"1.5"}],"reference":"RMA-1"}` {
		t.Fatalf("reserve: %+v %v %s %s %q %s", res, err, method, path, key, body)
	}
	status, reply = http.StatusOK, `{}`
	if err := c.Commit(ctx, le, "r1"); err != nil || path != "/v1/billing/credit-reservations/r1:commit" || key != "" {
		t.Fatalf("commit: %v %s", err, path)
	}
	if err := c.Release(ctx, le, "r1"); err != nil || path != "/v1/billing/credit-reservations/r1:release" {
		t.Fatalf("release: %v %s", err, path)
	}
	cases := []struct {
		status int
		reply  string
		call   func() error
		want   error
	}{
		{http.StatusUnprocessableEntity, `{"code":"BIL-0005"}`, func() error { _, e := c.Reserve(ctx, le, "i", "k", "r", nil); return e }, ErrNoRoom},
		{http.StatusNotFound, `{"code":"BIL-0100"}`, func() error { _, e := c.Reserve(ctx, le, "i", "k", "r", nil); return e }, ErrNotFound},
		{http.StatusConflict, `{"code":"idempotency_in_progress"}`, func() error { _, e := c.Reserve(ctx, le, "i", "k", "r", nil); return e }, ErrBusy},
		{http.StatusConflict, `{"code":"reservation_state"}`, func() error { return c.Commit(ctx, le, "r1") }, ErrReleased},
		{http.StatusConflict, `{"code":"reservation_state"}`, func() error { return c.Release(ctx, le, "r1") }, ErrConsumed},
	}
	for _, tc := range cases {
		status, reply = tc.status, tc.reply
		if err := tc.call(); !errors.Is(err, tc.want) {
			t.Fatalf("%d %s: got %v want %v", tc.status, tc.reply, err, tc.want)
		}
	}
	status, reply = http.StatusBadGateway, `{}`
	if _, err := c.Reserve(ctx, le, "i", "k", "r", nil); err == nil || errors.Is(err, ErrNoRoom) || errors.Is(err, ErrNotFound) {
		t.Fatalf("502 must be a plain failure: %v", err)
	}
}

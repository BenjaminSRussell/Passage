package budget

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type fakeConverter struct {
	calls [][4]string
	err   error
}

func (f *fakeConverter) Convert(_ context.Context, le, req, po, line string) error {
	f.calls = append(f.calls, [4]string{le, req, po, line})
	return f.err
}

func TestHook_ConvertsOnlyLinesBornFromARequisitionAndSurvivesFailure(t *testing.T) {
	f := &fakeConverter{err: errors.New("budgeting is down")}
	Hook(f)(context.Background(), []store.PurchaseOrder{{ID: "po1", LegalEntityID: "le", Lines: []store.PurchaseOrderLine{
		{ID: "l1", RequisitionLineID: "r1"}, {ID: "l2"}, {ID: "l3", RequisitionLineID: "r3"},
	}}})
	if len(f.calls) != 2 || f.calls[0] != [4]string{"le", "r1", "po1", "l1"} || f.calls[1] != [4]string{"le", "r3", "po1", "l3"} {
		t.Fatalf("calls = %v, want the two requisition-backed lines, in spite of the first failing", f.calls)
	}
}

func TestClient_SignsOnlyTheConvertDutyAndTreatsNothingToConvertAsDone(t *testing.T) {
	var gotPath, gotKey, gotBody string
	status, body := http.StatusOK, `{}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		if r.Header.Get(tenant.HeaderServiceToken) == "" {
			t.Error("request carries no service token")
		}
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		gotBody = string(b[:n])
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	signer, err := tenant.NewServiceSigner("purchasing", testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, nil, signer)
	le, req, po, line := ids.New(), ids.New(), ids.New(), ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "buyer", Duties: map[string]struct{}{}})
	if err := c.Convert(ctx, le, req, po, line); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/v1/budget/encumbrances:convert" || gotKey != "po-line-convert-"+line || !strings.Contains(gotBody, `"requisitionLineId":"`+req+`"`) {
		t.Fatalf("path %q key %q body %q", gotPath, gotKey, gotBody)
	}
	status = http.StatusNotFound
	if err := c.Convert(ctx, le, req, po, line); err != nil {
		t.Fatalf("404 means nothing was reserved: %v", err)
	}
	status, body = http.StatusConflict, `{"code":"BUD-0071"}`
	if err := c.Convert(ctx, le, req, po, line); err != nil {
		t.Fatalf("already converted: %v", err)
	}
	status, body = http.StatusConflict, `{"code":"BUD-0072"}`
	if err := c.Convert(ctx, le, req, po, line); err == nil {
		t.Fatal("a PO line that already carries a reservation must be reported")
	}
	status = http.StatusServiceUnavailable
	if err := c.Convert(ctx, le, req, po, line); err == nil {
		t.Fatal("a 503 must be reported")
	}
	if err := New(srv.URL, nil, nil).Convert(ctx, le, req, po, line); err == nil {
		t.Fatal("without a signing key the call must fail, not go out unsigned")
	}
}

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

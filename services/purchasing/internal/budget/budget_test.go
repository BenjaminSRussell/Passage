package budget

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

type fakeQueue struct {
	calls []store.BudgetCall
}

func (q *fakeQueue) BudgetCallsFor(_ context.Context, po, req string) ([]store.BudgetCall, error) {
	var out []store.BudgetCall
	for _, c := range q.calls {
		if (po != "" && c.PurchaseOrderID == po) || (req != "" && c.RequisitionID == req) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (q *fakeQueue) ListBudgetCalls(_ context.Context, status string, _ int) ([]store.BudgetCall, error) {
	var out []store.BudgetCall
	for _, c := range q.calls {
		if status == "" || c.Status == status {
			out = append(out, c)
		}
	}
	return out, nil
}

func (q *fakeQueue) RecordBudgetCall(_ context.Context, id string, err error) error {
	for i := range q.calls {
		if q.calls[i].ID == id {
			q.calls[i].Attempts++
			if err == nil {
				q.calls[i].Status = store.BudgetCallDone
			}
		}
	}
	return nil
}

type fakePort struct {
	converts [][4]string
	releases []string
	err      error
}

func (f *fakePort) Convert(_ context.Context, le, req, po, line string) error {
	f.converts = append(f.converts, [4]string{le, req, po, line})
	return f.err
}

func (f *fakePort) ReleaseLine(_ context.Context, _, _, line string) error {
	f.releases = append(f.releases, line)
	return f.err
}

func (f *fakePort) CheckRequisition(context.Context, store.Requisition) (Check, error) {
	return Check{}, nil
}

func TestHook_MakesTheQueuedCallsOfACreatedOrderAndAFailureStaysPending(t *testing.T) {
	q := &fakeQueue{calls: []store.BudgetCall{
		{ID: "c1", Kind: store.BudgetCallConvert, Status: store.BudgetCallPending, LegalEntityID: "le", RequisitionLineID: "r1", PurchaseOrderID: "po1", PurchaseOrderLineID: "l1"},
		{ID: "c2", Kind: store.BudgetCallConvert, Status: store.BudgetCallPending, LegalEntityID: "le", RequisitionLineID: "r3", PurchaseOrderID: "po1", PurchaseOrderLineID: "l3"},
		{ID: "c3", Kind: store.BudgetCallConvert, Status: store.BudgetCallDone, LegalEntityID: "le", RequisitionLineID: "r4", PurchaseOrderID: "po1", PurchaseOrderLineID: "l4"},
	}}
	p := &fakePort{err: errors.New("budgeting is down")}
	Hook(p, q)(context.Background(), []store.PurchaseOrder{{ID: "po1", LegalEntityID: "le"}})
	if len(p.converts) != 2 || p.converts[0] != [4]string{"le", "r1", "po1", "l1"} || p.converts[1] != [4]string{"le", "r3", "po1", "l3"} {
		t.Fatalf("converts = %v, want the two pending calls in spite of the first failing", p.converts)
	}
	if q.calls[0].Status != store.BudgetCallPending || q.calls[0].Attempts != 1 {
		t.Fatalf("a failed call must stay pending with an attempt noted: %+v", q.calls[0])
	}
	p.err = nil
	res, err := Retry(context.Background(), p, q)
	if err != nil || res.Tried != 2 || res.Done != 2 || res.Failed != 0 || q.calls[0].Status != store.BudgetCallDone || q.calls[1].Status != store.BudgetCallDone {
		t.Fatalf("retry = %+v, %v; calls %+v", res, err, q.calls)
	}
	if res, _ := Retry(context.Background(), p, q); res.Tried != 0 {
		t.Fatalf("nothing is pending any more: %+v", res)
	}
}

func TestFlush_MakesOnlyTheRequisitionsPendingReleases(t *testing.T) {
	q := &fakeQueue{calls: []store.BudgetCall{
		{ID: "a", Kind: store.BudgetCallRelease, Status: store.BudgetCallPending, RequisitionID: "req1", RequisitionLineID: "l1"},
		{ID: "b", Kind: store.BudgetCallRelease, Status: store.BudgetCallPending, RequisitionID: "req2", RequisitionLineID: "l2"},
		{ID: "c", Kind: store.BudgetCallRelease, Status: store.BudgetCallDone, RequisitionID: "req1", RequisitionLineID: "l3"},
	}}
	p := &fakePort{}
	Flush(context.Background(), p, q, "req1")
	if len(p.releases) != 1 || p.releases[0] != "l1" {
		t.Fatalf("releases = %v, want only line l1", p.releases)
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

func TestClient_ChecksARequisitionAndReleasesALine(t *testing.T) {
	var gotPath, gotKey, gotBody string
	status, body := http.StatusOK, `{"result":"FAIL","lines":[{"lineId":"l1","result":"FAIL","overPct":"12.5"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("Idempotency-Key")
		b := make([]byte, 2048)
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
	le := ids.New()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: ids.New(), LegalEntityID: le, UserID: "approver", Duties: map[string]struct{}{}})
	req := store.Requisition{ID: ids.New(), Number: "PR-9", LegalEntityID: le, Currency: "USD", Version: 5, Lines: []store.RequisitionLine{
		{ID: "l1", Status: "OPEN", Amount: decimal.NewFromInt(900), GLAccount: "6100", CostCenter: "CC-20", RequestedDate: domain.Day{Time: time.Date(2027, 3, 15, 0, 0, 0, 0, time.UTC)}},
		{ID: "l2", Status: "OPEN", Amount: decimal.NewFromInt(50)},
		{ID: "l3", Status: "CANCELLED", Amount: decimal.NewFromInt(70), GLAccount: "6100", CostCenter: "CC-20"},
	}}
	res, err := c.CheckRequisition(ctx, req)
	if err != nil || !res.Failed() || len(res.Lines) != 1 {
		t.Fatalf("check = %+v, %v", res, err)
	}
	if gotPath != "/v1/budget/requisition-checks" || gotKey != "requisition-check-"+req.ID+"-5" || !strings.Contains(gotBody, `"glAccount":"6100"`) ||
		!strings.Contains(gotBody, `"requestedDate":"2027-03-15"`) || strings.Contains(gotBody, `"l2"`) || strings.Contains(gotBody, `"l3"`) {
		t.Fatalf("path %q key %q body %s; want only the open line with an account and cost center", gotPath, gotKey, gotBody)
	}
	none := store.Requisition{ID: ids.New(), LegalEntityID: le, Lines: []store.RequisitionLine{{ID: "x", Status: "OPEN"}}}
	if res, err := c.CheckRequisition(ctx, none); err != nil || res.Result != "NOT_CONTROLLED" {
		t.Fatalf("a requisition with no controlled line = %+v, %v; want NOT_CONTROLLED without a call", res, err)
	}
	status = http.StatusUnprocessableEntity
	if _, err := c.CheckRequisition(ctx, req); err == nil {
		t.Fatal("a 422 from budgeting must be reported")
	}
	status, body = http.StatusOK, `{}`
	if err := c.ReleaseLine(ctx, le, req.ID, "l1"); err != nil || gotPath != "/v1/budget/requisition-checks:release-line" || gotKey != "requisition-line-release-l1" {
		t.Fatalf("release: %v path %q key %q", err, gotPath, gotKey)
	}
	status = http.StatusNotFound
	if err := c.ReleaseLine(ctx, le, req.ID, "l1"); err != nil {
		t.Fatalf("a line that was never checked has nothing to release: %v", err)
	}
	status, body = http.StatusConflict, `{"code":"BUD-0010"}`
	if err := c.ReleaseLine(ctx, le, req.ID, "l1"); err != nil {
		t.Fatalf("already released: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := c.ReleaseLine(ctx, le, req.ID, "l1"); err == nil {
		t.Fatal("a 503 must be reported")
	}
}

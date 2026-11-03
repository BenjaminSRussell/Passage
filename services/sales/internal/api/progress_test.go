package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type progressStore struct {
	store.Store
	got     store.NewProgress
	created bool
	listed  store.ProgressFilter
}

func (s *progressStore) RecordProgress(_ context.Context, in store.NewProgress, _ time.Time) (store.OrderProgress, bool, error) {
	s.got = in
	return store.OrderProgress{ID: ids.New(), OrderID: in.OrderID, Kind: in.Kind, SourceRef: in.SourceRef, Lines: []store.ProgressLineResult{}}, s.created, nil
}

func (s *progressStore) ListProgress(_ context.Context, f store.ProgressFilter) ([]store.OrderProgress, error) {
	s.listed = f
	return []store.OrderProgress{}, nil
}

func TestRecordProgress(t *testing.T) {
	id := ids.New()
	path := orders + "/" + id + ":record-progress"
	h := map[string]string{idempotencyKey: "k1"}
	body := `{"kind":"DELIVERED","sourceRef":" PS-9 ","lines":[{"lineNo":1,"quantity":"4.5"},{"lineNo":2,"quantity":"1"}]}`

	s := &progressStore{created: true}
	rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), h)
	if rec.Code != http.StatusCreated || s.got.OrderID != id || s.got.SourceRef != "PS-9" || len(s.got.Lines) != 2 || s.got.Lines[0].Quantity.String() != "4.5" ||
		s.got.IdempotencyKey != "k1" || s.got.RequestHash == "" || s.got.Actor != "u1" {
		t.Fatalf("record: %d %s %+v", rec.Code, rec.Body, s.got)
	}
	s.created = false
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), h); rec.Code != http.StatusOK {
		t.Fatalf("a replay is 200: %d", rec.Code)
	}

	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyWrite), h); rec.Code != http.StatusForbidden {
		t.Fatalf("the order write duty does not report deliveries: %d", rec.Code)
	}
	// Each kind needs its own duty: billing cannot post a delivery, fulfillment cannot post an invoice.
	invoice := strings.Replace(body, "DELIVERED", "INVOICED", 1)
	s.got = store.NewProgress{}
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordInvoice), h); rec.Code != http.StatusForbidden || s.got.OrderID != "" {
		t.Fatalf("the invoice duty does not report deliveries: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, invoice, principal(dutyRecordDelivery), h); rec.Code != http.StatusForbidden || s.got.OrderID != "" {
		t.Fatalf("the delivery duty does not report invoices: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, invoice, principal(dutyRecordInvoice), h); rec.Code != http.StatusOK || s.got.Kind != "INVOICED" {
		t.Fatalf("the invoice duty reports invoices: %d", rec.Code)
	}

	// The hash covers the report, not its spelling: lines in another order and
	// 4.50 for 4.5 hash alike, another quantity does not.
	hashOf := func(b string) string {
		t.Helper()
		if rec := serveBody(t, s, http.MethodPost, path, b, principal(dutyRecordDelivery), h); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", b, rec.Code, rec.Body)
		}
		return s.got.RequestHash
	}
	same := hashOf(`{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":2,"quantity":"1.0"},{"lineNo":1,"quantity":"4.50"}]}`)
	if same != hashOf(`{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":1,"quantity":"4.5"},{"lineNo":2,"quantity":"1"}]}`) {
		t.Fatal("reordered lines or trailing zeros must hash alike")
	}
	if same == hashOf(`{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":1,"quantity":"4.5"},{"lineNo":2,"quantity":"2"}]}`) {
		t.Fatal("another quantity must hash differently")
	}
	for name, bad := range map[string]string{
		"kind":       strings.Replace(body, "DELIVERED", "SHIPPED", 1),
		"sourceRef":  strings.Replace(body, " PS-9 ", " ", 1),
		"no lines":   `{"kind":"DELIVERED","sourceRef":"PS-9","lines":[]}`,
		"duplicate":  `{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":1,"quantity":"1"},{"lineNo":1,"quantity":"2"}]}`,
		"zero":       `{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":1,"quantity":"0"}]}`,
		"places":     `{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":1,"quantity":"0.0000001"}]}`,
		"not number": `{"kind":"DELIVERED","sourceRef":"PS-9","lines":[{"lineNo":1,"quantity":"x"}]}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, path, bad, principal(dutyRecordDelivery), h); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing Idempotency-Key: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, orders+"/nope:record-progress", body, principal(dutyRecordDelivery), h); rec.Code != http.StatusNotFound {
		t.Errorf("bad order id: %d", rec.Code)
	}
}

func TestListProgress(t *testing.T) {
	s := &progressStore{}
	order := ids.New()
	rec := serve(t, s, http.MethodGet, orderProgress+"?orderId="+order+"&kind=INVOICED&limit=5", principal(dutyRead), nil)
	if rec.Code != http.StatusOK || s.listed.OrderID != order || s.listed.Kind != "INVOICED" || s.listed.Limit != 5 || s.listed.LegalEntityID != testLE {
		t.Fatalf("list: %d %+v", rec.Code, s.listed)
	}
	for _, q := range []string{"?orderId=x", "?kind=SHIPPED", "?limit=0"} {
		if rec := serve(t, s, http.MethodGet, orderProgress+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

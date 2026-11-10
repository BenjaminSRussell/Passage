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

// TestRecordProgressFinal checks the final flag of a delivered line (close for
// delivery): it reaches the store, it counts in the request hash so a replay
// without it is not the same report, and an invoice refuses it.
func TestRecordProgressFinal(t *testing.T) {
	id := ids.New()
	path := orders + "/" + id + ":record-progress"
	h := map[string]string{idempotencyKey: "k1"}
	s := &progressStore{created: true}
	final := `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineNo":1,"quantity":"96","final":true},{"lineNo":2,"quantity":"3"}]}`
	rec := serveBody(t, s, http.MethodPost, path, final, principal(dutyRecordDelivery), h)
	if rec.Code != http.StatusCreated || len(s.got.Lines) != 2 || !s.got.Lines[0].Final || s.got.Lines[1].Final {
		t.Fatalf("final: %d %s %+v", rec.Code, rec.Body, s.got.Lines)
	}
	withFinal := s.got.RequestHash
	if rec := serveBody(t, s, http.MethodPost, path, strings.Replace(final, `,"final":true`, "", 1), principal(dutyRecordDelivery), h); rec.Code != http.StatusCreated || s.got.RequestHash == withFinal {
		t.Fatalf("final must count in the hash: %d %s", rec.Code, s.got.RequestHash)
	}
	if rec := serveBody(t, s, http.MethodPost, path, strings.Replace(final, `,"final":true`, `,"final":false`, 1), principal(dutyRecordDelivery), h); s.got.RequestHash == withFinal || rec.Code != http.StatusCreated {
		t.Fatalf("final false is the default: %d", rec.Code)
	}
	s.got = store.NewProgress{}
	invoice := strings.Replace(final, "DELIVERED", "INVOICED", 1)
	if rec := serveBody(t, s, http.MethodPost, path, invoice, principal(dutyRecordInvoice), h); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lines[0].final") || s.got.OrderID != "" {
		t.Fatalf("an invoice line is never final: %d %s", rec.Code, rec.Body)
	}
}

// TestRecordProgressByLineID checks that a line may be named by its id (as
// fulfillment does, #496) instead of its number: the id reaches the store in
// lower case, ids order the hash like numbers do, and a line named by both,
// by a bad id or by the same id twice is refused.
func TestRecordProgressByLineID(t *testing.T) {
	id, a, b := ids.New(), ids.New(), ids.New()
	path := orders + "/" + id + ":record-progress"
	h := map[string]string{idempotencyKey: "k1"}
	s := &progressStore{created: true}
	body := `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineId":"` + strings.ToUpper(a) + `","quantity":"2"},{"lineId":"` + b + `","quantity":"1"}]}`
	rec := serveBody(t, s, http.MethodPost, path, body, principal(dutyRecordDelivery), h)
	if rec.Code != http.StatusCreated || len(s.got.Lines) != 2 || s.got.Lines[0].LineID != a || s.got.Lines[0].LineNo != 0 || s.got.Lines[1].LineID != b {
		t.Fatalf("by id: %d %s %+v", rec.Code, rec.Body, s.got.Lines)
	}
	hash := s.got.RequestHash
	swapped := `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineId":"` + b + `","quantity":"1"},{"lineId":"` + a + `","quantity":"2.0"}]}`
	if rec := serveBody(t, s, http.MethodPost, path, swapped, principal(dutyRecordDelivery), h); rec.Code != http.StatusCreated || s.got.RequestHash != hash {
		t.Fatalf("reordered lines by id must hash alike: %d", rec.Code)
	}
	for name, bad := range map[string]string{
		"both":      `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineNo":1,"lineId":"` + a + `","quantity":"1"}]}`,
		"not an id": `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineId":"line-1","quantity":"1"}]}`,
		"twice":     `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"lineId":"` + a + `","quantity":"1"},{"lineId":"` + a + `","quantity":"1"}]}`,
		"neither":   `{"kind":"DELIVERED","sourceRef":"PS-1","lines":[{"quantity":"1"}]}`,
	} {
		if rec := serveBody(t, s, http.MethodPost, path, bad, principal(dutyRecordDelivery), h); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
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

package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type returnStore struct {
	store.Store
	created  *store.NewReturnOrder
	received []store.ReceiveLine
	inspect  []store.InspectLine
	version  int
	filter   store.ReturnOrderFilter
	err      error
}

func (s *returnStore) CreateReturnOrder(_ context.Context, in store.NewReturnOrder) (store.ReturnOrder, bool, error) {
	s.created = &in
	return store.ReturnOrder{ID: ids.New(), Version: 1, Status: "REQUESTED"}, true, s.err
}

func (s *returnStore) ListReturnOrders(_ context.Context, f store.ReturnOrderFilter) ([]store.ReturnOrder, error) {
	s.filter = f
	return []store.ReturnOrder{}, nil
}

func (s *returnStore) AuthorizeReturnOrder(_ context.Context, id string, version int, _ string, _ time.Time) (store.ReturnOrder, error) {
	s.version = version
	return store.ReturnOrder{ID: id, Version: version + 1, Status: "AUTHORIZED"}, s.err
}

func (s *returnStore) ReceiveReturnOrder(_ context.Context, id string, version int, lines []store.ReceiveLine, _ string, _ time.Time) (store.ReturnOrder, error) {
	s.version, s.received = version, lines
	return store.ReturnOrder{ID: id, Version: version + 1, Status: "RECEIVED"}, s.err
}

func (s *returnStore) InspectReturnOrder(_ context.Context, id string, version int, lines []store.InspectLine, _ string, _ time.Time) (store.ReturnOrder, error) {
	s.version, s.inspect = version, lines
	return store.ReturnOrder{ID: id, Version: version + 1, Status: "INSPECTED"}, s.err
}

func (s *returnStore) CreditReturnOrder(_ context.Context, id string, version int, _ string, _ time.Time) (store.ReturnOrder, error) {
	return store.ReturnOrder{ID: id, Version: version + 1}, s.err
}

func (s *returnStore) RejectReturnOrder(_ context.Context, id string, version int, _ string, _ time.Time) (store.ReturnOrder, error) {
	s.version = version
	return store.ReturnOrder{ID: id, Version: version + 1, Status: "REJECTED"}, s.err
}

func TestRejectReturnOrder_Route(t *testing.T) {
	id := ids.New()
	s := &returnStore{}
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":reject", principal(dutyRead), map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("reject without the write duty: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":reject", principal(dutyWrite), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("reject without If-Match: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":reject", principal(dutyWrite), map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusOK || s.version != 2 {
		t.Fatalf("reject: %d version %d", rec.Code, s.version)
	}
}

func returnBody(extra string) string {
	return `{"legalEntityId":"` + testLE + `","originalInvoiceId":"` + ids.New() + `","returnReasonCode":"DAMAGED",` + extra +
		`"lines":[{"lineNo":1,"productId":"` + ids.New() + `","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() + `","quantity":"4","disposition":"RETURN_TO_STOCK"}]}`
}

func TestCreateReturnOrder_Validation(t *testing.T) {
	key := map[string]string{idempotencyKey: "rma-1"}
	scoped := principal(dutyWrite)
	scoped.LegalEntityID = ids.New()
	good := returnBody("")
	tests := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
	}{
		{"without the write duty", good, key, http.StatusForbidden},
		{"missing Idempotency-Key", good, nil, http.StatusBadRequest},
		{"a price in the body is refused", strings.Replace(good, `"quantity":"4"`, `"quantity":"4","unitPrice":"1.00"`, 1), key, http.StatusBadRequest},
		{"original invoice is required", strings.Replace(good, `"originalInvoiceId"`, `"x"`, 1), key, http.StatusBadRequest},
		{"unknown cost method", strings.Replace(good, `"disposition"`, `"returnCostMethod":"GUESS","disposition"`, 1), key, http.StatusBadRequest},
		{"restocking above 100", strings.Replace(good, `"disposition"`, `"restockingPct":"101","disposition"`, 1), key, http.StatusBadRequest},
		{"zero quantity", strings.Replace(good, `"quantity":"4"`, `"quantity":"0"`, 1), key, http.StatusBadRequest},
		{"outside the caller's legal entity", good, key, http.StatusForbidden},
		{"valid", good, key, http.StatusCreated},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &returnStore{}
			p := principal(dutyWrite)
			switch tc.name {
			case "without the write duty":
				p = principal(dutyRead)
			case "outside the caller's legal entity":
				p = scoped
			}
			rec := serveBody(t, s, http.MethodPost, returnOrders, tc.body, p, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if (tc.status == http.StatusCreated) != (s.created != nil) {
				t.Fatalf("store called = %v for %d", s.created != nil, tc.status)
			}
			if s.created != nil && (s.created.IdempotencyKey != "rma-1" || s.created.Actor != "u1" || s.created.RequestHash == "" || s.created.Lines[0].Quantity.String() != "4") {
				t.Fatalf("forwarded %+v", s.created)
			}
		})
	}
}

func TestListReturnOrders_ValidatesStatus(t *testing.T) {
	s := &returnStore{}
	if rec := serve(t, s, http.MethodGet, returnOrders+"?status=BOGUS", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, returnOrders+"?status=INSPECTED", principal(dutyRead), nil); rec.Code != http.StatusOK || s.filter.Status != "INSPECTED" {
		t.Fatalf("valid status: %d %+v", rec.Code, s.filter)
	}
}

func TestReturnOrderCommands_PreconditionsAndErrors(t *testing.T) {
	id := ids.New()
	s := &returnStore{}
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":authorize", principal(dutyWrite), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("authorize without If-Match: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":authorize", principal(dutyRead), map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("authorize without the write duty: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodPost, returnOrders+"/nope:authorize", principal(dutyWrite), map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":authorize", principal(dutyWrite), map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || s.version != 3 || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("authorize: %d version %d etag %s", rec.Code, s.version, rec.Header().Get("ETag"))
	}
	s.err = httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "stale")
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":authorize", principal(dutyWrite), map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale version: %d", rec.Code)
	}
	s.err = httpx.NewProblem(http.StatusServiceUnavailable, store.CodeARUnavailable, "receivables is not configured")
	if rec := serve(t, s, http.MethodPost, returnOrders+"/"+id+":credit", principal(dutyWrite), map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("credit without receivables: %d", rec.Code)
	}
}

func TestReceiveAndInspectReturnOrder_Bodies(t *testing.T) {
	id := ids.New()
	h := map[string]string{"If-Match": `"2"`}
	s := &returnStore{}
	rec := serveBody(t, s, http.MethodPost, returnOrders+"/"+id+":receive", `{"lines":[{"lineNo":1,"quantity":"0"},{"lineNo":2,"quantity":"3"}]}`, principal(dutyWrite), h)
	if rec.Code != http.StatusOK || len(s.received) != 2 || !s.received[0].Quantity.IsZero() {
		t.Fatalf("receive with a 0 line: %d %+v", rec.Code, s.received)
	}
	for name, body := range map[string]string{
		"negative":  `{"lines":[{"lineNo":1,"quantity":"-1"}]}`,
		"duplicate": `{"lines":[{"lineNo":1,"quantity":"1"},{"lineNo":1,"quantity":"1"}]}`,
	} {
		if rec := serveBody(t, &returnStore{}, http.MethodPost, returnOrders+"/"+id+":receive", body, principal(dutyWrite), h); rec.Code != http.StatusBadRequest {
			t.Fatalf("receive %s: %d", name, rec.Code)
		}
	}
	good := `{"lines":[{"lineNo":1,"quantityAccepted":"2","disposition":"RETURN_TO_STOCK"}]}`
	si := &returnStore{}
	if rec := serveBody(t, si, http.MethodPost, returnOrders+"/"+id+":inspect", good, principal(dutyWrite), h); rec.Code != http.StatusOK || len(si.inspect) != 1 ||
		si.inspect[0].Accepted.String() != "2" || si.inspect[0].Disposition != "RETURN_TO_STOCK" {
		t.Fatalf("inspect: %d %+v", rec.Code, si.inspect)
	}
	// An RMA that received nothing (credit-only lines no goods came back for) is inspected with no lines: an empty
	// list and an empty body both reach the store, which refuses it only when a line was received.
	for name, body := range map[string]string{"empty list": `{"lines":[]}`, "no body": ""} {
		se := &returnStore{}
		if rec := serveBody(t, se, http.MethodPost, returnOrders+"/"+id+":inspect", body, principal(dutyWrite), h); rec.Code != http.StatusOK || len(se.inspect) != 0 || se.version != 2 {
			t.Fatalf("inspect %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	for name, body := range map[string]string{
		"bad disposition": `{"lines":[{"lineNo":1,"quantityAccepted":"2","disposition":"SCRAP"}]}`,
		"negative":        `{"lines":[{"lineNo":1,"quantityAccepted":"-2","disposition":"CREDIT_ONLY"}]}`,
	} {
		if rec := serveBody(t, &returnStore{}, http.MethodPost, returnOrders+"/"+id+":inspect", body, principal(dutyWrite), h); rec.Code != http.StatusBadRequest {
			t.Fatalf("inspect %s: %d", name, rec.Code)
		}
	}
	if rec := serveBody(t, &returnStore{}, http.MethodPost, returnOrders+"/"+id+":inspect", good, principal(dutyWrite), nil); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("inspect without If-Match: %d", rec.Code)
	}
}

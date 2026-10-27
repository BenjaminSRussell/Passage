package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

// The fake keeps batches by key and refuses a slip line already invoiced, as
// the unique index does in the store.
func (f *fakeStore) FindProposalBatch(_ context.Context, k string) (store.ProposalBatch, []store.Invoice, bool, error) {
	b, ok := f.batches[k]
	if !ok {
		return store.ProposalBatch{}, nil, false, nil
	}
	var out []store.Invoice
	for _, id := range f.batchInvoices[b.ID] {
		out = append(out, f.invoices[id])
	}
	return b, out, true, nil
}

func (f *fakeStore) CreateShipmentProposals(_ context.Context, b store.ProposalBatch, invs []store.Invoice) ([]store.Invoice, error) {
	if f.batches == nil {
		f.batches, f.batchInvoices, f.slipLines = map[string]store.ProposalBatch{}, map[string][]string{}, map[string]bool{}
	}
	for _, inv := range invs {
		for _, l := range inv.Lines {
			if f.slipLines[l.Source.PackingSlipLineID] {
				return nil, httpx.NewProblem(http.StatusConflict, store.CodeSlipLineInvoiced, "already invoiced")
			}
		}
	}
	f.batchCreates++
	f.batches[b.IdempotencyKey] = b
	out := make([]store.Invoice, len(invs))
	for i, inv := range invs {
		for _, l := range inv.Lines {
			f.slipLines[l.Source.PackingSlipLineID] = true
		}
		inv.Version, inv.ProposalBatchID, inv.ProposalBatchSeq = 1, b.ID, i+1
		f.invoices[inv.ID] = inv
		f.batchInvoices[b.ID] = append(f.batchInvoices[b.ID], inv.ID)
		out[i] = inv
	}
	return out, nil
}

func uuid(n int) string { return fmt.Sprintf("0190f0a0-0000-7000-8000-%012d", n) }

func lineID(n int) string { return uuid(5000 + n) }

type testSlip struct {
	id, order, date, charge string
	lines                   []int
}

// shipmentBody builds a POST /proposals body. Each slip line n has id
// lineID(n), order line uuid(6000+n), product uuid(7000+n) and bills 2 × 50.00 at DE19.
func shipmentBody(consolidate bool, slips ...testSlip) string {
	type line = map[string]string
	var ss []map[string]any
	for _, s := range slips {
		var ls []line
		for _, n := range s.lines {
			ls = append(ls, line{"id": lineID(n), "orderLineId": uuid(6000 + n), "productId": uuid(7000 + n), "description": fmt.Sprintf("Widget %d", n),
				"qty": "2", "unitPrice": "50", "revenueAccount": "4000", "taxCode": "DE19", "priceExplainRef": fmt.Sprintf("px-%d", n)})
		}
		slip := map[string]any{"id": s.id, "orderId": s.order, "date": s.date, "lines": ls}
		if s.charge != "" {
			slip["chargeAmount"] = s.charge
		}
		ss = append(ss, slip)
	}
	raw, _ := json.Marshal(map[string]any{"legalEntityId": leID, "customerId": custID, "invoiceAccountId": acctID, "currency": "EUR",
		"invoiceDate": "2027-03-10", "paymentTermDays": 30, "consolidate": consolidate, "slips": ss})
	return string(raw)
}

var (
	slipA = testSlip{id: uuid(101), order: uuid(201), date: "2027-03-08", lines: []int{1, 2}}
	slipB = testSlip{id: uuid(102), order: uuid(202), date: "2027-03-09", charge: "12.50", lines: []int{3}}
	slipC = testSlip{id: uuid(103), order: uuid(201), date: "2027-03-10", lines: []int{4}}
)

func TestShipmentProposals_OneInvoicePerOrder(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-1", shipmentBody(false, slipA, slipB, slipC))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got proposalBatchDTO
	decode(t, rec, &got)
	if len(got.Items) != 2 {
		t.Fatalf("%d invoices, want one per order (2)", len(got.Items))
	}
	first, second := got.Items[0], got.Items[1]
	// Order 201: slips A and C, three lines of 100.00 net and 19.00 tax.
	if first.Type != domain.TypeShipment || len(first.Lines) != 3 || first.Totals.Net != "300.00" || first.Totals.Tax != "57.00" || first.Totals.Gross != "357.00" {
		t.Errorf("order 201 invoice %+v", first)
	}
	// Order 202: slip B, one line plus the slip's 12.50 charge.
	if second.Type != domain.TypeShipment || second.Totals.Charges != "12.50" || second.Totals.Gross != "131.50" || second.DueDate != "2027-04-09" {
		t.Errorf("order 202 invoice %+v", second.summaryDTO)
	}
	l := first.Lines[2]
	if l.LineNo != 3 || l.PackingSlipID != slipC.id || l.PackingSlipLineID != lineID(4) || l.OrderID != uuid(201) ||
		l.OrderLineID != uuid(6004) || l.ProductID != uuid(7004) || l.PriceExplainRef != "px-4" {
		t.Errorf("line 3 does not carry its sources: %+v", l)
	}
	if s.batchCreates != 1 || len(s.invoices) != 2 {
		t.Errorf("batch creates %d, invoices %d", s.batchCreates, len(s.invoices))
	}

	replay := do(h, http.MethodPost, "/v1/billing/proposals", "ship-1", shipmentBody(false, slipA, slipB, slipC))
	var again proposalBatchDTO
	decode(t, replay, &again)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || len(again.Items) != 2 || again.Items[0].ID != first.ID {
		t.Fatalf("replay status %d, %d items: %s", replay.Code, len(again.Items), replay.Body)
	}
	if s.batchCreates != 1 {
		t.Errorf("a replay must store nothing; batch creates %d", s.batchCreates)
	}
	if reuse := do(h, http.MethodPost, "/v1/billing/proposals", "ship-1", shipmentBody(true, slipA, slipB, slipC)); reuse.Code != http.StatusUnprocessableEntity {
		t.Errorf("reused key with another body: status %d, want 422", reuse.Code)
	}
}

func TestShipmentProposals_ConsolidatedAC29_5(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-c", shipmentBody(true, slipA, slipB, slipC))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got proposalBatchDTO
	decode(t, rec, &got)
	if len(got.Items) != 1 || got.Items[0].Type != domain.TypeConsolidated || len(got.Items[0].Lines) != 4 || got.Items[0].Totals.Gross != "488.50" {
		t.Fatalf("want one consolidated invoice of 4 lines, gross 488.50; got %s", rec.Body)
	}

	// The invoice then posts like any other and publishes its slip lines.
	id := got.Items[0].ID
	post := do(h, http.MethodPost, "/v1/billing/invoices/"+id+":post", "", "")
	if post.Code != http.StatusOK || s.invoices[id].Status != domain.StatusPosted {
		t.Fatalf("post status %d: %s", post.Code, post.Body)
	}
}

func TestShipmentProposals_SlipLineInvoicedOnce(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	if rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-a", shipmentBody(false, slipA)); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-b", shipmentBody(false, slipA))
	var p httpx.Problem
	decode(t, rec, &p)
	if rec.Code != http.StatusConflict || p.Code != store.CodeSlipLineInvoiced {
		t.Fatalf("second request for the same slip: status %d code %s", rec.Code, p.Code)
	}
}

func TestShipmentProposals_Refusals(t *testing.T) {
	late := testSlip{id: uuid(104), order: uuid(203), date: "2027-03-11", lines: []int{5}}
	cases := []struct {
		name   string
		key    string
		body   string
		tax    *fakeTax
		status int
		code   string
		field  string
	}{
		{name: "missing key", body: shipmentBody(false, slipA), status: http.StatusBadRequest, code: "COM-0001"},
		{name: "BIL-0006 invoice date before slip date", key: "k1", body: shipmentBody(false, slipA, late), status: http.StatusUnprocessableEntity, code: domain.CodeDateBeforeSlip},
		{name: "BIL-0001 tax engine down stores nothing", key: "k2", body: shipmentBody(false, slipA), tax: &fakeTax{err: ErrTaxUnavailable}, status: http.StatusServiceUnavailable, code: CodeTaxUnavailable},
		{name: "no slips", key: "k3", body: shipmentBody(false), status: http.StatusBadRequest, code: "COM-0001", field: "slips"},
		{name: "slip line id not a UUID", key: "k4", body: strings.Replace(shipmentBody(false, slipA), lineID(1), "line-1", 1),
			status: http.StatusBadRequest, code: "COM-0001", field: "slips[0].lines[0].id"},
		{name: "BIL-0004 slip line without revenue account", key: "k5", body: strings.Replace(shipmentBody(false, slipA), `"revenueAccount":"4000"`, `"revenueAccount":""`, 1),
			status: http.StatusBadRequest, code: "COM-0001", field: "slips[0].lines[0].revenueAccount"},
		{name: "slip without date", key: "k6", body: strings.Replace(shipmentBody(false, slipA), `"date":"2027-03-08"`, `"date":""`, 1),
			status: http.StatusBadRequest, code: "COM-0001", field: "slips[0].date"},
		{name: "same slip line twice", key: "k7", body: shipmentBody(false, slipA, testSlip{id: uuid(105), order: uuid(201), date: "2027-03-08", lines: []int{1}}),
			status: http.StatusBadRequest, code: "COM-0001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newFakeStore()
			tax := tc.tax
			if tax == nil {
				tax = &fakeTax{perLine: "19.00"}
			}
			rec := do(setup(t, s, tax), http.MethodPost, "/v1/billing/proposals", tc.key, tc.body)
			var p httpx.Problem
			decode(t, rec, &p)
			if rec.Code != tc.status || p.Code != tc.code {
				t.Fatalf("status %d code %s, want %d %s: %s", rec.Code, p.Code, tc.status, tc.code, rec.Body)
			}
			if tc.field != "" && p.Errors[tc.field] == "" {
				t.Errorf("no field error %s in %v", tc.field, p.Errors)
			}
			if s.batchCreates != 0 {
				t.Errorf("a refused request stored %d batches", s.batchCreates)
			}
		})
	}
}

func TestShipmentProposals_ScopedCallerCannotInvoiceAnotherEntity(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	rec := doAs(h, http.MethodPost, "/v1/billing/proposals", "ship-s", shipmentBody(false, slipA), allDuties,
		map[string]string{tenant.HeaderLegalEntity: uuid(999)})
	if rec.Code != http.StatusForbidden || s.batchCreates != 0 {
		t.Fatalf("status %d, batches %d: %s", rec.Code, s.batchCreates, rec.Body)
	}
}

func TestInvoiceTrace(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	rec := do(h, http.MethodPost, "/v1/billing/proposals", "ship-t", shipmentBody(true, slipA, slipB))
	var batch proposalBatchDTO
	decode(t, rec, &batch)
	id := batch.Items[0].ID

	trace := do(h, http.MethodGet, "/v1/billing/invoices/"+id+"/trace", "", "")
	if trace.Code != http.StatusOK {
		t.Fatalf("status %d: %s", trace.Code, trace.Body)
	}
	var got traceDTO
	decode(t, trace, &got)
	if got.Invoice.ID != id || got.Invoice.Type != domain.TypeConsolidated || len(got.Lines) != 3 {
		t.Fatalf("trace %+v", got)
	}
	wantSlips := []traceSlipDTO{{ID: slipA.id, Date: "2027-03-08", OrderID: uuid(201)}, {ID: slipB.id, Date: "2027-03-09", OrderID: uuid(202)}}
	if len(got.PackingSlips) != 2 || got.PackingSlips[0] != wantSlips[0] || got.PackingSlips[1] != wantSlips[1] {
		t.Errorf("packing slips %+v, want %+v", got.PackingSlips, wantSlips)
	}
	if l := got.Lines[2]; l.PackingSlipLineID != lineID(3) || l.OrderLineID != uuid(6003) || l.PriceExplainRef != "px-3" || l.Net != "100.00" {
		t.Errorf("line 3 %+v", l)
	}

	// A free-text invoice has a trace with no documents behind it.
	ft := do(h, http.MethodPost, "/v1/billing/free-text-invoices", "ft-1", proposal("1", "100", "4900"))
	var inv invoiceDTO
	decode(t, ft, &inv)
	var empty traceDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/invoices/"+inv.ID+"/trace", "", ""), &empty)
	if len(empty.PackingSlips) != 0 || len(empty.Lines) != 1 || empty.Lines[0].PackingSlipID != "" {
		t.Errorf("free-text trace %+v", empty)
	}

	if nf := do(h, http.MethodGet, "/v1/billing/invoices/"+uuid(4242)+"/trace", "", ""); nf.Code != http.StatusNotFound {
		t.Errorf("unknown invoice: status %d", nf.Code)
	}
	if forbidden := doAs(h, http.MethodGet, "/v1/billing/invoices/"+id+"/trace", "", "", "billing.invoice.maintain", nil); forbidden.Code != http.StatusForbidden {
		t.Errorf("without InvoiceView: status %d", forbidden.Code)
	}
}

func TestInvoiceTrace_CreditNoteResolvesThroughTheOriginal(t *testing.T) {
	s := newFakeStore()
	h := setup(t, s, &fakeTax{perLine: "19.00"})
	var batch proposalBatchDTO
	decode(t, do(h, http.MethodPost, "/v1/billing/proposals", "ship-cn", shipmentBody(false, slipA)), &batch)
	id := batch.Items[0].ID
	if rec := do(h, http.MethodPost, "/v1/billing/invoices/"+id+":post", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("post %d: %s", rec.Code, rec.Body)
	}
	body := fmt.Sprintf(`{"originalInvoiceId":%q,"creditDate":"2027-03-12","reason":"Damaged","lines":[{"lineNo":2,"qty":"1"}]}`, id)
	cnRec := do(h, http.MethodPost, "/v1/billing/credit-notes", "cn-1", body)
	if cnRec.Code != http.StatusCreated {
		t.Fatalf("credit note %d: %s", cnRec.Code, cnRec.Body)
	}
	var cn invoiceDTO
	decode(t, cnRec, &cn)
	var got traceDTO
	decode(t, do(h, http.MethodGet, "/v1/billing/invoices/"+cn.ID+"/trace", "", ""), &got)
	if got.Original == nil || got.Original.ID != id || len(got.Lines) != 1 {
		t.Fatalf("credit note trace %+v", got)
	}
	if l := got.Lines[0]; l.OriginalLineNo != 2 || l.PackingSlipLineID != lineID(2) || l.OrderLineID != uuid(6002) {
		t.Errorf("credited line does not resolve to the original's slip line: %+v", l)
	}
}

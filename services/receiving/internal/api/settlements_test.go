package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	planRunPath        = "/v1/receiving/plan-settlements:run"
	consignmentRunPath = "/v1/receiving/consignment-settlements:run"
)

type runJSON struct {
	Settled []struct {
		ID            string `json:"id"`
		Kind          string `json:"kind"`
		SourceRef     string `json:"sourceRef"`
		VendorID      string `json:"vendorId"`
		Currency      string `json:"currency"`
		Amount        string `json:"amount"`
		LedgerEntryID string `json:"ledgerEntryId"`
	} `json:"settled"`
	Skipped []struct {
		SourceRef string `json:"sourceRef"`
		Reason    string `json:"reason"`
	} `json:"skipped"`
	HasMore bool `json:"hasMore"`
}

func (r runJSON) skips() map[string]string {
	out := map[string]string{}
	for _, s := range r.Skipped {
		out[s.SourceRef] = s.Reason
	}
	return out
}

func (r runJSON) settledRefs() string {
	var refs []string
	for _, s := range r.Settled {
		refs = append(refs, s.SourceRef)
	}
	return strings.Join(refs, ",")
}

func newRigPorts(st *memstore.Store, periods domain.PeriodStatus, mutate func(*api.Ports)) *rig {
	rg := &rig{st: st, ledger: &domain.FakeLedger{}}
	ports := api.Ports{
		Periods: domain.FakePeriods{Status: periods}, Ledger: rg.ledger,
		Profile:       domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000", Expense: "6000", InputTax: "1570"},
		ResolveMinAge: time.Nanosecond,
		Tax:           testTax, SelfBillParties: testParties,
	}
	mutate(&ports)
	a := api.New(rg.st, ports)
	router := httpx.NewRouter()
	a.Register(router)
	rg.h, rg.api = router, a
	return rg
}

func runBody(extra string, fields ...string) string {
	body := fmt.Sprintf(`{"legalEntityId":%q,"postingDate":"2026-10-15","ledgerId":%q,"fiscalPeriodId":%q`, legalEntityID, ledgerID, periodID)
	for _, f := range fields {
		body += "," + f
	}
	return body + extra + "}"
}

func (rg *rig) settle() headers { return rg.apPosterFor(legalEntityID) }

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func planPO() domain.PurchaseOrder {
	line := func(no int, status string) domain.PurchaseOrderLine {
		return domain.PurchaseOrderLine{LineNo: no, Status: status, ProductID: productID}
	}
	return domain.PurchaseOrder{
		ID: poID, VendorID: vendorID, Currency: "USD", LegalEntityID: legalEntityID, ApprovalStatus: "APPROVED",
		Lines: []domain.PurchaseOrderLine{line(1, "OPEN"), line(2, "OPEN"), line(3, "CANCELLED"), line(4, "OPEN"), line(5, "OPEN")},
	}
}

func planRows(rows ...domain.PlanRow) []domain.PlanRow { return rows }

func row(seq int, due time.Time, amount, status string) domain.PlanRow {
	return domain.PlanRow{Seq: seq, DueDate: due, Amount: dec(amount), Status: status}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func plansForPO() []domain.InvoicingPlan {
	return []domain.InvoicingPlan{
		{PurchaseOrderID: poID, LineNo: 5, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(row(1, day(2026, 9, 15), "0", "OPEN"))},
		{PurchaseOrderID: poID, LineNo: 1, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(
			row(1, day(2026, 8, 1), "1000.00", "INVOICED"), row(2, day(2026, 9, 1), "1000.00", "OPEN"),
			row(3, day(2026, 10, 1), "1000.50", "OPEN"), row(4, day(2026, 11, 1), "1000.00", "OPEN"))},
		{PurchaseOrderID: poID, LineNo: 2, SettleWithoutReceipt: false, Currency: "USD", Rows: planRows(row(1, day(2026, 9, 15), "10.00", "OPEN"))},
		{PurchaseOrderID: poID, LineNo: 3, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(row(1, day(2026, 9, 15), "10.00", "OPEN"))},
		{PurchaseOrderID: poID, LineNo: 4, SettleWithoutReceipt: true, Currency: "EUR", Rows: planRows(row(1, day(2026, 9, 15), "10.00", "OPEN"))},
	}
}

func planRig(periods domain.PeriodStatus, plans []domain.InvoicingPlan) *rig {
	return newRigPorts(memstore.New(), periods, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: planPO()}}
		p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{poID: plans}}
	})
}

func planBody(extra ...string) string {
	return runBody("", append([]string{`"purchaseOrderId":"` + poID + `"`}, extra...)...)
}

func TestPlanSettlement_SettlesDueOpenRowsOnceAndExplainsTheRest(t *testing.T) {
	rg := planRig(domain.PeriodOpen, plansForPO())

	first := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	if got := first.settledRefs(); got != poID+"/1/2,"+poID+"/1/3" {
		t.Fatalf("settled %s", got)
	}
	s := first.Settled[0]
	if s.Kind != "PLAN_ROW" || s.VendorID != vendorID || s.Currency != "USD" || s.Amount != "1000.00" || first.Settled[1].Amount != "1000.50" || s.LedgerEntryID != "fake-entry-1" || first.HasMore {
		t.Fatalf("settlement %+v", first)
	}
	wantSkips := map[string]string{
		poID + "/1/1": "ALREADY_INVOICED", poID + "/2/1": "AWAITS_RECEIPT", poID + "/3/1": "LINE_CANCELLED", poID + "/4/1": "CURRENCY_MISMATCH", poID + "/5/1": "NO_AMOUNT",
	}
	for ref, reason := range wantSkips {
		if first.skips()[ref] != reason {
			t.Fatalf("skip %s = %q, want %q (all: %v)", ref, first.skips()[ref], reason, first.skips())
		}
	}
	if len(first.Skipped) != len(wantSkips) {
		t.Fatalf("skipped %v, want only %v: rows not yet due are not reported", first.Skipped, wantSkips)
	}
	entry := rg.ledger.Entries[0]
	wantLines(t, entry, "2100=1000", "2000=-1000")
	if !entry.DocumentDate.Equal(day(2026, 9, 1)) || !entry.PostingDate.Equal(day(2026, 10, 15)) || entry.Source.DocumentType != "settlement" || entry.Source.Purpose != "plan_row" {
		t.Fatalf("entry %+v", entry)
	}
	if rg.ledger.Balance("2100").String() != "2000.5" || rg.ledger.Balance("2000").String() != "-2000.5" {
		t.Fatalf("GR/IR %s vendor control %s", rg.ledger.Balance("2100"), rg.ledger.Balance("2000"))
	}

	again := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	if len(again.Settled) != 0 || again.skips()[poID+"/1/2"] != "ALREADY_SETTLED" || again.skips()[poID+"/1/3"] != "ALREADY_SETTLED" || len(rg.ledger.Entries) != 2 {
		t.Fatalf("second run settled %d, skips %v, ledger entries %d", len(again.Settled), again.skips(), len(rg.ledger.Entries))
	}
	later := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, strings.Replace(planBody(), "2026-10-15", "2026-11-02", 1), rg.settle()), http.StatusOK)
	if later.settledRefs() != poID+"/1/4" || rg.ledger.Balance("2100").String() != "3000.5" {
		t.Fatalf("later run settled %s, GR/IR %s", later.settledRefs(), rg.ledger.Balance("2100"))
	}
}

func TestPlanSettlement_RunIsCappedAndResumes(t *testing.T) {
	var rows []domain.PlanRow
	for seq := 1; seq <= 101; seq++ {
		rows = append(rows, row(seq, day(2026, 9, 1), "1.00", "OPEN"))
	}
	rg := planRig(domain.PeriodOpen, []domain.InvoicingPlan{{PurchaseOrderID: poID, LineNo: 1, SettleWithoutReceipt: true, Currency: "USD", Rows: rows}})

	first := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)
	second := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	if len(first.Settled) != 100 || !first.HasMore || len(second.Settled) != 1 || second.HasMore || second.Settled[0].SourceRef != poID+"/1/101" || len(rg.ledger.Entries) != 101 {
		t.Fatalf("first %d (more %v), second %d, ledger entries %d", len(first.Settled), first.HasMore, len(second.Settled), len(rg.ledger.Entries))
	}
}

func TestPlanSettlement_RejectionsPostNothing(t *testing.T) {
	foreign := planPO()
	foreign.LegalEntityID = ledgerID
	unapproved := planPO()
	unapproved.ApprovalStatus = "DRAFT"
	tests := []struct {
		name   string
		mutate func(*api.Ports)
		body   string
		status int
		code   string
	}{
		{"purchase order of another legal entity", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: foreign}}
		}, planBody(), http.StatusUnprocessableEntity, "RCV-VAL-001"},
		{"purchase order not approved", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: unapproved}}
		}, planBody(), http.StatusUnprocessableEntity, "RCV-PO-001"},
		{"unknown purchase order", func(*api.Ports) {}, strings.Replace(planBody(), poID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1), http.StatusNotFound, "RCV-NF-004"},
		{"purchase order forbidden", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Err: domain.ErrPurchaseOrderForbidden}
		}, planBody(), http.StatusForbidden, "AUTH-0003"},
		{"purchase order lookup down", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Err: errors.New("down")}
		}, planBody(), http.StatusServiceUnavailable, "COM-0503"},
		{"plans lookup down", func(p *api.Ports) {
			p.InvoicingPlans = domain.FakeInvoicingPlans{Err: errors.New("down")}
		}, planBody(), http.StatusServiceUnavailable, "COM-0503"},
		{"plans forbidden", func(p *api.Ports) {
			p.InvoicingPlans = domain.FakeInvoicingPlans{Err: domain.ErrPurchaseOrderForbidden}
		}, planBody(), http.StatusForbidden, "AUTH-0003"},
		{"plans not found", func(p *api.Ports) {
			p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{}}
		}, planBody(), http.StatusNotFound, "RCV-NF-004"},
		{"plans port not configured", func(p *api.Ports) { p.InvoicingPlans = nil }, planBody(), http.StatusServiceUnavailable, "COM-0503"},
		{"purchase order port not configured", func(p *api.Ports) { p.PurchaseOrders = nil }, planBody(), http.StatusServiceUnavailable, "COM-0503"},
		{"foreign ledger", func(*api.Ports) {}, strings.Replace(planBody(), ledgerID, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 1), http.StatusUnprocessableEntity, "RCV-VAL-001"},
		{"ledger down", func(p *api.Ports) { p.Ledger = &domain.FakeLedger{Err: errors.New("down")} }, planBody(), http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
				p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: planPO()}}
				p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{poID: plansForPO()}}
				tc.mutate(p)
			})

			problemCode(t, rg.call(http.MethodPost, planRunPath, tc.body, rg.settle()), tc.status, tc.code)

			if len(rg.ledger.Entries) != 0 {
				t.Fatalf("a rejected run reached the ledger: %d entries", len(rg.ledger.Entries))
			}
			settled, err := rg.st.SettledSources(tenantCtx(), domain.SettlementPlanRow, []string{poID + "/1/2"})
			if err != nil || len(settled) != 0 {
				t.Fatalf("a rejected run recorded settlements: %v %v", settled, err)
			}
		})
	}
}

func TestPlanSettlement_ClosedPeriodPostsNothingThenSettlesWhenOpen(t *testing.T) {
	st := memstore.New()
	ports := func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: planPO()}}
		p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{poID: plansForPO()}}
	}
	closed := newRigPorts(st, domain.PeriodClosed, ports)
	open := newRigPorts(st, domain.PeriodOpen, ports)
	open.keys = 1000

	problemCode(t, closed.call(http.MethodPost, planRunPath, planBody(), closed.settle()), http.StatusUnprocessableEntity, "PER-AP-001")
	if len(closed.ledger.Entries) != 0 {
		t.Fatal("a closed period reached the ledger")
	}

	got := decode[runJSON](t, open.call(http.MethodPost, planRunPath, planBody(), open.settle()), http.StatusOK)
	if len(got.Settled) != 2 {
		t.Fatalf("settled %d after the period opened, want 2", len(got.Settled))
	}
}

func TestPlanSettlement_ValidationAuthAndScope(t *testing.T) {
	rg := planRig(domain.PeriodOpen, plansForPO())
	tests := []struct{ name, body, field string }{
		{"bad purchase order id", strings.Replace(planBody(), poID, "nope", 1), `"purchaseOrderId"`},
		{"bad legal entity", strings.Replace(planBody(), legalEntityID, "nope", 1), `"legalEntityId"`},
		{"bad posting date", strings.Replace(planBody(), "2026-10-15", "15/10/2026", 1), `"postingDate"`},
		{"bad period", strings.Replace(planBody(), periodID, "nope", 1), `"fiscalPeriodId"`},
		{"bad ledger", strings.Replace(planBody(), ledgerID, "nope", 1), `"ledgerId"`},
		{"unknown field", planBody(`"extra":1`), "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := rg.call(http.MethodPost, planRunPath, tc.body, rg.settle())
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s, want 400 with %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
	problemCode(t, rg.call(http.MethodPost, planRunPath, planBody(), duty("ReceivingPost", "k", entityHeader, legalEntityID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, planRunPath, planBody(), duty("VendorInvoicePost", "k", entityHeader, ledgerID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, planRunPath, planBody(), headers{"Idempotency-Key": "k"}), http.StatusUnauthorized, "AUTH-0001")
	if len(rg.ledger.Entries) != 0 {
		t.Fatal("an invalid or unauthorised run reached the ledger")
	}
}

func TestPlanSettlement_RunReplaysByIdempotencyKeyAndEmitsDocumentedEvent(t *testing.T) {
	rg := planRig(domain.PeriodOpen, plansForPO())
	hdr := duty("VendorInvoicePost", "run-1", entityHeader, legalEntityID)
	first := rg.call(http.MethodPost, planRunPath, planBody(), hdr)

	replay := rg.call(http.MethodPost, planRunPath, planBody(), hdr)

	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" || replay.Body.String() != first.Body.String() || len(rg.ledger.Entries) != 2 {
		t.Fatalf("replay %d entries %d", replay.Code, len(rg.ledger.Entries))
	}
	evts := rg.st.Events()
	if len(evts) != 2 || evts[0].Type != "dev.passage.payables.settlement.posted.v1" || !strings.Contains(string(evts[0].Data), `"kind":"PLAN_ROW"`) || !strings.Contains(string(evts[0].Data), `"amount":"1000"`) {
		t.Fatalf("events %+v", evts)
	}
	assertDocumented(t, evts[0])
	rec := httptest.NewRecorder()
	rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "receiving_settlements_posted_total 2") {
		t.Fatalf("metrics %s", rec.Body.String())
	}
}

func ownershipChanges() []domain.OwnershipChange {
	change := func(id, le, vendor, amount string) domain.OwnershipChange {
		return domain.OwnershipChange{JournalID: id, LegalEntityID: le, VendorID: vendor, Currency: "USD", AccrualAccount: "2300", PostingDate: day(2026, 9, 30), Amount: dec(amount)}
	}
	return []domain.OwnershipChange{
		change("journal-1", legalEntityID, "V-1", "1250.00"), change("journal-2", legalEntityID, "V-2", "80.25"),
		change("journal-3", ledgerID, "V-1", "999.00"), change("journal-4", legalEntityID, "V-1", "0"),
	}
}

func consignmentRig(periods domain.PeriodStatus, changes domain.OwnershipChanges) *rig {
	return newRigPorts(memstore.New(), periods, func(p *api.Ports) { p.OwnershipChanges = changes })
}

func TestConsignmentSettlement_SettlesConsumedStockAgainstTheAccrualOnce(t *testing.T) {
	rg := consignmentRig(domain.PeriodOpen, domain.FakeOwnershipChanges{Changes: ownershipChanges()})

	first := decode[runJSON](t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), rg.settle()), http.StatusOK)

	if first.settledRefs() != "journal-1,journal-2" || first.Settled[0].Amount != "1250.00" || first.Settled[0].VendorID != "V-1" || first.Settled[1].Amount != "80.25" || first.Settled[0].Kind != "CONSIGNMENT" {
		t.Fatalf("run %+v", first)
	}
	if first.skips()["journal-4"] != "NO_AMOUNT" || len(first.Skipped) != 1 {
		t.Fatalf("skipped %v: only the zero-value journal of this legal entity is skipped", first.Skipped)
	}
	entry := rg.ledger.Entries[0]
	wantLines(t, entry, "2300=1250", "2000=-1250")
	if !entry.DocumentDate.Equal(day(2026, 9, 30)) || !entry.PostingDate.Equal(day(2026, 10, 15)) || entry.Source.Purpose != "consignment" {
		t.Fatalf("entry %+v", entry)
	}
	if rg.ledger.Balance("2300").String() != "1330.25" || rg.ledger.Balance("2000").String() != "-1330.25" {
		t.Fatalf("accrual %s vendor control %s", rg.ledger.Balance("2300"), rg.ledger.Balance("2000"))
	}

	again := decode[runJSON](t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), rg.settle()), http.StatusOK)

	if len(again.Settled) != 0 || len(rg.ledger.Entries) != 2 {
		t.Fatalf("second run settled %d, ledger entries %d", len(again.Settled), len(rg.ledger.Entries))
	}
}

func TestConsignmentSettlement_VendorFilterLeavesOtherVendorsForALaterRun(t *testing.T) {
	rg := consignmentRig(domain.PeriodOpen, domain.FakeOwnershipChanges{Changes: ownershipChanges()})

	only := decode[runJSON](t, rg.call(http.MethodPost, consignmentRunPath, runBody("", `"vendorId":"v-2"`), rg.settle()), http.StatusOK)
	rest := decode[runJSON](t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), rg.settle()), http.StatusOK)

	if only.settledRefs() != "journal-2" || only.skips()["journal-1"] != "OTHER_VENDOR" || rest.settledRefs() != "journal-1" {
		t.Fatalf("filtered run %+v, follow-up %+v", only, rest)
	}
}

func TestConsignmentSettlement_RejectionsPostNothing(t *testing.T) {
	tests := []struct {
		name    string
		changes domain.OwnershipChanges
		periods domain.PeriodStatus
		status  int
		code    string
	}{
		{"port not configured", nil, domain.PeriodOpen, http.StatusServiceUnavailable, "COM-0503"},
		{"inventory down", domain.FakeOwnershipChanges{Err: errors.New("down")}, domain.PeriodOpen, http.StatusServiceUnavailable, "COM-0503"},
		{"inventory forbidden", domain.FakeOwnershipChanges{Err: domain.ErrInventoryForbidden}, domain.PeriodOpen, http.StatusForbidden, "AUTH-0003"},
		{"journal vanished", domain.FakeOwnershipChanges{Err: domain.ErrOwnershipChangeNotFound}, domain.PeriodOpen, http.StatusNotFound, "RCV-NF-006"},
		{"closed period", domain.FakeOwnershipChanges{Changes: ownershipChanges()}, domain.PeriodClosed, http.StatusUnprocessableEntity, "PER-AP-001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := consignmentRig(tc.periods, tc.changes)

			problemCode(t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), rg.settle()), tc.status, tc.code)

			settled, err := rg.st.SettledSources(tenantCtx(), domain.SettlementConsignment, []string{"journal-1", "journal-2"})
			if len(rg.ledger.Entries) != 0 || len(settled) != 0 || err != nil {
				t.Fatalf("a rejected run posted %d entries and recorded %v (%v)", len(rg.ledger.Entries), settled, err)
			}
		})
	}
}

func TestConsignmentSettlement_ValidationAuthAndScope(t *testing.T) {
	rg := consignmentRig(domain.PeriodOpen, domain.FakeOwnershipChanges{Changes: ownershipChanges()})

	rec := rg.call(http.MethodPost, consignmentRunPath, strings.Replace(runBody(""), legalEntityID, "nope", 1), rg.settle())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"legalEntityId"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	problemCode(t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), duty("ReceivingPost", "k", entityHeader, legalEntityID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), duty("VendorInvoicePost", "k", entityHeader, ledgerID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), headers{}), http.StatusUnauthorized, "AUTH-0001")
	if len(rg.ledger.Entries) != 0 {
		t.Fatal("an invalid or unauthorised run reached the ledger")
	}
}

func productPlanRig() *rig { return productPlanRigFor(productID) }

func productPlanRigFor(product string) *rig {
	po := planPO()
	po.Lines[0].ProductID = product
	plans := []domain.InvoicingPlan{{PurchaseOrderID: poID, LineNo: 1, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(row(1, day(2026, 9, 1), "1000.00", "OPEN"))}}
	return newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: po}}
		p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{poID: plans}}
	})
}

func (rg *rig) invoiceFor(receiptID, number string) *httptest.ResponseRecorder {
	return rg.call(http.MethodPost, invoicesPath, invoiceBody(number, receiptID, 4, ""), rg.apPosterFor(legalEntityID))
}

func TestPlanSettlement_SettledLineCannotBeInvoicedAgainstItsReceipt(t *testing.T) {
	rg := productPlanRig()
	receipt := rg.posted(t, "PS-1", 100, 100)
	settled := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)
	entries := len(rg.ledger.Entries)

	problemCode(t, rg.invoiceFor(receipt.ID, "INV-1"), http.StatusUnprocessableEntity, "AP-PLN-001")

	if settled.settledRefs() != poID+"/1/1" || len(rg.ledger.Entries) != entries {
		t.Fatalf("settled %s, ledger entries %d (was %d): the refused invoice must not post", settled.settledRefs(), len(rg.ledger.Entries), entries)
	}
}

func TestPlanSettlement_InvoicedLineIsSkippedByTheRun(t *testing.T) {
	rg := productPlanRig()
	receipt := rg.posted(t, "PS-1", 100, 100)
	decode[invoiceJSON](t, rg.invoiceFor(receipt.ID, "INV-1"), http.StatusCreated)
	entries := len(rg.ledger.Entries)

	run := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	if len(run.Settled) != 0 || run.skips()[poID+"/1/1"] != "LINE_INVOICED" || len(rg.ledger.Entries) != entries {
		t.Fatalf("settled %d, skips %v, ledger entries %d (was %d)", len(run.Settled), run.skips(), len(rg.ledger.Entries), entries)
	}
}

func TestPlanSettlement_ConcurrentInvoiceAndRunNeverBothPost(t *testing.T) {
	for round := range 20 {
		rg := productPlanRig()
		receipt := rg.posted(t, "PS-1", 100, 100)
		entries := len(rg.ledger.Entries)
		invoiceHdr, runHdr := rg.apPosterFor(legalEntityID), rg.settle()
		var invoiceRec, runRec *httptest.ResponseRecorder
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			invoiceRec = rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", receipt.ID, 4, ""), invoiceHdr)
		}()
		go func() {
			defer wg.Done()
			runRec = rg.call(http.MethodPost, planRunPath, planBody(), runHdr)
		}()
		wg.Wait()

		invoiced := invoiceRec.Code == http.StatusCreated
		settled := runRec.Code == http.StatusOK && strings.Contains(runRec.Body.String(), `"kind":"PLAN_ROW"`)
		if runRec.Code != http.StatusOK || (!invoiced && invoiceRec.Code != http.StatusUnprocessableEntity) || invoiced == settled || len(rg.ledger.Entries) != entries+1 {
			t.Fatalf("round %d: invoice %d %s, run %d %s, ledger entries %d (was %d)", round, invoiceRec.Code, invoiceRec.Body.String(), runRec.Code, runRec.Body.String(), len(rg.ledger.Entries), entries)
		}
	}
}

func TestPlanSettlement_UnknownRowStatusHasItsOwnSkipReason(t *testing.T) {
	plans := []domain.InvoicingPlan{{PurchaseOrderID: poID, LineNo: 1, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(
		row(1, day(2026, 9, 1), "10.00", "INVOICED"), row(2, day(2026, 9, 1), "10.00", "VOID"))}}
	rg := planRig(domain.PeriodOpen, plans)

	run := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	if run.skips()[poID+"/1/1"] != "ALREADY_INVOICED" || run.skips()[poID+"/1/2"] != "UNKNOWN_ROW_STATUS" || len(run.Settled) != 0 {
		t.Fatalf("run %+v", run)
	}
}

func TestSettlementRuns_RejectFieldsOfTheOtherRun(t *testing.T) {
	plan := planRig(domain.PeriodOpen, plansForPO())
	consignment := consignmentRig(domain.PeriodOpen, domain.FakeOwnershipChanges{Changes: ownershipChanges()})

	for name, rec := range map[string]*httptest.ResponseRecorder{
		"vendorId on a plan run":           plan.call(http.MethodPost, planRunPath, planBody(`"vendorId":"V-1"`), plan.settle()),
		"purchaseOrderId on a consignment": consignment.call(http.MethodPost, consignmentRunPath, runBody("", `"purchaseOrderId":"`+poID+`"`), consignment.settle()),
	} {
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s: got %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if len(plan.ledger.Entries)+len(consignment.ledger.Entries) != 0 {
		t.Fatal("a rejected run reached the ledger")
	}
}

func TestConsignmentSettlement_SkipsAJournalOfAnotherLegalEntityReturnedByGet(t *testing.T) {
	foreign := domain.OwnershipChange{JournalID: "journal-9", LegalEntityID: ledgerID, VendorID: "V-1", Currency: "USD", AccrualAccount: "2300", PostingDate: day(2026, 9, 30), Amount: dec("5.00")}
	rg := consignmentRig(domain.PeriodOpen, mislabelled{FakeOwnershipChanges: domain.FakeOwnershipChanges{Changes: []domain.OwnershipChange{foreign}}, listed: []string{"journal-9"}})

	run := decode[runJSON](t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), rg.settle()), http.StatusOK)

	if len(run.Settled) != 0 || run.skips()["journal-9"] != "OTHER_LEGAL_ENTITY" || len(rg.ledger.Entries) != 0 {
		t.Fatalf("run %+v ledger entries %d", run, len(rg.ledger.Entries))
	}
}

type mislabelled struct {
	domain.FakeOwnershipChanges
	listed []string
}

func (m mislabelled) List(context.Context, string) ([]string, bool, error) {
	return m.listed, false, nil
}

func TestConsignmentSettlement_TruncatedJournalListMarksTheRunAsHavingMore(t *testing.T) {
	rg := consignmentRig(domain.PeriodOpen, domain.FakeOwnershipChanges{Changes: ownershipChanges(), Truncated: true})

	run := decode[runJSON](t, rg.call(http.MethodPost, consignmentRunPath, runBody(""), rg.settle()), http.StatusOK)

	if !run.HasMore || run.settledRefs() != "journal-1,journal-2" {
		t.Fatalf("run %+v", run)
	}
}

func TestPlanSettlement_LineWithoutProductIsSkippedNotPaid(t *testing.T) {
	rg := productPlanRigFor("")

	run := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	if len(run.Settled) != 0 || run.skips()[poID+"/1/1"] != "NO_PRODUCT" || len(rg.ledger.Entries) != 0 {
		t.Fatalf("run %+v", run)
	}
}

func TestPlanSettlement_UppercaseProductIDStillExcludesInvoicing(t *testing.T) {
	rg := productPlanRigFor(strings.ToUpper(productID))
	receipt := rg.posted(t, "PS-1", 100, 100)
	decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

	problemCode(t, rg.invoiceFor(receipt.ID, "INV-1"), http.StatusUnprocessableEntity, "AP-PLN-001")
}

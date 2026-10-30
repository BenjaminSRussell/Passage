package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	ersRunPath      = "/v1/receiving/ers-settlements:run"
	auditPolicyPath = "/v1/receiving/audit-policy"
	ersVendorPath   = "/v1/receiving/ers-vendors/" + vendorID
	invoicesBase    = "/v1/receiving/invoices/"
	releaseSuffix   = ":release-audit-block"
)

type switchPeriods struct {
	mu     sync.Mutex
	status domain.PeriodStatus
}

func (s *switchPeriods) set(status domain.PeriodStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
}

func (s *switchPeriods) AP(context.Context, domain.PeriodQuery) (domain.PeriodStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, nil
}

type ersRunJSON struct {
	Settled []struct {
		InvoiceID     string `json:"invoiceId"`
		InvoiceNumber string `json:"invoiceNumber"`
		ReceiptID     string `json:"receiptId"`
		ReceiptNumber string `json:"receiptNumber"`
		VendorID      string `json:"vendorId"`
		Total         string `json:"total"`
		Status        string `json:"status"`
		LedgerEntryID string `json:"ledgerEntryId"`
	} `json:"settled"`
	Skipped []struct {
		SourceRef string `json:"sourceRef"`
		Reason    string `json:"reason"`
	} `json:"skipped"`
	HasMore bool `json:"hasMore"`
}

func (r ersRunJSON) reasons() []string {
	var out []string
	for _, s := range r.Skipped {
		out = append(out, s.Reason)
	}
	return out
}

type auditedInvoiceJSON struct {
	ID            string `json:"id"`
	InvoiceNumber string `json:"invoiceNumber"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	Total         string `json:"total"`
	LedgerEntryID string `json:"ledgerEntryId"`
	ReleasedBy    string `json:"releasedBy"`
	ReleasedAt    string `json:"releasedAt"`
}

func testSampler(seed string) *domain.Sampler {
	s, err := domain.NewSampler([]byte(seed))
	if err != nil {
		panic(err)
	}
	return s
}

func ersOrder(price string, mutate func(*domain.PurchaseOrder)) domain.PurchaseOrder {
	po := domain.PurchaseOrder{
		ID: poID, VendorID: vendorID, Currency: "USD", LegalEntityID: legalEntityID, ApprovalStatus: "APPROVED",
		Lines: []domain.PurchaseOrderLine{{LineNo: 1, ProductID: productID, Unit: "EA", UnitPrice: dec(price), PriceUnit: dec("1"), Status: "OPEN"}},
	}
	if mutate != nil {
		mutate(&po)
	}
	return po
}

func ersRig(price string, periods domain.PeriodControl, sampler *domain.Sampler) *rig {
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder(price, nil)}}
		p.InvoicingPlans = domain.FakeInvoicingPlans{Plans: map[string][]domain.InvoicingPlan{poID: {
			{PurchaseOrderID: poID, LineNo: 1, SettleWithoutReceipt: true, Currency: "USD", Rows: planRows(row(1, day(2026, 9, 1), "5.00", "OPEN"))},
		}}}
		p.AuditSampler = sampler
		if periods != nil {
			p.Periods = periods
		}
	})
	return rg
}

func userDuty(name, key, user string, extra ...string) headers {
	return duty(name, key, append([]string{tenant.HeaderUser, user}, extra...)...)
}

func (rg *rig) policyMaker() headers {
	return userDuty("MatchingPolicyMaintain", rg.key(), "policy-admin")
}

func (rg *rig) enableErs(t *testing.T, enabled bool) {
	t.Helper()
	body := fmt.Sprintf(`{"legalEntityId":%q,"enabled":%v}`, legalEntityID, enabled)
	rec := rg.call(http.MethodPut, ersVendorPath, body, rg.policyMaker())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"enabled":%v`, enabled)) {
		t.Fatalf("set ERS vendor: %d %s", rec.Code, rec.Body.String())
	}
}

func (rg *rig) sampleRate(t *testing.T, rate string) {
	t.Helper()
	rec := rg.call(http.MethodPut, auditPolicyPath, fmt.Sprintf(`{"legalEntityId":%q,"sampleRatePct":%s}`, legalEntityID, rate), rg.policyMaker())
	if rec.Code != http.StatusOK {
		t.Fatalf("set audit policy: %d %s", rec.Code, rec.Body.String())
	}
}

func (rg *rig) ersRun(t *testing.T, want int) ersRunJSON {
	t.Helper()
	return decode[ersRunJSON](t, rg.call(http.MethodPost, ersRunPath, runBody("", `"vendorId":"`+vendorID+`"`), rg.settle()), want)
}

func (rg *rig) manualInvoice(t *testing.T, number, receiptID string, qty int, user string) *httpResult {
	t.Helper()
	rec := rg.call(http.MethodPost, invoicesPath, invoiceBody(number, receiptID, qty, ""), userDuty("VendorInvoicePost", rg.key(), user))
	return &httpResult{code: rec.Code, body: rec.Body.String()}
}

type httpResult struct {
	code int
	body string
}

func (h *httpResult) invoice(t *testing.T, want int) auditedInvoiceJSON {
	t.Helper()
	if h.code != want {
		t.Fatalf("got %d %s, want %d", h.code, h.body, want)
	}
	var out auditedInvoiceJSON
	if err := json.Unmarshal([]byte(h.body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (rg *rig) release(invoiceID, user string) (int, string) {
	rec := rg.call(http.MethodPost, invoicesBase+invoiceID+releaseSuffix, fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID), userDuty("InvoiceAuditRelease", rg.key(), user))
	return rec.Code, rec.Body.String()
}

func grirStatus(t *testing.T, rg *rig) string {
	t.Helper()
	page := decode[struct {
		Items []struct{ Status string }
	}](t, rg.call(http.MethodGet, grirPath, "", duty("APInquire", "")), http.StatusOK)
	if len(page.Items) != 1 {
		t.Fatalf("grir items %+v", page.Items)
	}
	return page.Items[0].Status
}

func TestErs_SettlesAPostedReceiptAtThePurchaseOrderPriceExactlyOnce(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.enableErs(t, true)
	r := rg.posted(t, "PS-1", 10, 4)

	first := rg.ersRun(t, http.StatusOK)

	if len(first.Settled) != 1 || len(first.Skipped) != 0 || first.HasMore {
		t.Fatalf("first run %+v", first)
	}
	s := first.Settled[0]
	if s.InvoiceNumber != "ERS-"+s.ReceiptNumber || !strings.HasPrefix(s.ReceiptNumber, "RCV-") || s.ReceiptID != r.ID || s.VendorID != vendorID || s.Total != "10.40" || s.Status != "POSTED" || s.LedgerEntryID == "" {
		t.Fatalf("settled %+v", s)
	}
	if len(rg.ledger.Entries) != 2 {
		t.Fatalf("ledger entries %d, want the receipt and the self-billed invoice", len(rg.ledger.Entries))
	}
	entry := lastEntry(t, rg)
	wantLines(t, entry, "2100=10", "1400=0.4", "2000=-10.4")
	if entry.Source.DocumentType != "vendorinvoice" || entry.Source.DocumentID != s.InvoiceID || !entry.PostingDate.Equal(day(2026, 10, 15)) {
		t.Fatalf("entry %+v", entry)
	}
	if rg.ledger.Balance("2100").String() != "0" || rg.ledger.Balance("2000").String() != "-10.4" {
		t.Fatalf("GR/IR %s vendor control %s", rg.ledger.Balance("2100"), rg.ledger.Balance("2000"))
	}
	evts := rg.st.Events()
	last := evts[len(evts)-1]
	if last.Type != "dev.passage.payables.invoice.posted.v1" || !strings.Contains(string(last.Data), `"source":"ERS"`) || !strings.Contains(string(last.Data), `"invoice_number":"`+s.InvoiceNumber+`"`) {
		t.Fatalf("last event %s %s", last.Type, last.Data)
	}
	if grirStatus(t, rg) != "CLEARED" {
		t.Fatal("the GR/IR item is not cleared")
	}

	again := rg.ersRun(t, http.StatusOK)
	if len(again.Settled) != 0 || len(rg.ledger.Entries) != 2 {
		t.Fatalf("second run settled %d, ledger entries %d", len(again.Settled), len(rg.ledger.Entries))
	}
}

func TestErs_SettlesEveryOpenLineOfAReceiptAtItsOwnPOLinePrice(t *testing.T) {
	second := "99999999-9999-4999-8999-999999999990"
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(po *domain.PurchaseOrder) {
			po.Lines = append(po.Lines, domain.PurchaseOrderLine{LineNo: 2, ProductID: second, Unit: "EA", UnitPrice: dec("20"), PriceUnit: dec("10"), DiscountPct: dec("10"), Status: "OPEN"})
		})}}
	})
	rg.enableErs(t, true)
	body := strings.Replace(directBody("PS-2L", 10, 4, ""), `"lines":[`, `"lines":[{"poLineId":"`+poLineID[:len(poLineID)-1]+`6","productId":"`+second+`","orderedRemaining":10,"overDeliveryPct":5,"unitPrice":1.5,"unit":"EA","qty":10,"locationId":"`+locationID+`"},`, 1)
	r := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, body, rg.maintain()), http.StatusCreated)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":post", "", rg.poster(r.Version)), http.StatusOK)

	run := rg.ersRun(t, http.StatusOK)

	if len(run.Settled) != 1 || run.Settled[0].Total != "28.40" {
		t.Fatalf("run %+v: want 10 x (20/10 less 10%% = 1.80) = 18.00 plus 4 x 2.60 = 10.40", run)
	}
	wantLines(t, lastEntry(t, rg), "2100=15", "1400=3", "2100=10", "1400=0.4", "2000=-28.4")
}

func TestErs_OnlyEnabledVendorsAreSettledAndEverySkipIsExplained(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*api.Ports)
		reason string
	}{
		{"vendor not enabled", nil, "NOT_ERS_VENDOR"},
		{"order of another currency", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(o *domain.PurchaseOrder) { o.Currency = "EUR" })}}
		}, "CURRENCY_MISMATCH"},
		{"order not approved", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(o *domain.PurchaseOrder) { o.ApprovalStatus = "DRAFT" })}}
		}, "PO_NOT_APPROVED"},
		{"order missing", func(p *api.Ports) { p.PurchaseOrders = domain.FakePurchaseOrders{} }, "PO_NOT_FOUND"},
		{"order forbidden", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Err: domain.ErrPurchaseOrderForbidden}
		}, "PO_FORBIDDEN"},
		{"order of another vendor", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(o *domain.PurchaseOrder) { o.VendorID = poID })}}
		}, "OTHER_VENDOR"},
		{"no line for the product", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(o *domain.PurchaseOrder) { o.Lines[0].ProductID = vendorID })}}
		}, "NO_PO_LINE"},
		{"only a cancelled line for the product", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(o *domain.PurchaseOrder) { o.Lines[0].Status = "CANCELLED" })}}
		}, "NO_PO_LINE"},
		{"two lines for the product", func(p *api.Ports) {
			p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(o *domain.PurchaseOrder) {
				o.Lines = append(o.Lines, domain.PurchaseOrderLine{LineNo: 2, ProductID: productID, UnitPrice: dec("3"), PriceUnit: dec("1"), Status: "OPEN"})
			})}}
		}, "AMBIGUOUS_PO_LINE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
				p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", nil)}}
				if tc.mutate != nil {
					tc.mutate(p)
				}
			})
			if tc.reason != "NOT_ERS_VENDOR" {
				rg.enableErs(t, true)
			}
			rg.posted(t, "PS-1", 10, 4)

			run := rg.ersRun(t, http.StatusOK)

			if len(run.Settled) != 0 || strings.Join(run.reasons(), ",") != tc.reason || len(rg.ledger.Entries) != 1 {
				t.Fatalf("run %+v ledger entries %d, want one skip %s and only the receipt posted", run, len(rg.ledger.Entries), tc.reason)
			}
			if !strings.HasPrefix(run.Skipped[0].SourceRef, "RCV-") {
				t.Fatalf("skip names %q, want the receipt number", run.Skipped[0].SourceRef)
			}
		})
	}
}

func TestErs_DisablingAVendorStopsSettlement(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.enableErs(t, true)
	rg.enableErs(t, false)
	rg.posted(t, "PS-1", 10, 4)

	run := rg.ersRun(t, http.StatusOK)

	if len(run.Settled) != 0 || strings.Join(run.reasons(), ",") != "NOT_ERS_VENDOR" {
		t.Fatalf("run %+v", run)
	}
}

func TestErs_RejectionsPostNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*api.Ports)
		body   string
		header func(*rig) headers
		status int
		code   string
	}{
		{"neither vendor nor order", nil, runBody(""), nil, http.StatusBadRequest, "COM-0001"},
		{"vendor not a uuid", nil, runBody("", `"vendorId":"x"`), nil, http.StatusBadRequest, "COM-0001"},
		{"order not a uuid", nil, runBody("", `"purchaseOrderId":"x"`), nil, http.StatusBadRequest, "COM-0001"},
		{"foreign ledger", nil, strings.Replace(runBody("", `"vendorId":"`+vendorID+`"`), ledgerID, poID, 1), nil, http.StatusUnprocessableEntity, "RCV-VAL-001"},
		{"legal entity outside the caller's scope", nil, runBody("", `"vendorId":"`+vendorID+`"`), func(rg *rig) headers { return rg.apPosterFor(ledgerID) }, http.StatusForbidden, "AUTH-0003"},
		{"caller without the posting duty", nil, runBody("", `"vendorId":"`+vendorID+`"`), func(rg *rig) headers { return duty("ReceivingPost", rg.key()) }, http.StatusForbidden, "AUTH-0003"},
		{"purchase order lookup down", func(p *api.Ports) { p.PurchaseOrders = domain.FakePurchaseOrders{Err: errors.New("down")} }, runBody("", `"vendorId":"`+vendorID+`"`), nil, http.StatusServiceUnavailable, "COM-0503"},
		{"purchase order port missing", func(p *api.Ports) { p.PurchaseOrders = nil }, runBody("", `"vendorId":"`+vendorID+`"`), nil, http.StatusServiceUnavailable, "COM-0503"},
		{"ledger down", func(p *api.Ports) { p.Ledger = &domain.FakeLedger{Err: errors.New("down")} }, runBody("", `"vendorId":"`+vendorID+`"`), nil, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
				p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", nil)}}
				if tc.mutate != nil {
					tc.mutate(p)
				}
			})
			rg.enableErs(t, true)
			setup := newRigPorts(rg.st, domain.PeriodOpen, func(p *api.Ports) { p.Ledger = rg.ledger })
			setup.keys = 1000
			setup.posted(t, "PS-1", 10, 4)
			before := len(rg.ledger.Entries)
			hdr := rg.settle()
			if tc.header != nil {
				hdr = tc.header(rg)
			}

			problemCode(t, rg.call(http.MethodPost, ersRunPath, tc.body, hdr), tc.status, tc.code)

			if len(rg.ledger.Entries) != before {
				t.Fatalf("the run posted %d entries", len(rg.ledger.Entries)-before)
			}
		})
	}
}

func TestErs_ClosedPeriodStopsTheRunAndLeavesTheReceiptSettleable(t *testing.T) {
	periods := &switchPeriods{status: domain.PeriodOpen}
	rg := ersRig("2.60", periods, nil)
	rg.enableErs(t, true)
	rg.posted(t, "PS-1", 10, 4)
	periods.set(domain.PeriodClosed)

	problemCode(t, rg.call(http.MethodPost, ersRunPath, runBody("", `"vendorId":"`+vendorID+`"`), rg.settle()), http.StatusUnprocessableEntity, "PER-AP-001")
	if len(rg.ledger.Entries) != 1 || grirStatus(t, rg) != "OPEN" {
		t.Fatalf("ledger entries %d, GR/IR %s", len(rg.ledger.Entries), grirStatus(t, rg))
	}

	periods.set(domain.PeriodOpen)
	if run := rg.ersRun(t, http.StatusOK); len(run.Settled) != 1 {
		t.Fatalf("run after reopening %+v", run)
	}
}

func TestErs_RunIsScopedToTheOrderAndResumesAtTheCap(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.enableErs(t, true)
	for i := range 101 {
		rg.posted(t, fmt.Sprintf("PS-%03d", i), 10, 1)
	}
	byOrder := runBody("", `"purchaseOrderId":"`+poID+`"`)

	first := decode[ersRunJSON](t, rg.call(http.MethodPost, ersRunPath, byOrder, rg.settle()), http.StatusOK)
	second := decode[ersRunJSON](t, rg.call(http.MethodPost, ersRunPath, byOrder, rg.settle()), http.StatusOK)

	if len(first.Settled) != 100 || !first.HasMore || len(second.Settled) != 1 || second.HasMore || len(rg.ledger.Entries) != 202 {
		t.Fatalf("first %d (more %v), second %d (more %v), ledger entries %d", len(first.Settled), first.HasMore, len(second.Settled), second.HasMore, len(rg.ledger.Entries))
	}
}

func TestErsAndNormalInvoicingOfOneLineExcludeEachOtherInBothOrders(t *testing.T) {
	t.Run("ERS first refuses the manual invoice", func(t *testing.T) {
		rg := ersRig("2.60", nil, nil)
		rg.enableErs(t, true)
		r := rg.posted(t, "PS-1", 10, 4)
		rg.ersRun(t, http.StatusOK)
		before := len(rg.ledger.Entries)

		rec := rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-9", r.ID, 4, ""), rg.apPoster())

		problemCode(t, rec, http.StatusUnprocessableEntity, "AP-QTY-001")
		if len(rg.ledger.Entries) != before {
			t.Fatal("the refused invoice posted")
		}
	})
	t.Run("a partial manual invoice keeps the line away from ERS", func(t *testing.T) {
		rg := ersRig("2.60", nil, nil)
		rg.enableErs(t, true)
		r := rg.posted(t, "PS-1", 10, 4)
		rg.manualInvoice(t, "INV-1", r.ID, 1, "alice").invoice(t, http.StatusCreated)

		run := rg.ersRun(t, http.StatusOK)

		if len(run.Settled) != 0 || strings.Join(run.reasons(), ",") != "PARTIALLY_INVOICED" || len(rg.ledger.Entries) != 2 || grirStatus(t, rg) != "PARTIAL" {
			t.Fatalf("run %+v ledger entries %d GR/IR %s", run, len(rg.ledger.Entries), grirStatus(t, rg))
		}
	})
	t.Run("a fully invoiced line is not settled again", func(t *testing.T) {
		rg := ersRig("2.60", nil, nil)
		rg.enableErs(t, true)
		r := rg.posted(t, "PS-1", 10, 4)
		rg.manualInvoice(t, "INV-1", r.ID, 4, "alice").invoice(t, http.StatusCreated)

		run := rg.ersRun(t, http.StatusOK)

		if len(run.Settled) != 0 || len(rg.ledger.Entries) != 2 {
			t.Fatalf("run %+v ledger entries %d", run, len(rg.ledger.Entries))
		}
	})
	t.Run("an invoice already numbered like the self-billed one is reported, not duplicated", func(t *testing.T) {
		rg := ersRig("2.60", nil, nil)
		rg.enableErs(t, true)
		other := rg.posted(t, "PS-0", 10, 1)
		rg.posted(t, "PS-1", 10, 4)
		rg.manualInvoice(t, "ERS-RCV-00000002", other.ID, 1, "alice").invoice(t, http.StatusCreated)
		before := len(rg.ledger.Entries)

		run := rg.ersRun(t, http.StatusOK)

		if strings.Join(run.reasons(), ",") != "ALREADY_SETTLED" || len(run.Settled) != 0 || len(rg.ledger.Entries) != before {
			t.Fatalf("run %+v ledger entries %d (was %d)", run, len(rg.ledger.Entries), before)
		}
	})
	t.Run("a plan settled line is skipped and posts nothing", func(t *testing.T) {
		rg := ersRig("2.60", nil, nil)
		rg.enableErs(t, true)
		rg.posted(t, "PS-1", 10, 4)
		plan := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)
		if len(plan.Settled) != 1 {
			t.Fatalf("plan run %+v", plan)
		}

		run := rg.ersRun(t, http.StatusOK)

		if len(run.Settled) != 0 || strings.Join(run.reasons(), ",") != "PLAN_SETTLED" || len(rg.ledger.Entries) != 2 {
			t.Fatalf("run %+v ledger entries %d", run, len(rg.ledger.Entries))
		}
	})
	t.Run("a self-billed line is skipped by the plan settlement", func(t *testing.T) {
		rg := ersRig("2.60", nil, nil)
		rg.enableErs(t, true)
		rg.posted(t, "PS-1", 10, 4)
		rg.ersRun(t, http.StatusOK)

		plan := decode[runJSON](t, rg.call(http.MethodPost, planRunPath, planBody(), rg.settle()), http.StatusOK)

		if len(plan.Settled) != 0 || plan.skips()[poID+"/1/1"] != "LINE_INVOICED" || len(rg.ledger.Entries) != 2 {
			t.Fatalf("plan run %+v ledger entries %d", plan, len(rg.ledger.Entries))
		}
	})
}

func TestErsAndManualInvoiceRacingForOneReceiptPostOnce(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.enableErs(t, true)
	r := rg.posted(t, "PS-1", 10, 4)
	const racers = 6
	var wg sync.WaitGroup
	statuses := make([]int, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				statuses[i] = rg.call(http.MethodPost, ersRunPath, runBody("", `"vendorId":"`+vendorID+`"`), duty("VendorInvoicePost", fmt.Sprintf("race-ers-%d", i))).Code
				return
			}
			statuses[i] = rg.call(http.MethodPost, invoicesPath, invoiceBody(fmt.Sprintf("INV-%d", i), r.ID, 4, ""), duty("VendorInvoicePost", fmt.Sprintf("race-man-%d", i))).Code
		}()
	}
	wg.Wait()

	if len(rg.ledger.Entries) != 2 {
		t.Fatalf("%d ledger entries (statuses %v), want the receipt and exactly one invoice", len(rg.ledger.Entries), statuses)
	}
	if rg.ledger.Balance("2100").String() != "0" {
		t.Fatalf("GR/IR %s, the line was cleared more than once", rg.ledger.Balance("2100"))
	}
}

func TestAuditBlock_HoldsTheInvoiceUntilAnAuditorReleasesIt(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.sampleRate(t, "100")
	r := rg.posted(t, "PS-1", 10, 4)
	receiptEntries := len(rg.ledger.Entries)

	held := rg.manualInvoice(t, "INV-1", r.ID, 4, "alice").invoice(t, http.StatusCreated)

	if held.Status != "AUDIT_BLOCKED" || held.Source != "MANUAL" || held.LedgerEntryID != "" || held.Total != "10.00" || held.ReleasedBy != "" {
		t.Fatalf("held %+v", held)
	}
	if len(rg.ledger.Entries) != receiptEntries || grirStatus(t, rg) != "CLEARED" {
		t.Fatalf("ledger entries %d (was %d), GR/IR %s: want nothing posted and the quantity reserved", len(rg.ledger.Entries), receiptEntries, grirStatus(t, rg))
	}
	evts := rg.st.Events()
	if sampled := evts[len(evts)-1]; sampled.Type != "dev.passage.payables.invoice.sampled.v1" || !strings.Contains(string(sampled.Data), `"sample_rate_pct":"100"`) || !strings.Contains(string(sampled.Data), `"total":"10"`) {
		t.Fatalf("event %s %s", sampled.Type, sampled.Data)
	}
	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-2", r.ID, 1, ""), userDuty("VendorInvoicePost", rg.key(), "alice")), http.StatusUnprocessableEntity, "AP-QTY-001")

	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(held.ID, "DEBIT", "DN-1", adjLine(1, "1.00")), rg.apPosterFor(legalEntityID)), http.StatusConflict, "AP-AUD-001")

	code, body := rg.release(held.ID, "auditor")
	if code != http.StatusOK {
		t.Fatalf("release %d %s", code, body)
	}
	var released auditedInvoiceJSON
	if err := json.Unmarshal([]byte(body), &released); err != nil {
		t.Fatal(err)
	}
	if released.Status != "POSTED" || released.ReleasedBy != "auditor" || released.ReleasedAt == "" || released.LedgerEntryID == "" || released.ID != held.ID {
		t.Fatalf("released %+v", released)
	}
	entry := lastEntry(t, rg)
	wantLines(t, entry, "2100=10", "2000=-10")
	if entry.IdempotencyKey != "post:receiving:vendorinvoice:"+held.ID+":invoice" || len(rg.ledger.Entries) != receiptEntries+1 || rg.ledger.Balance("2100").String() != "0" {
		t.Fatalf("entry %+v ledger entries %d GR/IR %s", entry, len(rg.ledger.Entries), rg.ledger.Balance("2100"))
	}
	evts = rg.st.Events()
	if posted := evts[len(evts)-1]; posted.Type != "dev.passage.payables.invoice.posted.v1" || !strings.Contains(string(posted.Data), `"invoice_number":"INV-1"`) {
		t.Fatalf("event %s %s", posted.Type, posted.Data)
	}

	code, body = rg.release(held.ID, "second-auditor")
	if code != http.StatusConflict || !strings.Contains(body, "AP-AUD-002") || len(rg.ledger.Entries) != receiptEntries+1 {
		t.Fatalf("second release %d %s", code, body)
	}
	if rec := rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(held.ID, "DEBIT", "DN-1", adjLine(1, "1.00")), rg.apPosterFor(legalEntityID)); rec.Code != http.StatusCreated {
		t.Fatalf("adjustment after the release: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAuditRelease_DutyIdentityAndScopeChecks(t *testing.T) {
	periods := &switchPeriods{status: domain.PeriodOpen}
	rg := ersRig("2.60", periods, testSampler("seed-one-0123456789"))
	rg.sampleRate(t, "100")
	r := rg.posted(t, "PS-1", 10, 4)
	held := rg.manualInvoice(t, "INV-1", r.ID, 4, "alice").invoice(t, http.StatusCreated)
	path := invoicesBase + held.ID + releaseSuffix
	body := fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID)
	entries := len(rg.ledger.Entries)

	for _, tc := range []struct {
		name   string
		path   string
		body   string
		hdr    headers
		status int
		code   string
	}{
		{"the poster's duty is not the release duty", path, body, userDuty("VendorInvoicePost", rg.key(), "bob"), http.StatusForbidden, "AUTH-0003"},
		{"the policy duty is not the release duty", path, body, userDuty("MatchingPolicyMaintain", rg.key(), "bob"), http.StatusForbidden, "AUTH-0003"},
		{"the releaser must not be the poster", path, body, userDuty("InvoiceAuditRelease", rg.key(), "alice"), http.StatusForbidden, "AP-AUD-003"},
		{"a release needs a user", path, body, userDuty("InvoiceAuditRelease", rg.key(), ""), http.StatusUnauthorized, "AUTH-0001"},
		{"legal entity outside the caller's scope", path, strings.Replace(body, legalEntityID, ledgerID, 1), userDuty("InvoiceAuditRelease", rg.key(), "bob", entityHeader, legalEntityID), http.StatusForbidden, "AUTH-0003"},
		{"legal entity of another invoice owner", path, strings.Replace(body, legalEntityID, ledgerID, 1), userDuty("InvoiceAuditRelease", rg.key(), "bob", entityHeader, legalEntityID+","+ledgerID), http.StatusUnprocessableEntity, "RCV-VAL-001"},
		{"legal entity missing", path, `{}`, userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusBadRequest, "COM-0001"},
		{"id not a uuid", invoicesBase + "x" + releaseSuffix, body, userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusBadRequest, "COM-0001"},
		{"unknown invoice", invoicesBase + poID + releaseSuffix, body, userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusNotFound, "RCV-NF-005"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problemCode(t, rg.call(http.MethodPost, tc.path, tc.body, tc.hdr), tc.status, tc.code)
			if len(rg.ledger.Entries) != entries {
				t.Fatal("a refused release posted")
			}
		})
	}

	periods.set(domain.PeriodClosed)
	if code, out := rg.release(held.ID, "bob"); code != http.StatusUnprocessableEntity || !strings.Contains(out, "PER-AP-001") || len(rg.ledger.Entries) != entries {
		t.Fatalf("release into a closed period %d %s", code, out)
	}
	periods.set(domain.PeriodOpen)
	if code, out := rg.release(held.ID, "bob"); code != http.StatusOK || len(rg.ledger.Entries) != entries+1 {
		t.Fatalf("release after reopening %d %s", code, out)
	}
}

func TestAuditBlock_AppliesToSelfBilledInvoicesToo(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-two-0123456789"))
	rg.enableErs(t, true)
	rg.sampleRate(t, "100.00")
	rg.posted(t, "PS-1", 10, 4)

	run := rg.ersRun(t, http.StatusOK)

	if len(run.Settled) != 1 || run.Settled[0].Status != "AUDIT_BLOCKED" || run.Settled[0].LedgerEntryID != "" || run.Settled[0].Total != "10.40" || len(rg.ledger.Entries) != 1 {
		t.Fatalf("run %+v ledger entries %d", run, len(rg.ledger.Entries))
	}
	if again := rg.ersRun(t, http.StatusOK); len(again.Settled) != 0 {
		t.Fatalf("a held self-billed invoice was settled again: %+v", again)
	}

	code, body := rg.release(run.Settled[0].InvoiceID, "auditor")

	if code != http.StatusOK || !strings.Contains(body, `"source":"ERS"`) || !strings.Contains(body, `"status":"POSTED"`) {
		t.Fatalf("release %d %s", code, body)
	}
	wantLines(t, lastEntry(t, rg), "2100=10", "1400=0.4", "2000=-10.4")
}

func TestAuditBlock_DecisionIsReproduciblePerInvoiceAndFollowsTheRate(t *testing.T) {
	const invoices = 12
	rate := dec("40")
	twin := testSampler("seed-one-0123456789")
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.sampleRate(t, "40")
	blockedAny, postedAny := false, false
	for i := range invoices {
		r := rg.posted(t, fmt.Sprintf("PS-%02d", i), 10, 1)
		got := rg.manualInvoice(t, fmt.Sprintf("INV-%02d", i), r.ID, 1, "alice").invoice(t, http.StatusCreated)
		want := "POSTED"
		if twin.Blocks(got.ID, rate) {
			want = "AUDIT_BLOCKED"
		}
		if got.Status != want {
			t.Fatalf("invoice %d (%s) is %s, want %s from HMAC(seed, invoice id)", i, got.ID, got.Status, want)
		}
		blockedAny, postedAny = blockedAny || want == "AUDIT_BLOCKED", postedAny || want == "POSTED"
	}
	if !blockedAny || !postedAny {
		t.Fatal("the seed never produced both outcomes in 12 draws; pick a seed that does")
	}
}

func TestAuditBlock_DefaultRateIsZeroAndRatesAreKeptPerLegalEntity(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	r := rg.posted(t, "PS-1", 10, 4)
	if got := rg.manualInvoice(t, "INV-1", r.ID, 1, "alice").invoice(t, http.StatusCreated); got.Status != "POSTED" {
		t.Fatalf("with no policy the invoice is %s", got.Status)
	}
	other := rg.call(http.MethodPut, auditPolicyPath, fmt.Sprintf(`{"legalEntityId":%q,"sampleRatePct":100}`, ledgerID), userDuty("MatchingPolicyMaintain", rg.key(), "admin", entityHeader, legalEntityID+","+ledgerID))
	if other.Code != http.StatusOK {
		t.Fatalf("policy of another entity: %d %s", other.Code, other.Body.String())
	}
	if got := rg.manualInvoice(t, "INV-2", r.ID, 1, "alice").invoice(t, http.StatusCreated); got.Status != "POSTED" {
		t.Fatalf("another entity's rate leaked: %s", got.Status)
	}
	rg.sampleRate(t, "100")
	rg.sampleRate(t, "0")
	if got := rg.manualInvoice(t, "INV-3", r.ID, 1, "alice").invoice(t, http.StatusCreated); got.Status != "POSTED" {
		t.Fatalf("after resetting the rate the invoice is %s", got.Status)
	}
}

func TestPolicyEndpoints_ValidateAndAuthorise(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	entity := fmt.Sprintf(`"legalEntityId":%q`, legalEntityID)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		hdr    headers
		status int
		code   string
	}{
		{"rate above 100", http.MethodPut, auditPolicyPath, `{` + entity + `,"sampleRatePct":100.01}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"negative rate", http.MethodPut, auditPolicyPath, `{` + entity + `,"sampleRatePct":-1}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"three decimals", http.MethodPut, auditPolicyPath, `{` + entity + `,"sampleRatePct":"12.345"}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"rate missing", http.MethodPut, auditPolicyPath, `{` + entity + `}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"entity not a uuid", http.MethodPut, auditPolicyPath, `{"legalEntityId":"x","sampleRatePct":5}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"no user", http.MethodPut, auditPolicyPath, `{` + entity + `,"sampleRatePct":5}`, userDuty("MatchingPolicyMaintain", rg.key(), ""), http.StatusUnauthorized, "AUTH-0001"},
		{"wrong duty", http.MethodPut, auditPolicyPath, `{` + entity + `,"sampleRatePct":5}`, userDuty("VendorInvoicePost", rg.key(), "u"), http.StatusForbidden, "AUTH-0003"},
		{"entity outside scope", http.MethodPut, auditPolicyPath, `{"legalEntityId":"` + ledgerID + `","sampleRatePct":5}`, rg.policyMaker(), http.StatusForbidden, "AUTH-0003"},
		{"enabled missing", http.MethodPut, ersVendorPath, `{` + entity + `}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"vendor not a uuid", http.MethodPut, "/v1/receiving/ers-vendors/x", `{` + entity + `,"enabled":true}`, rg.policyMaker(), http.StatusBadRequest, "COM-0001"},
		{"ers wrong duty", http.MethodPut, ersVendorPath, `{` + entity + `,"enabled":true}`, userDuty("VendorInvoicePost", rg.key(), "u"), http.StatusForbidden, "AUTH-0003"},
		{"ers no user", http.MethodPut, ersVendorPath, `{` + entity + `,"enabled":true}`, userDuty("MatchingPolicyMaintain", rg.key(), ""), http.StatusUnauthorized, "AUTH-0001"},
		{"ers entity outside scope", http.MethodPut, ersVendorPath, `{"legalEntityId":"` + ledgerID + `","enabled":true}`, rg.policyMaker(), http.StatusForbidden, "AUTH-0003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problemCode(t, rg.call(tc.method, tc.path, tc.body, tc.hdr), tc.status, tc.code)
		})
	}
	rec := rg.call(http.MethodPut, auditPolicyPath, `{`+entity+`,"sampleRatePct":"7.5"}`, rg.policyMaker())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sampleRatePct":"7.50"`) {
		t.Fatalf("valid policy: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAsyncAPIDocumentsTheAuditAndSelfBillingEvents(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.enableErs(t, true)
	rg.sampleRate(t, "100")
	rg.posted(t, "PS-1", 10, 4)
	run := rg.ersRun(t, http.StatusOK)
	if code, body := rg.release(run.Settled[0].InvoiceID, "auditor"); code != http.StatusOK {
		t.Fatalf("release %d %s", code, body)
	}
	rg.posted(t, "PS-2", 10, 1)
	other := rg.ersRun(t, http.StatusOK)
	if code, body := rg.reject(other.Settled[0].InvoiceID, "auditor", "duplicate"); code != http.StatusOK {
		t.Fatalf("reject %d %s", code, body)
	}
	seen := map[string]bool{}
	for _, evt := range rg.st.Events() {
		seen[evt.Type] = true
		if !strings.Contains(string(spec), "name: "+evt.Type) {
			t.Errorf("asyncapi.yaml does not document %s", evt.Type)
		}
		var data map[string]any
		if err := json.Unmarshal(evt.Data, &data); err != nil {
			t.Fatal(err)
		}
		for key := range data {
			if !strings.Contains(string(spec), "        "+key+":") {
				t.Errorf("asyncapi.yaml does not document %s.%s", evt.Type, key)
			}
		}
	}
	for _, want := range []string{"dev.passage.payables.invoice.sampled.v1", "dev.passage.payables.invoice.posted.v1", "dev.passage.payables.invoice.rejected.v1", "dev.passage.receiving.auditpolicy.changed.v1", "dev.passage.receiving.ersvendor.changed.v1"} {
		if !seen[want] {
			t.Errorf("scenario did not emit %s", want)
		}
	}
}

func TestMetrics_CountSelfBillingAndAuditBlocks(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.enableErs(t, true)
	rg.sampleRate(t, "100")
	rg.posted(t, "PS-1", 10, 4)
	run := rg.ersRun(t, http.StatusOK)
	rg.release(run.Settled[0].InvoiceID, "auditor")

	text := metricsText(rg)

	for _, want := range []string{"receiving_ers_invoices_posted_total 1", "receiving_invoices_audit_blocked_total 1", "receiving_invoices_audit_released_total 1", "receiving_invoices_posted_total 1"} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics missing %q in %s", want, text)
		}
	}
}

func metricsText(rg *rig) string {
	rec := httptest.NewRecorder()
	rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestInvoicePostingAndErsRunRecordTheirActor(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.enableErs(t, true)
	r := rg.posted(t, "PS-1", 10, 4)
	noUser := func(name string) headers { return userDuty(name, rg.key(), "") }

	problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 1, ""), noUser("VendorInvoicePost")), http.StatusUnauthorized, "AUTH-0001")
	problemCode(t, rg.call(http.MethodPost, ersRunPath, runBody("", `"vendorId":"`+vendorID+`"`), noUser("VendorInvoicePost")), http.StatusUnauthorized, "AUTH-0001")

	if len(rg.ledger.Entries) != 1 || grirStatus(t, rg) != "OPEN" {
		t.Fatalf("a request without an actor posted: ledger entries %d GR/IR %s", len(rg.ledger.Entries), grirStatus(t, rg))
	}
}

func TestAuditBlock_ClosedPeriodIsRefusedWhetherOrNotTheInvoiceIsDrawn(t *testing.T) {
	for _, rate := range []string{"0", "100"} {
		t.Run("rate "+rate, func(t *testing.T) {
			periods := &switchPeriods{status: domain.PeriodOpen}
			rg := ersRig("2.60", periods, testSampler("seed-one-0123456789"))
			rg.sampleRate(t, rate)
			r := rg.posted(t, "PS-1", 10, 4)
			periods.set(domain.PeriodClosed)

			problemCode(t, rg.call(http.MethodPost, invoicesPath, invoiceBody("INV-1", r.ID, 4, ""), userDuty("VendorInvoicePost", rg.key(), "alice")), http.StatusUnprocessableEntity, "PER-AP-001")

			if len(rg.ledger.Entries) != 1 || grirStatus(t, rg) != "OPEN" {
				t.Fatalf("ledger entries %d GR/IR %s: a refused invoice must reserve nothing", len(rg.ledger.Entries), grirStatus(t, rg))
			}
		})
	}
}

func (rg *rig) reject(invoiceID, user, reason string) (int, string) {
	body := fmt.Sprintf(`{"legalEntityId":%q,"reason":%q}`, legalEntityID, reason)
	rec := rg.call(http.MethodPost, invoicesBase+invoiceID+":reject-audit-block", body, userDuty("InvoiceAuditRelease", rg.key(), user))
	return rec.Code, rec.Body.String()
}

func TestAuditReject_FreesTheQuantityPostsNothingAndIsAudited(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.sampleRate(t, "100")
	r := rg.posted(t, "PS-1", 10, 4)
	held := rg.manualInvoice(t, "INV-1", r.ID, 4, "alice").invoice(t, http.StatusCreated)
	entries := len(rg.ledger.Entries)

	code, body := rg.reject(held.ID, "auditor", "  duplicate of INV-0  ")

	if code != http.StatusOK {
		t.Fatalf("reject %d %s", code, body)
	}
	var out struct {
		Status       string `json:"status"`
		RejectedBy   string `json:"rejectedBy"`
		RejectedAt   string `json:"rejectedAt"`
		RejectReason string `json:"rejectReason"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != "AUDIT_REJECTED" || out.RejectedBy != "auditor" || out.RejectedAt == "" || out.RejectReason != "duplicate of INV-0" || len(rg.ledger.Entries) != entries {
		t.Fatalf("rejected %+v ledger entries %d (was %d)", out, len(rg.ledger.Entries), entries)
	}
	if grirStatus(t, rg) != "OPEN" {
		t.Fatalf("GR/IR %s, want the reservation freed", grirStatus(t, rg))
	}
	evts := rg.st.Events()
	if evt := evts[len(evts)-1]; evt.Type != "dev.passage.payables.invoice.rejected.v1" || !strings.Contains(string(evt.Data), `"rejected_by":"auditor"`) || !strings.Contains(string(evt.Data), `"reason":"duplicate of INV-0"`) {
		t.Fatalf("event %s %s", evt.Type, evt.Data)
	}
	if code, body := rg.release(held.ID, "second"); code != http.StatusConflict || !strings.Contains(body, "AP-AUD-002") {
		t.Fatalf("release after reject %d %s", code, body)
	}
	if code, body := rg.reject(held.ID, "second", "again"); code != http.StatusConflict || !strings.Contains(body, "AP-AUD-002") {
		t.Fatalf("second reject %d %s", code, body)
	}
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(held.ID, "DEBIT", "DN-1", adjLine(1, "1.00")), rg.apPosterFor(legalEntityID)), http.StatusConflict, "AP-AUD-001")
	rg.sampleRate(t, "0")
	if again := rg.manualInvoice(t, "INV-2", r.ID, 4, "alice").invoice(t, http.StatusCreated); again.Status != "POSTED" {
		t.Fatalf("invoicing the freed quantity: %+v", again)
	}
}

func TestAuditReject_ValidationDutyAndSegregation(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.sampleRate(t, "100")
	r := rg.posted(t, "PS-1", 10, 4)
	held := rg.manualInvoice(t, "INV-1", r.ID, 4, "alice").invoice(t, http.StatusCreated)
	path := invoicesBase + held.ID + ":reject-audit-block"
	good := fmt.Sprintf(`{"legalEntityId":%q,"reason":"no PO match"}`, legalEntityID)
	for _, tc := range []struct {
		name   string
		body   string
		hdr    headers
		status int
		code   string
	}{
		{"a reason is required", fmt.Sprintf(`{"legalEntityId":%q}`, legalEntityID), userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusBadRequest, "COM-0001"},
		{"a blank reason is no reason", fmt.Sprintf(`{"legalEntityId":%q,"reason":"   "}`, legalEntityID), userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusBadRequest, "COM-0001"},
		{"the reason is bounded", fmt.Sprintf(`{"legalEntityId":%q,"reason":%q}`, legalEntityID, strings.Repeat("x", 501)), userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusBadRequest, "COM-0001"},
		{"the poster's duty is not enough", good, userDuty("VendorInvoicePost", rg.key(), "bob"), http.StatusForbidden, "AUTH-0003"},
		{"the poster cannot reject", good, userDuty("InvoiceAuditRelease", rg.key(), "alice"), http.StatusForbidden, "AP-AUD-003"},
		{"the poster cannot reject in other letters", good, userDuty("InvoiceAuditRelease", rg.key(), " ALICE "), http.StatusForbidden, "AP-AUD-003"},
		{"a reject needs a user", good, userDuty("InvoiceAuditRelease", rg.key(), ""), http.StatusUnauthorized, "AUTH-0001"},
		{"scope", strings.Replace(good, legalEntityID, ledgerID, 1), userDuty("InvoiceAuditRelease", rg.key(), "bob"), http.StatusForbidden, "AUTH-0003"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problemCode(t, rg.call(http.MethodPost, path, tc.body, tc.hdr), tc.status, tc.code)
			if grirStatus(t, rg) != "CLEARED" {
				t.Fatal("a refused reject freed the quantity")
			}
		})
	}
	problemCode(t, rg.call(http.MethodPost, invoicesBase+poID+":reject-audit-block", good, userDuty("InvoiceAuditRelease", rg.key(), "bob")), http.StatusNotFound, "RCV-NF-005")
}

func TestPolicyChangesEmitEventsWithOldAndNewValues(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.sampleRate(t, "5")
	rg.sampleRate(t, "7.5")
	rg.enableErs(t, true)
	rg.enableErs(t, false)
	var got []string
	for _, evt := range rg.st.Events() {
		got = append(got, evt.Type+" "+string(evt.Data))
	}
	for _, want := range []string{
		`dev.passage.receiving.auditpolicy.changed.v1 {"legal_entity_id":"` + legalEntityID + `","old_rate_pct":"0","new_rate_pct":"5","changed_by":"policy-admin"}`,
		`dev.passage.receiving.auditpolicy.changed.v1 {"legal_entity_id":"` + legalEntityID + `","old_rate_pct":"5","new_rate_pct":"7.5","changed_by":"policy-admin"}`,
		`dev.passage.receiving.ersvendor.changed.v1 {"legal_entity_id":"` + legalEntityID + `","vendor_id":"` + vendorID + `","old_enabled":false,"new_enabled":true,"changed_by":"policy-admin"}`,
		`dev.passage.receiving.ersvendor.changed.v1 {"legal_entity_id":"` + legalEntityID + `","vendor_id":"` + vendorID + `","old_enabled":true,"new_enabled":false,"changed_by":"policy-admin"}`,
	} {
		if !slices.Contains(got, want) {
			t.Errorf("missing event %s in %v", want, got)
		}
	}
}

func TestErs_ReceiptWithSomeLinesInvoicedIsSkippedWholeAndKeepsItsNumber(t *testing.T) {
	second := "99999999-9999-4999-8999-999999999990"
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", func(po *domain.PurchaseOrder) {
			po.Lines = append(po.Lines, domain.PurchaseOrderLine{LineNo: 2, ProductID: second, Unit: "EA", UnitPrice: dec("2"), PriceUnit: dec("1"), Status: "OPEN"})
		})}}
	})
	rg.enableErs(t, true)
	body := strings.Replace(directBody("PS-2L", 10, 4, ""), `"lines":[`, `"lines":[{"poLineId":"`+poLineID[:len(poLineID)-1]+`6","productId":"`+second+`","orderedRemaining":10,"overDeliveryPct":5,"unitPrice":1.5,"unit":"EA","qty":10,"locationId":"`+locationID+`"},`, 1)
	r := decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath, body, rg.maintain()), http.StatusCreated)
	decode[receiptJSON](t, rg.call(http.MethodPost, receiptsPath+"/"+r.ID+":post", "", rg.poster(r.Version)), http.StatusOK)
	lineOne := strings.Replace(invoiceBody("INV-L1", r.ID, 10, ""), `"unitPrice":2.5`, `"unitPrice":1.5`, 1)
	rg.call(http.MethodPost, invoicesPath, lineOne, rg.apPoster())
	entries := len(rg.ledger.Entries)

	run := rg.ersRun(t, http.StatusOK)

	if len(run.Settled) != 0 || strings.Join(run.reasons(), ",") != "PARTIALLY_INVOICED" || len(rg.ledger.Entries) != entries {
		t.Fatalf("run %+v ledger entries %d (was %d): the receipt must be skipped whole", run, len(rg.ledger.Entries), entries)
	}
	if again := rg.ersRun(t, http.StatusOK); len(again.Settled) != 0 {
		t.Fatalf("second run %+v", again)
	}
}

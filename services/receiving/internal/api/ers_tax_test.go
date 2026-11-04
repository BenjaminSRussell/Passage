package api_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

func taxRig(tax domain.TaxCalculator, recorder domain.TaxRecorder, parties domain.SelfBillPartySource, sampler bool) *rig {
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) {
		p.PurchaseOrders = domain.FakePurchaseOrders{Orders: map[string]domain.PurchaseOrder{poID: ersOrder("2.60", nil)}}
		if sampler {
			p.AuditSampler = testSampler("seed-one-0123456789")
		}
		p.Tax, p.TaxRecorder, p.SelfBillParties = tax, recorder, parties
	})
	rg.confirmErsPrice = "2.60"
	return rg
}

func vat19() domain.FakeTaxCalculator {
	return domain.FakeTaxCalculator{Result: domain.TaxResult{Code: "VAT19", RatePct: decimal.RequireFromString("19"), Amount: decimal.RequireFromString("1.98")}}
}

func (rg *rig) deliverTax(evt events.Event) error {
	return rg.api.ErsTaxConsumerHandlers()[api.ErsSelfBillIssuedType].Handle(context.Background(), nil, evt)
}

func TestErs_DebitsInputTaxIssuesSelfBillAndRecordsTheTransactionFromTheOutbox(t *testing.T) {
	recorder := &domain.FakeTaxRecorder{}
	rg := taxRig(vat19(), recorder, testParties, true)
	rg.enableErs(t, true)
	r := rg.posted(t, "PS-TAX", 10, 4)
	run := rg.ersRun(t, http.StatusOK)
	if len(run.Settled) != 1 || run.Settled[0].Total != "12.38" || run.Settled[0].ReceiptID != r.ID {
		t.Fatalf("settled %+v", run)
	}
	wantLines(t, lastEntry(t, rg), "2100=10", "1400=0.4", "1570=1.98", "2000=-12.38")
	if len(recorder.Txns) != 0 {
		t.Fatalf("tax was recorded inline, not from the outbox event: %+v", recorder.Txns)
	}
	doc := rg.call(http.MethodGet, "/v1/receiving/ers-self-bills/"+run.Settled[0].InvoiceID, "", duty("APInquire", "", "X-Passage-User", "alice"))
	for _, want := range []string{"VAT19", "rate 19%", "12.38", "SB-00000001", "Acme Supplies GmbH", "Hauptstr 1", "Passage Werke AG", "Date of supply 2026-10-25"} {
		if doc.Code != http.StatusOK || !bytes.Contains(doc.Body.Bytes(), []byte(want)) {
			t.Fatalf("self-bill %d lacks %q: %s", doc.Code, want, doc.Body.String())
		}
	}
	evt := lastOfType(t, rg, api.ErsSelfBillIssuedType)
	for delivery := range 2 {
		if err := rg.deliverTax(evt); err != nil {
			t.Fatalf("delivery %d: %v", delivery, err)
		}
	}
	if len(recorder.Txns) != 2 || recorder.Txns[0].ID != run.Settled[0].InvoiceID || recorder.Txns[0].Code != "VAT19" || !recorder.Txns[0].Amount.Equal(decimal.RequireFromString("1.98")) || recorder.Txns[0].Date != "2026-10-15" {
		t.Fatalf("recorded %+v (the tax service dedupes by invoice id)", recorder.Txns)
	}
}

func TestErs_TaxRecordFailureIsRetriedFromTheEventNotLost(t *testing.T) {
	recorder := &domain.FakeTaxRecorder{Err: errors.New("tax 503")}
	rg := taxRig(vat19(), recorder, testParties, true)
	rg.enableErs(t, true)
	rg.posted(t, "PS-TAX", 10, 4)
	if run := rg.ersRun(t, http.StatusOK); len(run.Settled) != 1 {
		t.Fatalf("a failing tax recorder must not fail the settlement: %+v", run)
	}
	evt := lastOfType(t, rg, api.ErsSelfBillIssuedType)
	if err := rg.deliverTax(evt); err == nil || strings.Contains(err.Error(), "permanent") {
		t.Fatalf("a tax outage must be a retryable error, got %v", err)
	}
	recorder.Err = nil
	if err := rg.deliverTax(evt); err != nil || len(recorder.Txns) != 1 {
		t.Fatalf("redelivery: %v recorded %+v", err, recorder.Txns)
	}
}

func TestErs_ZeroTaxEventRecordsNothingAndAMissingRecorderIsPermanent(t *testing.T) {
	recorder := &domain.FakeTaxRecorder{}
	rg := taxRig(testTax, recorder, testParties, true)
	rg.enableErs(t, true)
	rg.posted(t, "PS-Z", 10, 4)
	rg.ersRun(t, http.StatusOK)
	if err := rg.deliverTax(lastOfType(t, rg, api.ErsSelfBillIssuedType)); err != nil || len(recorder.Txns) != 0 {
		t.Fatalf("zero tax: %v %+v", err, recorder.Txns)
	}
	taxed := taxRig(vat19(), nil, testParties, true)
	taxed.enableErs(t, true)
	taxed.posted(t, "PS-N", 10, 4)
	taxed.ersRun(t, http.StatusOK)
	if err := taxed.deliverTax(lastOfType(t, taxed, api.ErsSelfBillIssuedType)); err == nil {
		t.Fatal("input tax with no recorder was silently dropped")
	}
}

func TestErs_TaxCalculationFailureAnswers503AndPostsNothing(t *testing.T) {
	rg := taxRig(domain.FakeTaxCalculator{Err: errors.New("tax down")}, &domain.FakeTaxRecorder{}, testParties, true)
	rg.enableErs(t, true)
	rg.posted(t, "PS-503", 10, 4)
	before := len(rg.ledger.Entries)
	rec := rg.call(http.MethodPost, ersRunPath, runBody("", `"vendorId":"`+vendorID+`"`), rg.settle())
	problemCode(t, rec, http.StatusServiceUnavailable, "COM-0503")
	if len(rg.ledger.Entries) != before {
		t.Fatalf("ledger posted despite the tax failure: %+v", rg.ledger.Entries[before:])
	}
}

func TestErs_PartyMasterFailureAnswers503AndPostsNothing(t *testing.T) {
	rg := taxRig(vat19(), &domain.FakeTaxRecorder{}, domain.FakeSelfBillParties{Err: errors.New("party down")}, true)
	rg.enableErs(t, true)
	rg.posted(t, "PS-P", 10, 4)
	before := len(rg.ledger.Entries)
	problemCode(t, rg.call(http.MethodPost, ersRunPath, runBody("", `"vendorId":"`+vendorID+`"`), rg.settle()), http.StatusServiceUnavailable, "COM-0503")
	if len(rg.ledger.Entries) != before {
		t.Fatal("ledger posted without the supplier and customer master data")
	}
}

func TestErs_SkipsAsTaxNotDeterminedInsteadOfIssuingAZeroTaxDocument(t *testing.T) {
	foreign := testParties
	foreign.Parties.Supplier.Country = "FR"
	cases := map[string]*rig{
		"no tax service":          taxRig(nil, nil, testParties, true),
		"no rule for the country": taxRig(domain.FakeTaxCalculator{Err: domain.ErrTaxNotDetermined}, nil, testParties, true),
		"cross-border supplier":   taxRig(vat19(), &domain.FakeTaxRecorder{}, foreign, true),
	}
	for name, rg := range cases {
		rg.enableErs(t, true)
		rg.posted(t, "PS-TND", 10, 4)
		before := len(rg.ledger.Entries)
		run := rg.ersRun(t, http.StatusOK)
		if len(run.Settled) != 0 || len(run.Skipped) != 1 || run.Skipped[0].Reason != "TAX_NOT_DETERMINED" || run.Skipped[0].SourceRef == "" {
			t.Fatalf("%s: run %+v", name, run)
		}
		if len(rg.ledger.Entries) != before || grirStatus(t, rg) != "OPEN" {
			t.Fatalf("%s: the receipt was consumed", name)
		}
	}
}

func TestErs_AuditReleaseOfATaxedInvoiceWritesTheSelfBillInTheReleaseAndNumbersItGaplessly(t *testing.T) {
	recorder := &domain.FakeTaxRecorder{}
	rg := taxRig(vat19(), recorder, testParties, true)
	rg.enableErs(t, true)
	rg.sampleRate(t, "100")
	rg.posted(t, "PS-REL", 10, 4)
	run := rg.ersRun(t, http.StatusOK)
	if len(run.Settled) != 1 || run.Settled[0].Status != "AUDIT_BLOCKED" {
		t.Fatalf("run %+v", run)
	}
	id := run.Settled[0].InvoiceID
	if doc := rg.call(http.MethodGet, "/v1/receiving/ers-self-bills/"+id, "", duty("APInquire", "", "X-Passage-User", "alice")); doc.Code != http.StatusNotFound {
		t.Fatalf("a blocked invoice already has a self-bill: %d", doc.Code)
	}
	if code, body := rg.release(id, "auditor"); code != http.StatusOK {
		t.Fatalf("release %d %s", code, body)
	}
	wantLines(t, lastEntry(t, rg), "2100=10", "1400=0.4", "1570=1.98", "2000=-12.38")
	doc := rg.call(http.MethodGet, "/v1/receiving/ers-self-bills/"+id, "", duty("APInquire", "", "X-Passage-User", "alice"))
	for _, want := range []string{"SB-00000001", "VAT19", "rate 19%", "Acme Supplies GmbH", "Date of supply 2026-10-25"} {
		if doc.Code != http.StatusOK || !bytes.Contains(doc.Body.Bytes(), []byte(want)) {
			t.Fatalf("released self-bill %d lacks %q: %s", doc.Code, want, doc.Body.String())
		}
	}
	if err := rg.deliverTax(lastOfType(t, rg, api.ErsSelfBillIssuedType)); err != nil || len(recorder.Txns) != 1 {
		t.Fatalf("release did not drive the tax record: %v %+v", err, recorder.Txns)
	}
}

func TestErs_ReleaseWithoutPartyDataKeepsTheInvoiceBlocked(t *testing.T) {
	parties := &flakyParties{src: testParties}
	rg := taxRig(vat19(), &domain.FakeTaxRecorder{}, parties, true)
	rg.enableErs(t, true)
	rg.sampleRate(t, "100")
	rg.posted(t, "PS-RB", 10, 4)
	id := rg.ersRun(t, http.StatusOK).Settled[0].InvoiceID
	parties.fail = true
	entries := len(rg.ledger.Entries)
	if code, _ := rg.release(id, "auditor"); code != http.StatusServiceUnavailable {
		t.Fatalf("release answered %d", code)
	}
	parties.fail = false
	if code, body := rg.release(id, "auditor"); code != http.StatusOK || len(rg.ledger.Entries) != entries+1 {
		t.Fatalf("retry %d %s entries %d", code, body, len(rg.ledger.Entries))
	}
}

type flakyParties struct {
	src  domain.FakeSelfBillParties
	fail bool
}

func (f *flakyParties) SelfBillParties(ctx context.Context, le, vendor string) (domain.SelfBillParties, error) {
	if f.fail {
		return domain.SelfBillParties{}, errors.New("party down")
	}
	return f.src.SelfBillParties(ctx, le, vendor)
}

package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func (rg *rig) receiptPostedEvent(t *testing.T) events.Event {
	t.Helper()
	all := rg.st.Events()
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Type == api.ReceiptPostedType {
			return all[i]
		}
	}
	t.Fatal("no receipt.posted event was emitted")
	return events.Event{}
}

func (rg *rig) consume(evt events.Event) error {
	return rg.api.ErsConsumerHandlers()[api.ReceiptPostedType].Handle(context.Background(), nil, evt)
}

func TestErsConsumer_SettlesAPostedReceiptOnceAndRecordsTheServiceActor(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("0123456789abcdef"))
	rg.enableErs(t, true)
	rg.sampleRate(t, "100")
	rg.posted(t, "PS-1", 10, 4)
	evt := rg.receiptPostedEvent(t)

	for delivery := range 3 {
		if err := rg.consume(evt); err != nil {
			t.Fatalf("delivery %d: %v", delivery, err)
		}
	}

	invoices := 0
	for _, e := range rg.st.Events() {
		if e.Type == "dev.passage.payables.invoice.sampled.v1" {
			invoices++
		}
	}
	if invoices != 1 {
		t.Fatalf("%d sampled invoices after three deliveries, want exactly one", invoices)
	}
	if grirStatus(t, rg) == "OPEN" {
		t.Fatal("the GR/IR item was not claimed by the settlement")
	}
	id := invoiceIDFromEvents(t, rg)
	code, body := rg.release(id, "service:receiving-ers")
	if code != http.StatusForbidden || !strings.Contains(body, domain.CodeAuditSelfRelease) {
		t.Fatalf("the service actor released its own invoice: %d %s", code, body)
	}
	if code, body := rg.release(id, "auditor"); code != http.StatusOK {
		t.Fatalf("a different person must be able to release: %d %s", code, body)
	}
}

func invoiceIDFromEvents(t *testing.T, rg *rig) string {
	t.Helper()
	for _, e := range rg.st.Events() {
		if e.Type == "dev.passage.payables.invoice.sampled.v1" {
			return e.Subject
		}
	}
	t.Fatal("no invoice.sampled event")
	return ""
}

func TestErsConsumer_PostsTheSelfBilledInvoiceWhenNotSampled(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.enableErs(t, true)
	rg.posted(t, "PS-1", 10, 4)
	evt := rg.receiptPostedEvent(t)

	if err := rg.consume(evt); err != nil {
		t.Fatal(err)
	}
	if err := rg.consume(evt); err != nil {
		t.Fatal(err)
	}
	if len(rg.ledger.Entries) != 2 || grirStatus(t, rg) != "CLEARED" {
		t.Fatalf("ledger entries %d GR/IR %s; want the receipt and one invoice, cleared", len(rg.ledger.Entries), grirStatus(t, rg))
	}
	if again := rg.ersRun(t, http.StatusOK); len(again.Settled) != 0 {
		t.Fatalf("a manual run settled %d more", len(again.Settled))
	}
}

func TestErsConsumer_LeavesNonErsVendorsAlone(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	rg.posted(t, "PS-1", 10, 4)
	if err := rg.consume(rg.receiptPostedEvent(t)); err != nil {
		t.Fatal(err)
	}
	if len(rg.ledger.Entries) != 1 || grirStatus(t, rg) != "OPEN" {
		t.Fatalf("a non-ERS vendor was settled: ledger entries %d GR/IR %s", len(rg.ledger.Entries), grirStatus(t, rg))
	}
}

func TestErsConsumer_ClosedPeriodIsRetryableNotPoison(t *testing.T) {
	periods := &switchPeriods{status: domain.PeriodOpen}
	rg := ersRig("2.60", periods, nil)
	rg.enableErs(t, true)
	rg.posted(t, "PS-1", 10, 4)
	evt := rg.receiptPostedEvent(t)

	periods.set(domain.PeriodClosed)
	err := rg.consume(evt)
	if err == nil || eventing.IsPermanent(err) {
		t.Fatalf("closed period err = %v, want a retryable error", err)
	}
	if len(rg.ledger.Entries) != 1 {
		t.Fatalf("a closed period posted %d entries", len(rg.ledger.Entries))
	}
	periods.set(domain.PeriodOpen)
	if err := rg.consume(evt); err != nil || len(rg.ledger.Entries) != 2 {
		t.Fatalf("after reopening: %v, ledger entries %d", err, len(rg.ledger.Entries))
	}
}

func TestErsConsumer_RefusesBadEventsPermanently(t *testing.T) {
	rg := ersRig("2.60", nil, nil)
	good := `{"receipt_id":"` + locationID + `","legal_entity_id":"` + legalEntityID + `","posting_date":"2026-10-15"}`
	cases := map[string]events.Event{
		"not json":        {TenantID: testkit.TenantID(1), Data: []byte("[")},
		"no receipt":      {TenantID: testkit.TenantID(1), Data: []byte(`{"legal_entity_id":"` + legalEntityID + `","posting_date":"2026-10-15"}`)},
		"bad date":        {TenantID: testkit.TenantID(1), Data: []byte(strings.Replace(good, "2026-10-15", "15/10/2026", 1))},
		"bad tenant":      {TenantID: "nope", Data: []byte(good)},
		"unknown receipt": {TenantID: testkit.TenantID(1), Data: []byte(good)},
	}
	for name, evt := range cases {
		if err := rg.consume(evt); !eventing.IsPermanent(err) {
			t.Errorf("%s: err = %v, want a permanent error", name, err)
		}
	}
	if len(rg.ledger.Entries) != 0 {
		t.Fatalf("bad events posted %d entries", len(rg.ledger.Entries))
	}
}

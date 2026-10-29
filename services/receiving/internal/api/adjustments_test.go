package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

const (
	adjustmentsPath = "/v1/receiving/invoice-adjustments:post"
	entityHeader    = "X-Passage-Legal-Entity"
)

func (rg *rig) apPosterFor(entity string) headers {
	return duty("VendorInvoicePost", rg.key(), entityHeader, entity)
}

type adjustmentJSON struct {
	ID                string `json:"id"`
	OriginalInvoiceID string `json:"originalInvoiceId"`
	VendorID          string `json:"vendorId"`
	Kind              string `json:"kind"`
	DocumentNumber    string `json:"documentNumber"`
	Currency          string `json:"currency"`
	Total             string `json:"total"`
	LedgerEntryID     string `json:"ledgerEntryId"`
	Lines             []struct {
		LineNo        int    `json:"lineNo"`
		InvoiceLineNo int    `json:"invoiceLineNo"`
		Amount        string `json:"amount"`
	} `json:"lines"`
}

func adjustmentBody(invoiceID, kind, number string, lines ...string) string {
	return fmt.Sprintf(`{"originalInvoiceId":%q,"legalEntityId":%q,"kind":%q,"documentNumber":%q,"documentDate":"2026-10-27","postingDate":"2026-10-27","ledgerId":%q,"fiscalPeriodId":%q,"lines":[%s]}`,
		invoiceID, legalEntityID, kind, number, ledgerID, periodID, strings.Join(lines, ","))
}

func adjLine(invoiceLineNo int, amount string) string {
	return fmt.Sprintf(`{"invoiceLineNo":%d,"amount":%s}`, invoiceLineNo, amount)
}

func (rg *rig) invoiced(t *testing.T, slip, number string, qty int, unitPrice, extra string) (receiptJSON, invoiceJSON) {
	t.Helper()
	r := rg.posted(t, slip, 100, 100)
	body := strings.Replace(invoiceBody(number, r.ID, qty, extra), `"unitPrice":2.5`, `"unitPrice":`+unitPrice, 1)
	return r, decode[invoiceJSON](t, rg.call(http.MethodPost, invoicesPath, body, rg.apPosterFor(legalEntityID)), http.StatusCreated)
}

func lastEntry(t *testing.T, rg *rig) domain.JournalEntry {
	t.Helper()
	if len(rg.ledger.Entries) == 0 {
		t.Fatal("ledger holds no entries")
	}
	return rg.ledger.Entries[len(rg.ledger.Entries)-1]
}

func wantLines(t *testing.T, entry domain.JournalEntry, want ...string) {
	t.Helper()
	var got []string
	for _, l := range entry.Lines {
		got = append(got, l.Account+"="+l.Amount.Value.String())
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("journal lines %v, want %v", got, want)
	}
}

func TestAdjustment_DebitPostsPriceDifferenceAndLeavesGrirAlone(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	if inv.Total != "104.00" || rg.ledger.Balance("1400").String() != "254" {
		t.Fatalf("invoice total %s, inventory balance %s, want 104.00 and 250 received + 4 price difference", inv.Total, rg.ledger.Balance("1400"))
	}
	grirBefore, entriesBefore := rg.ledger.Balance("2100"), len(rg.ledger.Entries)

	rec := rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "DEBIT", "DN-1", adjLine(1, "3.50")), rg.apPosterFor(legalEntityID))

	got := decode[adjustmentJSON](t, rec, http.StatusCreated)
	if got.Kind != "DEBIT" || got.Total != "3.50" || got.VendorID != vendorID || got.Currency != "USD" || got.OriginalInvoiceID != inv.ID || got.LedgerEntryID == "" || len(got.Lines) != 1 || got.Lines[0].Amount != "3.50" {
		t.Fatalf("adjustment %+v", got)
	}
	entry := lastEntry(t, rg)
	wantLines(t, entry, "1400=3.5", "2000=-3.5")
	if entry.IdempotencyKey != "post:receiving:invoiceadjustment:"+got.ID+":DEBIT" || len(rg.ledger.Entries) != entriesBefore+1 || !rg.ledger.Balance("2100").Equal(grirBefore) {
		t.Fatalf("key %s entries %d grir %s (was %s)", entry.IdempotencyKey, len(rg.ledger.Entries), rg.ledger.Balance("2100"), grirBefore)
	}
}

func TestAdjustment_CreditsStopAtInvoicedPlusDebitsMinusCredits(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	post := func(kind, number, amount string) *httptest.ResponseRecorder {
		return rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, kind, number, adjLine(1, amount)), rg.apPosterFor(legalEntityID))
	}
	decode[adjustmentJSON](t, post("DEBIT", "DN-1", "3.50"), http.StatusCreated)

	decode[adjustmentJSON](t, post("CREDIT", "CN-1", "10.00"), http.StatusCreated)
	wantLines(t, lastEntry(t, rg), "1400=-10", "2000=10")
	problemCode(t, post("CREDIT", "CN-2", "97.51"), http.StatusUnprocessableEntity, "AP-CRD-001")
	entries := len(rg.ledger.Entries)
	decode[adjustmentJSON](t, post("CREDIT", "CN-3", "97.50"), http.StatusCreated)
	problemCode(t, post("CREDIT", "CN-4", "0.01"), http.StatusUnprocessableEntity, "AP-CRD-001")
	decode[adjustmentJSON](t, post("DEBIT", "DN-2", "0.01"), http.StatusCreated)
	decode[adjustmentJSON](t, post("CREDIT", "CN-5", "0.01"), http.StatusCreated)

	if len(rg.ledger.Entries) != entries+3 {
		t.Fatalf("ledger entries %d, want %d: rejected credits must not post", len(rg.ledger.Entries), entries+3)
	}
}

func TestAdjustment_TwoLinesOfOneRequestCountAgainstTheSameInvoiceLine(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	entries := len(rg.ledger.Entries)

	rec := rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "CREDIT", "CN-1", adjLine(1, "60.00"), adjLine(1, "44.01")), rg.apPosterFor(legalEntityID))

	problemCode(t, rec, http.StatusUnprocessableEntity, "AP-CRD-001")
	if len(rg.ledger.Entries) != entries {
		t.Fatal("the over-credit reached the ledger")
	}
	decode[adjustmentJSON](t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "CREDIT", "CN-1", adjLine(1, "60.00"), adjLine(1, "44.00")), rg.apPosterFor(legalEntityID)), http.StatusCreated)
	wantLines(t, lastEntry(t, rg), "1400=-60", "1400=-44", "2000=104")
}

func TestAdjustment_StandardCostLinePostsToPriceVariance(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 10, "2.6", `,"standardCost":true`)

	decode[adjustmentJSON](t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "DEBIT", "DN-1", adjLine(1, "1.00")), rg.apPosterFor(legalEntityID)), http.StatusCreated)

	wantLines(t, lastEntry(t, rg), "5200=1", "2000=-1")
}

func TestAdjustment_IdempotentByKeyAndByDocumentNumber(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	hdr := duty("VendorInvoicePost", "same-key", entityHeader, legalEntityID)
	body := adjustmentBody(inv.ID, "DEBIT", "DN-1", adjLine(1, "3.50"))
	first := rg.call(http.MethodPost, adjustmentsPath, body, hdr)
	entries := len(rg.ledger.Entries)

	replay := rg.call(http.MethodPost, adjustmentsPath, body, hdr)

	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" || replay.Body.String() != first.Body.String() || len(rg.ledger.Entries) != entries {
		t.Fatalf("first %d replay %d %s entries %d", first.Code, replay.Code, replay.Header(), len(rg.ledger.Entries))
	}
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, body, duty("VendorInvoicePost", "other-key", entityHeader, legalEntityID)), http.StatusConflict, "AP-DUP-001")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, strings.Replace(body, `"DN-1"`, `" dn-1 "`, 1), rg.apPosterFor(legalEntityID)), http.StatusConflict, "AP-DUP-001")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, strings.Replace(body, `3.50`, `4.00`, 1), hdr), http.StatusUnprocessableEntity, "COM-0409")
	if len(rg.ledger.Entries) != entries {
		t.Fatalf("duplicates reached the ledger: %d entries, want %d", len(rg.ledger.Entries), entries)
	}
}

func TestAdjustment_Validation(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	ok := adjustmentBody(inv.ID, "DEBIT", "DN-1", adjLine(1, "3.50"))
	tests := []struct {
		name, body, field string
	}{
		{"bad invoice id", strings.Replace(ok, inv.ID, "nope", 1), `"originalInvoiceId"`},
		{"bad kind", strings.Replace(ok, `"DEBIT"`, `"REFUND"`, 1), `"kind"`},
		{"blank number", strings.Replace(ok, `"DN-1"`, `" "`, 1), `"documentNumber"`},
		{"long number", strings.Replace(ok, `"DN-1"`, `"`+strings.Repeat("n", 65)+`"`, 1), `"documentNumber"`},
		{"bad document date", strings.Replace(ok, `"documentDate":"2026-10-27"`, `"documentDate":"27/10/2026"`, 1), `"documentDate"`},
		{"bad posting date", strings.Replace(ok, `"postingDate":"2026-10-27"`, `"postingDate":"x"`, 1), `"postingDate"`},
		{"bad period", strings.Replace(ok, periodID, "nope", 1), `"fiscalPeriodId"`},
		{"no lines", strings.Replace(ok, adjLine(1, "3.50"), "", 1), `"lines"`},
		{"zero amount", strings.Replace(ok, `"amount":3.50`, `"amount":0`, 1), `"lines[0].amount"`},
		{"negative amount", strings.Replace(ok, `"amount":3.50`, `"amount":-1`, 1), `"lines[0].amount"`},
		{"missing amount", strings.Replace(ok, `,"amount":3.50`, ``, 1), `"lines[0].amount"`},
		{"line number zero", strings.Replace(ok, `"invoiceLineNo":1`, `"invoiceLineNo":0`, 1), `"lines[0].invoiceLineNo"`},
		{"unknown field", strings.Replace(ok, `"kind"`, `"extra":1,"kind"`, 1), "COM-0001"},
	}
	entries := len(rg.ledger.Entries)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := rg.call(http.MethodPost, adjustmentsPath, tc.body, rg.apPosterFor(legalEntityID))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Fatalf("got %d %s, want 400 with %s", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
	if len(rg.ledger.Entries) != entries {
		t.Fatal("an invalid request reached the ledger")
	}
}

func TestAdjustment_DomainRejections(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	other := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	entries := len(rg.ledger.Entries)

	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(other, "DEBIT", "DN-1", adjLine(1, "1.00")), rg.apPosterFor(legalEntityID)), http.StatusNotFound, "RCV-NF-005")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "DEBIT", "DN-2", adjLine(9, "1.00")), rg.apPosterFor(legalEntityID)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "DEBIT", "DN-3", adjLine(1, "1.005")), rg.apPosterFor(legalEntityID)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	foreignLedger := strings.Replace(adjustmentBody(inv.ID, "DEBIT", "DN-4", adjLine(1, "1.00")), ledgerID, other, 1)
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, foreignLedger, rg.apPosterFor(legalEntityID)), http.StatusUnprocessableEntity, "RCV-VAL-001")
	otherEntity := strings.Replace(adjustmentBody(inv.ID, "DEBIT", "DN-5", adjLine(1, "1.00")), legalEntityID, ledgerID, 1)
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, otherEntity, rg.apPosterFor(ledgerID)), http.StatusUnprocessableEntity, "RCV-VAL-001")

	if len(rg.ledger.Entries) != entries {
		t.Fatalf("rejected adjustments reached the ledger: %d entries, want %d", len(rg.ledger.Entries), entries)
	}
}

func TestAdjustment_AuthAndScope(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	body := adjustmentBody(inv.ID, "DEBIT", "DN-1", adjLine(1, "1.00"))

	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, body, duty("ReceivingPost", "k", entityHeader, legalEntityID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, body, duty("VendorInvoicePost", "k", entityHeader, ledgerID)), http.StatusForbidden, "AUTH-0003")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, body, headers{"Idempotency-Key": "k"}), http.StatusUnauthorized, "AUTH-0001")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, body, headers{"X-Passage-Tenant": tenantTwo(), "X-Passage-Duties": "VendorInvoicePost", "Idempotency-Key": "k", entityHeader: legalEntityID}), http.StatusNotFound, "RCV-NF-005")
	problemCode(t, rg.call(http.MethodPost, adjustmentsPath, body, duty("VendorInvoicePost", "", entityHeader, legalEntityID)), http.StatusBadRequest, "COM-0001")
}

func tenantTwo() string { return "00000000-0000-0000-0000-000000000002" }

func TestAdjustment_ClosedPeriodPostsNothingAndTheSameDocumentPostsOnceOpen(t *testing.T) {
	st := memstore.New()
	open := newRigOn(st, domain.PeriodOpen)
	_, inv := open.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	closed := newRigOn(st, domain.PeriodClosed)
	closed.keys = 1000
	body := adjustmentBody(inv.ID, "CREDIT", "CN-1", adjLine(1, "5.00"))

	problemCode(t, closed.call(http.MethodPost, adjustmentsPath, body, closed.apPosterFor(legalEntityID)), http.StatusUnprocessableEntity, "PER-AP-001")
	if len(closed.ledger.Entries) != 0 {
		t.Fatal("a closed period reached the ledger")
	}

	decode[adjustmentJSON](t, open.call(http.MethodPost, adjustmentsPath, body, open.apPosterFor(legalEntityID)), http.StatusCreated)
}

func TestAdjustment_EmitsDocumentedEventAndMetric(t *testing.T) {
	rg := newRig(domain.PeriodOpen)
	_, inv := rg.invoiced(t, "PS-1", "INV-1", 40, "2.6", "")
	decode[adjustmentJSON](t, rg.call(http.MethodPost, adjustmentsPath, adjustmentBody(inv.ID, "CREDIT", "CN-1", adjLine(1, "5.00")), rg.apPosterFor(legalEntityID)), http.StatusCreated)

	evts := rg.st.Events()
	last := evts[len(evts)-1]
	if last.Type != "dev.passage.payables.invoice.adjusted.v1" || !strings.Contains(string(last.Data), `"kind":"CREDIT"`) || !strings.Contains(string(last.Data), `"total":"5"`) {
		t.Fatalf("event %s %s", last.Type, last.Data)
	}
	assertDocumented(t, last)
	rec := httptest.NewRecorder()
	rg.api.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "receiving_invoice_adjustments_posted_total 1") {
		t.Fatalf("metrics %s", rec.Body.String())
	}
}

func assertDocumented(t *testing.T, evt events.Event) {
	t.Helper()
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
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

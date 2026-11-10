package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	billingevents "github.com/BenjaminSRussell/passage/services/billing/internal/events"
)

// bookedEvents reads the creditnote.booked events billing wrote to its outbox for a document (the admin pool: the app role
// cannot read the outbox).
func (g *glRig) bookedEvents(t *testing.T, documentID string) []billingevents.CreditNoteBookedData {
	t.Helper()
	rows, err := g.admin.Query(context.Background(), `select payload from outbox where aggregate_id = $1 and type = $2 order by created_at`,
		documentID, billingevents.CreditNoteBooked)
	if err != nil {
		t.Fatal(err)
	}
	payloads, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		t.Fatal(err)
	}
	var out []billingevents.CreditNoteBookedData
	for _, p := range payloads {
		var evt struct {
			Source string                             `json:"source"`
			Data   billingevents.CreditNoteBookedData `json:"data"`
		}
		if err := json.Unmarshal(p, &evt); err != nil {
			t.Fatal(err)
		}
		if evt.Source != "billing" {
			t.Errorf("creditnote.booked source %q, want billing", evt.Source)
		}
		out = append(out, evt.Data)
	}
	return out
}

func (g *glRig) orderLine(t *testing.T, invoiceID string, lineNo int) (string, string) {
	t.Helper()
	var order, product string
	if err := g.admin.QueryRow(context.Background(), `select order_id::text, product_id::text from invoice_line where invoice_id = $1::uuid and line_no = $2`,
		invoiceID, lineNo).Scan(&order, &product); err != nil {
		t.Fatal(err)
	}
	return order, product
}

// TestGLPostings_BookedCreditNotePublishesTheDeferredItReversed: booking a credit note publishes creditnote.booked with the
// allocated and deferred amounts its journal reversed per line and the order and product of the credited line, so revrec reverses
// its schedules by what 2400 was debited (#29, P3). Posting the note and booking an invoice publish none, and a later booking call
// publishes nothing again.
func TestGLPostings_BookedCreditNotePublishesTheDeferredItReversed(t *testing.T) {
	g := newGLRig(t, true)
	id := g.bundleInvoice(t)
	g.book(t, http.StatusOK, "")
	if got := g.bookedEvents(t, id); len(got) != 0 {
		t.Fatalf("invoice booking published %+v, want no creditnote.booked", got)
	}

	var partial struct {
		ID string `json:"id"`
	}
	rec := do(g.handler, http.MethodPost, "/v1/billing/credit-notes", "cn-part", creditBody(id, "2027-03-20", `,"lines":[{"lineNo":3,"qty":"0.5"}]`))
	decode(t, rec, &partial)
	if rec.Code != http.StatusCreated || do(g.handler, http.MethodPost, "/v1/billing/credit-notes/"+partial.ID+":post", "", "").Code != http.StatusOK {
		t.Fatalf("partial credit note %d %s", rec.Code, rec.Body)
	}
	if got := g.bookedEvents(t, partial.ID); len(got) != 0 {
		t.Fatalf("posting (not booking) published %+v, want creditnote.booked only once the journal is booked", got)
	}
	g.book(t, http.StatusOK, "")
	entry := g.entryFor(t, partial.ID)
	if got := lineAmounts(entry); got["Cr 1200"] != "725.00" || got["Dr 2400"] == "" {
		t.Fatalf("partial credit note entry %v, want 725.00 credited to 1200 and a debit to 2400", got)
	}
	order, support := g.orderLine(t, id, 3)
	events := g.bookedEvents(t, partial.ID)
	if len(events) != 1 {
		t.Fatalf("%d creditnote.booked events for the partial note, want 1", len(events))
	}
	e := events[0]
	wantLine := billingevents.CreditNoteBookedLine{LineNo: 1, OriginalLineNo: 3, OrderID: order, Product: support, Net: "725.00", Allocated: "1125.00", Deferred: "1125.00"}
	if e.CreditNoteID != partial.ID || e.OriginalInvoiceID != id || e.LegalEntityID != leID || e.Currency != "USD" || e.PostingDate != "2027-03-12" ||
		e.JournalEntryID != entry.ID || !strings.HasPrefix(e.Number, "CN-") || len(e.Lines) != 1 || e.Lines[0] != wantLine {
		t.Errorf("creditnote.booked %+v, want line %+v (half the support's 2,250.00 deferred) and the ledger entry %s", e, wantLine, entry.ID)
	}

	full := g.creditNote(t, id)
	g.book(t, http.StatusOK, "")
	g.book(t, http.StatusOK, "")
	events = g.bookedEvents(t, full)
	if len(events) != 1 || len(events[0].Lines) != 3 {
		t.Fatalf("remainder credit note events %+v, want one with three lines", events)
	}
	deferred := map[int]string{}
	for _, l := range events[0].Lines {
		deferred[l.OriginalLineNo] = l.Deferred
		if wantOrder, wantProduct := g.orderLine(t, id, l.OriginalLineNo); l.OrderID != wantOrder || l.Product != wantProduct {
			t.Errorf("line %+v names another order line than the original's %d", l, l.OriginalLineNo)
		}
	}
	if deferred[1] != "0.00" || deferred[2] != "1350.00" || deferred[3] != "1125.00" {
		t.Errorf("remainder deferred per original line %v, want 0.00 / 1350.00 / 1125.00 (the support's other half)", deferred)
	}
	if b := g.balance(t, "2400"); !b.Net().IsZero() {
		t.Errorf("2400 nets %s after crediting the whole invoice, want 0", b.Net())
	}
}

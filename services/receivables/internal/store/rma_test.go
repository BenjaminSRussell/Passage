package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

// TestPostRMACredit_AC305_PostsARAndGL posts the AC-30.5 credit note: a
// CREDIT_NOTE open item of -340.00 and Dr 4000 400 / Cr 4200 60 / Cr 1200 340.
func TestPostRMACredit_AC305_PostsARAndGL(t *testing.T) {
	s := setup(t)
	gl := ledgerclient.NewFake()
	s.WithLedger(gl)
	ctx := tenantCtx(1)
	in := RMACredit{RMAID: ids.New(), RMANumber: "RMA-0000001", CustomerID: "00000000-0000-0000-0000-0000000000c1",
		LegalEntityID: legalEntity, Currency: "USD", PostingDate: "2027-03-01", IdempotencyKey: "rma-ac305",
		LedgerID: ids.New(), Gross: amt("400.00"), Restocking: amt("60.00"), Net: amt("340.00")}
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || note.DocumentNumber != "CN-RMA-0000001" || note.OpenItemID == "" || note.JournalPostingID == "" {
		t.Fatalf("post: %+v %v", note, err)
	}
	items, err := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: in.CustomerID, OpenOnly: true}, 10)
	if err != nil || len(items) != 1 || items[0].SourceType != "CREDIT_NOTE" || items[0].Side != "CREDIT" ||
		!items[0].Amount.Equal(amt("-340.00")) || !items[0].OpenAmount.Equal(amt("-340.00")) {
		t.Fatalf("open item %+v %v", items, err)
	}
	entries := gl.Entries()
	if len(entries) != 1 || len(entries[0].Lines) != 3 {
		t.Fatalf("journal %+v", entries)
	}
	l0, l1, l2 := entries[0].Lines[0], entries[0].Lines[1], entries[0].Lines[2]
	if l0.Account != domain.AccountSalesReturns || l0.Credit || !l0.Amount.Equal(amt("400.00")) ||
		l1.Account != domain.AccountRestocking || !l1.Credit || !l1.Amount.Equal(amt("60.00")) ||
		l2.Account != domain.AccountReceivable || !l2.Credit || !l2.Amount.Equal(amt("340.00")) {
		t.Fatalf("GL lines %+v", entries[0].Lines)
	}
	replay, err := s.PostRMACredit(ctx, in)
	if err != nil || replay.OpenItemID != note.OpenItemID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CreditNotePosted); n != 1 {
		t.Fatalf("creditnote.posted count %d", n)
	}
}

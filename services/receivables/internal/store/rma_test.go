package store

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

type failingLedger struct {
	*ledgerclient.Fake
	err   error
	calls int
}

func (f *failingLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.Fake.Post(ctx, e)
}

func rmaSetup(t *testing.T) (*Postgres, *failingLedger, string) {
	t.Helper()
	s := setup(t)
	gl := &failingLedger{Fake: ledgerclient.NewFake()}
	ledgerID := ids.New()
	gl.AddLedger(ledgerID, legalEntity)
	s.WithLedger(gl).WithRMALedgers(map[string]string{legalEntity: ledgerID})
	return s, gl, ledgerID
}

func rmaIn(invoice string) RMACredit {
	return RMACredit{RMAID: ids.New(), RMANumber: "RMA-0000001", LegalEntityID: legalEntity, InvoiceNumber: invoice,
		PostingDate: "2027-03-01", Gross: amt("400.00"), Restocking: amt("60.00"), Net: amt("340.00")}
}

func TestPostRMACredit_AC305_PostsARAndGLFromStoredData(t *testing.T) {
	s, gl, ledgerID := rmaSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := rmaIn(inv.DocumentNumber)
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || note.DocumentNumber != "CN-RMA-0000001" || note.OpenItemID == "" || note.JournalPostingID == "" ||
		note.CustomerID != inv.CustomerID || note.Currency != "USD" {
		t.Fatalf("post: %+v %v", note, err)
	}
	items, err := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: inv.CustomerID, OpenOnly: true}, 10)
	credit := 0
	for _, it := range items {
		if it.SourceType == "CREDIT_NOTE" {
			credit++
			if it.Side != "CREDIT" || !it.Amount.Equal(amt("-340.00")) || !it.OpenAmount.Equal(amt("-340.00")) {
				t.Fatalf("credit item %+v", it)
			}
		}
	}
	if err != nil || credit != 1 {
		t.Fatalf("open items %+v %v", items, err)
	}
	entries := gl.Entries()
	if len(entries) != 1 || len(entries[0].Lines) != 3 || entries[0].LedgerID != ledgerID || entries[0].IdempotencyKey != "rma-credit:"+legalEntity+":CN-RMA-0000001" {
		t.Fatalf("journal %+v", entries)
	}
	l0, l1, l2 := entries[0].Lines[0], entries[0].Lines[1], entries[0].Lines[2]
	if l0.Account != domain.AccountSalesReturns || l0.Credit || !l0.Amount.Equal(amt("400.00")) ||
		l1.Account != domain.AccountRestocking || !l1.Credit || !l1.Amount.Equal(amt("60.00")) ||
		l2.Account != domain.AccountReceivable || !l2.Credit || !l2.Amount.Equal(amt("340.00")) {
		t.Fatalf("GL lines %+v", entries[0].Lines)
	}
	replay, err := s.PostRMACredit(ctx, in)
	if err != nil || replay.OpenItemID != note.OpenItemID || replay.JournalPostingID != note.JournalPostingID || !replay.Net.Equal(amt("340.00")) {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CreditNotePosted); n != 1 || len(gl.Entries()) != 1 {
		t.Fatalf("creditnote.posted count %d, journals %d", n, len(gl.Entries()))
	}
}

func TestPostRMACredit_MismatchedReplayIs422AndStoredCreditUntouched(t *testing.T) {
	s, _, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := rmaIn(inv.DocumentNumber)
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	other := in
	other.Gross, other.Restocking, other.Net = amt("500.00"), amt("75.00"), amt("425.00")
	if _, err := s.PostRMACredit(ctx, other); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("mismatched replay: %v", err)
	}
}

func TestPostRMACredit_RefusesWhatTheInvoiceCannotCover(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)

	tooBig := rmaIn(inv.DocumentNumber)
	tooBig.Gross, tooBig.Restocking, tooBig.Net = amt("1200.00"), amt("0"), amt("1200.00")
	if _, err := s.PostRMACredit(ctx, tooBig); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("credit above the invoice: %v", err)
	}
	unknown := rmaIn("INV-NOPE")
	if _, err := s.PostRMACredit(ctx, unknown); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("unknown invoice: %v", err)
	}
	first := rmaIn(inv.DocumentNumber)
	first.RMANumber = "RMA-0000010"
	first.Gross, first.Restocking, first.Net = amt("700.00"), amt("0"), amt("700.00")
	if _, err := s.PostRMACredit(ctx, first); err != nil {
		t.Fatalf("first credit: %v", err)
	}
	second := rmaIn(inv.DocumentNumber)
	second.RMANumber = "RMA-0000011"
	second.Gross, second.Restocking, second.Net = amt("400.00"), amt("0"), amt("400.00")
	if _, err := s.PostRMACredit(ctx, second); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("cumulative credit above the invoice: %v", err)
	}
	if _, err := s.PostRMACredit(ctx, RMACredit{}); problemCode(err) != CodeRMACreditInvalid {
		t.Fatalf("zero amounts: %v", err)
	}
	if len(gl.Entries()) != 1 {
		t.Fatalf("only the first credit may post: %d journals", len(gl.Entries()))
	}
}

func TestPostRMACredit_NoLedgerConfigurationIs503(t *testing.T) {
	s := setup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	_, err := s.PostRMACredit(ctx, rmaIn(inv.DocumentNumber))
	if problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("no ledger: %v", err)
	}
	items, _ := s.ListOpenItems(ctx, OpenItemFilter{CustomerID: inv.CustomerID}, 10)
	if len(items) != 1 {
		t.Fatalf("nothing may be stored without a ledger: %+v", items)
	}
}

func clearBackoff(t *testing.T, s *Postgres, ctx context.Context) {
	t.Helper()
	if err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update rma_credit set gl_next_attempt_at = $1`, time.Now().Add(-time.Second))
		return err
	}); err != nil {
		t.Fatalf("clear backoff: %v", err)
	}
}

func TestPostRMACredit_LedgerOutageKeepsPendingCreditAndRetriesWithBackoff(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := rmaIn(inv.DocumentNumber)

	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable, Code: "down"}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("outage: %v", err)
	}
	calls := gl.calls
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAUnavailable || gl.calls != calls {
		t.Fatalf("a retry inside the backoff must not call the ledger: %v calls %d->%d", err, calls, gl.calls)
	}
	gl.err = nil
	clearBackoff(t, s, ctx)
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || note.JournalPostingID == "" || len(gl.Entries()) != 1 {
		t.Fatalf("recovered: %+v %v", note, err)
	}
	if n := countOutbox(t, s, ctx, rcvevents.CreditNotePosted); n != 1 {
		t.Fatalf("creditnote.posted count %d", n)
	}
}

func TestPostRMACredit_LedgerRefusalParksTheCredit(t *testing.T) {
	s, gl, _ := rmaSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := rmaIn(inv.DocumentNumber)
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "period_closed"}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAParked {
		t.Fatalf("refusal: %v", err)
	}
	gl.err = nil
	clearBackoff(t, s, ctx)
	calls := gl.calls
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAParked || gl.calls != calls {
		t.Fatalf("a parked credit must not retry: %v", err)
	}
}

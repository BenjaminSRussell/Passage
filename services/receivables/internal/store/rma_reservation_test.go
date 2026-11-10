package store

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/billingclient"
)

func reservedSetup(t *testing.T) (*Postgres, *failingLedger, *billingclient.Fake) {
	t.Helper()
	s, gl, _ := rmaSetup(t)
	bill := billingclient.NewFake()
	s.WithReservations(bill)
	return s, gl, bill
}

func reservedIn(invoice string) RMACredit {
	in := rmaIn(invoice)
	in.InvoiceID = billingInvoiceID
	in.InvoiceLines = []RMAInvoiceLine{{LineNo: 1, Qty: amt("2")}}
	return in
}

func keyOf(in RMACredit) string { return reservationKey(in.LegalEntityID, in.RMAID) }

func storedCredit(t *testing.T, s *Postgres, ctx context.Context, rmaID string) (rmaCreditRow, bool) {
	t.Helper()
	var row rmaCreditRow
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		row, err = loadRMACredit(ctx, tx, `rma_id = $2`, testkit.TenantID(1), rmaID)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return rmaCreditRow{}, false
	}
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return row, true
}

func reservationStatus(t *testing.T, bill *billingclient.Fake, in RMACredit) string {
	t.Helper()
	r, ok := bill.Reservation(keyOf(in))
	if !ok {
		return ""
	}
	return r.Status
}

func TestPostRMACredit_ReservesBeforePostingAndCommitsAfter(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	r, _ := bill.Reservation(keyOf(in))
	if r.Status != "CONSUMED" || r.InvoiceID != billingInvoiceID || len(r.Lines) != 1 || r.Lines[0].LineNo != 1 || !r.Lines[0].Qty.Equal(amt("2")) {
		t.Fatalf("reservation %+v", r)
	}
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if row.status != "POSTED" || !row.committed || row.reservationID != r.ID || len(gl.Entries()) != 1 {
		t.Fatalf("row %+v entries %d", row, len(gl.Entries()))
	}
	if _, err := s.PostRMACredit(ctx, in); err != nil || bill.ReserveCalls() != 1 {
		t.Fatalf("a replay of a posted, committed credit must not call billing: %v calls %d", err, bill.ReserveCalls())
	}
}

func TestPostRMACredit_BillingUnavailableStoresAndPostsNothingAndIsRetryable(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.ReserveErr = errors.New("billing: connection refused")
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("outage: %v", err)
	}
	if _, found := storedCredit(t, s, ctx, in.RMAID); found || gl.calls != 0 {
		t.Fatalf("nothing may be stored or posted: found %v ledger calls %d", found, gl.calls)
	}
	bill.ReserveErr = nil
	if _, err := s.PostRMACredit(ctx, in); err != nil || reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("retry: %v status %q", err, reservationStatus(t, bill, in))
	}
}

func TestPostRMACredit_BillingRefusalIsAPlainRefusalAndStoresNothing(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.NoRoom = true
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("no room: %v", err)
	}
	if _, found := storedCredit(t, s, ctx, in.RMAID); found || gl.calls != 0 || reservationStatus(t, bill, in) != "" {
		t.Fatalf("nothing may be stored, posted or held: found %v ledger %d reservation %q", found, gl.calls, reservationStatus(t, bill, in))
	}
	bill.NoRoom = false
	bill.ReserveErr = billingclient.ErrNotFound
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("unknown invoice: %v", err)
	}
}

func TestPostRMACredit_RefusedAfterReservingReleasesTheReservation(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	in.Gross, in.Restocking, in.Net = amt("5000.00"), amt("0.00"), amt("5000.00")
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("over the invoice amount: %v", err)
	}
	if reservationStatus(t, bill, in) != "RELEASED" || gl.calls != 0 {
		t.Fatalf("status %q ledger calls %d", reservationStatus(t, bill, in), gl.calls)
	}
}

func TestPostRMACredit_LedgerRefusalReleasesAndRetryReReservesTheSameKey(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "period_closed"}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAParked {
		t.Fatalf("park: %v", err)
	}
	held, _ := bill.Reservation(keyOf(in))
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if held.Status != "RELEASED" || row.reservationID != "" || !row.parked {
		t.Fatalf("a parked credit must not hold room: %+v %+v", held, row)
	}
	gl.err = nil
	view, err := s.RetryRMACredit(ctx, row.id, "")
	if err != nil || view.Status != "POSTED" {
		t.Fatalf("retry: %+v %v", view, err)
	}
	again, _ := bill.Reservation(keyOf(in))
	if again.ID != held.ID || again.Status != "CONSUMED" || view.ReservationID != held.ID || !view.ReservationCommitted {
		t.Fatalf("the same key must bring the same reservation back: %+v %+v", again, view)
	}
}

func TestRetryRMACredit_RefusedWhenTheRoomIsGoneAndStaysParked(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "period_closed"}
	_, _ = s.PostRMACredit(ctx, in)
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	gl.err = nil
	bill.NoRoom = true
	calls := gl.calls
	if _, err := s.RetryRMACredit(ctx, row.id, ""); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("no room on retry: %v", err)
	}
	after, _ := storedCredit(t, s, ctx, in.RMAID)
	if after.status != "PENDING" || !after.parked || after.reservationID != "" || gl.calls != calls || reservationStatus(t, bill, in) != "RELEASED" {
		t.Fatalf("a credit with no room must stay parked and unposted: %+v ledger %d->%d", after, calls, gl.calls)
	}
}

func TestPostRMACredit_CommitFailureLeavesAPostedCreditThatIsCommittedLater(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.CommitErr = errors.New("billing: connection reset")
	note, err := s.PostRMACredit(ctx, in)
	if err != nil || note.JournalPostingID == "" {
		t.Fatalf("a posted credit is not undone by a failed commit: %+v %v", note, err)
	}
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if row.status != "POSTED" || row.committed || reservationStatus(t, bill, in) != "RESERVED" {
		t.Fatalf("row %+v status %q", row, reservationStatus(t, bill, in))
	}
	if _, err := s.RetryRMACredit(ctx, row.id, ""); problemCode(err) != CodeRMACreditState {
		t.Fatalf("a posted credit is never retried, so its reservation is never released: %v", err)
	}
	if res, err := s.SweepRMACredits(ctx, time.Now().UTC(), 10); err != nil || res.Failed != 1 || res.Committed != 0 {
		t.Fatalf("sweep while billing is down: %+v %v", res, err)
	}
	bill.CommitErr = nil
	res, err := s.SweepRMACredits(ctx, time.Now().UTC(), 10)
	if err != nil || res.Committed != 1 || reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("sweep: %+v %v status %q", res, err, reservationStatus(t, bill, in))
	}
	if after, _ := storedCredit(t, s, ctx, in.RMAID); !after.committed || len(gl.Entries()) != 1 {
		t.Fatalf("after %+v entries %d", after, len(gl.Entries()))
	}
	if les, err := s.RMASweepLegalEntities(ctx); err != nil || len(les) != 0 {
		t.Fatalf("a committed credit needs no sweep: %v %v", les, err)
	}
}

func TestPostRMACredit_ReplayCommitsAPostedCreditWhoseCommitFailed(t *testing.T) {
	s, _, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.CommitErr = errors.New("billing: timeout")
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	bill.CommitErr = nil
	if _, err := s.PostRMACredit(ctx, in); err != nil || reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("replay: %v status %q", err, reservationStatus(t, bill, in))
	}
}

func TestPostRMACredit_CommitOfAReleasedReservationReReservesThenCommits(t *testing.T) {
	s, _, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.CommitErr = errors.New("billing: timeout")
	_, _ = s.PostRMACredit(ctx, in)
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if err := bill.Release(ctx, in.LegalEntityID, row.reservationID); err != nil {
		t.Fatalf("stray release: %v", err)
	}
	bill.CommitErr = nil
	if res, err := s.SweepRMACredits(ctx, time.Now().UTC(), 10); err != nil || res.Committed != 1 || reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("sweep: %+v %v status %q", res, err, reservationStatus(t, bill, in))
	}
}

func TestPostRMACredit_CrashAfterReservingBeforeStoringReusesTheReservation(t *testing.T) {
	s, _, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	orphan, err := bill.Reserve(ctx, in.LegalEntityID, in.InvoiceID, keyOf(in), in.RMANumber, []billingclient.Line{{LineNo: 1, Qty: amt("2")}})
	if err != nil {
		t.Fatalf("orphan: %v", err)
	}
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if row.reservationID != orphan.ID || reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("the retry must adopt the orphan reservation: %+v", row)
	}
}

func TestPostRMACredit_CrashAfterStoringBeforePostingIsFinishedBySweepAndCommitted(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable, Code: "down"}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("outage: %v", err)
	}
	if reservationStatus(t, bill, in) != "RESERVED" {
		t.Fatalf("a pending credit keeps its room: %q", reservationStatus(t, bill, in))
	}
	gl.err = nil
	res, err := s.SweepRMACredits(ctx, time.Now().UTC().Add(2*time.Hour), 10)
	if err != nil || res.Posted != 1 || reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("sweep: %+v %v status %q", res, err, reservationStatus(t, bill, in))
	}
}

func TestPostRMACredit_InvoiceLinesAreRequiredWhenBillingIsConfiguredAndRefusedWhenNot(t *testing.T) {
	s, _, _ := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	in.InvoiceLines = nil
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMACreditInvalid {
		t.Fatalf("no lines: %v", err)
	}
	in.InvoiceLines = []RMAInvoiceLine{{LineNo: 1, Qty: amt("1")}, {LineNo: 1, Qty: amt("1")}}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMACreditInvalid {
		t.Fatalf("duplicate line: %v", err)
	}
	s.reserver = nil
	in.InvoiceLines = []RMAInvoiceLine{{LineNo: 1, Qty: amt("1")}}
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("billing not configured: %v", err)
	}
}

func TestPostRMACredit_ReplayWithOtherInvoiceLinesIsRefusedWithoutReserving(t *testing.T) {
	s, _, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("post: %v", err)
	}
	other := in
	other.InvoiceLines = []RMAInvoiceLine{{LineNo: 2, Qty: amt("2")}}
	if _, err := s.PostRMACredit(ctx, other); problemCode(err) != CodeIdempotencyReuse || bill.ReserveCalls() != 1 {
		t.Fatalf("other lines: %v calls %d", err, bill.ReserveCalls())
	}
}

func TestPostRMACredit_ConcurrentRepeatsPostOnceAndKeepTheReservation(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.PostRMACredit(ctx, in)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && problemCode(err) != CodeIdempotencyBusy {
			t.Fatalf("a repeat is a replay or a retryable conflict: %v", err)
		}
	}
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("settled repeat: %v", err)
	}
	if reservationStatus(t, bill, in) != "CONSUMED" || len(gl.Entries()) != 1 || len(creditItems(t, s, ctx, inv.CustomerID)) != 1 {
		t.Fatalf("status %q journals %d", reservationStatus(t, bill, in), len(gl.Entries()))
	}
}

type gatedLedger struct {
	*ledgerclient.Fake
	entered chan struct{}
	proceed chan struct{}
}

func (g *gatedLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	g.entered <- struct{}{}
	<-g.proceed
	return g.Fake.Post(ctx, e)
}

func TestPostRMACredit_ATwinWhileAnAttemptIsPostingNeitherParksNorReleases(t *testing.T) {
	s, _, bill := reservedSetup(t)
	gate := &gatedLedger{Fake: ledgerclient.NewFake(), entered: make(chan struct{}, 2), proceed: make(chan struct{})}
	ledgerID := s.rmaLedgers[legalEntity]
	gate.AddLedger(ledgerID, legalEntity)
	s.WithLedger(gate)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	first := make(chan error, 1)
	go func() {
		_, err := s.PostRMACredit(ctx, in)
		first <- err
	}()
	<-gate.entered
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeIdempotencyBusy {
		t.Fatalf("the twin must wait its turn: %v", err)
	}
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	if row.parked || row.reservationID == "" || reservationStatus(t, bill, in) != "RESERVED" {
		t.Fatalf("the twin must not park or release: %+v status %q", row, reservationStatus(t, bill, in))
	}
	close(gate.proceed)
	if err := <-first; err != nil {
		t.Fatalf("first: %v", err)
	}
	if reservationStatus(t, bill, in) != "CONSUMED" {
		t.Fatalf("status %q", reservationStatus(t, bill, in))
	}
	calls := bill.ReserveCalls()
	stale := row
	if got, err := s.postRMAJournal(ctx, testkit.TenantID(1), stale, time.Now().Add(time.Hour)); err != nil || got.status != "POSTED" || bill.ReserveCalls() != calls {
		t.Fatalf("a stale snapshot of a posted credit must not post or reserve again: %+v %v", got, err)
	}
}

func TestPostRMACredit_CommitFindingNoRoomFlagsThePostedCreditForAnOperator(t *testing.T) {
	s, _, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.CommitErr = errors.New("billing: timeout")
	_, _ = s.PostRMACredit(ctx, in)
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	_ = bill.Release(ctx, in.LegalEntityID, row.reservationID)
	bill.CommitErr = nil
	bill.NoRoom = true
	before := ReservationAttention.Load()
	if res, err := s.SweepRMACredits(ctx, time.Now().UTC(), 10); err != nil || res.Failed != 1 {
		t.Fatalf("sweep: %+v %v", res, err)
	}
	flagged, _ := storedCredit(t, s, ctx, in.RMAID)
	if flagged.status != "POSTED" || flagged.attention == "" || ReservationAttention.Load() != before+1 {
		t.Fatalf("flag %+v", flagged)
	}
	list, err := s.ListRMACredits(ctx, RMACreditFilter{AttentionOnly: true, Limit: 10})
	if err != nil || len(list) != 1 || list[0].NeedsAttention == "" {
		t.Fatalf("the operator list must show it: %+v %v", list, err)
	}
	calls := bill.ReserveCalls()
	if res, err := s.SweepRMACredits(ctx, time.Now().UTC(), 10); err != nil || res != (RMASweepResult{}) || bill.ReserveCalls() != calls {
		t.Fatalf("a flagged credit is not retried forever: %+v %v", res, err)
	}
}

func TestPostRMACredit_AnyOtherBillingRejectionIsAPlain422AndParksAStoredCredit(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	bill.ReserveErr = billingclient.ErrRejected
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("400 from billing: %v", err)
	}
	bill.ReserveErr = billingclient.ErrKeyReuse
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("key reuse: %v", err)
	}
	if _, found := storedCredit(t, s, ctx, in.RMAID); found {
		t.Fatalf("nothing may be stored")
	}
	bill.ReserveErr = nil
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: "period_closed"}
	_, _ = s.PostRMACredit(ctx, in)
	row, _ := storedCredit(t, s, ctx, in.RMAID)
	gl.err = nil
	bill.ReserveErr = billingclient.ErrRejected
	if _, err := s.RetryRMACredit(ctx, row.id, ""); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("retry: %v", err)
	}
	if after, _ := storedCredit(t, s, ctx, in.RMAID); !after.parked || after.status != "PENDING" {
		t.Fatalf("a rejected re-reserve parks instead of backing off forever: %+v", after)
	}
}

func TestPostRMACredit_ReservedLessThanTheReturnValueIsRefusedAndReleased(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	short := amt("100.00")
	bill.Net = &short
	if _, err := s.PostRMACredit(ctx, in); problemCode(err) != CodeRMAInvoice {
		t.Fatalf("short: %v", err)
	}
	if _, found := storedCredit(t, s, ctx, in.RMAID); found || gl.calls != 0 || reservationStatus(t, bill, in) != "RELEASED" {
		t.Fatalf("found %v ledger %d status %q", found, gl.calls, reservationStatus(t, bill, in))
	}
	enough := amt("400.00")
	bill.Net = &enough
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("enough: %v", err)
	}
}

func TestPostRMACredit_ACreditStoredBeforeReservationsGetsItsLinesFromTheRepeatAndReservesBeforePosting(t *testing.T) {
	s, gl, bill := reservedSetup(t)
	s.reserver = nil
	ctx := tenantCtx(1)
	inv := seedInvoice(t, s, ctx)
	in := reservedIn(inv.DocumentNumber)
	legacy := in
	legacy.InvoiceLines = nil
	gl.err = &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable, Code: "down"}
	if _, err := s.PostRMACredit(ctx, legacy); problemCode(err) != CodeRMAUnavailable {
		t.Fatalf("legacy store: %v", err)
	}
	gl.err = nil
	clearBackoff(t, s, ctx)
	s.WithReservations(bill)
	if res, err := s.SweepRMACredits(ctx, time.Now().UTC().Add(time.Hour), 10); err != nil || res.Posted != 0 || len(gl.Entries()) != 0 {
		t.Fatalf("the sweep must not post a credit nothing was reserved for: %+v %v", res, err)
	}
	if _, err := s.PostRMACredit(ctx, in); err != nil {
		t.Fatalf("repeat with lines: %v", err)
	}
	if reservationStatus(t, bill, in) != "CONSUMED" || len(gl.Entries()) != 1 {
		t.Fatalf("status %q journals %d", reservationStatus(t, bill, in), len(gl.Entries()))
	}
}

package store

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/domain"
)

func requirePostingRequest(t *testing.T, ctx context.Context, s *Postgres, key, status string) PostingRequest {
	t.Helper()
	pr, err := s.GetPostingRequest(ctx, key)
	if err != nil {
		t.Fatalf("GetPostingRequest(%s): %v", key, err)
	}
	if pr.Status != status {
		t.Fatalf("key %s: status %s, want %s (%+v)", key, pr.Status, status, pr)
	}
	return pr
}

func requireNoPostingRequest(t *testing.T, ctx context.Context, s *Postgres, key string) {
	t.Helper()
	_, err := s.GetPostingRequest(ctx, key)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusNotFound || p.Code != CodePostingRequestNotFound {
		t.Fatalf("key %s: want 404 %s, got %v", key, CodePostingRequestNotFound, err)
	}
}

func unbalancedInvoice(ledgerID, key string) PostInput {
	in := vendorInvoice(ledgerID, key, "10.00")
	in.Lines[1].Amount = decimal.RequireFromString("9.00")
	return in
}

func TestPostgres_PostingRequestRecordedInPostingTx(t *testing.T) {
	e, _ := setup(t)
	out, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "REG-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	pr := requirePostingRequest(t, e.ctx, e.store, "REG-1", PostingRequestPosted)
	if pr.EntryID == nil || *pr.EntryID != out.Entries[0].ID || pr.DocumentNumber == nil || *pr.DocumentNumber != out.Entries[0].Voucher || pr.LedgerID != e.ledger.ID || pr.ViolationCode != nil {
		t.Fatalf("posted row %+v, entry %s", pr, out.Entries[0].ID)
	}
}

func TestPostgres_PostingRequestReplayLeavesTheRowAndAnswersTheSameEntry(t *testing.T) {
	e, _ := setup(t)
	in := vendorInvoice(e.ledger.ID, "REG-REPLAY", "100.00")
	first, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	before := requirePostingRequest(t, e.ctx, e.store, "REG-REPLAY", PostingRequestPosted)
	again, err := e.store.PostEntry(e.ctx, in)
	if err != nil || !again.Idempotency.Replayed {
		t.Fatalf("replay: %+v %v", again, err)
	}
	after := requirePostingRequest(t, e.ctx, e.store, "REG-REPLAY", PostingRequestPosted)
	if !after.UpdatedAt.Equal(before.UpdatedAt) || *after.EntryID != first.Entries[0].ID {
		t.Fatalf("replay changed the row: before %+v after %+v", before, after)
	}
}

func TestPostgres_PostingRequestRejectedPostLeavesNoPostedRow(t *testing.T) {
	e, _ := setup(t)
	_, err := e.store.PostEntry(e.ctx, unbalancedInvoice(e.ledger.ID, "REG-BAD"))
	if err == nil {
		t.Fatal("an unbalanced entry must be rejected")
	}
	pr := requirePostingRequest(t, e.ctx, e.store, "REG-BAD", PostingRequestRejected)
	if pr.EntryID != nil || pr.ViolationCode == nil || *pr.ViolationCode == "" {
		t.Fatalf("rejected row %+v", pr)
	}
	var entries int
	if err := e.admin.QueryRow(context.Background(), `select count(*) from journal_entry where idempotency_key = 'REG-BAD'`).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("a rejected post left %d entries (%v)", entries, err)
	}
}

func TestPostgres_PostingRequestRejectedKeyBecomesPostedOnRetryAndIsNeverDowngraded(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.store.PostEntry(e.ctx, unbalancedInvoice(e.ledger.ID, "REG-RETRY")); err == nil {
		t.Fatal("first attempt must be rejected")
	}
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "REG-RETRY", "10.00")); err != nil {
		t.Fatal(err)
	}
	requirePostingRequest(t, e.ctx, e.store, "REG-RETRY", PostingRequestPosted)
	e.store.recordRejectedPosting(e.ctx, e.tenant, e.ledger.ID, "REG-RETRY", &domain.RejectedError{Violations: []domain.Violation{{Code: "X"}}})
	requirePostingRequest(t, e.ctx, e.store, "REG-RETRY", PostingRequestPosted)
}

func TestPostgres_PostingRequestWriteFailureRollsBackThePosting(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.admin.Exec(context.Background(), `revoke insert, update on posting_request from depot_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "REG-FAIL", "10.00")); err == nil {
		t.Fatal("a post whose registry row cannot be written must fail")
	}
	var entries int
	if err := e.admin.QueryRow(context.Background(), `select count(*) from journal_entry where idempotency_key = 'REG-FAIL'`).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("posting committed without its registry row: %d entries (%v)", entries, err)
	}
}

func TestPostgres_PostingRequestRecordedForAReversal(t *testing.T) {
	e, _ := setup(t)
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "REG-ORIG", "10.00"))
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := domain.HashPayload("reverse")
	rev, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: orig.Entries[0].ID, Key: "REG-REV", Hash: hash, TraceID: testTrace, Today: testToday})
	if err != nil {
		t.Fatal(err)
	}
	pr := requirePostingRequest(t, e.ctx, e.store, "REG-REV", PostingRequestPosted)
	if *pr.EntryID != rev.Entries[0].ID {
		t.Fatalf("reversal row %+v, entry %s", pr, rev.Entries[0].ID)
	}
}

func TestPostgres_PostingRequestIsHiddenFromOtherTenantsAndLegalEntities(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "REG-RLS", "10.00")); err != nil {
		t.Fatal(err)
	}
	requireNoPostingRequest(t, principalCtx(testkit.TenantID(2), ids.New()), e.store, "REG-RLS")
	requireNoPostingRequest(t, principalCtx(e.tenant, ids.New()), e.store, "REG-RLS")
}

func TestPostgres_PostingRequestUnknownKeyIs404(t *testing.T) {
	e, _ := setup(t)
	requireNoPostingRequest(t, e.ctx, e.store, "never-seen")
}

package store

import (
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

func closePeriodsBefore(t *testing.T, e env, firstOpen string) {
	t.Helper()
	if _, err := e.admin.Exec(e.ctx, `update ledger set first_open_date = $1 where id = $2`, firstOpen, e.ledger.ID); err != nil {
		t.Fatalf("close periods: %v", err)
	}
}

func requirePeriodClosed(t *testing.T, path string, err error) {
	t.Helper()
	var rej *domain.RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("%s: want a rejection, got %v", path, err)
	}
	for _, v := range rej.Violations {
		if v.Code == "LEDGER_PERIOD_CLOSED" {
			return
		}
	}
	t.Fatalf("%s: want LEDGER_PERIOD_CLOSED, got %+v", path, rej.Violations)
}

func entryTotal(t *testing.T, e env) int {
	t.Helper()
	return setEnv{env: e}.entryCount(t)
}

func TestPostgres_ClosedPeriodRejectsEveryPostingPath(t *testing.T) {
	e := setupLedgerSet(t)
	putName(t, e.env, "GJ", JournalName{Active: true})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "OPEN-1", "100.00"))
	if err != nil {
		t.Fatalf("post while open: %v", err)
	}
	draft, err := e.store.CreateBatch(e.ctx, batchOf(e.env, "GJ", "55"))
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	validated, err := e.store.SubmitBatch(e.ctx, draft.ID, 1, testToday)
	if err != nil {
		t.Fatalf("submit batch: %v", err)
	}
	before := entryTotal(t, e.env)

	closePeriodsBefore(t, e.env, "2026-04-01")

	t.Run("direct post", func(t *testing.T) {
		_, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "CLOSED-1", "10.00"))
		requirePeriodClosed(t, "PostEntry", err)
	})
	t.Run("validate only", func(t *testing.T) {
		res, err := e.store.ValidateEntry(e.ctx, vendorInvoice(e.ledger.ID, "CLOSED-V", "10.00"))
		if err != nil || res.Valid() || res.Violations[0].Code != "LEDGER_PERIOD_CLOSED" {
			t.Fatalf("ValidateEntry: %+v %v", res, err)
		}
	})
	t.Run("ledger set post", func(t *testing.T) {
		_, err := e.store.PostSetEntry(e.ctx, e.set.ID, vendorInvoice("", "CLOSED-SET", "10.00"))
		requirePeriodClosed(t, "PostSetEntry", err)
	})
	t.Run("reversal into the closed period", func(t *testing.T) {
		hash, _ := domain.HashPayload("reverse closed")
		reversalDate := time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC)
		_, err := e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: orig.Entries[0].ID, PostingDate: &reversalDate, Key: "REV-CLOSED", Hash: hash, Today: testToday})
		requirePeriodClosed(t, "ReverseEntry", err)
	})
	t.Run("batch post", func(t *testing.T) {
		_, err := postBatch(e.env, e.ctx, validated, "BATCH-CLOSED", true)
		requirePeriodClosed(t, "PostBatch", err)
	})

	if after := entryTotal(t, e.env); after != before {
		t.Fatalf("a rejected posting wrote entries: %d before, %d after", before, after)
	}
	if got, err := e.store.GetBatch(e.ctx, validated.ID); err != nil || got.Status != domain.BatchValidated {
		t.Fatalf("a rejected batch post must leave the batch validated: %+v %v", got, err)
	}
}

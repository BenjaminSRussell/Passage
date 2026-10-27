package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

// activePrice creates and activates a PRICE record for customer cust and returns it (version 2).
func activePrice(t *testing.T, ctx context.Context, s *store.Postgres, cust, amount string) store.Entry {
	t.Helper()
	e := entry("2026-01-01", "2026-12-31")
	e.CustomerKey, e.Amount = cust, decimal.RequireFromString(amount)
	created, err := s.CreateRecord(ctx, e, "")
	if err != nil {
		t.Fatal(err)
	}
	active, err := s.ActivateRecord(ctx, created.ID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func adjust(id string, version int, pct string) domain.JournalLine {
	return domain.JournalLine{Op: domain.OpUpdate, RecordID: id, ExpectedVersion: version, AdjustPct: decimal.RequireFromString(pct)}
}

// recordState is every condition record's (id, status, version, amount), for "nothing changed" checks.
func recordState(t *testing.T, ctx context.Context, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, status || '/' || version || '/' || amount::text from condition_record`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, state string
			if err := rows.Scan(&id, &state); err != nil {
				return err
			}
			out[id] = state
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// AC-26.7 and AC-26.10 against Postgres: a smart-rounded 5% raise of 7.41 stores 7.99, the
// post writes one activated event per record plus journal.posted, and the reversal restores 7.41.
func TestPostgres_JournalPostAndReverse(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	raised := activePrice(t, ctx, s, "C1", "7.41")
	dropped := activePrice(t, ctx, s, "C2", "20")
	newRec := entry("2026-01-01", "2026-12-31")
	newRec.CustomerKey, newRec.Amount = "C3", decimal.RequireFromString("12")

	j, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Autumn prices", SmartRound: true,
		Lines: []domain.JournalLine{adjust(raised.ID, 2, "5"), {Op: domain.OpDelete, RecordID: dropped.ID, ExpectedVersion: 2},
			{Op: domain.OpCreate, Record: newRec.Record}}}, "jr-1")
	if err != nil || j.Status != domain.JournalOpen || j.LineCount != 3 || j.Lines[2].RecordID == "" {
		t.Fatalf("create = %+v, %v; want OPEN with 3 lines and an id for the CREATE line", j, err)
	}
	if _, err := s.PostJournal(ctx, j.ID, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("posting an OPEN journal = %v; want %s", err, store.CodeStateInvalid)
	}
	v, err := s.ValidateJournal(ctx, j.ID)
	if err != nil || v.Status != domain.JournalValidated || v.ErrorCount != 0 {
		t.Fatalf("validate = %+v, %v", v, err)
	}
	if !v.Lines[0].After.Amount.Equal(decimal.RequireFromString("7.99")) || !v.Lines[0].Before.Amount.Equal(decimal.RequireFromString("7.41")) {
		t.Fatalf("line 1 before/after = %s/%s; want 7.41/7.99", v.Lines[0].Before.Amount, v.Lines[0].After.Amount)
	}

	posted, err := s.PostJournal(ctx, j.ID, "post-1")
	if err != nil || posted.Status != domain.JournalPosted || posted.PostedAt == nil {
		t.Fatalf("post = %+v, %v", posted, err)
	}
	again, err := s.PostJournal(ctx, j.ID, "post-1")
	if err != nil || again.ID != j.ID || again.Status != domain.JournalPosted {
		t.Fatalf("replayed post = %+v, %v; want the first response", again, err)
	}
	if got, _ := s.GetRecord(ctx, raised.ID); !got.Amount.Equal(decimal.RequireFromString("7.99")) || got.Version != 3 {
		t.Fatalf("raised record = %s v%d; want 7.99 v3", got.Amount, got.Version)
	}
	if got, _ := s.GetRecord(ctx, dropped.ID); got.Status != domain.StatusWithdrawn {
		t.Fatalf("deleted record = %s; want WITHDRAWN", got.Status)
	}
	created, err := s.GetRecord(ctx, j.Lines[2].RecordID)
	if err != nil || created.Status != domain.StatusActive || created.CustomerKey != "C3" {
		t.Fatalf("created record = %+v, %v", created, err)
	}

	events := outboxRows(t, ctx, pool)
	tail := events[len(events)-4:]
	if tail[0].Type != "dev.passage.pricing.conditionrecord.activated.v1" || tail[0].Data["journal_id"] != j.ID ||
		tail[0].Data["legal_entity_id"] != legalEntity || tail[0].Data["retroactive"] != true {
		t.Fatalf("first journal event = %+v; want activated with journal_id, legal entity and retroactive", tail[0])
	}
	if tail[1].Type != "dev.passage.pricing.conditionrecord.withdrawn.v1" || tail[3].Type != "dev.passage.pricing.journal.posted.v1" ||
		tail[3].Data["line_count"] != float64(3) {
		t.Fatalf("journal events = %+v; want activated, withdrawn, activated, journal.posted with 3 lines", tail)
	}

	rev, err := s.ReverseJournal(ctx, j.ID, "rev-1")
	if err != nil || rev.Status != domain.JournalPosted || rev.ReversesID != j.ID {
		t.Fatalf("reverse = %+v, %v", rev, err)
	}
	if replay, err := s.ReverseJournal(ctx, j.ID, "rev-1"); err != nil || replay.ID != rev.ID {
		t.Fatalf("replayed reverse = %+v, %v; want the same reversing journal", replay, err)
	}
	if _, err := s.ReverseJournal(ctx, j.ID, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("reversing a REVERSED journal = %v; want %s", err, store.CodeStateInvalid)
	}
	if got, _ := s.GetRecord(ctx, raised.ID); !got.Amount.Equal(decimal.RequireFromString("7.41")) || got.Status != domain.StatusActive {
		t.Fatalf("after reversal raised record = %s %s; want 7.41 ACTIVE", got.Amount, got.Status)
	}
	if got, _ := s.GetRecord(ctx, dropped.ID); got.Status != domain.StatusActive || !got.Amount.Equal(decimal.RequireFromString("20")) {
		t.Fatalf("after reversal deleted record = %s %s; want ACTIVE 20", got.Status, got.Amount)
	}
	if got, _ := s.GetRecord(ctx, created.ID); got.Status != domain.StatusWithdrawn {
		t.Fatalf("after reversal created record = %s; want WITHDRAWN", got.Status)
	}
	orig, _ := s.GetJournal(ctx, j.ID)
	if orig.Status != domain.JournalReversed || orig.ReversedByID != rev.ID {
		t.Fatalf("original = %s reversed by %q; want REVERSED by %s", orig.Status, orig.ReversedByID, rev.ID)
	}
	logged, err := s.ListJournals(ctx, store.JournalFilter{LegalEntityID: legalEntity})
	if err != nil || len(logged) != 2 || len(logged[0].Lines) != 0 {
		t.Fatalf("journal log = %d journals, %v; want the journal and its reversal without lines", len(logged), err)
	}
}

// Journal atomicity (PRD 5.26.9): a change made after validation fails the post, the error
// names the line, and no record changes.
func TestPostgres_JournalPostIsAtomic(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	a := activePrice(t, ctx, s, "C1", "10")
	b := activePrice(t, ctx, s, "C2", "20")
	j, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Two raises",
		Lines: []domain.JournalLine{adjust(a.ID, 2, "10"), adjust(b.ID, 2, "10")}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.ValidateJournal(ctx, j.ID); err != nil || v.Status != domain.JournalValidated {
		t.Fatalf("validate = %+v, %v", v, err)
	}
	if _, err := s.WithdrawRecord(ctx, b.ID, 2, "changed meanwhile", ""); err != nil {
		t.Fatal(err)
	}
	before := recordState(t, ctx, pool)
	_, err = s.PostJournal(ctx, j.ID, "")
	if problemCode(err) != domain.CodeJournalInvalid {
		t.Fatalf("post after a concurrent change = %v; want %s", err, domain.CodeJournalInvalid)
	}
	if msg := err.Error(); !strings.Contains(msg, "line 2") {
		t.Fatalf("error %q must name line 2", msg)
	}
	after := recordState(t, ctx, pool)
	if len(after) != len(before) {
		t.Fatalf("record count changed: %d -> %d", len(before), len(after))
	}
	for id, st := range before {
		if after[id] != st {
			t.Fatalf("record %s changed from %s to %s although the post failed", id, st, after[id])
		}
	}
	if got, _ := s.GetJournal(ctx, j.ID); got.Status != domain.JournalValidated {
		t.Fatalf("journal = %s; want it left VALIDATED", got.Status)
	}
}

// A journal only reaches records of its own legal entity, and a key reused with another body is refused.
func TestPostgres_JournalScopeAndIdempotentCreate(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	other := entry("2026-01-01", "2026-12-31")
	other.LegalEntityID = otherLegalEntity
	created, err := s.CreateRecord(ctx, other, "")
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := s.ActivateRecord(ctx, created.ID, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	in := store.JournalInput{LegalEntityID: legalEntity, Name: "Reach across", Lines: []domain.JournalLine{adjust(foreign.ID, 2, "10")}}
	j, err := s.CreateJournal(ctx, in, "jk-1")
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.CreateJournal(ctx, in, "jk-1"); err != nil || replay.ID != j.ID {
		t.Fatalf("replayed create = %+v, %v; want journal %s", replay, err, j.ID)
	}
	changed := in
	changed.Lines = []domain.JournalLine{adjust(foreign.ID, 2, "20")}
	if _, err := s.CreateJournal(ctx, changed, "jk-1"); problemCode(err) != store.CodeIdempotencyUse {
		t.Fatalf("same key, different lines = %v; want %s", err, store.CodeIdempotencyUse)
	}
	v, err := s.ValidateJournal(ctx, j.ID)
	if err != nil || v.Status != domain.JournalOpen || v.ErrorCount != 1 || len(v.Lines[0].Errors) != 1 {
		t.Fatalf("validate = %+v, %v; want OPEN with the foreign record not found", v, err)
	}
	if _, err := s.GetJournal(ctx, "00000000-0000-0000-0000-00000000dead"); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("unknown journal = %v; want %s", err, store.CodeJournalNotFound)
	}
	// Another tenant cannot see the journal (row-level security).
	if _, err := s.GetJournal(principal(2, ""), j.ID); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("other tenant read = %v; want %s", err, store.CodeJournalNotFound)
	}
}

// Migration 0002 widens the condition type check: MULTILINE_DISC and SURCHARGE records store.
func TestPostgres_StoresMultilineAndSurchargeRecords(t *testing.T) {
	pool := appPool(t)
	ctx := principal(1, "")
	s := store.New(pool)
	ml := entry("2026-01-01", "2026-12-31")
	ml.Type, ml.ProductKind, ml.ProductKey, ml.DiscPct1 = domain.TypeMultiline, domain.ProductKindGroup, "PG-A", decimal.NewFromInt(5)
	sur := entry("2026-01-01", "2026-12-31")
	sur.Type, sur.ProductKind, sur.ProductKey, sur.Amount = domain.TypeSurcharge, domain.ProductKindAll, "", decimal.NewFromInt(15)
	for _, e := range []store.Entry{ml, sur} {
		got, err := s.CreateRecord(ctx, e, "")
		if err != nil || got.Type != e.Type {
			t.Fatalf("create %s = %+v, %v", e.Type, got, err)
		}
	}
}

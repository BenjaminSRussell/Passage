package store_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const massLines = 100000

// massCents is the seeded price of product i in cents: 1.00 to 999.99, spread so that
// neighbouring records differ.
func massCents(i int) int64 { return 100 + int64(i*7919)%99900 }

type massRow struct {
	id      string
	version int
	amount  decimal.Decimal
	status  string
}

// massState reads every condition record by product key, plus the outbox row count.
func massState(t *testing.T, ctx context.Context, app, admin *pgxpool.Pool) (map[string]massRow, int) {
	t.Helper()
	out := make(map[string]massRow, massLines)
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select product_key, id::text, version, amount::text, status from condition_record`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key, amount string
			var r massRow
			if err := rows.Scan(&key, &r.id, &r.version, &amount, &r.status); err != nil {
				return err
			}
			r.amount = decimal.RequireFromString(amount)
			out[key] = r
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	return out, events
}

func sameMassState(t *testing.T, phase string, want, got map[string]massRow) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d records, want %d", phase, len(got), len(want))
	}
	for k, w := range want {
		if g := got[k]; g.version != w.version || g.status != w.status || !g.amount.Equal(w.amount) {
			t.Fatalf("%s: record %s is %s v%d %s, want %s v%d %s", phase, k, g.status, g.version, g.amount, w.status, w.version, w.amount)
		}
	}
}

// TestPostgres_Journal100kAtomicAndReversible (Wave 26 AC4, FR-26.9, AC-26.7, PRD 5.26.9
// price-journal-100k): a mass update of 100,000 prices is atomic and reversible.
//
//  1. 100,000 ACTIVE prices are created by one 100,000-line CREATE journal.
//  2. A 100,000-line +7.5% journal with one stale line (line 73,421) does not validate:
//     the error names that line, the journal cannot post, and nothing is written.
//  3. The corrected journal validates; its post is made to fail on the very last line
//     (a test trigger raises on that record's update, after 99,999 updates ran in the
//     transaction): every record, the journal status and the outbox are unchanged.
//  4. The post succeeds: every price is old x 1.075 (worked in integer cents here, not by
//     the engine) at version 2, with 100,000 activated events plus journal.posted.
//  5. The reversal restores every prior amount, ACTIVE, at version 3.
//
// Post and reversal durations are logged against the FR-26.9 budget (100k lines < 120 s).
func TestPostgres_Journal100kAtomicAndReversible(t *testing.T) {
	if testing.Short() {
		t.Skip("posts several 100,000-line journals")
	}
	admin, pool := adminAndAppPools(t)
	ctx := principal(1, "")
	s := store.New(pool)

	recs := make([]domain.Record, massLines)
	for i := range recs {
		recs[i] = domain.Record{Type: domain.TypePrice, CustomerKind: domain.CustomerKindAll, ProductKind: domain.ProductKindProduct,
			ProductKey: fmt.Sprintf("M%06d", i), Currency: "USD", Unit: "EA", FromQuantity: decimal.Zero,
			Amount: decimal.New(massCents(i), -2), ValidFrom: day("2026-01-01"), ValidTo: day("2026-12-31")}
	}
	start := time.Now()
	seedViaJournal(t, ctx, s, recs)
	t.Logf("create journal of %d lines: create, validate and post in %s", massLines, time.Since(start).Round(time.Millisecond))
	seeded, events0 := massState(t, ctx, pool, admin)
	if len(seeded) != massLines {
		t.Fatalf("seeded %d records, want %d", len(seeded), massLines)
	}
	for i := 0; i < massLines; i += 9973 {
		if r := seeded[fmt.Sprintf("M%06d", i)]; r.version != 1 || r.status != domain.StatusActive || !r.amount.Equal(decimal.New(massCents(i), -2)) {
			t.Fatalf("seeded M%06d = %+v", i, r)
		}
	}

	raise := func(badLine int) []domain.JournalLine {
		lines := make([]domain.JournalLine, massLines)
		for i := range lines {
			r := seeded[fmt.Sprintf("M%06d", i)]
			lines[i] = adjust(r.id, r.version, "7.5")
			if i+1 == badLine {
				lines[i].ExpectedVersion = r.version + 98
			}
		}
		return lines
	}

	// 2. One invalid line: validation names it, the journal stays OPEN and cannot post.
	const badLine = 73421
	bad, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Raise 7.5% (one stale line)", Lines: raise(badLine)}, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ValidateJournal(ctx, bad.ID)
	if err != nil || v.Status != domain.JournalOpen || v.ErrorCount != 1 {
		t.Fatalf("validate = %s with %d errors, %v; want OPEN with exactly 1 error", v.Status, v.ErrorCount, err)
	}
	if errs := v.Lines[badLine-1].Errors; len(errs) != 1 || errs[0].Code != domain.CodeConflictVersion || v.Lines[badLine-1].No != badLine {
		t.Fatalf("line %d errors = %+v; want one %s", badLine, errs, domain.CodeConflictVersion)
	}
	if _, err := s.PostJournal(ctx, bad.ID, ""); problemCode(err) != store.CodeStateInvalid {
		t.Fatalf("posting the invalid journal = %v; want %s", err, store.CodeStateInvalid)
	}
	after, events := massState(t, ctx, pool, admin)
	sameMassState(t, "after the refused post", seeded, after)
	if events != events0 {
		t.Fatalf("outbox grew from %d to %d although nothing posted", events0, events)
	}

	// 3. The corrected journal validates; a failure on its last line rolls back every update.
	good, err := s.CreateJournal(ctx, store.JournalInput{LegalEntityID: legalEntity, Name: "Raise 7.5%", Lines: raise(0)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := s.ValidateJournal(ctx, good.ID); err != nil || v.Status != domain.JournalValidated {
		t.Fatalf("validate corrected journal = %s with %d errors, %v", v.Status, v.ErrorCount, err)
	}
	last := seeded[fmt.Sprintf("M%06d", massLines-1)].id
	if _, err := admin.Exec(context.Background(), `create function fail_last_journal_line() returns trigger language plpgsql as
		$$ begin raise exception 'injected failure on the last journal line'; end $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), fmt.Sprintf(`create trigger fail_last_journal_line before update on condition_record
		for each row when (old.id = '%s') execute function fail_last_journal_line()`, last)); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	_, err = s.PostJournal(ctx, good.ID, "")
	if err == nil || !strings.Contains(err.Error(), "injected failure on the last journal line") {
		t.Fatalf("post with a failing last line = %v; want the injected failure", err)
	}
	t.Logf("post failing on line %d rolled back after %s", massLines, time.Since(start).Round(time.Millisecond))
	if _, err := admin.Exec(context.Background(), `drop trigger fail_last_journal_line on condition_record`); err != nil {
		t.Fatal(err)
	}
	after, events = massState(t, ctx, pool, admin)
	sameMassState(t, "after the failed post", seeded, after)
	if events != events0 {
		t.Fatalf("outbox grew from %d to %d although the post rolled back", events0, events)
	}
	if j, _ := s.GetJournal(ctx, good.ID); j.Status != domain.JournalValidated {
		t.Fatalf("journal = %s after the failed post; want VALIDATED", j.Status)
	}

	// 4. The post applies every line.
	start = time.Now()
	posted, err := s.PostJournal(ctx, good.ID, "post-100k")
	if err != nil || posted.Status != domain.JournalPosted {
		t.Fatalf("post = %s, %v", posted.Status, err)
	}
	postTook := time.Since(start)
	raised, events := massState(t, ctx, pool, admin)
	for i := 0; i < massLines; i++ {
		k := fmt.Sprintf("M%06d", i)
		want := decimal.New(massCents(i)*1075, -5) // cents x 1.075, exact at 5 decimals
		if r := raised[k]; r.version != 2 || r.status != domain.StatusActive || !r.amount.Equal(want) {
			t.Fatalf("after post %s = %s v%d %s; want ACTIVE v2 %s", k, r.status, r.version, r.amount, want)
		}
	}
	if events != events0+massLines+1 {
		t.Fatalf("post wrote %d events, want %d activated + 1 journal.posted", events-events0, massLines)
	}

	// 5. The reversal restores every prior amount.
	start = time.Now()
	rev, err := s.ReverseJournal(ctx, good.ID, "reverse-100k")
	if err != nil || rev.ReversesID != good.ID {
		t.Fatalf("reverse = %+v, %v", rev.Journal.ID, err)
	}
	revTook := time.Since(start)
	restored, _ := massState(t, ctx, pool, admin)
	for k, w := range seeded {
		if r := restored[k]; r.version != 3 || r.status != domain.StatusActive || !r.amount.Equal(w.amount) {
			t.Fatalf("after reversal %s = %s v%d %s; want ACTIVE v3 %s", k, r.status, r.version, r.amount, w.amount)
		}
	}
	if j, _ := s.GetJournal(ctx, good.ID); j.Status != domain.JournalReversed || j.ReversedByID != rev.ID {
		t.Fatalf("original journal = %s reversed by %q; want REVERSED by %s", j.Status, j.ReversedByID, rev.ID)
	}
	t.Logf("%d-line post %s, reversal %s (FR-26.9 budget: 120 s each)", massLines, postTook.Round(time.Millisecond), revTook.Round(time.Millisecond))
	if postTook > 120*time.Second || revTook > 120*time.Second {
		t.Errorf("post %s / reversal %s exceed the FR-26.9 budget of 120 s for 100k lines", postTook, revTook)
	}
}

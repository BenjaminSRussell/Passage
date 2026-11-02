package store_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func blockingOf(ctx context.Context, product, key string) store.BlockingInput {
	return store.BlockingInput{IdempotencyKey: key, LegalEntityID: legalEntity(ctx), ProductID: product, Dims: dims(), Qty: dec("1"),
		Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
}

// BR-16.8 covers every movement path, not only journals: a blocking (which
// moves stock between dimension sets) and a status step that changes physical
// stock flag a counted line on a plan without a freeze.
func TestBlockingAndStatusStepFlagCountedLines(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", dims()))
	plan := controlPlan(t, s, ctx, func(p *domain.CountPlan) {})

	sheet := generate(t, s, ctx, plan)
	if _, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("20")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateBlocking(ctx, blockingOf(ctx, product, ids.New())); err != nil {
		t.Fatalf("blocking on a plan without a freeze: %v", err)
	}
	if got, _ := s.GetCountSheet(ctx, sheet.ID); !lineOf(got, product).RecountRequested || lineOf(got, product).RecountSource != domain.RecountByMovement {
		t.Fatalf("blocking did not flag the counted line: %+v", lineOf(got, product))
	}

	again := generate(t, s, ctx, plan)
	l := lineOf(again, product)
	if _, err := s.EnterCount(ctx, again.ID, l.ID, again.Version, dec("19")); err != nil {
		t.Fatal(err)
	}
	created, err := register(ctx, s, "PURCH_LINE", product, "4", "2", domain.StatusOrdered, dims())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetCountSheet(ctx, again.ID); lineOf(got, product).RecountRequested {
		t.Fatalf("an ordered receipt does not move counted stock: %+v", lineOf(got, product))
	}
	if _, err := step(ctx, s, created.TransactionID, domain.CommandStep, domain.StatusOrdered, domain.StatusReceived); err != nil {
		t.Fatalf("receive step: %v", err)
	}
	if got, _ := s.GetCountSheet(ctx, again.ID); !lineOf(got, product).RecountRequested {
		t.Fatalf("a receipt onto the counted row did not flag it: %+v", lineOf(got, product))
	}
}

// A movement that already holds its on-hand row locks never waits for a count
// sheet lock (that order would deadlock against a count posting, which locks
// its sheet and then the rows): it answers 409 INV-CNT-009 at once. A journal
// posting takes sheet locks before any row lock, so it waits and then flags.
func TestMovementNeverWaitsForASheetLockWhileHoldingRows(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", dims()))
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))
	if _, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("20")); err != nil {
		t.Fatal(err)
	}

	hold, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(context.Background(), `select 1 from count_sheet where id = $1 for update`, sheet.ID); err != nil {
		t.Fatal(err)
	}
	quick, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err := s.CreateBlocking(quick, blockingOf(ctx, product, ids.New())); problemCode(err) != domain.CodeCountBusy {
		t.Fatalf("blocking while the sheet is locked: %v, want %s", err, domain.CodeCountBusy)
	}

	j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-1", "2", dims()))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.PostJournal(ctx, j.ID, j.Version, postParams())
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the journal posting did not wait for the sheet lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := hold.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("journal after the sheet lock is released: %v", err)
	}
	if got, _ := s.GetCountSheet(ctx, sheet.ID); !lineOf(got, product).RecountRequested {
		t.Fatalf("the journal did not flag the counted line: %+v", lineOf(got, product))
	}
}

// The deadlock BR-16.8 must not introduce: a blocking locks its two rows in
// key order and then the sheets it flags, while a count posting locks its
// sheet and then the rows. The blocking holds the source row and waits for the
// blocked row (held here), the posting holds its sheet and waits for the
// source row; once the blocked row is free the blocking reaches the flag. It
// must refuse at once (409 INV-CNT-009) rather than wait for the posting's
// sheet, so the posting completes and nothing fails with a database error.
func TestCountPostingAndBlockingOfTheSameRowDoNotDeadlock(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	// A location whose source row sorts before its blocked row, so the blocking
	// locks the source first.
	var d domain.Dimensions
	for i := 0; d == nil; i++ {
		c := dims(domain.DimLocation, fmt.Sprintf("L%d", i))
		n, _ := c.Normalize()
		b, _ := domain.BlockedDims(n, domain.StockBlocked).Normalize()
		if bytes.Compare(n.Hash(), b.Hash()) < 0 {
			d = c
		}
	}
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "2", d))
	in := blockingOf(ctx, product, ids.New())
	in.Dims = d
	first, _, err := s.CreateBlocking(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReleaseBlocking(ctx, first.ID, first.Version, domain.DispositionRelease, time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), postParams()); err != nil {
		t.Fatal(err)
	}
	sheet := generate(t, s, ctx, controlPlan(t, s, ctx, func(p *domain.CountPlan) {}))
	counted, err := s.EnterCount(ctx, sheet.ID, sheet.Lines[0].ID, sheet.Version, dec("19"))
	if err != nil {
		t.Fatal(err)
	}

	hold, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	bn, _ := domain.BlockedDims(d, domain.StockBlocked).Normalize()
	if tag, err := hold.Exec(context.Background(), `select 1 from on_hand where product_id = $1 and dimension_set_hash = $2 for update`, product, bn.Hash()); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("lock the blocked row: %v %v", err, tag)
	}
	var blockErr, postErr error
	var wg sync.WaitGroup
	wg.Add(2)
	in.IdempotencyKey = ids.New()
	go func() { defer wg.Done(); _, _, blockErr = s.CreateBlocking(ctx, in) }()
	time.Sleep(300 * time.Millisecond)
	go func() {
		defer wg.Done()
		_, postErr = s.PostCountSheet(approverCtx(ctx), sheet.ID, counted.Version, true, postParams())
	}()
	time.Sleep(300 * time.Millisecond)
	if err := hold.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	var pg *pgconn.PgError
	if errors.As(blockErr, &pg) || errors.As(postErr, &pg) {
		t.Fatalf("database error: blocking %v, posting %v", blockErr, postErr)
	}
	if problemCode(blockErr) != domain.CodeCountBusy || postErr != nil {
		t.Fatalf("blocking %v (want %s), posting %v (want success)", blockErr, domain.CodeCountBusy, postErr)
	}
}

package eventing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestPgInboxAppliesEffectOnceAndRollsBackFailures(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA})
	if _, err := pool.Exec(ctx, `create table consumer_inbox (tenant_id uuid not null, consumer text not null, event_id text not null, processed_at timestamptz not null default now(), primary key (tenant_id, consumer, event_id))`); err != nil {
		t.Fatal(err)
	}
	inbox := eventing.NewPgInbox(pool)

	if _, err := inbox.Once(context.Background(), "c", "e1", func(pgx.Tx) error { return nil }); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("no tenant err = %v", err)
	}
	failure := errors.New("effect failed")
	if applied, err := inbox.Once(ctx, "c", "e1", func(pgx.Tx) error { return failure }); applied || !errors.Is(err, failure) {
		t.Fatalf("failing effect = %v, %v", applied, err)
	}
	runs := 0
	for want := range 2 {
		applied, err := inbox.Once(ctx, "c", "e1", func(pgx.Tx) error { runs++; return nil })
		if err != nil || applied != (want == 0) {
			t.Fatalf("delivery %d = %v, %v", want, applied, err)
		}
	}
	if runs != 1 {
		t.Fatalf("effect ran %d times, want 1 (the failed attempt left no inbox row)", runs)
	}
}

func TestPgInboxSeenAndRecordAreTenantScopedAndIdempotent(t *testing.T) {
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA})
	if _, err := pool.Exec(ctx, `create table consumer_inbox (tenant_id uuid not null, consumer text not null, event_id text not null, processed_at timestamptz not null default now(), primary key (tenant_id, consumer, event_id))`); err != nil {
		t.Fatal(err)
	}
	inbox := eventing.NewPgInbox(pool)
	if seen, err := inbox.Seen(ctx, "c", "e1"); err != nil || seen {
		t.Fatalf("before record = %v, %v", seen, err)
	}
	for range 2 {
		if err := inbox.Record(ctx, "c", "e1"); err != nil {
			t.Fatal(err)
		}
	}
	if seen, err := inbox.Seen(ctx, "c", "e1"); err != nil || !seen {
		t.Fatalf("after record = %v, %v", seen, err)
	}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: "22222222-2222-4222-8222-222222222222"})
	if seen, err := inbox.Seen(other, "c", "e1"); err != nil || seen {
		t.Fatalf("other tenant saw the row: %v, %v", seen, err)
	}
	if _, err := inbox.Seen(context.Background(), "c", "e1"); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("no tenant err = %v", err)
	}
}

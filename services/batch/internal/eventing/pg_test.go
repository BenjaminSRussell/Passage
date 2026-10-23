package eventing_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/batch/internal/eventing"
	"github.com/BenjaminSRussell/passage/services/batch/migrations"
)

type recordingPublisher struct {
	messages  []eventing.Message
	afterEach func()
}

func (p *recordingPublisher) Publish(_ context.Context, msg eventing.Message) error {
	p.messages = append(p.messages, msg)
	if p.afterEach != nil {
		p.afterEach()
	}
	return nil
}

func TestPg_CrashBetweenPublishAndAckDeliversExactlyOnce(t *testing.T) {
	pool := testkit.DB(t)
	tenantID := testkit.TenantID(1)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: testkit.TenantID(9)})
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	evt, err := events.New("dev.passage.platform.legalentity.created.v1", "/passage/platform/test", "agg-1", tenantID, map[string]string{"id": "agg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Enqueue(ctx, tx, "legalentity", "agg-1", evt)
	}); err != nil {
		t.Fatal(err)
	}

	crashCtx, crash := context.WithCancel(ctx)
	broker := &recordingPublisher{afterEach: crash}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if _, err := eventing.NewRelay(eventing.NewPgSource(pool), broker, log).Drain(crashCtx, 10); err == nil {
		t.Fatal("expected the killed drain to fail")
	}
	var remaining int
	if err := pool.QueryRow(ctx, `select count(*) from outbox`).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("killed drain must leave the row in the outbox, have %d, %v", remaining, err)
	}

	if n, err := eventing.NewRelay(eventing.NewPgSource(pool), broker, log).Drain(ctx, 10); err != nil || n != 1 {
		t.Fatalf("restart drain = %d, %v; want 1, nil", n, err)
	}
	if len(broker.messages) != 2 {
		t.Fatalf("broker must see the event twice, saw %d", len(broker.messages))
	}

	inbox := eventing.NewPgInbox(pool)
	applied := 0
	for _, msg := range broker.messages {
		if _, err := inbox.Once(ctx, "test-consumer", msg.ID, func(pgx.Tx) error { applied++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if applied != 1 {
		t.Fatalf("effect applied %d times, want 1", applied)
	}
}

func TestPgInbox_Once(t *testing.T) {
	pool := testkit.DB(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	inbox := eventing.NewPgInbox(pool)
	noop := func(pgx.Tx) error { return nil }

	if _, err := inbox.Once(ctx, "", "e1", noop); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("empty consumer err = %v", err)
	}
	if _, err := inbox.Once(ctx, "c", "e1", nil); !errors.Is(err, eventing.ErrInvalidArgument) {
		t.Fatalf("nil effect err = %v", err)
	}

	failure := errors.New("effect failed")
	if applied, err := inbox.Once(ctx, "c", "e1", func(pgx.Tx) error { return failure }); applied || !errors.Is(err, failure) {
		t.Fatalf("failing effect = %v, %v", applied, err)
	}
	var rows int
	if err := pool.QueryRow(ctx, `select count(*) from consumer_inbox`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed effect must leave no inbox row, have %d, %v", rows, err)
	}

	runs := 0
	for want := range 2 {
		applied, err := inbox.Once(ctx, "c", "e1", func(pgx.Tx) error { runs++; return nil })
		if err != nil || applied != (want == 0) {
			t.Fatalf("attempt %d = %v, %v", want, applied, err)
		}
	}
	if runs != 1 {
		t.Fatalf("effect ran %d times", runs)
	}
}

package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

// The consumers of this service run with Idempotent set, so eventing.NewPgInbox needs consumer_inbox, as the app role, under RLS.
func TestConsumerInboxAppliesAnEventOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1)})
	inbox := eventing.NewPgInbox(app)
	applied := 0
	for range 2 {
		if _, err := inbox.Once(ctx, "test-consumer", "evt-1", func(pgx.Tx) error { applied++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if applied != 1 {
		t.Fatalf("applied %d times, want once", applied)
	}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(2)})
	if seen, err := inbox.Seen(other, "test-consumer", "evt-1"); err != nil || seen {
		t.Fatalf("another tenant sees the row: %v %v", seen, err)
	}
}

package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

// The platform inbox runs as passage_app: the consumer records an event id once
// per tenant and consumer, and another tenant never sees it (RLS).
func TestConsumerInbox_AppRoleRecordsEventsPerTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	inbox := eventing.NewPgInbox(app)
	if seen, err := inbox.Seen(ctx, "finreport-ledger", "evt-1"); err != nil || seen {
		t.Fatalf("fresh inbox: seen %v, %v", seen, err)
	}
	if err := inbox.Record(ctx, "finreport-ledger", "evt-1"); err != nil {
		t.Fatalf("record as passage_app: %v", err)
	}
	if err := inbox.Record(ctx, "finreport-ledger", "evt-1"); err != nil {
		t.Fatalf("record twice: %v", err)
	}
	if seen, err := inbox.Seen(ctx, "finreport-ledger", "evt-1"); err != nil || !seen {
		t.Fatalf("seen after record: %v, %v", seen, err)
	}
	other := testkit.Ctx(testkit.TenantID(2))
	if seen, err := inbox.Seen(other, "finreport-ledger", "evt-1"); err != nil || seen {
		t.Fatalf("other tenant sees the event: %v, %v", seen, err)
	}
}

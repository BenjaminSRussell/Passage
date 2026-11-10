package store

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// The application role writes and reads UX events but cannot change or remove them.
func TestUXEvents_AppRoleInsertsAndReadsButCannotRewrite(t *testing.T) {
	app := appPool(t)
	st := New(app)
	ctx := ctxFor(1, "")
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	events := []domain.UXEvent{
		{SessionID: "sess-1234abcd", Release: "r1", Type: domain.UXFunnelStep, Flow: "onboarding", Step: "legal_entity_created", OccurredAt: &at},
		{SessionID: "sess-1234abcd", Release: "r1", Type: domain.UXFunnelStep, Flow: "onboarding", Step: "first_transaction", OccurredAt: &at},
	}
	if err := st.AddUXEvents(ctx, events); err != nil {
		t.Fatalf("insert as passage_app: %v", err)
	}
	f, err := st.GetFunnel(ctx, "onboarding", "", at, at)
	if err != nil || f.Steps[0].Sessions != 1 || f.Steps[5].Sessions != 1 {
		t.Fatalf("funnel as passage_app: %+v %v", f, err)
	}
	for _, stmt := range []string{`update ux_event set release = 'x'`, `delete from ux_event`} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
}

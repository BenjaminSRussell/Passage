package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/edi/migrations"
)

func TestGrants_AppRoleLocksAndRewritesButPartnerKeysAreWriteOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app, 30)
	ctx := testkit.Ctx(testkit.TenantID(1), "edi.operate")
	p, _, err := st.CreatePartner(ctx, "k", "h", profile("GRANTS"))
	if err != nil {
		t.Fatal(err)
	}
	failed := msg("850", "0001", "FAILED", "EDI-0105: no item cross-reference")
	ic := receive(t, st, ctx, p.ID, "000000001", failed)
	plan := func(_ string, in []Message) ([]Message, error) {
		return []Message{{ID: in[0].ID, Status: "MAPPED", Canonical: []byte(`{}`)}}, nil
	}
	res, _, err := st.ReprocessInterchange(ctx, "rp", "h", ic.InterchangeID, false, plan)
	if err != nil || res.Changed != 1 {
		t.Fatalf("reprocess locks interchange and message FOR UPDATE as passage_app: %+v, %v", res, err)
	}
	if _, _, err := st.CreateOutbound(ctx, "out", "h", outbound(p.ID, "856")); err != nil {
		t.Fatalf("control number upsert as passage_app: %v", err)
	}
	for _, stmt := range []string{
		// The partner update writes only its own columns (0009).
		`update trading_partner set environment = environment`,
		`update trading_partner set tenant_id = tenant_id`,
		`update trading_partner set created_at = created_at`,
		`delete from trading_partner`,
		`select count(*) from partner_entity_index`,
		`delete from message`,
		`update outbox set type = type`,
		`select count(*) from outbox`,
	} {
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

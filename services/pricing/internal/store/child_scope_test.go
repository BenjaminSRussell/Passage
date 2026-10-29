package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestChildTablesFollowParentLegalEntityScope(t *testing.T) {
	admin, app := adminAndAppPools(t)
	ctx := context.Background()
	cases := []struct {
		child, parentColumn string
		parent, seedChild   string
		insertChild         string
	}{
		{
			child: "price_journal_line", parentColumn: "journal_id",
			parent:      `insert into price_journal (tenant_id, id, legal_entity_id, name, status, line_count) values ($1, $2, $3, 'j', 'OPEN', 0)`,
			seedChild:   `insert into price_journal_line (tenant_id, journal_id, line_no, op, record_id, record) values ($1, $2, 1, 'CREATE', gen_random_uuid(), '{}')`,
			insertChild: `insert into price_journal_line (tenant_id, journal_id, line_no, op, record_id, record) values ($1, $2, 2, 'CREATE', gen_random_uuid(), '{}')`,
		},
		{
			child: "rebate_period", parentColumn: "agreement_id",
			parent: `insert into rebate_agreement (tenant_id, id, legal_entity_id, name, customer_id, basis, period, tiers, currency, valid_from, valid_to)
				values ($1, $2, $3, 'r', gen_random_uuid(), 'NET_AMOUNT', 'MONTH', '[]', 'USD', '2027-01-01', '2027-12-31')`,
			seedChild:   `insert into rebate_period (tenant_id, agreement_id, period_key) values ($1, $2, '2027-01')`,
			insertChild: `insert into rebate_period (tenant_id, agreement_id, period_key) values ($1, $2, '2027-02')`,
		},
		{
			child: "sales_group_rep", parentColumn: "sales_group_id",
			parent:      `insert into sales_group (tenant_id, id, legal_entity_id, name) values ($1, $2, $3, 'g')`,
			seedChild:   `insert into sales_group_rep (tenant_id, sales_group_id, seq, rep_id, share_pct) values ($1, $2, 1, 'R1', 100)`,
			insertChild: `insert into sales_group_rep (tenant_id, sales_group_id, seq, rep_id, share_pct) values ($1, $2, 2, 'R2', 10)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.child, func(t *testing.T) {
			entityA, entityB := ids.New(), ids.New()
			parentID := ids.New()
			tenantID := testkit.TenantID(1)
			if _, err := admin.Exec(ctx, tc.parent, tenantID, parentID, entityA); err != nil {
				t.Fatal(err)
			}
			if _, err := admin.Exec(ctx, tc.seedChild, tenantID, parentID); err != nil {
				t.Fatal(err)
			}
			count := func(le string) (n int) {
				err := dbx.WithTenantTx(principal(1, le), app, func(tx pgx.Tx) error {
					return tx.QueryRow(ctx, "select count(*) from "+tc.child+" where "+tc.parentColumn+" = $1", parentID).Scan(&n)
				})
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			if count(entityA) != 1 {
				t.Error("entity A cannot read its own child rows")
			}
			if n := count(entityB); n != 0 {
				t.Errorf("entity B reads %d child rows under entity A's parent", n)
			}
			err := dbx.WithTenantTx(principal(1, entityB), app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tc.insertChild, tenantID, parentID)
				return err
			})
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Errorf("entity B insert under entity A's parent: got %v, want SQLSTATE 42501", err)
			}
			err = dbx.WithTenantTx(principal(1, entityA), app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tc.insertChild, tenantID, parentID)
				return err
			})
			if err != nil {
				t.Errorf("entity A insert under its own parent: %v", err)
			}
		})
	}
}

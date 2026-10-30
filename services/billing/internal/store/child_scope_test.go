package store

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

func scopedCtx(le string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "u", Duties: testkit.ScopeDuties(le)})
}

func TestChildTablesFollowParentLegalEntityScope(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	entityA, entityB := ids.New(), ids.New()
	inv, err := New(app).CreateProposal(scopedCtx(entityA), newProposal(t, entityA))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `insert into invoice_installment (tenant_id, invoice_id, installment_no, percent, offset_days, amount, due_date)
		values ($1, $2, 1, 100, 30, 119, '2027-04-09')`, testkit.TenantID(1), inv.ID); err != nil {
		t.Fatal(err)
	}

	tables := []struct {
		table  string
		insert string
	}{
		{"invoice_line", `insert into invoice_line (tenant_id, invoice_id, line_no, description, qty, unit_price, disc_pct, line_net, tax_amount, revenue_account, tax_code)
			values ($1, $2, 2, 'x', 1, 1, 0, 1, 0, '4000', 'DE19')`},
		{"invoice_installment", `insert into invoice_installment (tenant_id, invoice_id, installment_no, percent, offset_days, amount, due_date)
			values ($1, $2, 2, 10, 30, 1, '2027-04-09')`},
	}
	for _, tc := range tables {
		t.Run(tc.table, func(t *testing.T) {
			count := func(le string) (n int) {
				err := dbx.WithTenantTx(scopedCtx(le), app, func(tx pgx.Tx) error {
					return tx.QueryRow(ctx, "select count(*) from "+tc.table+" where invoice_id = $1", inv.ID).Scan(&n)
				})
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			if count(entityA) == 0 {
				t.Error("entity A cannot read its own child rows")
			}
			if n := count(entityB); n != 0 {
				t.Errorf("entity B reads %d child rows under entity A's invoice", n)
			}
			err := dbx.WithTenantTx(scopedCtx(entityB), app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tc.insert, testkit.TenantID(1), inv.ID)
				return err
			})
			requireInsufficientPrivilege(t, err, "entity B insert under entity A's invoice must violate the policy")
			err = dbx.WithTenantTx(scopedCtx(entityA), app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, tc.insert, testkit.TenantID(1), inv.ID)
				return err
			})
			if err != nil {
				t.Errorf("entity A insert under its own invoice: %v", err)
			}
		})
	}
}

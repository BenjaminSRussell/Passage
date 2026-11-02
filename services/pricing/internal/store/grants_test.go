package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

func TestGrants_LockedTablesAllowUpdateForAppRole(t *testing.T) {
	admin, _ := adminAndAppPools(t)
	ctx := context.Background()
	for _, table := range []string{"condition_record", "trade_fund", "pricing_procedure", "campaign", "rebate_agreement", "rebate_period", "price_journal", "vendor_rebate_claim", "commodity_basis", "commodity_calculation"} {
		var ok bool
		if err := admin.QueryRow(ctx, `select has_table_privilege('passage_app', $1, 'UPDATE')`, table).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Errorf("passage_app lacks UPDATE on %s, so FOR UPDATE / FOR NO KEY UPDATE fails with permission denied", table)
		}
	}
}

func TestGrants_AppRoleCannotRewriteAppendOnlyTables(t *testing.T) {
	_, app := adminAndAppPools(t)
	ctx := principal(1, "")
	for _, stmt := range []string{
		`update rebate_accrual set accrued_amount = accrued_amount`,
		`delete from commission_transaction`,
		`select count(*) from outbox`,
		`update outbox set type = type`,
		`update commodity_index_value set value = value`,
		`delete from commodity_index_value`,
		`update commodity_run set price = price`,
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

func TestGrants_RebateAccrualLocksAgreementAndPeriodAsAppRole(t *testing.T) {
	s := store.New(appPool(t))
	ctx := principal(1, "")
	a, err := s.CreateAgreement(ctx, agreement(tier("0", "2")), "grants-1")
	if err != nil {
		t.Fatal(err)
	}
	acc, created, err := s.AccrueInvoiceLine(ctx, invoice(a.ID, "INV-G/1", "2026-10-05", "100.00"))
	if err != nil || !created || !acc.Accrued.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("accrual = %+v created=%v, %v; want 2.00", acc, created, err)
	}
}

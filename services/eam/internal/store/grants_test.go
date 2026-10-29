package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

func TestGrants_AppRoleNumbersLocksAndAppendOnly(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(app, func() time.Time { return clock })
	ctx := testkit.Ctx(testkit.TenantID(1))
	a, err := s.CreateAsset(ctx, Asset{Name: "Pump", AssetType: "PUMP", SiteID: testkit.TenantID(9), Criticality: 3}, "g-asset")
	if err != nil {
		t.Fatalf("asset number sequence as passage_app: %v", err)
	}
	if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
		t.Fatal(err)
	}
	wo, err := s.CreateWorkOrder(ctx, WorkOrder{AssetID: a.ID, Type: "CORRECTIVE", Status: "CREATED", Priority: 3, Currency: "USD"}, "g-wo")
	if err != nil {
		t.Fatalf("work order number sequence as passage_app: %v", err)
	}
	if _, err := s.RecordLabor(ctx, wo.ID, decimal.NewFromInt(2), decimal.NewFromInt(10), nil, "g-labor"); err != nil {
		t.Fatalf("labor locks and updates the work order as passage_app: %v", err)
	}
	for _, stmt := range []string{
		`update work_order_labor set hours = hours`,
		`delete from work_order_transition`,
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

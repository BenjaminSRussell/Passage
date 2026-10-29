package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

func scopedCtx(tenantID, scope string) context.Context {
	duties := map[string]struct{}{}
	if scope == "" {
		duties["org.switcher.all_scope"] = struct{}{}
	}
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: scope, UserID: "u", Duties: duties})
}

func countAs(t *testing.T, app *pgxpool.Pool, ctx context.Context, query string, args ...any) (n int) {
	t.Helper()
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, query, args...).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func requireRLSViolation(t *testing.T, err error, msg string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("%s: %v, want insufficient_privilege 42501", msg, err)
	}
}

func TestLegalEntityScope_LoadsBillsVisitsAndGoodsInTransit(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	s := store.New(app)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	all, otherTenant := scopedCtx(testkit.TenantID(1), ""), scopedCtx(testkit.TenantID(2), "")
	carrier, site := ids.New(), ids.New()

	loads, err := s.BuildLoads(ctxA, built(t, shipments(3, site, "1600")), store.Idem{})
	if err != nil {
		t.Fatalf("entity A loads: %v", err)
	}
	bill, err := s.CreateFreightBill(ctxA, newBill(carrier, "100.00"), store.Idem{})
	if err != nil {
		t.Fatalf("entity A freight bill: %v", err)
	}
	if _, _, err := s.RegisterCarrierInvoice(ctxA, newInvoice(carrier, "INV-A", &bill.ID, "100.00")); err != nil {
		t.Fatalf("entity A matched invoice: %v", err)
	}
	if _, _, err := s.RegisterCarrierInvoice(ctxA, newInvoice(carrier, "INV-A2", nil, "10.00")); err != nil {
		t.Fatalf("entity A unmatched invoice: %v", err)
	}
	if _, err := s.CheckIn(ctxA, checkIn(t, site, domain.DirectionInbound, "TR-1", "555"), store.Idem{}); err != nil {
		t.Fatalf("entity A check-in: %v", err)
	}
	voyage, err := s.CreateVoyage(all, newVoyage(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGoodsInTransit(ctxA, voyage.ID, "FOB", site, []int{1}, store.Idem{}); err != nil {
		t.Fatalf("entity A goods in transit: %v", err)
	}
	for _, stmt := range []string{
		`insert into dg_check (tenant_id, id, load_id, regulation, result, adr_points, findings, shipping_papers, checked_by, checked_at)
			select tenant_id, gen_random_uuid(), id, 'ADR', 'PASS', 0, '[]', '{}', 'u', now() from load`,
		`insert into screening_hold (tenant_id, id, load_id, status, parties, created_by) select tenant_id, gen_random_uuid(), id, 'OPEN', '[]', 'u' from load limit 1`,
		`insert into freight_bill_decision (tenant_id, id, freight_bill_id, action, from_status, to_status, decided_by, decided_at)
			select tenant_id, gen_random_uuid(), id, 'DISPUTE', 'VARIANCE', 'DISPUTED', 'u', now() from freight_bill`,
	} {
		if _, err := admin.Exec(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}

	tables := []string{"load", "load_shipment", "dg_check", "screening_hold", "freight_bill", "carrier_invoice", "freight_bill_decision",
		"gate_visit", "gate_event", "goods_in_transit_order", "goods_in_transit_line"}
	for _, table := range tables {
		t.Run(table, func(t *testing.T) {
			q := "select count(*) from " + table
			own := countAs(t, app, ctxA, q)
			if own == 0 {
				t.Fatal("entity A cannot read its own rows")
			}
			if n := countAs(t, app, all, q); n != own {
				t.Errorf("all-entity caller sees %d rows, want %d", n, own)
			}
			if n := countAs(t, app, ctxB, q); n != 0 {
				t.Errorf("entity B reads %d of entity A's rows", n)
			}
			if n := countAs(t, app, otherTenant, q); n != 0 {
				t.Errorf("another tenant reads %d rows", n)
			}
		})
	}

	t.Run("entity B cannot read the aggregates through the store", func(t *testing.T) {
		if _, err := s.GetLoad(ctxB, loads[0].ID); err == nil {
			t.Error("entity B fetched entity A's load")
		}
		if _, err := s.GetFreightBill(ctxB, bill.ID); err == nil {
			t.Error("entity B fetched entity A's freight bill")
		}
		if got, err := s.ListLoads(ctxB, store.LoadFilter{}); err != nil || len(got) != 0 {
			t.Errorf("entity B lists %d loads (%v)", len(got), err)
		}
		if _, _, err := s.RegisterCarrierInvoice(ctxB, newInvoice(carrier, "INV-B", &bill.ID, "100.00")); err == nil {
			t.Error("entity B matched an invoice to entity A's freight bill")
		}
	})

	t.Run("line inserts follow the parent's entity", func(t *testing.T) {
		insert := `insert into load_shipment (tenant_id, load_id, shipment_id, position) values ($1, $2, $3, 99)`
		run := func(ctx context.Context) error {
			return dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, insert, testkit.TenantID(1), loads[0].ID, ids.New())
				return err
			})
		}
		requireRLSViolation(t, run(ctxB), "entity B load_shipment under entity A's load")
		if err := run(ctxA); err != nil {
			t.Errorf("entity A load_shipment under its own load: %v", err)
		}
	})

	t.Run("each entity owns what it creates", func(t *testing.T) {
		billB, err := s.CreateFreightBill(ctxB, newBill(carrier, "50.00"), store.Idem{})
		if err != nil {
			t.Fatalf("entity B freight bill: %v", err)
		}
		if _, err := s.GetFreightBill(ctxA, billB.ID); err == nil {
			t.Error("entity A fetched entity B's freight bill")
		}
		if _, err := s.GetFreightBill(all, billB.ID); err != nil {
			t.Errorf("all-entity caller cannot fetch entity B's freight bill: %v", err)
		}
	})
}

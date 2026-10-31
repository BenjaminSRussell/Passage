package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
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
	bill, err := s.CreateFreightBill(ctxA, billOn(t, s, ctxA, carrier, "100.00"), store.Idem{})
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
	voyage, err := s.CreateVoyage(ctxA, newVoyage(), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateGoodsInTransit(ctxA, voyage.ID, "FOB", site, "", []int{1}, store.Idem{}); err != nil {
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
		"gate_visit", "gate_event", "goods_in_transit_order", "goods_in_transit_line", "voyage"}
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
		billB, err := s.CreateFreightBill(ctxB, billOn(t, s, ctxB, carrier, "50.00"), store.Idem{})
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

	t.Run("another entity cannot open an order on a voyage it cannot see", func(t *testing.T) {
		_, err := s.CreateGoodsInTransit(ctxB, voyage.ID, "FOB", site, "", []int{1}, store.Idem{})
		if got := problemCode(t, err); got != domain.CodeNotFound {
			t.Fatalf("entity B order on entity A's voyage = %v, want %s", err, domain.CodeNotFound)
		}
	})

	t.Run("a voyage line already on an order is a conflict inside the entity", func(t *testing.T) {
		_, err := s.CreateGoodsInTransit(ctxA, voyage.ID, "FOB", site, "", []int{1}, store.Idem{})
		if got := problemCode(t, err); got != domain.CodeGITState || problemStatus(err) != http.StatusConflict {
			t.Fatalf("second order on a line held by the entity's own order = %v, want 409 %s", err, domain.CodeGITState)
		}
	})
}

func problemStatus(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

func TestLegalEntityScope_ChildrenFollowTheirLoad(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	all := scopedCtx(testkit.TenantID(1), "")
	loadA := seedLoad(t, s, ctxA)
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := admin.Exec(context.Background(), stmt, args...); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("another entity cannot tender a load held by screening", func(t *testing.T) {
		exec(`insert into screening_hold (tenant_id, id, load_id, status, parties, created_by) values ($1, gen_random_uuid(), $2, 'OPEN', '[]', 'u')`, testkit.TenantID(1), loadA)
		_, err := s.CreateTender(ctxB, sentTender(loadA, time.Hour, ids.New()), store.Idem{}, t0)
		if got := problemCode(t, err); got != domain.CodeNotFound {
			t.Fatalf("entity B tender = %v, want %s", err, domain.CodeNotFound)
		}
		if _, err := s.CreateTender(ctxA, sentTender(loadA, time.Hour, ids.New()), store.Idem{}, t0); problemCode(t, err) != domain.CodeScreeningHold {
			t.Fatalf("entity A tender = %v, want the hold to block it", err)
		}
	})

	t.Run("a scoped caller cannot tender a load without an entity", func(t *testing.T) {
		legacy := seedLoad(t, s, ctxA)
		exec(`update load set legal_entity_id = null where id = $1`, legacy)
		if _, err := s.CreateTender(ctxA, sentTender(legacy, time.Hour, ids.New()), store.Idem{}, t0); problemCode(t, err) != domain.CodeNotFound {
			t.Fatalf("scoped tender of an unassigned load = %v", err)
		}
		if _, err := s.CreateTender(all, sentTender(legacy, time.Hour, ids.New()), store.Idem{}, t0); err != nil {
			t.Fatalf("all-entity tender of an unassigned load: %v", err)
		}
	})

	t.Run("tenders and tracking follow the load", func(t *testing.T) {
		open := seedLoad(t, s, ctxA)
		if _, err := s.CreateTender(ctxA, sentTender(open, time.Hour, ids.New()), store.Idem{}, t0); err != nil {
			t.Fatal(err)
		}
		event := domain.TrackingEvent{LoadID: open, StopNo: 1, Code: "AF", OccurredAt: t0, Source: "API", Ref: "a-1"}
		if _, _, err := s.RecordTrackingEvent(ctxA, event); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"tender", "tracking_event"} {
			q := "select count(*) from " + table + " where load_id = $1"
			if countAs(t, app, ctxA, q, open) != 1 || countAs(t, app, all, q, open) != 1 || countAs(t, app, ctxB, q, open) != 0 {
				t.Errorf("%s visibility must be A=1 all=1 B=0", table)
			}
		}
		if list, err := s.ListTenders(ctxB, store.TenderFilter{}); err != nil || len(list) != 0 {
			t.Errorf("entity B lists %d tenders (%v)", len(list), err)
		}
		other := event
		other.Ref = "b-1"
		if _, _, err := s.RecordTrackingEvent(ctxB, other); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B tracking on entity A's load = %v", err)
		}
	})

	t.Run("a report key held by another entity's event is a conflict, not a 500", func(t *testing.T) {
		loadB := seedLoad(t, s, ctxB)
		held := domain.TrackingEvent{LoadID: loadA, StopNo: 1, Code: "AF", OccurredAt: t0, Source: "API", Ref: "shared-1"}
		if _, _, err := s.RecordTrackingEvent(ctxA, held); err != nil {
			t.Fatal(err)
		}
		clash := held
		clash.LoadID = loadB
		if _, _, err := s.RecordTrackingEvent(ctxB, clash); problemStatus(err) != http.StatusConflict {
			t.Errorf("entity B reusing entity A's source and ref = %v, want 409", err)
		}
	})

	t.Run("children inherit the parent's entity", func(t *testing.T) {
		if _, err := s.CreateFreightBill(ctxB, billOn(t, s, ctxA, ids.New(), "10.00"), store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B bill on entity A's load = %v", err)
		}
		bill := billOn(t, s, ctxA, ids.New(), "10.00")
		bill.LoadID = loadA
		created, err := s.CreateFreightBill(all, bill, store.Idem{})
		if err != nil {
			t.Fatalf("all-entity bill on entity A's load: %v", err)
		}
		if countAs(t, app, ctxA, `select count(*) from freight_bill where id = $1`, created.ID) != 1 {
			t.Error("the bill did not inherit entity A")
		}
		mismatch := bill
		mismatch.LegalEntityID = entityB
		if _, err := s.CreateFreightBill(all, mismatch, store.Idem{}); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("mismatched asserted entity = %v, want 422", err)
		}
		visit := checkIn(t, ids.New(), domain.DirectionInbound, "TR-9", "777")
		visit.LoadID = loadA
		saved, err := s.CheckIn(all, visit, store.Idem{})
		if err != nil {
			t.Fatalf("all-entity check-in on entity A's load: %v", err)
		}
		if countAs(t, app, ctxA, `select count(*) from gate_visit where id = $1`, saved.ID) != 1 {
			t.Error("the visit did not inherit entity A")
		}
		visit.TrailerNo = "TR-10"
		if _, err := s.CheckIn(ctxB, visit, store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B check-in on entity A's load = %v", err)
		}
	})

	t.Run("all-entity callers must name the owner of a new parent", func(t *testing.T) {
		if _, err := s.BuildLoads(all, built(t, shipments(1, ids.New(), "10")), store.Idem{}); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("all-entity load build without an entity = %v, want 422", err)
		}
		loads := built(t, shipments(1, ids.New(), "10"))
		loads[0].LegalEntityID = entityB
		created, err := s.BuildLoads(all, loads, store.Idem{})
		if err != nil {
			t.Fatalf("all-entity load build naming entity B: %v", err)
		}
		if countAs(t, app, ctxB, `select count(*) from load where id = $1`, created[0].ID) != 1 {
			t.Error("the load is not owned by the named entity")
		}
		if _, _, err := s.RegisterCarrierInvoice(all, newInvoice(ids.New(), "INV-ALL", nil, "5.00")); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("all-entity unmatched invoice without an entity = %v, want 422", err)
		}
		if _, err := s.CreateVoyage(all, newVoyage(), store.Idem{}); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("all-entity voyage without an entity = %v, want 422", err)
		}
	})
}

package store_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
	"github.com/BenjaminSRussell/passage/services/transport/migrations"
)

func TestLegalEntityScope_VoyagesAndRoutePlans(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	s := store.New(app)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	ctxAB := scopedCtx(testkit.TenantID(1), entityA+","+entityB)
	all, otherTenant := scopedCtx(testkit.TenantID(1), ""), scopedCtx(testkit.TenantID(2), "")
	site := ids.New()

	voyage, err := s.CreateVoyage(ctxA, newVoyage(), store.Idem{Key: "v-a", Fingerprint: "fp-a"})
	if err != nil {
		t.Fatalf("entity A voyage: %v", err)
	}
	if voyage.LegalEntityID != entityA {
		t.Fatalf("voyage entity = %q, want the caller's single entity %q", voyage.LegalEntityID, entityA)
	}
	req := routeRequest(100)
	hash, _ := req.Hash()
	sol := domain.RouteSolution{Routes: []domain.RouteSolutionRoute{{VehicleID: "V1", DistanceKm: 10.25,
		Stops: []domain.RouteSolutionStop{{StopID: "S1", ArrivalMin: 5}}, EndMin: 20}}, Dropped: []string{}}
	planA, created, err := s.SaveRoutePlan(ctxA, domain.NewRoutePlan(req, hash, sol))
	if err != nil || !created || planA.LegalEntityID != entityA {
		t.Fatalf("entity A route plan = %+v created %v, %v", planA, created, err)
	}

	for _, table := range []string{"voyage", "route_plan"} {
		t.Run(table, func(t *testing.T) {
			q := "select count(*) from " + table
			if countAs(t, app, ctxA, q) != 1 || countAs(t, app, all, q) != 1 || countAs(t, app, ctxAB, q) != 1 {
				t.Error("entity A, a caller scoped to A and B, and the all-entity caller must each see 1 row")
			}
			if n := countAs(t, app, ctxB, q); n != 0 {
				t.Errorf("entity B reads %d of entity A's rows", n)
			}
			if n := countAs(t, app, otherTenant, q); n != 0 {
				t.Errorf("another tenant reads %d rows", n)
			}
		})
	}

	t.Run("a voyage and its folios, cost lines and allocation are invisible to another entity", func(t *testing.T) {
		got, err := s.GetVoyage(ctxA, voyage.ID)
		if err != nil || len(got.Folios) != 1 || len(got.CostLines) != 2 {
			t.Fatalf("entity A voyage = %+v, %v", got, err)
		}
		if _, err := s.GetVoyage(ctxB, voyage.ID); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B get = %v", err)
		}
		if list, err := s.ListVoyages(ctxB, store.VoyageFilter{}); err != nil || len(list) != 0 {
			t.Errorf("entity B lists %d voyages (%v)", len(list), err)
		}
		if _, err := s.AllocateVoyage(ctxB, voyage.ID, t0); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B allocate = %v", err)
		}
		if _, err := s.AllocateVoyage(all, voyage.ID, t0); err != nil {
			t.Errorf("all-entity caller cannot allocate entity A's voyage: %v", err)
		}
	})

	t.Run("a goods-in-transit order takes its voyage's entity and gates hold across entities", func(t *testing.T) {
		fresh, err := s.CreateVoyage(ctxA, newVoyage(), store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateGoodsInTransit(ctxB, fresh.ID, "FOB", site, "", []int{1}, store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B order on entity A's voyage = %v", err)
		}
		if _, err := s.CreateGoodsInTransit(ctxAB, fresh.ID, "FOB", site, entityB, []int{1}, store.Idem{}); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("order asserting the wrong entity = %v, want 422", err)
		}
		order, err := s.CreateGoodsInTransit(all, fresh.ID, "FOB", site, "", []int{1}, store.Idem{})
		if err != nil {
			t.Fatalf("all-entity order on entity A's voyage: %v", err)
		}
		if countAs(t, app, ctxA, `select count(*) from goods_in_transit_order where id = $1`, order.ID) != 1 ||
			countAs(t, app, ctxB, `select count(*) from goods_in_transit_order where id = $1`, order.ID) != 0 {
			t.Error("the order did not inherit entity A")
		}
		if _, err := s.CreateGoodsInTransit(ctxA, fresh.ID, "FOB", site, "", []int{1}, store.Idem{}); problemStatus(err) != http.StatusConflict {
			t.Errorf("second order on a held line = %v, want 409", err)
		}
	})

	t.Run("a legacy voyage without an entity is visible only to the all-entity caller", func(t *testing.T) {
		var legacy string
		if err := admin.QueryRow(context.Background(), `insert into voyage (tenant_id, id, reference, currency, status, legs, containers, folios,
			cost_lines, receipt_lines, cost_total) values ($1, gen_random_uuid(), 'LEGACY', 'USD', 'ESTIMATED', '[]', '[]', '[]', '[]',
			'[{"line":1,"value":"1","weight":"1"}]', 0) returning id::text`, testkit.TenantID(1)).Scan(&legacy); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetVoyage(ctxA, legacy); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity A reads an unassigned voyage = %v", err)
		}
		if _, err := s.GetVoyage(all, legacy); err != nil {
			t.Errorf("all-entity caller cannot read an unassigned voyage: %v", err)
		}
		if _, err := s.CreateGoodsInTransit(ctxA, legacy, "FOB", site, "", []int{1}, store.Idem{}); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity A order on an unassigned voyage = %v", err)
		}
	})

	t.Run("an idempotency key held by another entity's voyage is refused, not a 500", func(t *testing.T) {
		_, err := s.CreateVoyage(ctxB, newVoyage(), store.Idem{Key: "v-a", Fingerprint: "fp-a"})
		if problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("entity B reusing entity A's key = %v, want 422", err)
		}
		again, err := s.CreateVoyage(ctxA, newVoyage(), store.Idem{Key: "v-a", Fingerprint: "fp-a"})
		if err != nil || again.ID != voyage.ID {
			t.Errorf("entity A replay = %+v, %v", again, err)
		}
	})

	t.Run("all-entity callers must name the owner and may name any entity", func(t *testing.T) {
		if _, err := s.CreateVoyage(all, newVoyage(), store.Idem{}); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("all-entity voyage without an entity = %v, want 422", err)
		}
		if _, _, err := s.SaveRoutePlan(all, domain.NewRoutePlan(req, hash, sol)); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("all-entity route plan without an entity = %v, want 422", err)
		}
		if _, _, err := s.FindRoutePlan(all, hash, ""); problemStatus(err) != http.StatusUnprocessableEntity {
			t.Errorf("all-entity route plan lookup without an entity = %v, want 422", err)
		}
		named := newVoyage()
		named.LegalEntityID = entityB
		v, err := s.CreateVoyage(all, named, store.Idem{})
		if err != nil {
			t.Fatalf("all-entity voyage naming entity B: %v", err)
		}
		if _, err := s.GetVoyage(ctxB, v.ID); err != nil {
			t.Errorf("entity B cannot read the voyage created for it: %v", err)
		}
		outside := newVoyage()
		outside.LegalEntityID = entityB
		if _, err := s.CreateVoyage(ctxA, outside, store.Idem{}); problemStatus(err) != http.StatusForbidden {
			t.Errorf("entity A voyage owned by entity B = %v, want 403", err)
		}
	})

	t.Run("route plans are per entity: the same request is its own plan and lookups never cross", func(t *testing.T) {
		if _, found, err := s.FindRoutePlan(ctxB, hash, ""); err != nil || found {
			t.Fatalf("entity B finds entity A's plan = %v, %v", found, err)
		}
		if _, err := s.GetRoutePlan(ctxB, planA.ID); problemCode(t, err) != domain.CodeNotFound {
			t.Errorf("entity B get = %v", err)
		}
		if list, err := s.ListRoutePlans(ctxB); err != nil || len(list) != 0 {
			t.Errorf("entity B lists %d plans (%v)", len(list), err)
		}
		planB, created, err := s.SaveRoutePlan(ctxB, domain.NewRoutePlan(req, hash, sol))
		if err != nil || !created || planB.ID == planA.ID || planB.LegalEntityID != entityB {
			t.Fatalf("entity B plan for the same request = %+v created %v, %v", planB, created, err)
		}
		if found, ok, err := s.FindRoutePlan(ctxA, hash, ""); err != nil || !ok || found.ID != planA.ID {
			t.Errorf("entity A lookup = %+v %v, %v", found, ok, err)
		}
		if found, ok, err := s.FindRoutePlan(all, hash, entityB); err != nil || !ok || found.ID != planB.ID {
			t.Errorf("all-entity lookup naming entity B = %+v %v, %v", found, ok, err)
		}
		if _, _, err := s.FindRoutePlan(ctxA, hash, entityB); problemStatus(err) != http.StatusForbidden {
			t.Errorf("entity A lookup naming entity B = %v, want 403", err)
		}
		if _, err := s.GetRoutePlan(all, planA.ID); err != nil {
			t.Errorf("all-entity caller cannot get entity A's plan: %v", err)
		}
	})
}

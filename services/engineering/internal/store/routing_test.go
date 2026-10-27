package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

const routeActivated = "dev.passage.engineering.routeversion.activated.v1"

func routeOp(no, next int, reqs ...domain.OperationRequirement) domain.VersionOperation {
	return domain.VersionOperation{
		RouteOperation: domain.RouteOperation{No: no, NextNo: next, SetupH: decimal.RequireFromString("0.5"), RunH: decimal.NewFromInt(2), ProcessQty: decimal.NewFromInt(1)},
		OperationID:    "OP", LinkType: domain.LinkSoft, Requirements: reqs,
	}
}

func newRoute(product, site string, versionNo int, from time.Time, ops ...domain.VersionOperation) domain.RouteVersion {
	return domain.RouteVersion{ID: ids.New(), ProductID: product, SiteID: site, RouteCode: "R", VersionNo: versionNo, EffectiveFrom: from, Operations: ops}
}

func seedCapableMachine(t *testing.T, s *store.Postgres, ctx context.Context, site string, from time.Time) (resourceID, capabilityID string) {
	t.Helper()
	skill := domain.Capability{ID: ids.New(), Code: "WELD-" + ids.New(), Name: "Welding"}
	if _, err := s.CreateCapability(ctx, skill, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	m := machine("M-"+ids.New(), site)
	m.Capabilities = []domain.ResourceCapability{{CapabilityID: skill.ID, Level: 3, EffectiveFrom: from}}
	if _, err := s.CreateResource(ctx, m, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	return m.ID, skill.ID
}

func TestPostgres_CreateRouteVersion_RoundTripsAndReplays(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	req := domain.OperationRequirement{Kind: domain.ReqCapability, RefID: ids.New(), Level: 2, Quantity: 2}
	v := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 20, req), routeOp(20, 0))
	idem := store.Idem{Key: "route-key", Hash: "h1"}
	created, err := s.CreateRouteVersion(ctx, v, idem)
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != domain.StatusDraft || len(created.Operations) != 2 || created.Operations[0].No != 10 || created.Operations[0].Requirements[0] != req ||
		created.Operations[0].LinkType != domain.LinkSoft || !created.Operations[0].SetupH.Equal(decimal.RequireFromString("0.5")) {
		t.Fatalf("created = %+v", created)
	}
	replayed, err := s.CreateRouteVersion(ctx, newRoute("BIKE", "S1", 2, releaseDay, routeOp(10, 0)), idem)
	if err != nil || replayed.ID != v.ID {
		t.Fatalf("replay = %+v, %v; want the first version", replayed, err)
	}
	for name, c := range map[string]struct {
		v    domain.RouteVersion
		idem store.Idem
		code string
	}{
		"reused key":    {newRoute("BIKE", "S1", 3, releaseDay, routeOp(10, 0)), store.Idem{Key: "route-key", Hash: "other"}, store.CodeIdempotencyReuse},
		"duplicate key": {newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0)), store.Idem{}, store.CodeDuplicate},
		"dangling next": {newRoute("BIKE", "S1", 4, releaseDay, routeOp(10, 99)), store.Idem{}, domain.CodeRouteNext},
		"empty header":  {domain.RouteVersion{ID: ids.New()}, store.Idem{}, domain.CodeRouteVersion},
	} {
		_, err := s.CreateRouteVersion(ctx, c.v, c.idem)
		if problemCode(err) != c.code && !domain.HasCode(err, c.code) {
			t.Fatalf("%s: err = %v; want %s", name, err, c.code)
		}
	}
	if _, err := s.GetRouteVersion(ctx, ids.New()); problemCode(err) != store.CodeRouteNotFound {
		t.Fatalf("unknown id = %v; want %s", err, store.CodeRouteNotFound)
	}
}

func TestPostgres_ActivateRouteVersion_EmitsOnceAndIsIdempotent(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	resourceID, capabilityID := seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, -1, 0))
	v := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0,
		domain.OperationRequirement{Kind: domain.ReqResource, RefID: resourceID, Quantity: 1},
		domain.OperationRequirement{Kind: domain.ReqCapability, RefID: capabilityID, Level: 3, Quantity: 1},
		domain.OperationRequirement{Kind: domain.ReqType, RefID: domain.TypeMachine, Quantity: 1}))
	if _, err := s.CreateRouteVersion(ctx, v, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, err := s.ActivateRouteVersion(ctx, v.ID)
		if err != nil || got.Status != domain.StatusActive {
			t.Fatalf("activate = %+v, %v", got, err)
		}
	}
	if types := outboxTypes(t, pool, ctx, v.ID); !slices.Equal(types, []string{routeActivated}) {
		t.Fatalf("outbox = %v; want exactly one routeversion.activated", types)
	}
	if _, err := s.ActivateRouteVersion(ctx, ids.New()); problemCode(err) != store.CodeRouteNotFound {
		t.Fatalf("unknown id = %v; want %s", err, store.CodeRouteNotFound)
	}
}

func TestPostgres_ActivateRouteVersion_UnsatisfiedStaysDraft(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	seedCapableMachine(t, s, ctx, "S1", releaseDay.AddDate(0, 1, 0))
	v := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0), routeOp(20, 0,
		domain.OperationRequirement{Kind: domain.ReqCapability, RefID: ids.New(), Level: 1, Quantity: 1}))
	if _, err := s.CreateRouteVersion(ctx, v, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRouteVersion(ctx, v.ID); !domain.HasCode(err, domain.CodeRouteUnsatisfied) || !strings.Contains(err.Error(), "operation 20 requirement[0]") {
		t.Fatalf("err = %v; want %s", err, domain.CodeRouteUnsatisfied)
	}
	got, err := s.GetRouteVersion(ctx, v.ID)
	if err != nil || got.Status != domain.StatusDraft || len(outboxTypes(t, pool, ctx, v.ID)) != 0 {
		t.Fatalf("after failed activation: %+v, %v, outbox %v; want draft and no event", got.Status, err, outboxTypes(t, pool, ctx, v.ID))
	}
}

func TestPostgres_ActivateRouteVersion_RefusesOverlapOnlyForSameProductAndSite(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	first := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0))
	clash := newRoute("BIKE", "S1", 2, releaseDay.AddDate(0, 1, 0), routeOp(10, 0))
	otherSite := newRoute("BIKE", "S2", 1, releaseDay, routeOp(10, 0))
	later := newRoute("BIKE", "S1", 3, releaseDay.AddDate(-1, 0, 0), routeOp(10, 0))
	later.EffectiveTo = releaseDay.AddDate(0, 0, -1)
	for _, v := range []domain.RouteVersion{first, clash, otherSite, later} {
		if _, err := s.CreateRouteVersion(ctx, v, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ActivateRouteVersion(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateRouteVersion(ctx, clash.ID); !domain.HasCode(err, domain.CodeRouteOverlap) {
		t.Fatalf("overlapping activation = %v; want %s", err, domain.CodeRouteOverlap)
	}
	for _, id := range []string{otherSite.ID, later.ID} {
		if got, err := s.ActivateRouteVersion(ctx, id); err != nil || got.Status != domain.StatusActive {
			t.Fatalf("non-overlapping activation = %+v, %v", got, err)
		}
	}
	if got, _ := s.GetRouteVersion(ctx, clash.ID); got.Status != domain.StatusDraft {
		t.Fatalf("refused version is %s; want draft", got.Status)
	}
}

func TestPostgres_RouteVersions_RLSHidesOtherTenant(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	v := newRoute("BIKE", "S1", 1, releaseDay, routeOp(10, 0))
	if _, err := s.CreateRouteVersion(principalContext(1), v, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"get":      func() error { _, err := s.GetRouteVersion(principalContext(2), v.ID); return err },
		"activate": func() error { _, err := s.ActivateRouteVersion(principalContext(2), v.ID); return err },
	} {
		if err := call(); problemCode(err) != store.CodeRouteNotFound {
			t.Fatalf("tenant 2 %s = %v; want %s", name, err, store.CodeRouteNotFound)
		}
	}
	if got, err := s.GetRouteVersion(principalContext(1), v.ID); err != nil || got.Status != domain.StatusDraft {
		t.Fatalf("tenant 1 version = %+v, %v; want untouched draft", got, err)
	}
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	costevents "github.com/BenjaminSRussell/passage/services/productcost/internal/events"
	"github.com/BenjaminSRussell/passage/services/productcost/migrations"
)

// setup migrates a fresh schema. With PASSAGE_TEST_APP_DATABASE_URL the store
// runs as the application role, so row-level security is enforced; otherwise
// it runs on the admin pool (functional checks only). admin reads the outbox.
func setup(t *testing.T) (s *Postgres, admin *pgxpool.Pool, rls bool) {
	t.Helper()
	ctx := context.Background()
	if os.Getenv(testkit.EnvURL) != "" && os.Getenv(testkit.EnvAppURL) != "" {
		adminPool, app := testkit.DBWithApp(t)
		if err := dbx.Migrate(ctx, adminPool, migrations.FS); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		return New(app), adminPool, true
	}
	pool := testkit.DB(t)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(pool), pool, false
}

// as is a principal of tenant n; scope is its legal-entity scope ("" for all).
func as(n int, scope string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: scope, UserID: "cost.accountant", Duties: map[string]struct{}{}})
}

func codeOf(err error) string {
	var ce *domain.CodedError
	if errors.As(err, &ce) {
		return ce.Code
	}
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func statusOf(err error) int {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Status
	}
	return 0
}

// outboxTypes returns the event types in the outbox for aggregate id, oldest first.
func outboxTypes(t *testing.T, admin *pgxpool.Pool, id string) []string {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		out = append(out, typ)
	}
	return out
}

func component(code string) domain.CostComponent {
	return domain.CostComponent{Code: code, Name: "Component " + code, CostGroupType: domain.GroupIndirect, Behaviour: domain.BehaviourVariable}
}

func TestCostComponents_CreateReplayDuplicateAndLimit(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	dm := domain.CostComponent{Code: "DM", Name: "Direct material", CostGroupType: domain.GroupDirectMaterial, Behaviour: domain.BehaviourVariable}
	first, replayed, err := s.CreateComponent(ctx, dm, "k-dm")
	if err != nil || replayed {
		t.Fatalf("create: replayed=%v err=%v", replayed, err)
	}
	again, replayed, err := s.CreateComponent(ctx, dm, "k-dm")
	if err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("replay: %+v replayed=%v err=%v, want the original %s", again, replayed, err, first.ID)
	}
	if got := outboxTypes(t, admin, first.ID); len(got) != 1 || got[0] != costevents.CostComponentCreated {
		t.Fatalf("outbox %v, want one %s (a replay writes no event)", got, costevents.CostComponentCreated)
	}
	if _, _, err := s.CreateComponent(ctx, component("DL"), "k-dm"); codeOf(err) != CodeIdempotencyReuse || statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("key reused with another body: %v", err)
	}
	if _, _, err := s.CreateComponent(ctx, dm, "k-dm-2"); codeOf(err) != domain.CodeComponentDuplicate || statusOf(err) != http.StatusConflict {
		t.Fatalf("duplicate code: %v", err)
	}
	for i := 1; i < domain.MaxComponents; i++ {
		if _, _, err := s.CreateComponent(ctx, component(fmt.Sprintf("C%02d", i)), fmt.Sprintf("k-%d", i)); err != nil {
			t.Fatalf("component %d: %v", i, err)
		}
	}
	if _, _, err := s.CreateComponent(ctx, component("OVER"), "k-over"); codeOf(err) != domain.CodeComponentLimit {
		t.Fatalf("21st component: %v, want %s", err, domain.CodeComponentLimit)
	}
	list, err := s.ListComponents(ctx)
	if err != nil || len(list) != domain.MaxComponents || list[0].Code != "C01" || list[len(list)-1].Code != "DM" {
		t.Fatalf("list: %d items err=%v, want %d ordered by code", len(list), err, domain.MaxComponents)
	}
}

func TestCostComponents_LimitHoldsUnderConcurrentCreates(t *testing.T) {
	s, _, _ := setup(t)
	ctx := as(1, "")
	for i := 0; i < domain.MaxComponents-1; i++ {
		if _, _, err := s.CreateComponent(ctx, component(fmt.Sprintf("C%02d", i)), fmt.Sprintf("k-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = s.CreateComponent(ctx, component(fmt.Sprintf("RACE%d", i)), fmt.Sprintf("race-%d", i))
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case codeOf(err) != domain.CodeComponentLimit:
			t.Fatalf("unexpected error %v", err)
		}
	}
	list, _ := s.ListComponents(ctx)
	if ok != 1 || len(list) != domain.MaxComponents {
		t.Fatalf("%d concurrent creates succeeded and %d components exist, want 1 and %d", ok, len(list), domain.MaxComponents)
	}
}

func TestCostGroups_MapToComponents(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	dl, _, err := s.CreateComponent(ctx, domain.CostComponent{Code: "DL", Name: "Direct labor", CostGroupType: domain.GroupDirectManufacturing, Behaviour: domain.BehaviourVariable}, "k-dl")
	if err != nil {
		t.Fatal(err)
	}
	mach := domain.CostGroup{Code: "MACH", Name: "Machining", Type: domain.GroupDirectManufacturing, Behaviour: domain.BehaviourVariable, ComponentID: dl.ID}
	g, _, err := s.CreateGroup(ctx, mach, "k-mach")
	if err != nil {
		t.Fatal(err)
	}
	if g.ComponentCode != "DL" || g.ComponentID != dl.ID {
		t.Fatalf("group %+v, want mapped to DL", g)
	}
	var payload struct {
		Data costevents.CostGroupCreatedData `json:"data"`
	}
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where aggregate_id = $1`, g.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Data.ComponentCode != "DL" || payload.Data.Code != "MACH" {
		t.Fatalf("event payload %s (err %v)", raw, err)
	}
	if _, _, err := s.CreateGroup(ctx, mach, "k-mach-2"); codeOf(err) != domain.CodeGroupDuplicate || statusOf(err) != http.StatusConflict {
		t.Fatalf("duplicate group: %v", err)
	}
	unknown := mach
	unknown.Code, unknown.ComponentID = "ORPHAN", ids.New()
	if _, _, err := s.CreateGroup(ctx, unknown, "k-orphan"); codeOf(err) != domain.CodeGroupComponent {
		t.Fatalf("unknown component: %v, want %s", err, domain.CodeGroupComponent)
	}
	// Another tenant's component cannot be referenced.
	if _, _, err := s.CreateGroup(as(2, ""), mach, "k-other"); codeOf(err) != domain.CodeGroupComponent {
		t.Fatalf("component of another tenant: %v, want %s", err, domain.CodeGroupComponent)
	}
	for i := 1; i < domain.MaxCostGroups; i++ {
		next := mach
		next.Code = fmt.Sprintf("G%02d", i)
		if _, _, err := s.CreateGroup(ctx, next, fmt.Sprintf("k-g%d", i)); err != nil {
			t.Fatalf("group %d: %v", i, err)
		}
	}
	over := mach
	over.Code = "OVER"
	if _, _, err := s.CreateGroup(ctx, over, "k-over"); codeOf(err) != domain.CodeGroupLimit {
		t.Fatalf("51st group: %v, want %s", err, domain.CodeGroupLimit)
	}
	list, err := s.ListGroups(ctx)
	if err != nil || len(list) != domain.MaxCostGroups || list[0].ComponentCode != "DL" {
		t.Fatalf("list: %d err=%v", len(list), err)
	}
}

func spec(le, code string) domain.VersionSpec {
	return domain.VersionSpec{LegalEntityID: le, Code: code}.Normalize()
}

func TestCostingVersions_CreateIsIdempotentAndUniquePerLegalEntity(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	le, site := ids.New(), ids.New()
	in := spec(le, "STD-2027")
	in.SiteIDs = []string{site}
	first, replayed, err := s.CreateVersion(ctx, in, "key-1")
	if err != nil || replayed {
		t.Fatalf("create: replayed=%v err=%v", replayed, err)
	}
	if first.Status != domain.VersionOpen || first.Version != 1 || first.Type != domain.VersionStandard || first.Fallback != domain.FallbackActive ||
		len(first.SiteIDs) != 1 || first.SiteIDs[0] != site || first.LegalEntityID != le {
		t.Fatalf("new version %+v", first)
	}
	again, replayed, err := s.CreateVersion(ctx, in, "key-1")
	if err != nil || !replayed || again.ID != first.ID || !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("replay %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.CreateVersion(ctx, spec(le, "STD-2028"), "key-1"); codeOf(err) != CodeIdempotencyReuse {
		t.Fatalf("key reused with another body: %v", err)
	}
	// A key is scoped to its command: the component create cannot replay it.
	if _, _, err := s.CreateComponent(ctx, component("DM"), "key-1"); codeOf(err) != CodeIdempotencyReuse {
		t.Fatalf("key reused on another command: %v", err)
	}
	if _, _, err := s.CreateVersion(ctx, in, "key-2"); codeOf(err) != domain.CodeVersionDuplicate || statusOf(err) != http.StatusConflict {
		t.Fatalf("duplicate code: %v", err)
	}
	// The same code is free in another legal entity.
	if _, _, err := s.CreateVersion(ctx, spec(ids.New(), "STD-2027"), "key-3"); err != nil {
		t.Fatalf("same code, other legal entity: %v", err)
	}
	if got := outboxTypes(t, admin, first.ID); len(got) != 1 || got[0] != costevents.VersionCreated {
		t.Fatalf("outbox %v", got)
	}
}

func TestCostingVersions_SpecifiedFallbackMustExistInTheSameLegalEntity(t *testing.T) {
	s, _, _ := setup(t)
	ctx := as(1, "")
	le := ids.New()
	base, _, err := s.CreateVersion(ctx, spec(le, "BASE"), "k-base")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.CreateVersion(ctx, spec(ids.New(), "OTHER"), "k-other")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, fallback, want string
	}{
		{"existing version", base.ID, ""},
		{"unknown version", ids.New(), domain.CodeFallbackVersion},
		{"other legal entity", other.ID, domain.CodeFallbackVersion},
	}
	for i, tc := range tests {
		in := spec(le, fmt.Sprintf("SIM-%d", i))
		in.Fallback, in.FallbackVersionID = domain.FallbackSpecified, tc.fallback
		v, _, err := s.CreateVersion(ctx, in, fmt.Sprintf("k-%d", i))
		if codeOf(err) != tc.want {
			t.Fatalf("%s: err %v, want %q", tc.name, err, tc.want)
		}
		if tc.want == "" && (v.FallbackVersionID == nil || *v.FallbackVersionID != base.ID) {
			t.Fatalf("%s: fallback %v", tc.name, v.FallbackVersionID)
		}
	}
}

func TestCostingVersions_LifecycleUnderOptimisticConcurrency(t *testing.T) {
	s, admin, _ := setup(t)
	ctx := as(1, "")
	v, _, err := s.CreateVersion(ctx, spec(ids.New(), "STD-LOCK"), "k-lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionVersion(ctx, v.ID, 2, domain.ActionBlock); codeOf(err) != domain.CodeVersionConflict || statusOf(err) != http.StatusConflict {
		t.Fatalf("stale version: %v", err)
	}
	blocked, err := s.TransitionVersion(ctx, v.ID, 1, domain.ActionBlock)
	if err != nil || blocked.Status != domain.VersionBlocked || blocked.Version != 2 || blocked.ActivatedAt != nil {
		t.Fatalf("block: %+v err=%v", blocked, err)
	}
	active, err := s.TransitionVersion(ctx, v.ID, 2, domain.ActionActivate)
	if err != nil || active.Status != domain.VersionActive || active.Version != 3 || active.ActivatedAt == nil {
		t.Fatalf("activate: %+v err=%v", active, err)
	}
	if _, err := s.TransitionVersion(ctx, v.ID, 3, domain.ActionBlock); codeOf(err) != domain.CodeInvalidTransition {
		t.Fatalf("block an Active version: %v", err)
	}
	closed, err := s.TransitionVersion(ctx, v.ID, 3, domain.ActionClose)
	if err != nil || closed.Status != domain.VersionClosed || closed.Version != 4 || !closed.ActivatedAt.Equal(*active.ActivatedAt) {
		t.Fatalf("close: %+v err=%v", closed, err)
	}
	got, err := s.GetVersion(ctx, v.ID)
	if err != nil || got.Status != domain.VersionClosed || got.Version != 4 {
		t.Fatalf("get: %+v err=%v", got, err)
	}
	if _, err := s.GetVersion(ctx, ids.New()); codeOf(err) != domain.CodeVersionNotFound || statusOf(err) != http.StatusNotFound {
		t.Fatalf("missing version: %v", err)
	}
	if _, err := s.TransitionVersion(ctx, ids.New(), 1, domain.ActionBlock); codeOf(err) != domain.CodeVersionNotFound {
		t.Fatalf("transition a missing version: %v", err)
	}
	want := []string{costevents.VersionCreated, costevents.VersionTransitioned, costevents.VersionTransitioned, costevents.VersionTransitioned}
	if got := outboxTypes(t, admin, v.ID); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("outbox %v, want %v (refused moves write nothing)", got, want)
	}
}

func TestCostingVersions_SimulationCannotActivate(t *testing.T) {
	s, _, _ := setup(t)
	ctx := as(1, "")
	in := spec(ids.New(), "SIM-1")
	in.Type = domain.VersionSimulation
	v, _, err := s.CreateVersion(ctx, in, "k-sim")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionVersion(ctx, v.ID, 1, domain.ActionActivate); codeOf(err) != domain.CodeSimulationActivation {
		t.Fatalf("activate simulation: %v", err)
	}
	if got, _ := s.GetVersion(ctx, v.ID); got.Status != domain.VersionOpen || got.Version != 1 {
		t.Fatalf("a refused move changed the version: %+v", got)
	}
}

func TestCostingVersions_ListPagesAndFilters(t *testing.T) {
	s, _, _ := setup(t)
	ctx := as(1, "")
	le := ids.New()
	var created []string
	for i := 0; i < 5; i++ {
		v, _, err := s.CreateVersion(ctx, spec(le, fmt.Sprintf("V%d", i)), fmt.Sprintf("k-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		created = append(created, v.ID)
	}
	if _, _, err := s.CreateVersion(ctx, spec(ids.New(), "ELSEWHERE"), "k-else"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionVersion(ctx, created[1], 1, domain.ActionBlock); err != nil {
		t.Fatal(err)
	}
	var seen []string
	after := ""
	for pages := 0; ; pages++ {
		page, err := s.ListVersions(ctx, VersionFilter{LegalEntityID: le}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range page.Items {
			seen = append(seen, v.ID)
		}
		if page.Next == "" {
			if pages != 2 {
				t.Fatalf("%d pages, want 3", pages+1)
			}
			break
		}
		after = page.Next
	}
	if fmt.Sprint(seen) != fmt.Sprint(created) {
		t.Fatalf("paged %v, want %v in creation order", seen, created)
	}
	blocked, err := s.ListVersions(ctx, VersionFilter{Status: domain.VersionBlocked}, "", 10)
	if err != nil || len(blocked.Items) != 1 || blocked.Items[0].ID != created[1] {
		t.Fatalf("status filter: %+v err=%v", blocked.Items, err)
	}
	all, _ := s.ListVersions(ctx, VersionFilter{}, "", 10)
	if len(all.Items) != 6 {
		t.Fatalf("unfiltered %d, want 6", len(all.Items))
	}
}

func TestRowLevelSecurity_TenantsAndLegalEntities(t *testing.T) {
	s, _, rls := setup(t)
	if !rls {
		t.Skipf("%s is needed to run as the application role", testkit.EnvAppURL)
	}
	t1 := as(1, "")
	leA, leB := ids.New(), ids.New()
	if _, _, err := s.CreateComponent(t1, component("DM"), "k-dm"); err != nil {
		t.Fatal(err)
	}
	va, _, err := s.CreateVersion(t1, spec(leA, "A"), "k-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateVersion(t1, spec(leB, "B"), "k-b"); err != nil {
		t.Fatal(err)
	}
	t2 := as(2, "")
	if list, err := s.ListComponents(t2); err != nil || len(list) != 0 {
		t.Fatalf("tenant 2 components: %d err=%v", len(list), err)
	}
	if page, err := s.ListVersions(t2, VersionFilter{}, "", 10); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 versions: %d err=%v", len(page.Items), err)
	}
	if _, err := s.GetVersion(t2, va.ID); codeOf(err) != domain.CodeVersionNotFound {
		t.Fatalf("tenant 2 reads tenant 1's version: %v", err)
	}
	// Tenant 2 may reuse tenant 1's idempotency key without replaying its response.
	if v, replayed, err := s.CreateVersion(t2, spec(leA, "A"), "k-a"); err != nil || replayed || v.ID == va.ID {
		t.Fatalf("tenant 2 with tenant 1's key: %+v replayed=%v err=%v", v, replayed, err)
	}
	scopedB := as(1, leB)
	page, err := s.ListVersions(scopedB, VersionFilter{}, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].LegalEntityID != leB {
		t.Fatalf("legal-entity scope B sees %+v err=%v", page.Items, err)
	}
	if _, err := s.TransitionVersion(scopedB, va.ID, 1, domain.ActionBlock); codeOf(err) != domain.CodeVersionNotFound {
		t.Fatalf("scope B moves a version of A: %v", err)
	}
	if _, _, err := s.CreateVersion(scopedB, spec(leA, "SNEAK"), "k-sneak"); codeOf(err) != domain.CodeLegalEntityScope || statusOf(err) != http.StatusForbidden {
		t.Fatalf("scope B creates in A: %v", err)
	}
}

package store_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
	"github.com/BenjaminSRussell/passage/services/engineering/migrations"
)

var releaseDay = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func principalContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "alice"})
}

func newStore(t *testing.T, n int) (*store.Postgres, *pgxpool.Pool, context.Context) {
	pool := testkit.DB(t)
	ctx := principalContext(n)
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(pool), pool, ctx
}

func version(product, status string, from time.Time, components ...string) domain.BomVersion {
	v := domain.BomVersion{ID: ids.New(), ProductID: product, EffectiveFrom: from, Status: status}
	for i, c := range components {
		v.Lines = append(v.Lines, domain.BomLine{
			LineNo: i + 1, ComponentProductID: c, Quantity: decimal.RequireFromString("2.5"), Series: decimal.NewFromInt(1),
			LineType: domain.LineItem, ScrapVarPct: decimal.NewFromInt(5),
		})
	}
	return v
}

func noCheck(store.Snapshot) error { return nil }

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestPostgres_ReleaseEco_ExpiresOldActivatesNew(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL", "FRAME")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoSubmitted, ReleaseDate: releaseDay, Replaces: []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}}
	if _, err := s.CreateEco(ctx, eco, store.Idem{Key: "eco-key", Hash: "h1"}); err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateEco(ctx, domain.Eco{ID: ids.New(), Number: "ECO-1"}, store.Idem{Key: "eco-key", Hash: "h1"})
	if err != nil || replay.ID != eco.ID || len(replay.Replaces) != 1 {
		t.Fatalf("replay must return the original ECO, got %+v, %v", replay, err)
	}
	if _, err := s.CreateEco(ctx, eco, store.Idem{Key: "eco-key", Hash: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another body must be %s, got %v", store.CodeIdempotencyReuse, err)
	}

	record, err := s.GetEco(ctx, eco.ID)
	if err != nil || len(record.Versions) != 2 {
		t.Fatalf("GetEco = %+v, %v; want both versions of the product", record, err)
	}
	loaded := record.Eco
	approver := approverOf(true)
	if err := loaded.Review(ctx, approver); err != nil {
		t.Fatal(err)
	}
	plan, err := loaded.Release(record.Versions, releaseDay)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEco(ctx, loaded, domain.EcoSubmitted, plan); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEco(ctx, loaded, domain.EcoSubmitted, plan); problemCode(err) != domain.CodeEcoState {
		t.Fatalf("a second release of the same ECO must conflict with %s, got %v", domain.CodeEcoState, err)
	}

	before, err := s.Catalog(ctx, []string{"BIKE"}, false)
	if err != nil {
		t.Fatal(err)
	}
	pick := func(day time.Time) string {
		v, err := domain.ResolveVersion(before.VersionsFor("BIKE"), "BIKE", "", day, decimal.NewFromInt(1))
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	if pick(releaseDay.AddDate(0, 0, -1)) != old.ID || pick(releaseDay) != next.ID {
		t.Fatalf("old version must serve until %s and the new one from %s", releaseDay.AddDate(0, 0, -1).Format("2006-01-02"), releaseDay.Format("2006-01-02"))
	}
	var events []string
	rows, err := pool.Query(ctx, `select type from outbox order by type`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		events = append(events, typ)
	}
	want := []string{
		"dev.passage.engineering.bomversion.activated.v1", "dev.passage.engineering.bomversion.activated.v1", "dev.passage.engineering.bomversion.expired.v1",
		"dev.passage.engineering.eco.approved.v1", "dev.passage.engineering.eco.released.v1", "dev.passage.engineering.eco.submitted.v1",
	}
	if len(events) != len(want) {
		t.Fatalf("outbox = %v; want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("outbox = %v; want %v", events, want)
		}
	}
}

func TestPostgres_CreateBomVersion_RollsBackWhenCheckFails(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	rejected := errors.New("cycle")
	v := version("BIKE", domain.StatusActive, releaseDay, "WHEEL")
	if _, err := s.CreateBomVersion(ctx, v, store.Idem{Key: "k", Hash: "h"}, func(store.Snapshot) error { return rejected }); !errors.Is(err, rejected) {
		t.Fatalf("got %v, want the check error", err)
	}
	if page, err := s.ListBomVersions(ctx, store.BomVersionFilter{}, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("a rejected version must not persist, got %+v, %v", page, err)
	}
	created, err := s.CreateBomVersion(ctx, v, store.Idem{Key: "k", Hash: "h"}, noCheck)
	if err != nil || created.ID != v.ID {
		t.Fatalf("retry after rejection = %+v, %v", created, err)
	}
	replay, err := s.CreateBomVersion(ctx, version("BIKE", domain.StatusActive, releaseDay, "WHEEL"), store.Idem{Key: "k", Hash: "h"}, noCheck)
	if err != nil || replay.ID != v.ID || len(replay.Lines) != 1 || !replay.Lines[0].Quantity.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("replay must return the original with its lines, got %+v, %v", replay, err)
	}
	if _, err := s.CreateBomVersion(ctx, v, store.Idem{Key: "k", Hash: "different"}, noCheck); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("got %v, want %s", err, store.CodeIdempotencyReuse)
	}
}

func TestPostgres_CatalogExplodesDownAndWhereUsedUp(t *testing.T) {
	s, _, ctx := newStore(t, 2)
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, v := range []domain.BomVersion{
		version("BIKE", domain.StatusActive, from, "WHEEL"),
		version("WHEEL", domain.StatusActive, from, "SPOKE"),
		version("UNRELATED", domain.StatusActive, from, "NUT"),
	} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	down, err := s.Catalog(ctx, []string{"BIKE"}, false)
	if err != nil || len(down) != 2 || len(down["UNRELATED"]) != 0 {
		t.Fatalf("downward catalog = %v, %v; want BIKE and WHEEL only", down, err)
	}
	exploded, err := domain.Explode(down, domain.ExplodeRequest{ProductID: "BIKE", AsOf: releaseDay, Quantity: decimal.NewFromInt(1), Levels: 5})
	if err != nil || len(exploded.Flat) != 1 || exploded.Flat[0].ProductID != "SPOKE" {
		t.Fatalf("explosion = %+v, %v", exploded, err)
	}
	up, err := s.Catalog(ctx, []string{"SPOKE"}, true)
	if err != nil {
		t.Fatal(err)
	}
	used, err := domain.WhereUsed(up, "SPOKE", releaseDay, 5)
	if err != nil || len(used) != 1 || used[0].ProductID != "WHEEL" || len(used[0].Parents) != 1 || used[0].Parents[0].ProductID != "BIKE" {
		t.Fatalf("where-used = %+v, %v", used, err)
	}
}

func TestPostgres_RLS_CrossTenantInvisible(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	owner, intruder := principalContext(1), principalContext(2)
	live := version("BIKE", domain.StatusActive, releaseDay, "WHEEL")
	draft := version("BIKE", domain.StatusDraft, releaseDay.AddDate(1, 0, 0), "WHEEL")
	for _, v := range []domain.BomVersion{live, draft} {
		if _, err := s.CreateBomVersion(owner, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoSubmitted, Replaces: []domain.Replacement{{OldVersionID: live.ID, NewVersionID: draft.ID}}}
	if _, err := s.CreateEco(owner, eco, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	if page, err := s.ListBomVersions(intruder, store.BomVersionFilter{}, "", store.MaxPageSize); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 list = %+v, %v; want nothing", page, err)
	}
	if catalog, err := s.Catalog(intruder, []string{"BIKE"}, false); err != nil || len(catalog) != 0 {
		t.Fatalf("tenant 2 catalog = %v, %v; want nothing", catalog, err)
	}
	if _, err := s.GetEco(intruder, eco.ID); problemCode(err) != store.CodeEcoNotFound {
		t.Fatalf("tenant 2 reading tenant 1 ECO = %v; want %s", err, store.CodeEcoNotFound)
	}
	stolen := domain.Eco{ID: ids.New(), Number: "ECO-X", State: domain.EcoSubmitted, Replaces: eco.Replaces}
	err := func() error { _, err := s.CreateEco(intruder, stolen, store.Idem{}); return err }()
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusNotFound || problem.Code != store.CodeVersionNotFound {
		t.Fatalf("tenant 2 ECO over tenant 1 versions = %v; want 404 %s", err, store.CodeVersionNotFound)
	}
	if err := s.ReleaseEco(intruder, eco, domain.EcoSubmitted, domain.ReleasePlan{Expire: []domain.BomVersion{live}}); problemCode(err) != domain.CodeEcoState {
		t.Fatalf("tenant 2 release over tenant 1 rows = %v; want a conflict", err)
	}
	if got, err := s.Catalog(owner, []string{"BIKE"}, false); err != nil || len(got["BIKE"]) != 1 || got["BIKE"][0].Status != domain.StatusActive {
		t.Fatalf("tenant 1 rows must be untouched, got %v, %v", got, err)
	}
}

type approverOf bool

func (a approverOf) Decide(context.Context, string) (domain.Decision, error) {
	return domain.Decision{Approved: bool(a), Actor: "alice"}, nil
}

func outboxTypes(t *testing.T, pool *pgxpool.Pool, ctx context.Context, aggregateID string) []string {
	t.Helper()
	rows, err := pool.Query(ctx, `select type from outbox where aggregate_id = $1 order by type`, aggregateID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
	}
	return types
}

func TestPostgres_CreateActiveVersion_EnqueuesActivatedEvent(t *testing.T) {
	s, pool, ctx := newStore(t, 1)
	v := version("BIKE", domain.StatusActive, releaseDay, "WHEEL")
	draft := version("BIKE", domain.StatusDraft, releaseDay.AddDate(1, 0, 0), "WHEEL")
	for _, created := range []domain.BomVersion{v, draft} {
		if _, err := s.CreateBomVersion(ctx, created, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	if got := outboxTypes(t, pool, ctx, v.ID); len(got) != 1 || got[0] != "dev.passage.engineering.bomversion.activated.v1" {
		t.Fatalf("active insert outbox = %v; want one activated event", got)
	}
	if got := outboxTypes(t, pool, ctx, draft.ID); len(got) != 0 {
		t.Fatalf("draft insert outbox = %v; want none", got)
	}
}

func TestPostgres_ConcurrentActiveCreates_OnlyOneWins(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	const racers = 2
	var inCheck atomic.Int32
	errs := make(chan error, racers)
	for i := 0; i < racers; i++ {
		go func() {
			candidate := version("BIKE", domain.StatusActive, releaseDay, "WHEEL")
			_, err := s.CreateBomVersion(ctx, candidate, store.Idem{}, func(existing store.Snapshot) error {
				inCheck.Add(1)
				for deadline := time.Now().Add(time.Second); inCheck.Load() < racers && time.Now().Before(deadline); {
					time.Sleep(5 * time.Millisecond)
				}
				return domain.CheckNoOverlap(existing.VersionsFor("BIKE"), candidate)
			})
			errs <- err
		}()
	}
	won := 0
	for i := 0; i < racers; i++ {
		if err := <-errs; err == nil {
			won++
		} else if !domain.HasCode(err, domain.CodeBomOverlap) {
			t.Fatalf("loser error = %v; want %s", err, domain.CodeBomOverlap)
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent active creates committed; want exactly 1", won)
	}
}

func TestPostgres_ReleaseEco_RefusesOverlapCreatedAfterDryRun(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL", "FRAME")
	intruder := version("BIKE", domain.StatusActive, releaseDay.AddDate(0, 1, 0), "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	eco := domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoApproved, ReleaseDate: releaseDay, Replaces: []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}}
	if _, err := s.CreateEco(ctx, domain.Eco{ID: eco.ID, Number: "ECO-1", State: domain.EcoSubmitted, ReleaseDate: releaseDay, Replaces: eco.Replaces}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	record, err := s.GetEco(ctx, eco.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded := record.Eco
	loaded.State = domain.EcoApproved
	plan, err := loaded.Release(record.Versions, releaseDay)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBomVersion(ctx, intruder, store.Idem{}, noCheck); err != nil {
		t.Fatal(err)
	}
	if err := s.ReleaseEco(ctx, loaded, domain.EcoSubmitted, plan); !domain.HasCode(err, domain.CodeBomOverlap) {
		t.Fatalf("release over a version activated after the dry run = %v; want %s", err, domain.CodeBomOverlap)
	}
	if got, err := s.GetEco(ctx, eco.ID); err != nil || got.Eco.State != domain.EcoSubmitted {
		t.Fatalf("a refused release must leave the ECO untouched, got %+v, %v", got.Eco, err)
	}
}

func TestPostgres_ListBomVersions_WalksEveryPage(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	created := map[string]bool{}
	for i := 0; i < 3; i++ {
		v := version("P"+string(rune('A'+i)), domain.StatusDraft, releaseDay, "WHEEL")
		created[v.ID] = true
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	after, pages := "", 0
	for {
		page, err := s.ListBomVersions(ctx, store.BomVersionFilter{}, after, 1)
		if err != nil || len(page.Items) != 1 || !created[page.Items[0].ID] {
			t.Fatalf("page %d = %+v, %v", pages, page, err)
		}
		delete(created, page.Items[0].ID)
		pages++
		if page.NextID == "" {
			break
		}
		after = page.NextID
	}
	if pages != 3 || len(created) != 0 {
		t.Fatalf("walked %d pages, %d versions unseen; want 3 and 0", pages, len(created))
	}
}

func TestPostgres_ListBomVersions_Filters(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	ended := version("PA", domain.StatusDraft, releaseDay, "WHEEL")
	ended.SiteID, ended.EffectiveTo = "S1", releaseDay.AddDate(0, 1, 0)
	for _, v := range []domain.BomVersion{ended, version("PB", domain.StatusActive, releaseDay, "WHEEL")} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	for filter, want := range map[store.BomVersionFilter]int{
		{}:                                   2,
		{ProductID: "PA"}:                    1,
		{Status: domain.StatusActive}:        1,
		{ProductID: "PA", Status: "active"}:  0,
		{SiteID: "S1"}:                       1,
		{SiteID: "nowhere"}:                  0,
		{AsOf: releaseDay.AddDate(0, 0, -1)}: 0,
		{AsOf: releaseDay}:                   2,
		{AsOf: releaseDay.AddDate(0, 1, 0)}:  2,
		{AsOf: releaseDay.AddDate(0, 1, 1)}:  1,
	} {
		if page, err := s.ListBomVersions(ctx, filter, "", store.MaxPageSize); err != nil || len(page.Items) != want {
			t.Fatalf("filter %+v = %d items, %v; want %d", filter, len(page.Items), err, want)
		}
	}
}

func TestPostgres_DuplicateEcoNumber_IsConflict(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, releaseDay.AddDate(-1, 0, 0), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay, "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	replaces := []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}
	if _, err := s.CreateEco(ctx, domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoSubmitted, Replaces: replaces}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateEco(ctx, domain.Eco{ID: ids.New(), Number: "ECO-1", State: domain.EcoSubmitted, Replaces: replaces}, store.Idem{})
	if problemCode(err) != store.CodeDuplicate {
		t.Fatalf("duplicate ECO number = %v; want %s", err, store.CodeDuplicate)
	}
}

func TestPostgres_ReleaseEco_PersistsRejectedAndScheduledStates(t *testing.T) {
	s, _, ctx := newStore(t, 1)
	old := version("BIKE", domain.StatusActive, releaseDay.AddDate(-1, 0, 0), "WHEEL")
	next := version("BIKE", domain.StatusDraft, releaseDay.AddDate(0, 2, 0), "WHEEL")
	for _, v := range []domain.BomVersion{old, next} {
		if _, err := s.CreateBomVersion(ctx, v, store.Idem{}, noCheck); err != nil {
			t.Fatal(err)
		}
	}
	replaces := []domain.Replacement{{OldVersionID: old.ID, NewVersionID: next.ID}}
	for _, tc := range []struct {
		number string
		to     domain.EcoState
	}{{"ECO-REJ", domain.EcoRejected}, {"ECO-SCH", domain.EcoScheduled}} {
		eco := domain.Eco{ID: ids.New(), Number: tc.number, State: domain.EcoSubmitted, ReleaseDate: next.EffectiveFrom, Replaces: replaces}
		if _, err := s.CreateEco(ctx, eco, store.Idem{}); err != nil {
			t.Fatal(err)
		}
		eco.State = tc.to
		if err := s.ReleaseEco(ctx, eco, domain.EcoSubmitted, domain.ReleasePlan{}); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetEco(ctx, eco.ID); err != nil || got.Eco.State != tc.to {
			t.Fatalf("%s state = %+v, %v; want %s", tc.number, got.Eco, err, tc.to)
		}
	}
}

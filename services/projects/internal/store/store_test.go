package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/migrations"
)

// tenantCtx returns a principal for tenant n with no legal-entity restriction, so
// the restrictive RLS scope policy (passage.legal_entity_scopes = '*') allows all rows.
func tenantCtx(n int, duties ...string) context.Context {
	p := tenant.Principal{TenantID: testkit.TenantID(n), UserID: "user-test", Duties: testkit.ScopeDuties("")}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

// maxTestTenant is the highest tenant number the store tests use.
const maxTestTenant = 20

// setup migrates a fresh schema with the owner pool and returns a store on the
// app pool (no BYPASSRLS), plus a counter of outbox rows read via the owner pool.
func setup(t *testing.T) (*Postgres, func(int, string) int) {
	t.Helper()
	owner, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), owner, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// project.project_group_id is a foreign key: every test tenant gets group ENG.
	for n := 1; n <= maxTestTenant; n++ {
		err := dbx.WithTenantTx(tenantCtx(n), owner, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), `insert into project_group (tenant_id, id, name) values ($1, 'ENG', 'Engineering')`, testkit.TenantID(n))
			return err
		})
		if err != nil {
			t.Fatalf("seed group: %v", err)
		}
	}
	countOutbox := func(tenantN int, aggregateID string) int {
		var n int
		err := dbx.WithTenantTx(tenantCtx(tenantN), owner, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`select count(*) from outbox where aggregate_id = $1`, aggregateID).Scan(&n)
		})
		if err != nil {
			t.Fatalf("count outbox: %v", err)
		}
		return n
	}
	return New(app), countOutbox
}

func sampleProject(id string) domain.Project {
	return domain.Project{ID: id, LegalEntityID: ids.New(), ProjectGroupID: "ENG", ProjectType: domain.TypeInternal, Currency: "USD", Stage: domain.StageCreated, StartDate: "2026-10-01"}
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestCreateProject_IdempotencyReplayAndOutbox(t *testing.T) {
	s, outboxCount := setup(t)
	ctx := tenantCtx(1, "projects.maintain")
	in := sampleProject("PROJ-IDEM")

	first, created, err := s.CreateProject(ctx, in, "key-1")
	if err != nil || !created || first.Version != 1 {
		t.Fatalf("first create: created=%v version=%d err=%v", created, first.Version, err)
	}
	replay, created, err := s.CreateProject(ctx, in, "key-1")
	if err != nil || created || replay.ID != first.ID {
		t.Fatalf("replay: created=%v id=%s err=%v", created, replay.ID, err)
	}
	other := in
	other.Currency = "EUR"
	if _, _, err := s.CreateProject(ctx, other, "key-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("different body under same key: got %v", err)
	}
	if n := outboxCount(1, first.ID); n != 1 {
		t.Fatalf("outbox rows for project = %d, want 1 (replay must not re-enqueue)", n)
	}
}

func TestProjects_RowsAreTenantScoped(t *testing.T) {
	s, _ := setup(t)
	tenantOne := tenantCtx(1, "projects.maintain", "projects.inquire")
	tenantTwo := tenantCtx(2, "projects.inquire")
	if _, _, err := s.CreateProject(tenantOne, sampleProject("PROJ-T1"), ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.GetProject(tenantTwo, "PROJ-T1"); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant two read tenant one's project: %v", err)
	}
	page, err := s.ListProjects(tenantTwo, Filter{}, "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant two list: %d items, err %v", len(page.Items), err)
	}
}

func TestTransitionProject_VersionAndStageRules(t *testing.T) {
	s, outboxCount := setup(t)
	ctx := tenantCtx(3, "projects.maintain", "projects.manage")
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-TR"), ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.TransitionProject(ctx, "PROJ-TR", 7, domain.StageInProcess); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	var de *domain.Error
	if _, err := s.TransitionProject(ctx, "PROJ-TR", 1, domain.StageFinished); !errors.As(err, &de) || de.Code != domain.CodeStage {
		t.Fatalf("CREATED to FINISHED should be refused: %v", err)
	}
	out, err := s.TransitionProject(ctx, "PROJ-TR", 1, domain.StageInProcess)
	if err != nil || out.Stage != domain.StageInProcess || out.Version != 2 {
		t.Fatalf("legal move: stage=%s version=%d err=%v", out.Stage, out.Version, err)
	}
	if n := outboxCount(3, "PROJ-TR"); n != 2 {
		t.Fatalf("outbox rows = %d, want 2 (created + updated)", n)
	}
}

// leCtx is a principal of tenant n scoped to one legal entity.
func leCtx(n int, legalEntity string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: legalEntity, UserID: "user-le", Duties: map[string]struct{}{}})
}

func TestCreateProject_SubProjectInheritsFromParent(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(4)
	customer, contract := ids.New(), ids.New()
	parent := sampleProject("PROJ-P")
	parent.ProjectType = domain.TypeFixedPrice
	parent.CustomerPartyID, parent.ContractID = &customer, &contract
	if _, _, err := s.CreateProject(ctx, parent, "p"); err != nil {
		t.Fatalf("parent: %v", err)
	}
	parentID := parent.ID
	sub := domain.Project{ID: "PROJ-P-01", LegalEntityID: parent.LegalEntityID, ParentProjectID: &parentID, ProjectType: domain.TypeFixedPrice,
		Currency: "USD", StartDate: "2026-10-15"}
	got, created, err := s.CreateProject(ctx, sub, "sub")
	if err != nil || !created {
		t.Fatalf("sub-project: created=%v err=%v", created, err)
	}
	if got.ProjectGroupID != "ENG" || got.CustomerPartyID == nil || *got.CustomerPartyID != customer || got.ContractID == nil || *got.ContractID != contract {
		t.Fatalf("inherited group %q customer %v contract %v", got.ProjectGroupID, got.CustomerPartyID, got.ContractID)
	}
	// The replay compares the inherited project, so the same omitted fields replay cleanly.
	if again, created, err := s.CreateProject(ctx, sub, "sub"); err != nil || created || again.ID != got.ID {
		t.Fatalf("sub-project replay: created=%v err=%v", created, err)
	}

	other := ids.New()
	change := sub
	change.ID, change.CustomerPartyID = "PROJ-P-02", &other
	var de *domain.Error
	if _, _, err := s.CreateProject(ctx, change, "change"); !errors.As(err, &de) || de.Code != domain.CodeParentConflict {
		t.Fatalf("customer change: %v", err)
	}
	unknown := "NOPE"
	orphan := sub
	orphan.ID, orphan.ParentProjectID = "PROJ-P-03", &unknown
	if _, _, err := s.CreateProject(ctx, orphan, "orphan"); problemCode(err) != CodeParentUnknown {
		t.Fatalf("unknown parent: %v", err)
	}
}

func TestCreateProject_DuplicateIDIsConflict(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(5)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-DUP"), "a"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-DUP"), "b"); problemCode(err) != CodeDuplicate {
		t.Fatalf("duplicate id under a new key: %v", err)
	}
	// Another tenant may use the same id.
	if _, _, err := s.CreateProject(tenantCtx(6), sampleProject("PROJ-DUP"), "a"); err != nil {
		t.Fatalf("same id in another tenant: %v", err)
	}
}

func TestCreateProject_ConcurrentSameKeyCreatesOnce(t *testing.T) {
	s, outboxCount := setup(t)
	ctx := tenantCtx(7)
	in := sampleProject("PROJ-RACE")
	const n = 8
	var wg sync.WaitGroup
	createdCount := make(chan bool, n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := s.CreateProject(ctx, in, "race-key")
			createdCount <- created
			errs <- err
		}()
	}
	wg.Wait()
	close(createdCount)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create with one key failed: %v", err)
		}
	}
	created := 0
	for c := range createdCount {
		if c {
			created++
		}
	}
	if created != 1 || outboxCount(7, in.ID) != 1 {
		t.Fatalf("created %d times with %d outbox rows, want 1 and 1", created, outboxCount(7, in.ID))
	}
}

func TestProjects_LegalEntityScopeHidesOtherEntities(t *testing.T) {
	s, _ := setup(t)
	mine, theirs := sampleProject("PROJ-MINE"), sampleProject("PROJ-THEIRS")
	if _, _, err := s.CreateProject(leCtx(8, mine.LegalEntityID), mine, ""); err != nil {
		t.Fatalf("create in scope: %v", err)
	}
	if _, _, err := s.CreateProject(tenantCtx(8), theirs, ""); err != nil {
		t.Fatalf("create unscoped: %v", err)
	}
	scoped := leCtx(8, mine.LegalEntityID)
	if _, err := s.GetProject(scoped, theirs.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("read across legal entities: %v", err)
	}
	page, err := s.ListProjects(scoped, Filter{}, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != mine.ID {
		t.Fatalf("scoped list = %v, %v", page.Items, err)
	}
	// A parent outside the caller's scope is unknown, not silently linked.
	parentID := theirs.ID
	sub := mine
	sub.ID, sub.ParentProjectID = "PROJ-MINE-1", &parentID
	if _, _, err := s.CreateProject(scoped, sub, ""); problemCode(err) != CodeParentUnknown {
		t.Fatalf("parent outside scope: %v", err)
	}
	// Writing a row into another legal entity is refused by RLS.
	if _, _, err := s.CreateProject(scoped, sampleProject("PROJ-OTHER"), ""); err == nil {
		t.Fatal("insert into another legal entity must fail")
	}
}

func TestListProjects_FiltersAndPages(t *testing.T) {
	s, _ := setup(t)
	ctx := tenantCtx(9)
	for i := 1; i <= 5; i++ {
		p := sampleProject(fmt.Sprintf("PROJ-L%d", i))
		if i%2 == 0 {
			p.ProjectType = domain.TypeCost
		}
		if _, _, err := s.CreateProject(ctx, p, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.TransitionProject(ctx, "PROJ-L1", 1, domain.StageInProcess); err != nil {
		t.Fatal(err)
	}
	first, err := s.ListProjects(ctx, Filter{}, "", 2)
	if err != nil || len(first.Items) != 2 || first.NextID != "PROJ-L2" {
		t.Fatalf("first page = %d items next %q err %v", len(first.Items), first.NextID, err)
	}
	rest, err := s.ListProjects(ctx, Filter{}, first.NextID, 10)
	if err != nil || len(rest.Items) != 3 || rest.NextID != "" || rest.Items[0].ID != "PROJ-L3" {
		t.Fatalf("second page = %v next %q err %v", rest.Items, rest.NextID, err)
	}
	cost, _ := s.ListProjects(ctx, Filter{ProjectType: string(domain.TypeCost)}, "", 10)
	inProcess, _ := s.ListProjects(ctx, Filter{Stage: string(domain.StageInProcess)}, "", 10)
	if len(cost.Items) != 2 || len(inProcess.Items) != 1 || inProcess.Items[0].ID != "PROJ-L1" {
		t.Fatalf("filters: %d cost, %d in process", len(cost.Items), len(inProcess.Items))
	}
}

func TestTransitionProject_OutboxCarriesCloudEvent(t *testing.T) {
	s, _ := setup(t)
	app := s.pool // the app role reads its own tenant's outbox rows through RLS
	ctx := tenantCtx(10)
	if _, _, err := s.CreateProject(ctx, sampleProject("PROJ-EVT"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionProject(ctx, "PROJ-EVT", 1, domain.StageInProcess); err != nil {
		t.Fatal(err)
	}
	var types []string
	var last struct {
		Type     string `json:"type"`
		Subject  string `json:"subject"`
		TenantID string `json:"tenantid"`
		Actor    string `json:"actor"`
		Data     struct {
			Stage         string   `json:"stage"`
			Version       int      `json:"version"`
			ChangedFields []string `json:"changedFields"`
		} `json:"data"`
	}
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select type, payload from outbox where aggregate_id = 'PROJ-EVT' order by created_at, type`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			var payload []byte
			if err := rows.Scan(&typ, &payload); err != nil {
				return err
			}
			types = append(types, typ)
			if err := json.Unmarshal(payload, &last); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(types) != "[dev.passage.projects.project.created.v1 dev.passage.projects.project.updated.v1]" {
		t.Fatalf("outbox types = %v", types)
	}
	if last.Subject != "PROJ-EVT" || last.TenantID != testkit.TenantID(10) || last.Actor != "user-test" ||
		last.Data.Stage != "IN_PROCESS" || last.Data.Version != 2 || fmt.Sprint(last.Data.ChangedFields) != "[stage]" {
		t.Fatalf("updated event = %+v", last)
	}
}

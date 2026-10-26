package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func teamStore(t *testing.T) (*store.Postgres, context.Context, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	seed(t, ctx, admin)
	return store.New(app), ctx, admin
}

func teamType(code string, kinds ...string) store.TeamType {
	return store.TeamType{Code: code, Name: "Type " + code, MemberKinds: kinds}
}

func TestPostgres_Migration0021IsRepeatableAndEnforcesChecks(t *testing.T) {
	_, ctx, admin := teamStore(t)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	tenantID := testkit.TenantID(1)
	typeID := "00000000-0000-0000-0000-0000000000a1"
	for _, kinds := range []string{"{}", "{robot}", "{user,robot}"} {
		if _, err := admin.Exec(ctx, `insert into team_type (tenant_id, id, code, name, member_kinds) values ($1, $2, 'BAD', 'Bad', $3::text::text[])`, tenantID, typeID, kinds); err == nil {
			t.Fatalf("member_kinds %s was accepted", kinds)
		}
	}
	if _, err := admin.Exec(ctx, `insert into team_type (tenant_id, id, code, name, member_kinds) values ($1, $2, 'OK', 'Ok', '{user}')`, tenantID, typeID); err != nil {
		t.Fatal(err)
	}
	insertTeam := `insert into team (tenant_id, id, code, name, team_type_id, effective_from, effective_to)
		values ($1, '00000000-0000-0000-0000-0000000000b1', 'T1', 'Team', $2, $3::date, $4::date)`
	if _, err := admin.Exec(ctx, insertTeam, tenantID, typeID, "2026-01-01", "2025-12-31"); err == nil {
		t.Fatal("effective_to before effective_from was accepted")
	}
	if _, err := admin.Exec(ctx, insertTeam, tenantID, typeID, "2026-01-01", "2026-01-01"); err != nil {
		t.Fatalf("one-day team rejected: %v", err)
	}
}

func TestPostgres_CreateTeamTypeReplay(t *testing.T) {
	s, ctx, _ := teamStore(t)
	first, inserted, err := s.CreateTeamType(ctx, teamType("STEW", "user", "party"), "key-1")
	if err != nil || !inserted {
		t.Fatalf("first = %+v, %v, %v", first, inserted, err)
	}
	replay, inserted, err := s.CreateTeamType(ctx, teamType("STEW", "user", "party"), "key-1")
	if err != nil || inserted || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v, %v; want the original row and inserted false", replay, inserted, err)
	}
	changed := teamType("STEW", "user", "party")
	changed.Name = "Other"
	if _, _, err := s.CreateTeamType(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed name error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if _, _, err := s.CreateTeamType(ctx, teamType("STEW", "party", "user"), "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("reordered kinds error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	page, err := s.ListTeamTypes(ctx, store.TeamTypeFilter{}, "", 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("list = %+v, %v; want one row", page, err)
	}
}

func TestPostgres_CreateTeamTypeDuplicateCode(t *testing.T) {
	s, ctx, _ := teamStore(t)
	if _, _, err := s.CreateTeamType(ctx, teamType("STEW", "user"), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTeamType(ctx, teamType("STEW", "party"), ""); problemCode(t, err) != store.CodeTeamTypeCodeDuplicate {
		t.Fatalf("duplicate error = %v; want %s", err, store.CodeTeamTypeCodeDuplicate)
	}
}

func TestPostgres_ListTeamTypesPagesInCodeOrderAndFiltersMemberKind(t *testing.T) {
	s, ctx, _ := teamStore(t)
	for _, in := range []store.TeamType{teamType("VEND", "party"), teamType("AUD", "user"), teamType("STEW", "user", "party")} {
		if _, _, err := s.CreateTeamType(ctx, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	var codes []string
	after := ""
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		page, err := s.ListTeamTypes(ctx, store.TeamTypeFilter{}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			codes = append(codes, item.Code)
		}
		if page.NextCode == "" {
			break
		}
		after = page.NextCode
	}
	if want := []string{"AUD", "STEW", "VEND"}; !slices.Equal(codes, want) {
		t.Fatalf("codes = %v; want %v", codes, want)
	}
	party, err := s.ListTeamTypes(ctx, store.TeamTypeFilter{MemberKind: "party"}, "", 10)
	if err != nil || len(party.Items) != 2 || party.Items[0].Code != "STEW" || party.Items[1].Code != "VEND" {
		t.Fatalf("party filter = %+v, %v; want STEW and VEND", party, err)
	}
}

func TestPostgres_TeamTypesAreTenantIsolated(t *testing.T) {
	s, ctx, _ := teamStore(t)
	created, _, err := s.CreateTeamType(ctx, teamType("STEW", "user"), "")
	if err != nil {
		t.Fatal(err)
	}
	other := principalContext(2)
	page, err := s.ListTeamTypes(other, store.TeamTypeFilter{}, "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant B list = %+v, %v; want empty", page, err)
	}
	if _, err := s.GetTeamType(other, created.ID); problemCode(t, err) != store.CodeTeamTypeNotFound {
		t.Fatalf("tenant B get error = %v; want %s", err, store.CodeTeamTypeNotFound)
	}
	if _, _, err := s.CreateTeamType(other, teamType("STEW", "party"), ""); err != nil {
		t.Fatalf("tenant B reuse of the code: %v", err)
	}
}

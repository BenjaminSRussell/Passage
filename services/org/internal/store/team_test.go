package store_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
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

func newTeam(typeID, code string) store.Team {
	return store.Team{Code: code, Name: "Team " + code, TeamTypeID: typeID, EffectiveFrom: "2026-01-01"}
}

func teamFixture(t *testing.T) (*store.Postgres, context.Context, *pgxpool.Pool, store.TeamType) {
	t.Helper()
	s, ctx, admin := teamStore(t)
	tt, _, err := s.CreateTeamType(ctx, teamType("STEW", "user"), "")
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, admin, tt
}

func strPtr(s string) *string { return &s }

func TestPostgres_CreateTeamStoresDatesAndDefaults(t *testing.T) {
	s, ctx, _, tt := teamFixture(t)
	in := newTeam(tt.ID, "MDSTEW")
	in.Description, in.EffectiveTo = strPtr("Master data"), strPtr("2026-12-31")
	created, inserted, err := s.CreateTeam(ctx, in, "")
	if err != nil || !inserted {
		t.Fatalf("create = %+v, %v, %v", created, inserted, err)
	}
	if created.EffectiveFrom != "2026-01-01" || deref(created.EffectiveTo) != "2026-12-31" || created.Status != "active" || created.Version != 1 || deref(created.Description) != "Master data" || created.OperatingUnitID != nil {
		t.Fatalf("created = %+v", created)
	}
	got, err := s.GetTeam(ctx, created.ID)
	if err != nil || got.ID != created.ID || got.EffectiveFrom != "2026-01-01" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetTeam(ctx, "00000000-0000-0000-0000-0000000000ff"); problemCode(t, err) != store.CodeTeamNotFound {
		t.Fatalf("unknown get error = %v; want %s", err, store.CodeTeamNotFound)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestPostgres_CreateTeamRefusesInvalidReferents(t *testing.T) {
	s, ctx, admin, tt := teamFixture(t)
	if _, _, err := s.CreateTeam(ctx, newTeam("00000000-0000-0000-0000-0000000000ee", "T1"), ""); problemCode(t, err) != store.CodeTeamTypeInvalid {
		t.Fatalf("unknown type error = %v; want %s", err, store.CodeTeamTypeInvalid)
	}
	missingUnit := newTeam(tt.ID, "T2")
	missingUnit.OperatingUnitID = strPtr("00000000-0000-0000-0000-0000000000dd")
	if _, _, err := s.CreateTeam(ctx, missingUnit, ""); problemCode(t, err) != store.CodeTeamOperatingUnitInvalid {
		t.Fatalf("unknown unit error = %v; want %s", err, store.CodeTeamOperatingUnitInvalid)
	}
	ou, err := s.CreateOperatingUnit(ctx, unit("department", "D1"), "")
	if err != nil {
		t.Fatal(err)
	}
	active := newTeam(tt.ID, "T3")
	active.OperatingUnitID = &ou.ID
	if created, _, err := s.CreateTeam(ctx, active, ""); err != nil || deref(created.OperatingUnitID) != ou.ID {
		t.Fatalf("active unit create = %+v, %v", created, err)
	}
	if _, err := admin.Exec(ctx, `update operating_unit set status = 'inactive' where id = $1`, ou.ID); err != nil {
		t.Fatal(err)
	}
	inactive := newTeam(tt.ID, "T4")
	inactive.OperatingUnitID = &ou.ID
	if _, _, err := s.CreateTeam(ctx, inactive, ""); problemCode(t, err) != store.CodeTeamOperatingUnitInvalid {
		t.Fatalf("inactive unit error = %v; want %s", err, store.CodeTeamOperatingUnitInvalid)
	}
}

func TestPostgres_CreateTeamDuplicateCode(t *testing.T) {
	s, ctx, _, tt := teamFixture(t)
	if _, _, err := s.CreateTeam(ctx, newTeam(tt.ID, "T1"), ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateTeam(ctx, newTeam(tt.ID, "T1"), ""); problemCode(t, err) != store.CodeTeamCodeDuplicate {
		t.Fatalf("duplicate error = %v; want %s", err, store.CodeTeamCodeDuplicate)
	}
}

func TestPostgres_CreateTeamLimit(t *testing.T) {
	s, ctx, admin, tt := teamFixture(t)
	previous := store.MaxTeamsPerTenant
	store.MaxTeamsPerTenant = 2
	t.Cleanup(func() { store.MaxTeamsPerTenant = previous })
	for _, code := range []string{"T1", "T2"} {
		if _, _, err := s.CreateTeam(ctx, newTeam(tt.ID, code), ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.CreateTeam(ctx, newTeam(tt.ID, "T3"), ""); problemCode(t, err) != store.CodeTeamLimit {
		t.Fatalf("over-limit error = %v; want %s", err, store.CodeTeamLimit)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from team`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("team count = %d, %v; want 2", n, err)
	}
}

func TestPostgres_CreateTeamReplay(t *testing.T) {
	s, ctx, admin, tt := teamFixture(t)
	first, inserted, err := s.CreateTeam(ctx, newTeam(tt.ID, "T1"), "key-1")
	if err != nil || !inserted {
		t.Fatalf("first = %+v, %v, %v", first, inserted, err)
	}
	replay, inserted, err := s.CreateTeam(ctx, newTeam(tt.ID, "T1"), "key-1")
	if err != nil || inserted || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v, %v; want the original row and inserted false", replay, inserted, err)
	}
	changed := newTeam(tt.ID, "T1")
	changed.Name = "Other"
	if _, _, err := s.CreateTeam(ctx, changed, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed name error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from team`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("team count = %d, %v; want 1", n, err)
	}
}

func TestPostgres_ListTeamsFiltersAndPages(t *testing.T) {
	s, ctx, admin, tt := teamFixture(t)
	other, _, err := s.CreateTeamType(ctx, teamType("AUD", "user"), "")
	if err != nil {
		t.Fatal(err)
	}
	ou, err := s.CreateOperatingUnit(ctx, unit("department", "D1"), "")
	if err != nil {
		t.Fatal(err)
	}
	ranged := newTeam(tt.ID, "MDSTEW")
	ranged.EffectiveTo = strPtr("2026-12-31")
	inUnit := newTeam(other.ID, "B2")
	inUnit.OperatingUnitID = &ou.ID
	for _, in := range []store.Team{ranged, inUnit, newTeam(other.ID, "C3")} {
		if _, _, err := s.CreateTeam(ctx, in, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `update team set status = 'inactive' where code = 'C3'`); err != nil {
		t.Fatal(err)
	}
	codes := func(f store.TeamFilter) []string {
		t.Helper()
		page, err := s.ListTeams(ctx, f, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, item := range page.Items {
			out = append(out, item.Code)
		}
		return out
	}
	day := func(v string) *time.Time {
		d, err := time.Parse("2006-01-02", v)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	for asOf, want := range map[string]bool{"2026-06-01": true, "2026-12-31": true, "2027-01-15": false, "2025-12-31": false} {
		got := slices.Contains(codes(store.TeamFilter{AsOf: day(asOf)}), "MDSTEW")
		if got != want || got != domain.TeamInForce(*day("2026-01-01"), day("2026-12-31"), *day(asOf)) {
			t.Fatalf("asOf %s includes MDSTEW = %v; want %v", asOf, got, want)
		}
	}
	for name, tc := range map[string]struct {
		filter store.TeamFilter
		want   []string
	}{
		"all":         {store.TeamFilter{}, []string{"B2", "C3", "MDSTEW"}},
		"team type":   {store.TeamFilter{TeamTypeID: other.ID}, []string{"B2", "C3"}},
		"unit":        {store.TeamFilter{OperatingUnitID: ou.ID}, []string{"B2"}},
		"status":      {store.TeamFilter{Status: "inactive"}, []string{"C3"}},
		"after range": {store.TeamFilter{AsOf: day("2027-01-15")}, []string{"B2", "C3"}},
	} {
		if got := codes(tc.filter); !slices.Equal(got, tc.want) {
			t.Fatalf("%s = %v; want %v", name, got, tc.want)
		}
	}
	var paged []string
	after := ""
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		page, err := s.ListTeams(ctx, store.TeamFilter{}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			paged = append(paged, item.Code)
		}
		if page.NextCode == "" {
			break
		}
		after = page.NextCode
	}
	if want := []string{"B2", "C3", "MDSTEW"}; !slices.Equal(paged, want) {
		t.Fatalf("paged = %v; want %v", paged, want)
	}
}

func TestPostgres_TeamCrossTenantIsolation(t *testing.T) {
	s, ctx, _, tt := teamFixture(t)
	created, _, err := s.CreateTeam(ctx, newTeam(tt.ID, "T1"), "")
	if err != nil {
		t.Fatal(err)
	}
	other := principalContext(2)
	page, err := s.ListTeams(other, store.TeamFilter{}, "", 10)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant B list = %+v, %v; want empty", page, err)
	}
	if _, err := s.GetTeam(other, created.ID); problemCode(t, err) != store.CodeTeamNotFound {
		t.Fatalf("tenant B get error = %v; want %s", err, store.CodeTeamNotFound)
	}
	if _, _, err := s.CreateTeam(other, newTeam(tt.ID, "T2"), ""); problemCode(t, err) != store.CodeTeamTypeInvalid {
		t.Fatalf("tenant B create against tenant A's type error = %v; want %s", err, store.CodeTeamTypeInvalid)
	}
	if _, _, err := s.CreateTeam(other, newTeam(tt.ID, "T1"), ""); problemCode(t, err) != store.CodeTeamTypeInvalid {
		t.Fatalf("tenant B reuse of the code error = %v; want %s", err, store.CodeTeamTypeInvalid)
	}
}

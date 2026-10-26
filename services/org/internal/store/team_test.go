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
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
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

func dateOn(t *testing.T, v string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", v)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func newMember(kind, memberID, from string, to *string) store.TeamMember {
	return store.TeamMember{MemberKind: kind, MemberID: memberID, ValidFrom: from, ValidTo: to}
}

const (
	memberA = "00000000-0000-0000-0000-0000000000a1"
	memberB = "00000000-0000-0000-0000-0000000000a2"
	memberC = "00000000-0000-0000-0000-0000000000a3"
)

func memberFixture(t *testing.T) (*store.Postgres, context.Context, *pgxpool.Pool, store.Team) {
	t.Helper()
	s, ctx, admin, tt := teamFixture(t)
	in := newTeam(tt.ID, "MDSTEW")
	in.EffectiveTo = strPtr("2026-12-31")
	team, _, err := s.CreateTeam(ctx, in, "")
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, admin, team
}

func membershipEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []map[string]any {
	t.Helper()
	return unitEvents(t, ctx, pool, orgevents.TeamMembershipChanged)
}

func TestPostgres_TeamMemberTableEnforcesNaturalKeyRangeAndRLS(t *testing.T) {
	_, ctx, admin, team := memberFixture(t)
	insert := `insert into team_member (tenant_id, id, team_id, member_kind, member_id, valid_from, valid_to)
		values ($1, $2, $3, 'user', $4, $5::date, $6::date)`
	if _, err := admin.Exec(ctx, insert, testkit.TenantID(1), memberC, team.ID, memberA, "2026-02-01", "2026-01-31"); err == nil {
		t.Fatal("valid_to before valid_from accepted")
	}
	if _, err := admin.Exec(ctx, insert, testkit.TenantID(1), memberC, team.ID, memberA, "2026-02-01", "2026-03-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, insert, testkit.TenantID(1), memberB, team.ID, memberA, "2026-02-01", "2026-04-01"); err == nil {
		t.Fatal("natural key duplicate accepted")
	}
	var enabled, forced bool
	if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where oid = 'team_member'::regclass`).Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("rls enabled=%v forced=%v err=%v", enabled, forced, err)
	}
}

func TestPostgres_AddTeamMemberStoresDefaultsAndEmitsOneEvent(t *testing.T) {
	s, ctx, admin, team := memberFixture(t)
	to := "2026-06-30"
	got, inserted, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), dateOn(t, "2026-03-01"), "")
	if err != nil || !inserted || got.Source != "manual" || got.MemberRole != "member" || got.TeamID != team.ID || got.ValidFrom != "2026-02-01" || deref(got.ValidTo) != to {
		t.Fatalf("add = %+v, %v, %v", got, inserted, err)
	}
	events := membershipEvents(t, ctx, admin)
	if len(events) != 1 || events[0]["team_id"] != team.ID || events[0]["as_of"] != "2026-02-01" {
		t.Fatalf("events = %+v", events)
	}
	added, _ := events[0]["added"].([]any)
	removed, ok := events[0]["removed"].([]any)
	if len(added) != 1 || !ok || len(removed) != 0 {
		t.Fatalf("added = %v, removed = %v (%v)", added, events[0]["removed"], ok)
	}
	if ref := added[0].(map[string]any); ref["member_kind"] != "user" || ref["member_id"] != memberA || ref["member_role"] != "member" {
		t.Fatalf("added ref = %v", ref)
	}
}

func TestPostgres_AddTeamMemberRefusals(t *testing.T) {
	s, ctx, admin, team := memberFixture(t)
	to, late := "2026-06-30", "2027-02-01"
	today := dateOn(t, "2026-03-01")
	tests := []struct {
		name   string
		teamID string
		in     store.TeamMember
		today  time.Time
		want   string
	}{
		{"unknown team", "00000000-0000-0000-0000-0000000000ff", newMember("user", memberA, "2026-02-01", &to), today, store.CodeTeamNotFound},
		{"kind not allowed by team type", team.ID, newMember("party", memberA, "2026-02-01", &to), today, store.CodeTeamMemberKindInvalid},
		{"starts after the team ends", team.ID, newMember("user", memberA, "2027-01-15", &late), today, store.CodeTeamMemberRange},
		{"starts before the team", team.ID, newMember("user", memberA, "2025-12-31", &to), today, store.CodeTeamMemberRange},
		{"open-ended in a bounded team", team.ID, newMember("user", memberA, "2026-02-01", nil), today, store.CodeTeamMemberRange},
		{"team ended", team.ID, newMember("user", memberA, "2026-02-01", &to), dateOn(t, "2027-01-01"), store.CodeTeamExpired},
	}
	for _, tc := range tests {
		if _, _, err := s.AddTeamMember(ctx, tc.teamID, tc.in, tc.today, ""); problemCode(t, err) != tc.want {
			t.Fatalf("%s error = %v; want %s", tc.name, err, tc.want)
		}
	}
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), dateOn(t, "2026-12-31"), ""); err != nil {
		t.Fatalf("add on the last day of the team = %v", err)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from team_member`).Scan(&n); err != nil || n != 1 || len(membershipEvents(t, ctx, admin)) != 1 {
		t.Fatalf("members = %d, %v; events = %d; want one of each", n, err, len(membershipEvents(t, ctx, admin)))
	}
}

func TestPostgres_AddTeamMemberDuplicateAndLimit(t *testing.T) {
	s, ctx, admin, team := memberFixture(t)
	previous := store.MaxMembersPerTeam
	store.MaxMembersPerTeam = 2
	t.Cleanup(func() { store.MaxMembersPerTeam = previous })
	to := "2026-06-30"
	today := dateOn(t, "2026-03-01")
	for _, id := range []string{memberA, memberB} {
		if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", id, "2026-02-01", &to), today, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), today, ""); problemCode(t, err) != store.CodeTeamMemberLimit {
		t.Fatalf("over-limit duplicate error = %v; want %s", err, store.CodeTeamMemberLimit)
	}
	store.MaxMembersPerTeam = 3
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), today, ""); problemCode(t, err) != store.CodeTeamMemberDuplicate {
		t.Fatalf("duplicate error = %v; want %s", err, store.CodeTeamMemberDuplicate)
	}
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberC, "2026-02-01", &to), today, ""); err != nil {
		t.Fatal(err)
	}
	store.MaxMembersPerTeam = 2
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", "00000000-0000-0000-0000-0000000000a4", "2026-02-01", &to), today, ""); problemCode(t, err) != store.CodeTeamMemberLimit {
		t.Fatalf("limit error = %v; want %s", err, store.CodeTeamMemberLimit)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from team_member`).Scan(&n); err != nil || n != 3 || len(membershipEvents(t, ctx, admin)) != 3 {
		t.Fatalf("members = %d, %v; want 3 with 3 events", n, err)
	}
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", "00000000-0000-0000-0000-0000000000a4", "2026-02-01", &to), dateOn(t, "2026-07-01"), ""); err != nil {
		t.Fatalf("members ended before today must not count toward the cap: %v", err)
	}
}

func TestPostgres_AddTeamMemberReplay(t *testing.T) {
	s, ctx, admin, team := memberFixture(t)
	to := "2026-06-30"
	today := dateOn(t, "2026-03-01")
	first, inserted, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), today, "key-1")
	if err != nil || !inserted {
		t.Fatalf("first = %+v, %v, %v", first, inserted, err)
	}
	replay, inserted, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), today, "key-1")
	if err != nil || inserted || replay.ID != first.ID {
		t.Fatalf("replay = %+v, %v, %v; want the original row and inserted false", replay, inserted, err)
	}
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberB, "2026-02-01", &to), today, "key-1"); problemCode(t, err) != store.CodeIdempotencyReuse {
		t.Fatalf("changed member error = %v; want %s", err, store.CodeIdempotencyReuse)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from team_member`).Scan(&n); err != nil || n != 1 || len(membershipEvents(t, ctx, admin)) != 1 {
		t.Fatalf("members = %d, %v; want 1 with 1 event", n, err)
	}
}

func TestPostgres_ListTeamMembersFiltersAndPages(t *testing.T) {
	s, ctx, _, team := memberFixture(t)
	if _, err := s.ListTeamMembers(ctx, "00000000-0000-0000-0000-0000000000ff", store.TeamMemberFilter{}, "", 10); problemCode(t, err) != store.CodeTeamNotFound {
		t.Fatalf("unknown team error = %v; want %s", err, store.CodeTeamNotFound)
	}
	to := "2026-06-30"
	today := dateOn(t, "2026-03-01")
	lead := newMember("user", memberB, "2026-08-01", strPtr("2026-12-31"))
	lead.MemberRole = "lead"
	for _, in := range []store.TeamMember{newMember("user", memberA, "2026-02-01", &to), lead, newMember("user", memberC, "2026-02-01", &to)} {
		if _, _, err := s.AddTeamMember(ctx, team.ID, in, today, ""); err != nil {
			t.Fatal(err)
		}
	}
	list := func(f store.TeamMemberFilter) []string {
		t.Helper()
		page, err := s.ListTeamMembers(ctx, team.ID, f, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, item := range page.Items {
			out = append(out, item.MemberID[len(item.MemberID)-2:])
		}
		return out
	}
	asOf := func(v string) *time.Time { d := dateOn(t, v); return &d }
	for name, tc := range map[string]struct {
		filter store.TeamMemberFilter
		want   []string
	}{
		"all":            {store.TeamMemberFilter{}, []string{"a1", "a2", "a3"}},
		"as of in range": {store.TeamMemberFilter{AsOf: asOf("2026-03-01")}, []string{"a1", "a3"}},
		"as of last day": {store.TeamMemberFilter{AsOf: asOf("2026-06-30")}, []string{"a1", "a3"}},
		"as of gap":      {store.TeamMemberFilter{AsOf: asOf("2026-07-01")}, nil},
		"as of later":    {store.TeamMemberFilter{AsOf: asOf("2026-09-01")}, []string{"a2"}},
		"role":           {store.TeamMemberFilter{MemberRole: "lead"}, []string{"a2"}},
		"kind":           {store.TeamMemberFilter{MemberKind: "party"}, nil},
	} {
		if got := list(tc.filter); !slices.Equal(got, tc.want) {
			t.Fatalf("%s = %v; want %v", name, got, tc.want)
		}
	}
	var paged []string
	after := ""
	for pages := 0; ; pages++ {
		if pages > 3 {
			t.Fatal("paging did not terminate")
		}
		page, err := s.ListTeamMembers(ctx, team.ID, store.TeamMemberFilter{}, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			paged = append(paged, item.MemberID[len(item.MemberID)-2:])
		}
		if page.NextID == "" {
			break
		}
		after = page.NextID
	}
	if want := []string{"a1", "a2", "a3"}; !slices.Equal(paged, want) {
		t.Fatalf("paged = %v; want %v", paged, want)
	}
}

func TestPostgres_TeamMembersAreTenantIsolated(t *testing.T) {
	s, ctx, admin, team := memberFixture(t)
	to := "2026-06-30"
	if _, _, err := s.AddTeamMember(ctx, team.ID, newMember("user", memberA, "2026-02-01", &to), dateOn(t, "2026-03-01"), ""); err != nil {
		t.Fatal(err)
	}
	other := principalContext(2)
	if _, _, err := s.AddTeamMember(other, team.ID, newMember("user", memberB, "2026-02-01", &to), dateOn(t, "2026-03-01"), ""); problemCode(t, err) != store.CodeTeamNotFound {
		t.Fatalf("tenant B add error = %v; want %s", err, store.CodeTeamNotFound)
	}
	if _, err := s.ListTeamMembers(other, team.ID, store.TeamMemberFilter{}, "", 10); problemCode(t, err) != store.CodeTeamNotFound {
		t.Fatalf("tenant B list error = %v; want %s", err, store.CodeTeamNotFound)
	}
	var n int
	if err := admin.QueryRow(ctx, `select count(*) from team_member`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("members = %d, %v; want 1", n, err)
	}
}

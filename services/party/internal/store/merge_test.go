package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

type mergeFixture struct {
	app                  *pgxpool.Pool
	ctx                  context.Context
	merger               context.Context
	winner, loser, payer string
}

func mergerContext(tenantNumber int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantNumber), UserID: "merger", Duties: map[string]struct{}{store.DutyMerge: {}}})
}

func seedMerge(t *testing.T, loserLegalEntity string) mergeFixture {
	t.Helper()
	f := mergeFixture{app: migratedApp(t), ctx: principalContext(1, ""), merger: mergerContext(1)}
	tenantID := testkit.TenantID(1)
	var err error
	for i, target := range []*string{&f.winner, &f.loser, &f.payer} {
		if *target, err = insertParty(f.ctx, f.app, tenantID, "P-"+string(rune('1'+i))); err != nil {
			t.Fatal(err)
		}
	}
	roles := []struct{ party, legalEntity, account string }{
		{f.winner, legalEntityA, "CUST-W"}, {f.loser, loserLegalEntity, "CUST-L"}, {f.payer, legalEntityA, "CUST-P"},
	}
	for _, r := range roles {
		if err := insertCustomerRole(f.ctx, f.app, tenantID, r.party, r.legalEntity, r.account); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, f, `update party set time_zone = 'UTC' where id = $1`, f.winner)
	exec(t, f, `update party set time_zone = 'CET', org_trade_name = 'Acme Trading' where id = $1`, f.loser)
	exec(t, f, `update customer_role set invoice_account_party_id = $1 where party_id = $2`, f.loser, f.payer)
	return f
}

func exec(t *testing.T, f mergeFixture, sql string, args ...any) {
	t.Helper()
	err := dbx.WithTenantTx(f.ctx, f.app, func(tx pgx.Tx) error {
		_, err := tx.Exec(f.ctx, sql, args...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, f mergeFixture, sql string, args ...any) string {
	t.Helper()
	var out string
	err := dbx.WithTenantTx(f.ctx, f.app, func(tx pgx.Tx) error {
		return tx.QueryRow(f.ctx, sql, args...).Scan(&out)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMerge_RewritesAtomicallyAndAuditIsClean(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	before, err := s.Audit(f.merger)
	if err != nil || !before.Clean() || before.CustomerRoles != 3 {
		t.Fatalf("pre-merge audit = %+v, %v; want clean with 3 roles", before, err)
	}

	loserRole := scalar(t, f, `select id::text from customer_role where party_id = $1`, f.loser)
	rules := map[string]domain.Rule{"time_zone": domain.KeepLoser, "org_trade_name": domain.NonEmpty}
	res, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser, Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if res.RolesMoved != 1 || res.ReferencesRewritten != 1 {
		t.Fatalf("result = %+v, want 1 role moved and 1 reference rewritten", res)
	}

	after, err := s.Audit(f.merger)
	if err != nil || !after.Clean() || after.CustomerRoles != before.CustomerRoles {
		t.Fatalf("post-merge audit = %+v, %v; want clean with %d roles", after, err, before.CustomerRoles)
	}
	checks := []struct {
		name, query, want string
		args              []any
	}{
		{"winner roles", `select count(*)::text from customer_role where party_id = $1`, "2", []any{f.winner}},
		{"loser roles", `select count(*)::text from customer_role where party_id = $1`, "0", []any{f.loser}},
		{"references to winner", `select count(*)::text from customer_role where invoice_account_party_id = $1`, "1", []any{f.winner}},
		{"references to loser", `select count(*)::text from customer_role where invoice_account_party_id = $1`, "0", []any{f.loser}},
		{"loser soft-merged", `select status || ':' || (merged_into_id = $2)::text from party where id = $1`, "merged:true", []any{f.loser, f.winner}},
		{"survivorship", `select time_zone || '/' || org_trade_name from party where id = $1`, "CET/Acme Trading", []any{f.winner}},
		{"outbox event", `select count(*)::text from outbox where type = 'dev.passage.party.party.merged.v1' and aggregate_id = $1`, "1", []any{f.winner}},
		{"merge record", `select count(*)::text from party_merge where merged_party_id = $1 and survivor_party_id = $2`, "1", []any{f.loser, f.winner}},
	}
	for _, c := range checks {
		if got := scalar(t, f, c.query, c.args...); got != c.want {
			t.Errorf("%s = %q, want %q", c.name, got, c.want)
		}
	}

	var payload domain.PartyMerged
	if err := json.Unmarshal([]byte(scalar(t, f, `select (payload->'data')::text from outbox`)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SurvivorPartyID != f.winner || !slices.Equal(payload.MergedPartyIDs, []string{f.loser}) || payload.MergeID != res.MergeID {
		t.Errorf("payload = %+v, want survivor %s, merged [%s], merge %s", payload, f.winner, f.loser, res.MergeID)
	}
	wantMap := []domain.RoleIDMapping{{Role: domain.RoleCustomer, FromID: loserRole, ToID: loserRole}}
	if !slices.Equal(payload.RoleIDMap, wantMap) {
		t.Errorf("roleIdMap = %+v, want %+v", payload.RoleIDMap, wantMap)
	}
	if got := scalar(t, f, `select (merged_at = $1)::text from party_merge`, payload.MergedAt); got != "true" {
		t.Errorf("event mergedAt %v differs from party_merge.merged_at", payload.MergedAt)
	}
}

func TestMerge_FailureAfterOutboxWriteRollsBackEverything(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	injected := errors.New("injected failure before commit")
	s := store.WithFailpoint(store.New(f.app), func() error { return injected })

	rules := map[string]domain.Rule{"time_zone": domain.KeepLoser}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser, Rules: rules}); !errors.Is(err, injected) {
		t.Fatalf("merge error = %v, want the injected failure", err)
	}

	report, err := store.New(f.app).Audit(f.merger)
	if err != nil || !report.Clean() || report.CustomerRoles != 3 {
		t.Fatalf("audit after rollback = %+v, %v; want clean with 3 roles", report, err)
	}
	if got := scalar(t, f, `select status || '/' || time_zone from party where id = $1`, f.loser); got != "active/CET" {
		t.Errorf("loser = %q, want untouched active/CET", got)
	}
	if got := scalar(t, f, `select time_zone from party where id = $1`, f.winner); got != "UTC" {
		t.Errorf("winner time_zone = %q, want UTC", got)
	}
	if got := scalar(t, f, `select count(*)::text from customer_role where party_id = $1`, f.loser); got != "1" {
		t.Errorf("loser roles = %s, want 1", got)
	}
	if got := scalar(t, f, `select count(*)::text from customer_role where invoice_account_party_id = $1`, f.loser); got != "1" {
		t.Errorf("references to loser = %s, want 1", got)
	}
	for _, table := range []string{"outbox", "party_merge"} {
		if got := count(f.ctx, t, f.app, table); got != 0 {
			t.Errorf("%s rows = %d, want 0", table, got)
		}
	}
}

func TestMerge_SameLegalEntityRoleConflictIsRejected(t *testing.T) {
	f := seedMerge(t, legalEntityA)
	_, err := store.New(f.app).Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser})
	if !errors.Is(err, store.ErrRoleConflict) {
		t.Fatalf("merge error = %v, want ErrRoleConflict", err)
	}
	if got := scalar(t, f, `select status from party where id = $1`, f.loser); got != "active" {
		t.Fatalf("loser status = %s, want active", got)
	}
}

func TestMerge_RejectsInvalidRequests(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	s := store.New(f.app)
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.winner}); !errors.Is(err, store.ErrNotMergeable) {
		t.Errorf("self merge = %v, want ErrNotMergeable", err)
	}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: strings.ToUpper(f.winner), LoserID: f.winner}); !errors.Is(err, store.ErrNotMergeable) {
		t.Errorf("upper-case winner equal to loser = %v, want ErrNotMergeable", err)
	}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: "not-a-uuid"}); !errors.Is(err, store.ErrInvalidID) {
		t.Errorf("malformed id = %v, want ErrInvalidID", err)
	}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: testkit.TenantID(9)}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown loser = %v, want ErrNotFound", err)
	}
	if _, err := s.Merge(mergerContext(2), store.MergeRequest{WinnerID: f.winner, LoserID: f.loser}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("other tenant = %v, want ErrNotFound", err)
	}
	noDuty := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u"})
	if _, err := s.Merge(noDuty, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser}); !errors.Is(err, store.ErrScopedPrincipal) {
		t.Errorf("no claim and no merge duty = %v, want ErrScopedPrincipal", err)
	}
	if _, err := s.Merge(principalContext(1, legalEntityA), store.MergeRequest{WinnerID: f.winner, LoserID: f.loser}); !errors.Is(err, store.ErrScopedPrincipal) {
		t.Errorf("scoped principal = %v, want ErrScopedPrincipal", err)
	}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Merge(f.merger, store.MergeRequest{WinnerID: f.winner, LoserID: f.loser}); !errors.Is(err, store.ErrNotMergeable) {
		t.Errorf("second merge of the same loser = %v, want ErrNotMergeable", err)
	}
}

func TestAudit_FlagsNonAtomicMerge(t *testing.T) {
	f := seedMerge(t, legalEntityB)
	exec(t, f, `update party set status = 'merged', merged_into_id = $2 where id = $1`, f.loser, f.winner)
	report, err := store.New(f.app).Audit(f.merger)
	if err != nil {
		t.Fatal(err)
	}
	want := store.IntegrityReport{CustomerRoles: 3, RolesOwnedByMerged: 1, InvoiceRefsToMerged: 1, MergedWithoutRecord: 1}
	if report != want || report.Clean() {
		t.Fatalf("audit = %+v, want %+v", report, want)
	}
}

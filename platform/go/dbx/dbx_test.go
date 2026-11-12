package dbx_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func principal(legalEntity string, duties ...string) tenant.Principal {
	p := tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: legalEntity, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return p
}

func TestLegalEntityScopes(t *testing.T) {
	leA, leB := testkit.TenantID(11), testkit.TenantID(12)
	cases := []struct {
		name string
		p    tenant.Principal
		want string
	}{
		{"single entity", principal(leA), leA},
		{"entity list", principal(leA + "," + leB), leA + "," + leB},
		{"entity wins over all-scope duty", principal(leA, tenant.DutyAllScope), leA},
		{"wildcard entity dropped", principal("*", tenant.DutyAllScope), ""},
		{"spaces trimmed and case folded", principal(" " + strings.ToUpper(leA) + " , " + leB), leA + "," + leB},
		{"wildcard in list dropped", principal(leA + ",*"), leA},
		{"all keyword dropped", principal("all", tenant.DutyAllScope), ""},
		{"no entity, no duty fails closed", principal(""), ""},
		{"no entity, unrelated duty fails closed", principal("", "org.read"), ""},
		{"no entity with all-scope duty", principal("", tenant.DutyAllScope), "*"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dbx.LegalEntityScopes(c.p); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestWithTenantTxLegalEntityVisibility(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	leA, leB := testkit.TenantID(11), testkit.TenantID(12)
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	ddl := `grant usage on schema ` + schema + ` to passage_app;create table scoped (tenant_id uuid not null, legal_entity_id uuid not null);` +
		dbx.RLSPolicy("scoped") + dbx.LegalEntityScopePolicy("scoped") +
		`grant select, insert on scoped to passage_app;`
	if _, err := admin.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	for _, le := range []string{leA, leB} {
		if _, err := admin.Exec(ctx, `insert into scoped values ($1, $2)`, testkit.TenantID(1), le); err != nil {
			t.Fatal(err)
		}
	}
	count := func(p tenant.Principal) int {
		var n int
		err := dbx.WithTenantTx(tenant.With(ctx, p), app, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `select count(*) from scoped`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(principal("")); got != 0 {
		t.Fatalf("no-entity principal saw %d rows, want 0", got)
	}
	if got := count(principal("", tenant.DutyAllScope)); got != 2 {
		t.Fatalf("all-scope principal saw %d rows, want 2", got)
	}
	if got := count(principal(leA)); got != 1 {
		t.Fatalf("single-entity principal saw %d rows, want 1", got)
	}
}

func TestTenantWidePredicate(t *testing.T) {
	const duty = "x.maintain"
	holds := map[string]struct{}{duty: {}, tenant.DutyTenantAdmin: {}}
	cases := []struct {
		name string
		p    tenant.Principal
		want bool
	}{
		{"no claim, duty and tenant.administer", tenant.Principal{Duties: holds}, true},
		{"no claim and duty without tenant.administer", tenant.Principal{Duties: map[string]struct{}{duty: {}}}, false},
		{"no claim and tenant.administer without duty", tenant.Principal{Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}}}, false},
		{"claim and duty", tenant.Principal{LegalEntityID: testkit.TenantID(9), Duties: holds}, false},
		{"no claim and no duty", tenant.Principal{}, false},
		{"other duty only", tenant.Principal{Duties: map[string]struct{}{"y": {}}}, false},
	}
	for _, c := range cases {
		if got := dbx.TenantWide(c.p, duty); got != c.want {
			t.Errorf("%s: TenantWide = %v, want %v", c.name, got, c.want)
		}
		if c.want {
			continue
		}
		if err := dbx.WithTenantWideAdminTx(context.Background(), nil, c.p, duty, func(pgx.Tx) error { return nil }); !errors.Is(err, dbx.ErrNotTenantWide) {
			t.Errorf("%s: err = %v, want ErrNotTenantWide", c.name, err)
		}
	}
}

func TestWithTenantTxReleasesTheConnectionWhenTheClosurePanics(t *testing.T) {
	_, app := testkit.DBWithApp(t)
	cfg := app.Config().Copy()
	cfg.MaxConns = 1
	ctx := context.Background()
	single, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic must reach the caller")
			}
		}()
		_ = dbx.WithTenantTx(tenant.With(ctx, principal("")), single, func(pgx.Tx) error { panic("closure failed") })
	}()

	timeout, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var one int
	if err := single.QueryRow(timeout, `select 1`).Scan(&one); err != nil {
		t.Fatalf("the only pooled connection was not released after a panic: %v", err)
	}
	if n := single.Stat().AcquiredConns(); n != 0 {
		t.Fatalf("%d connections still acquired", n)
	}
}

// Under a profile that requires verified TLS, Connect refuses a weaker sslmode before it dials (#602).
func TestConnectRefusesWeakTLSUnderAGovernmentProfile(t *testing.T) {
	t.Setenv("PASSAGE_CLOUD_PROFILE", "gcc-high")
	_, err := dbx.Connect(context.Background(), "postgres://u:p@127.0.0.1:1/x?sslmode=disable")
	if err == nil || !strings.Contains(err.Error(), "verify-full") {
		t.Fatalf("Connect err = %v, want a verify-full refusal", err)
	}
	t.Setenv("PASSAGE_CLOUD_PROFILE", "no-such-profile")
	if _, err := dbx.Connect(context.Background(), "postgres://u:p@127.0.0.1:1/x"); err == nil || !strings.Contains(err.Error(), "unknown cloud profile") {
		t.Fatalf("a typo in the profile name must fail closed, got %v", err)
	}
}

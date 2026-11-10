package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func TestDemoCompany_Postgres_LoadsOnceAndIsRepeatable(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	call := func(duties string) (int, api.DemoCompanyResult) {
		rec := doWithoutTenantAdmin(r, http.MethodPost, "/v1/org/demo-company", "", duties, true)
		var out api.DemoCompanyResult
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, _ := call("org.demo-company.manage"); code != http.StatusForbidden {
		t.Fatalf("without tenant.administer got %d, want 403", code)
	}
	if code, _ := call("org.read,tenant.administer"); code != http.StatusForbidden {
		t.Fatalf("without the duty got %d, want 403", code)
	}
	code, first := call("org.demo-company.manage,tenant.administer")
	if code != http.StatusOK || first.Created == 0 || first.Package != "demo-manufacturer" {
		t.Fatalf("first load = %d %+v", code, first)
	}
	code, second := call("org.demo-company.manage,tenant.administer")
	if code != http.StatusOK || second.Created != 0 || second.Present != first.Created+first.Present {
		t.Fatalf("second load = %d %+v, want everything present and nothing created", code, second)
	}
}

const demoDuties = "org.demo-company.manage,tenant.administer"

type demoEnv struct {
	t     *testing.T
	admin *pgxpool.Pool
	st    *store.Postgres
	r     http.Handler
	ctx   context.Context
}

func newDemoEnv(t *testing.T) demoEnv {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	r, _ := memRouterFor(st)
	tctx := tenant.With(ctx, tenant.Principal{TenantID: testkit.TenantID(1), UserID: "admin-1", Duties: testkit.ScopeDuties("")})
	return demoEnv{t: t, admin: admin, st: st, r: r, ctx: tctx}
}

func (e demoEnv) post(path, duties string) (int, []byte) {
	rec := doWithoutTenantAdmin(e.r, http.MethodPost, path, "", duties, true, "X-Passage-User", "admin-1")
	return rec.Code, rec.Body.Bytes()
}

func (e demoEnv) reset() (int, api.DemoCompanyResetResult, []byte) {
	code, body := e.post("/v1/org/demo-company:reset", demoDuties)
	var out api.DemoCompanyResetResult
	_ = json.Unmarshal(body, &out)
	return code, out, body
}

func (e demoEnv) entityIDs() map[string]string {
	e.t.Helper()
	page, err := e.st.ListLegalEntities(e.ctx, store.LegalEntityFilter{}, "", 100)
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]string{}
	for _, le := range page.Items {
		out[le.Code] = le.ID
	}
	return out
}

func (e demoEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestDemoCompanyReset_Postgres_RemovesAndReloadsOnlyDemoRecords(t *testing.T) {
	e := newDemoEnv(t)
	if code, _ := e.post("/v1/org/demo-company:reset", "org.demo-company.manage"); code != http.StatusForbidden {
		t.Fatalf("reset without tenant.administer got %d, want 403", code)
	}
	if code, _ := e.post("/v1/org/demo-company:reset", "org.read,tenant.administer"); code != http.StatusForbidden {
		t.Fatalf("reset without the duty got %d, want 403", code)
	}
	code, body := e.post("/v1/org/demo-company", demoDuties)
	var loaded api.DemoCompanyResult
	_ = json.Unmarshal(body, &loaded)
	if code != http.StatusOK || loaded.Created == 0 {
		t.Fatalf("load = %d %s", code, body)
	}
	own, err := e.st.CreateLegalEntity(e.ctx, store.LegalEntity{Code: "ACME", Name: "Acme Live", Country: "US", Currency: "USD", TimeZone: "UTC"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ownSite, err := e.st.CreateSite(e.ctx, store.Site{LegalEntityID: own.ID, Code: "HQ", Name: "Head office", DefaultWarehouse: &store.Warehouse{Code: "HQ-MAIN", Name: "HQ stock"}}, "own-site")
	if err != nil {
		t.Fatal(err)
	}
	before := e.entityIDs()
	usmf := before["USMF"]
	renamed := "Renamed during the demo"
	if _, err := e.st.UpdateLegalEntity(e.ctx, usmf, 1, store.LegalEntityPatch{Name: &renamed}); err != nil {
		t.Fatal(err)
	}

	code, reset, body := e.reset()
	if code != http.StatusOK || reset.Removed == 0 || reset.Present != 0 || reset.Created != loaded.Created {
		t.Fatalf("reset = %d %s, want everything removed and created again (%d)", code, body, loaded.Created)
	}
	after := e.entityIDs()
	if after["ACME"] != own.ID {
		t.Fatalf("the tenant's own legal entity was touched: %v", after)
	}
	if got, err := e.st.GetSite(e.ctx, ownSite.ID); err != nil || got.DefaultWarehouseID == nil {
		t.Fatalf("the tenant's own site and its default warehouse must stay: %+v %v", got, err)
	}
	if after["USMF"] == "" || after["USMF"] == usmf {
		t.Fatalf("USMF must be created again with a new id, got %q (was %q)", after["USMF"], usmf)
	}
	if le, err := e.st.GetLegalEntity(e.ctx, after["USMF"]); err != nil || le.Name != "Demo Manufacturing USA" {
		t.Fatalf("USMF after reset = %+v %v, want the demo name back", le, err)
	}
	if n := e.count(`select count(*) from outbox where type = 'dev.passage.org.democompany.removed.v1' and payload->'data'->>'actor' = 'admin-1'`); n != 1 {
		t.Fatalf("removed events naming the actor = %d, want 1", n)
	}
	if n := e.count(`select count(*) from warehouse where tenant_id = $1 and code in ('DET-MAIN', 'AUS-MAIN')`, testkit.TenantID(1)); n != 2 {
		t.Fatalf("default warehouses after reset = %d, want 2", n)
	}

	code, again, body := e.reset()
	if code != http.StatusOK || again.Removed != reset.Removed || again.Created != loaded.Created {
		t.Fatalf("second reset = %d %s, want the same counts", code, body)
	}
}

func TestDemoCompanyReset_Postgres_RefusesWhenOtherRecordsHangOffTheDemo(t *testing.T) {
	e := newDemoEnv(t)
	if code, body := e.post("/v1/org/demo-company", demoDuties); code != http.StatusOK {
		t.Fatalf("load = %d %s", code, body)
	}
	usmf := e.entityIDs()["USMF"]
	if _, err := e.st.CreateSite(e.ctx, store.Site{LegalEntityID: usmf, Code: "FLT", Name: "Flint, added by a user"}, ""); err != nil {
		t.Fatal(err)
	}
	code, _, body := e.reset()
	if code != http.StatusConflict || !strings.Contains(string(body), store.CodeDemoCompanyInUse) || !strings.Contains(string(body), "1 site") {
		t.Fatalf("reset = %d %s, want 409 %s naming the site", code, body, store.CodeDemoCompanyInUse)
	}
	if e.entityIDs()["USMF"] != usmf {
		t.Fatal("a refused reset must delete nothing")
	}
	if n := e.count(`select count(*) from outbox where type = 'dev.passage.org.democompany.removed.v1'`); n != 0 {
		t.Fatalf("a refused reset emitted %d removed events", n)
	}
}

func TestDemoCompanyReset_Postgres_LoadsTheDemoIntoATenantWithoutOne(t *testing.T) {
	e := newDemoEnv(t)
	code, out, body := e.reset()
	if code != http.StatusOK || out.Removed != 0 || out.Created == 0 {
		t.Fatalf("reset on an empty tenant = %d %s, want nothing removed and the demo loaded", code, body)
	}
}

func TestDemoCompanyReset_PassesStoreProblemsThrough(t *testing.T) {
	r, _ := newRouter(&fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodeDemoCompanyInUse, "in use")})
	rec := do(r, http.MethodPost, "/v1/org/demo-company:reset", "", "org.demo-company.manage", true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeDemoCompanyInUse) {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body.String())
	}
	scoped := do(r, http.MethodPost, "/v1/org/demo-company:reset", "", "org.demo-company.manage", true, "X-Passage-Legal-Entity", testkit.TenantID(9))
	if scoped.Code != http.StatusForbidden {
		t.Fatalf("a legal-entity scoped reset = %d, want 403", scoped.Code)
	}
}

func TestIdempotencyKey_SeedNamespaceIsReservedForTheLoader(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	body := `{"code":"ACME","name":"Acme","country":"US","currency":"USD","timeZone":"UTC"}`
	rec := do(r, http.MethodPost, "/v1/org/legal-entities", body, "org.legal-entity.write", true, "Idempotency-Key", "seed:demo-manufacturer:le:ACME")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "reserved") {
		t.Fatalf("a request that writes a seed key = %d %s, want 400 naming the reserved prefix", rec.Code, rec.Body.String())
	}
	if rec := do(r, http.MethodPost, "/v1/org/legal-entities", body, "org.legal-entity.write", true, "Idempotency-Key", "my-own-key"); rec.Code == http.StatusBadRequest {
		t.Fatalf("an ordinary key was refused: %s", rec.Body.String())
	}
}

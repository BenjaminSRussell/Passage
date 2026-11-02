package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/migrations"
)

func sqlProfile(name string) store.ConnectionProfile {
	return store.ConnectionProfile{Name: name, Kind: domain.ConnSQL, SourceSystem: "custom_acme", Status: domain.ConnActive,
		Config: map[string]any{"driver": "postgres", "host": "erp.acme.example", "port": float64(5432), "database": "erp", "user": "passage"}}
}

func testKeys(t *testing.T) *fieldcrypt.Keys {
	t.Helper()
	k, err := fieldcrypt.NewKeys("k1", map[string][]byte{"k1": bytes.Repeat([]byte{7}, 32), "k0": bytes.Repeat([]byte{3}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// exerciseConnections runs against Postgres (under the app role) and Mem so the fake keeps matching the contract.
func exerciseConnections(t *testing.T, s store.Store, one, two context.Context, tenantAware bool) {
	saved, created, err := s.SaveConnectionProfile(one, sqlProfile("Acme ERP"), "k1")
	if err != nil || !created || saved.ID == "" || saved.Version != 1 || saved.SecretSet || saved.Status != "active" || saved.Config["host"] != "erp.acme.example" {
		t.Fatalf("save = %+v %v %v", saved, created, err)
	}
	if again, created, err := s.SaveConnectionProfile(one, sqlProfile("Acme ERP"), "k1"); err != nil || created || again.ID != saved.ID {
		t.Fatalf("same key must replay: %+v %v %v", again, created, err)
	}
	_, _, err = s.SaveConnectionProfile(one, sqlProfile("Other"), "k1")
	problemStatus(t, err, 422, "idempotency_key_reuse")
	_, _, err = s.SaveConnectionProfile(one, sqlProfile("Acme ERP"), "k2")
	problemStatus(t, err, 409, store.CodeConnectionName)
	rest := store.ConnectionProfile{Name: "Acme API", Kind: domain.ConnREST, SourceSystem: "custom_acme", Status: domain.ConnActive, Config: map[string]any{"baseUrl": "https://api.acme.example"}}
	if _, _, err := s.SaveConnectionProfile(one, rest, ""); err != nil {
		t.Fatal(err)
	}

	if got, err := s.GetConnectionProfile(one, saved.ID); err != nil || got.Name != "Acme ERP" {
		t.Fatalf("get = %+v %v", got, err)
	}
	if _, err := s.GetConnectionProfile(one, "not-a-uuid"); err == nil {
		t.Fatal("a bad id must be not found")
	}
	page, err := s.ListConnectionProfiles(one, store.ConnectionFilter{Limit: 1})
	if err != nil || len(page) != 1 || page[0].Name != "Acme API" {
		t.Fatalf("first page = %+v %v", page, err)
	}
	next, err := s.ListConnectionProfiles(one, store.ConnectionFilter{Limit: 5, After: &store.ProjectCursor{Name: page[0].Name, ID: page[0].ID}})
	if err != nil || len(next) != 1 || next[0].ID != saved.ID {
		t.Fatalf("second page = %+v %v", next, err)
	}
	if only, _ := s.ListConnectionProfiles(one, store.ConnectionFilter{Kind: domain.ConnREST}); len(only) != 1 || only[0].Name != "Acme API" {
		t.Fatalf("kind filter = %+v", only)
	}

	renamed, err := s.UpdateConnectionProfile(one, saved.ID, 1, func(c *store.ConnectionProfile) error {
		c.Name = "Acme ERP prod"
		c.Config["host"] = "erp2.acme.example"
		return nil
	})
	if err != nil || renamed.Version != 2 || renamed.Name != "Acme ERP prod" || renamed.Config["host"] != "erp2.acme.example" {
		t.Fatalf("update = %+v %v", renamed, err)
	}
	_, err = s.UpdateConnectionProfile(one, saved.ID, 1, func(*store.ConnectionProfile) error { return nil })
	problemStatus(t, err, 409, store.CodeVersionConflict)
	_, err = s.UpdateConnectionProfile(one, saved.ID, 2, func(c *store.ConnectionProfile) error { c.Name = "Acme API"; return nil })
	problemStatus(t, err, 409, store.CodeConnectionName)
	if _, err := s.UpdateConnectionProfile(one, saved.ID, 2, func(*store.ConnectionProfile) error { return errors.New("rejected") }); err == nil {
		t.Fatal("an edit error must abort the update")
	}

	// The secret: a sealed value goes in, only secretSet and secretUpdatedAt come out.
	keys := testKeys(t)
	tp, _ := tenant.FromContext(one)
	field := "migration.connection_profile.secret:" + saved.ID
	const plain = "s3cret-Passw0rd!"
	sealed, err := keys.Seal(tp.TenantID, field, []byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	withSecret, err := s.SetConnectionSecret(one, saved.ID, sealed, keys.CurrentID())
	if err != nil || !withSecret.SecretSet || withSecret.SecretUpdatedAt == nil || withSecret.Version != 3 {
		t.Fatalf("set secret = %+v %v", withSecret, err)
	}
	got, _ := s.GetConnectionProfile(one, saved.ID)
	if !got.SecretSet || got.SecretUpdatedAt == nil {
		t.Fatalf("get after secret = %+v", got)
	}
	stored, keyID, err := s.ConnectionSecret(one, saved.ID)
	if err != nil || keyID != "k1" || bytes.Contains(stored, []byte(plain)) || len(stored) == 0 {
		t.Fatalf("stored secret must be ciphertext: %q %q %v", stored, keyID, err)
	}
	if opened, err := keys.Open(tp.TenantID, field, stored); err != nil || string(opened) != plain {
		t.Fatalf("open = %q %v", opened, err)
	}
	if none, _, err := s.ConnectionSecret(one, mustID(t, s, one, "Acme API")); err != nil || none != nil {
		t.Fatalf("an unset secret is nil: %q %v", none, err)
	}
	_, err = s.SetConnectionSecret(one, "00000000-0000-7000-8000-00000000ffff", sealed, "k1")
	problemStatus(t, err, 404, store.CodeConnectionNotFound)

	// Ciphertext bound to tenant 1 does not open as tenant 2, or for another profile.
	two1, _ := tenant.FromContext(two)
	if _, err := keys.Open(two1.TenantID, field, stored); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Fatalf("tenant 2 opened tenant 1's secret: %v", err)
	}
	if _, err := keys.Open(tp.TenantID, "migration.connection_profile.secret:"+ids.New(), stored); !errors.Is(err, fieldcrypt.ErrOpen) {
		t.Fatalf("another profile's binding opened the secret: %v", err)
	}

	// An edit keeps the secret unless it clears it; it can never set one.
	kept, err := s.UpdateConnectionProfile(one, saved.ID, 3, func(c *store.ConnectionProfile) error { c.Config["timezone"] = "UTC"; return nil })
	if err != nil || !kept.SecretSet || kept.SecretUpdatedAt == nil {
		t.Fatalf("an edit that does not clear keeps the secret: %+v %v", kept, err)
	}
	cleared, err := s.UpdateConnectionProfile(one, saved.ID, 4, func(c *store.ConnectionProfile) error {
		c.Config["host"] = "elsewhere.example"
		c.SecretSet = false
		return nil
	})
	if err != nil || cleared.SecretSet || cleared.SecretUpdatedAt != nil || cleared.Version != 5 {
		t.Fatalf("clear = %+v %v", cleared, err)
	}
	if gone, keyID, err := s.ConnectionSecret(one, saved.ID); err != nil || gone != nil || keyID != "" {
		t.Fatalf("a cleared secret is gone: %q %q %v", gone, keyID, err)
	}
	invented, err := s.UpdateConnectionProfile(one, saved.ID, 5, func(c *store.ConnectionProfile) error { c.SecretSet = true; return nil })
	if err != nil || invented.SecretSet {
		t.Fatalf("an edit must not mark a secret as set: %+v %v", invented, err)
	}
	if _, err := s.SetConnectionSecret(one, saved.ID, sealed, keys.CurrentID()); err != nil {
		t.Fatal(err)
	}

	if !tenantAware {
		return
	}
	if _, err := s.GetConnectionProfile(two, saved.ID); err == nil {
		t.Fatal("another tenant must not read the profile")
	}
	if list, _ := s.ListConnectionProfiles(two, store.ConnectionFilter{}); len(list) != 0 {
		t.Fatalf("tenant 2 lists %d profiles", len(list))
	}
	if _, _, err := s.ConnectionSecret(two, saved.ID); err == nil {
		t.Fatal("another tenant must not read the sealed secret")
	}
	if _, err := s.SetConnectionSecret(two, saved.ID, sealed, "k1"); err == nil {
		t.Fatal("another tenant must not set the secret")
	}
	if _, err := s.UpdateConnectionProfile(two, saved.ID, 3, func(*store.ConnectionProfile) error { return nil }); err == nil {
		t.Fatal("another tenant must not update the profile")
	}
}

func mustID(t *testing.T, s store.Store, ctx context.Context, name string) string {
	t.Helper()
	list, _ := s.ListConnectionProfiles(ctx, store.ConnectionFilter{})
	for _, c := range list {
		if c.Name == name {
			return c.ID
		}
	}
	t.Fatalf("no profile %q", name)
	return ""
}

func TestMem_ConnectionContract(t *testing.T) {
	exerciseConnections(t, store.NewMem(), principalContext(1), principalContext(2), false)
}

func TestPostgres_ConnectionProfilesUnderRLS(t *testing.T) {
	admin, pool := testkit.DBWithApp(t) // the app role proves the 0008 grants are enough for the store
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	exerciseConnections(t, store.New(pool), principalContext(1), principalContext(2), true)

	// The column holds ciphertext: read it as the table owner, past RLS, and look for the plaintext.
	var plainHits, rows int
	if err := admin.QueryRow(context.Background(), `select count(*) filter (where position('s3cret-Passw0rd!'::bytea in secret_sealed) > 0), count(*) filter (where secret_sealed is not null) from migration.connection_profile`).Scan(&plainHits, &rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || plainHits != 0 {
		t.Fatalf("sealed rows=%d plaintext hits=%d", rows, plainHits)
	}
	var cfgHits int
	if err := admin.QueryRow(context.Background(), `select count(*) from migration.connection_profile where config::text ilike '%s3cret%'`).Scan(&cfgHits); err != nil || cfgHits != 0 {
		t.Fatalf("config leaks the secret: %d %v", cfgHits, err)
	}
}

// Connection profiles are tenant-wide like every migration table.
func TestPostgres_ConnectionProfilesAreHiddenFromLegalEntityScopedCallers(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	saved, _, err := s.SaveConnectionProfile(principalContext(1), sqlProfile("Acme ERP"), "")
	if err != nil {
		t.Fatal(err)
	}
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leA, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if _, err := s.GetConnectionProfile(scoped, saved.ID); err == nil {
		t.Fatal("a legal-entity-scoped caller must not read the profile")
	}
	if list, _ := s.ListConnectionProfiles(scoped, store.ConnectionFilter{}); len(list) != 0 {
		t.Fatalf("a scoped caller lists %d profiles", len(list))
	}
	if _, _, err := s.SaveConnectionProfile(scoped, sqlProfile("scoped"), ""); err == nil {
		t.Fatal("a scoped caller must not create a profile")
	}
}

// 0008 widens the source system checks: a run, xref rows and a migrate project under custom_acme insert; a malformed
// name does not. The project reads from a profile, lands in a target legal entity, and the foreign key holds.
func TestPostgres_CustomSourceSystemRunXrefAndProject(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	drop := func() { _, _ = admin.Exec(context.Background(), "drop schema if exists migration cascade") }
	drop()
	t.Cleanup(drop)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	ctx := principalContext(1)

	prof, _, err := s.SaveConnectionProfile(ctx, sqlProfile("Acme ERP"), "")
	if err != nil {
		t.Fatal(err)
	}
	proj, _, err := s.SaveProject(ctx, store.Project{Name: "Acme migration", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive,
		TargetLegalEntityID: leB, ConnectionProfileID: prof.ID}, "")
	if err != nil || proj.ConnectionProfileID != prof.ID || proj.TargetLegalEntityID != leB || proj.SourceSystem != "custom_acme" {
		t.Fatalf("project = %+v %v", proj, err)
	}
	if got, _ := s.GetProject(ctx, proj.ID); got.ConnectionProfileID != prof.ID {
		t.Fatalf("get project = %+v", got)
	}
	detached, err := s.UpdateProject(ctx, proj.ID, 1, func(p *store.Project) error { p.ConnectionProfileID = ""; return nil })
	if err != nil || detached.ConnectionProfileID != "" {
		t.Fatalf("detach = %+v %v", detached, err)
	}
	_, _, err = s.SaveProject(ctx, store.Project{Name: "Ghost", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive, ConnectionProfileID: ids.New()}, "")
	problemStatus(t, err, 422, store.CodeConnectionUnknown)
	if _, _, err := s.SaveProject(ctx, store.Project{Name: "Other tenant", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive, ConnectionProfileID: prof.ID}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SaveProject(principalContext(2), store.Project{Name: "Cross tenant", Type: domain.ProjectMigrate, SourceSystem: "custom_acme", Status: domain.ProjectActive, ConnectionProfileID: prof.ID}, ""); err == nil {
		t.Fatal("a project must not reference another tenant's profile")
	}

	run := sampleRun()
	run.SourceSystem, run.ProjectID = "custom_acme", proj.ID
	saved, err := s.SaveRun(ctx, run, "")
	if err != nil || saved.SourceSystem != "custom_acme" {
		t.Fatalf("run = %+v %v", saved, err)
	}
	key := domain.XrefKey{SourceSystem: "custom_acme", Entity: "customer", SourceKey: "C1"}
	if err := s.Xref().Put(ctx, key, domain.XrefEntry{TargetID: ids.New()}); err != nil {
		t.Fatalf("xref under custom_acme: %v", err)
	}
	if rows, err := s.ListXref(ctx, store.XrefFilter{SourceSystem: "custom_acme", Limit: 10}); err != nil || len(rows) != 1 {
		t.Fatalf("xref list = %+v %v", rows, err)
	}
	for _, bad := range []string{"custom_", "custom_Acme", "custom_acme-1", "acme", "custom_" + string(bytes.Repeat([]byte("a"), 41))} {
		r := sampleRun()
		r.SourceSystem = bad
		if _, err := s.SaveRun(ctx, r, ""); err == nil {
			t.Errorf("run with source system %q must be refused", bad)
		}
		if err := s.Xref().Put(ctx, domain.XrefKey{SourceSystem: bad, Entity: "customer", SourceKey: "C1"}, domain.XrefEntry{TargetID: ids.New()}); err == nil {
			t.Errorf("xref with source system %q must be refused", bad)
		}
		if _, _, err := s.SaveProject(ctx, store.Project{Name: "bad " + bad[:min(len(bad), 10)], Type: domain.ProjectMigrate, SourceSystem: bad, Status: domain.ProjectActive}, ""); err == nil {
			t.Errorf("project with source system %q must be refused", bad)
		}
	}
	if _, _, err := s.SaveConnectionProfile(ctx, store.ConnectionProfile{Name: "bad", Kind: domain.ConnFile, SourceSystem: "custom_BAD", Status: domain.ConnActive, Config: map[string]any{}}, ""); err == nil {
		t.Error("a profile with a bad source system must be refused")
	}
}

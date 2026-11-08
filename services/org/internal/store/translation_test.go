package store_test

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

type translationWorld struct {
	warehouseWorld
	ou     store.OperatingUnit
	owners []translationOwnerCase
}

type translationOwnerCase struct {
	ownerType, id, notFound string
}

func newTranslationWorld(t *testing.T) translationWorld {
	t.Helper()
	w := translationWorld{warehouseWorld: newWarehouseWorld(t)}
	w.ou = mustCreateUnit(t, w.s, w.ctx, unit("department", "D100"))
	w.owners = []translationOwnerCase{
		{"legal_entity", w.usmf.ID, store.CodeLegalEntityNotFound},
		{"operating_unit", w.ou.ID, store.CodeOUNotFound},
		{"site", w.s1.ID, store.CodeSiteNotFound},
		{"warehouse", w.wh1.ID, store.CodeWarehouseNotFound},
	}
	return w
}

func (w translationWorld) put(t *testing.T, ownerType, id, language, name string) (store.NameTranslation, bool) {
	t.Helper()
	row, created, err := w.s.PutNameTranslation(w.ctx, ownerType, id, language, name)
	if err != nil {
		t.Fatal(err)
	}
	return row, created
}

func (w translationWorld) rowCount(t *testing.T) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(w.ctx, w.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(w.ctx, `select count(*) from org_unit_translation`).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNameTranslation_PutCreatesReplacesAndIgnoresIdenticalRepeats(t *testing.T) {
	w := newTranslationWorld(t)
	for _, o := range w.owners {
		t.Run(o.ownerType, func(t *testing.T) {
			first, created := w.put(t, o.ownerType, o.id, "fr-CA", "Passage Fabrication")
			if !created || !first.Changed || first.OwnerType != o.ownerType || first.OwnerID != o.id || first.Language != "fr-CA" {
				t.Fatalf("first put = %+v created %v", first, created)
			}
			same, created := w.put(t, o.ownerType, o.id, "fr-CA", "Passage Fabrication")
			if created || same.Changed || !same.UpdatedAt.Equal(first.UpdatedAt) {
				t.Fatalf("identical repeat = %+v created %v; want an unchanged row", same, created)
			}
			renamed, created := w.put(t, o.ownerType, o.id, "fr-CA", "Passage Fabrication QC")
			if created || !renamed.Changed || renamed.Name != "Passage Fabrication QC" || !renamed.CreatedAt.Equal(first.CreatedAt) {
				t.Fatalf("replace = %+v created %v", renamed, created)
			}
			w.put(t, o.ownerType, o.id, "de", "Passage Fertigung")
			w.put(t, o.ownerType, o.id, "fr", "Passage")
			items, err := w.s.ListNameTranslations(w.ctx, o.ownerType, o.id)
			if err != nil || len(items) != 3 || items[0].Language != "de" || items[1].Language != "fr" || items[2].Language != "fr-CA" {
				t.Fatalf("list = %+v, %v; want de, fr, fr-CA", items, err)
			}
		})
	}
	empty, err := w.s.ListNameTranslations(w.ctx, "site", w.s1b.ID)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("owner without rows = %#v, %v; want an empty slice", empty, err)
	}
}

func TestNameTranslation_RefusesUnknownAndDeactivatedLanguagesWithoutWriting(t *testing.T) {
	w := newTranslationWorld(t)
	if _, _, err := w.s.PutNameTranslation(w.ctx, "site", w.s1.ID, "xx-XX", "Nope"); problemCode(t, err) != store.CodeTranslationLanguageInvalid {
		t.Fatalf("unknown language error = %v", err)
	}
	if _, err := w.s.SetReferenceStatus(w.ctx, store.KindLanguage, "fr", "inactive"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.PutNameTranslation(w.ctx, "site", w.s1.ID, "fr-CA", "Nope"); problemCode(t, err) != store.CodeTranslationLanguageInvalid {
		t.Fatalf("deactivated language error = %v", err)
	}
	if n := w.rowCount(t); n != 0 {
		t.Fatalf("rows after refusals = %d; want 0", n)
	}
}

func TestNameTranslation_DeactivatedLanguageStillResolvesAndInactiveOwnerAcceptsPuts(t *testing.T) {
	w := newTranslationWorld(t)
	w.put(t, "site", w.s1.ID, "fr", "Site français")
	if _, err := w.s.SetReferenceStatus(w.ctx, store.KindLanguage, "fr", "inactive"); err != nil {
		t.Fatal(err)
	}
	if got, err := w.s.ResolveOrgUnitName(w.ctx, "site", w.s1.ID, "fr"); err != nil || got.Name != "Site français" || got.Source != "translation" {
		t.Fatalf("resolve after deactivation = %+v, %v", got, err)
	}
	inactive := "inactive"
	if _, err := w.s.UpdateSite(w.ctx, w.s1b.ID, 1, store.SitePatch{Status: &inactive}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.s.PutNameTranslation(w.ctx, "site", w.s1b.ID, "de", "Inaktiv"); err != nil {
		t.Fatalf("put for an inactive site: %v", err)
	}
}

func TestNameTranslation_DeleteThenRepeat(t *testing.T) {
	w := newTranslationWorld(t)
	w.put(t, "warehouse", w.wh1.ID, "fr-CA", "Entrepôt")
	if err := w.s.DeleteNameTranslation(w.ctx, "warehouse", w.wh1.ID, "fr-CA"); err != nil {
		t.Fatal(err)
	}
	if err := w.s.DeleteNameTranslation(w.ctx, "warehouse", w.wh1.ID, "fr-CA"); problemCode(t, err) != store.CodeTranslationNotFound {
		t.Fatalf("repeat delete error = %v", err)
	}
	if n := w.rowCount(t); n != 0 {
		t.Fatalf("rows after delete = %d", n)
	}
}

func TestNameTranslation_ResolveFollowsFallbackAndTracksBaseRenames(t *testing.T) {
	w := newTranslationWorld(t)
	resolve := func(tag string) store.NameResolution {
		t.Helper()
		got, err := w.s.ResolveOrgUnitName(w.ctx, "site", w.s1.ID, tag)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := resolve("fr-CA"); got.Source != "base" || got.Language != "" || got.Name != w.s1.Name {
		t.Fatalf("no translations = %+v", got)
	}
	w.put(t, "site", w.s1.ID, "fr", "Site")
	if got := resolve("fr-CA"); got.Source != "translation" || got.Language != "fr" || got.Name != "Site" {
		t.Fatalf("primary subtag = %+v", got)
	}
	w.put(t, "site", w.s1.ID, "fr-CA", "Site QC")
	if got := resolve("fr-CA"); got.Language != "fr-CA" || got.Name != "Site QC" {
		t.Fatalf("exact tag = %+v", got)
	}
	if got := resolve(""); got.Source != "base" || got.Name != w.s1.Name {
		t.Fatalf("empty tag = %+v", got)
	}
	if got := resolve("de"); got.Source != "base" {
		t.Fatalf("unmatched tag = %+v", got)
	}
	renamed := "Renamed site"
	if _, err := w.s.UpdateSite(w.ctx, w.s1.ID, 1, store.SitePatch{Name: &renamed}); err != nil {
		t.Fatal(err)
	}
	if got := resolve("de"); got.Source != "base" || got.Name != renamed {
		t.Fatalf("after base rename = %+v", got)
	}
}

func TestNameTranslation_IsolationAndLegalEntityScope(t *testing.T) {
	w := newTranslationWorld(t)
	for _, o := range w.owners {
		w.put(t, o.ownerType, o.id, "fr", "Nom")
	}
	other := principalContext(2)
	scoped := scopedTo(w.demf.ID)
	calls := map[string]func(ctx context.Context, o translationOwnerCase) error{
		"put": func(ctx context.Context, o translationOwnerCase) error {
			_, _, err := w.s.PutNameTranslation(ctx, o.ownerType, o.id, "de", "Hijacked")
			return err
		},
		"list": func(ctx context.Context, o translationOwnerCase) error {
			_, err := w.s.ListNameTranslations(ctx, o.ownerType, o.id)
			return err
		},
		"delete": func(ctx context.Context, o translationOwnerCase) error {
			return w.s.DeleteNameTranslation(ctx, o.ownerType, o.id, "fr")
		},
		"resolve": func(ctx context.Context, o translationOwnerCase) error {
			_, err := w.s.ResolveOrgUnitName(ctx, o.ownerType, o.id, "fr")
			return err
		},
	}
	for name, call := range calls {
		for _, o := range w.owners {
			if err := call(other, o); problemCode(t, err) != o.notFound {
				t.Fatalf("%s %s as tenant 2 error = %v; want %s", name, o.ownerType, err, o.notFound)
			}
			if o.ownerType == "operating_unit" {
				continue
			}
			if err := call(scoped, o); problemCode(t, err) != o.notFound {
				t.Fatalf("%s %s as DEMF caller error = %v; want %s", name, o.ownerType, err, o.notFound)
			}
		}
	}
	if _, created, err := w.s.PutNameTranslation(scoped, "operating_unit", w.ou.ID, "de", "Abteilung"); err != nil || !created {
		t.Fatalf("scoped put on an operating unit = %v created %v", err, created)
	}
	if items, err := w.s.ListNameTranslations(scoped, "operating_unit", w.ou.ID); err != nil || len(items) != 2 {
		t.Fatalf("scoped list on an operating unit = %+v, %v", items, err)
	}
	if _, _, err := w.s.PutNameTranslation(scoped, "site", w.s2.ID, "de", "Standort"); err != nil {
		t.Fatalf("scoped put on DEMF's own site: %v", err)
	}
	if n := w.rowCount(t); n != 6 {
		t.Fatalf("rows = %d; want the four seeded plus the two scoped puts", n)
	}
}

func TestNameTranslation_TableRejectsBadRows(t *testing.T) {
	w := newTranslationWorld(t)
	insert := `insert into org_unit_translation (tenant_id, owner_type, owner_id, language, name) values ($1, $2, $3, $4, $5)`
	for name, args := range map[string][]any{
		"owner type": {"location", w.s1.ID, "fr", "x"},
		"language":   {"site", w.s1.ID, "fr_CA", "x"},
		"empty name": {"site", w.s1.ID, "fr", ""},
		"long name":  {"site", w.s1.ID, "fr", strings.Repeat("x", 121)},
	} {
		args := append([]any{testkit.TenantID(1)}, args...)
		err := w.poolExec(w.ctx, insert, args...)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("%s: error = %v; want SQLSTATE 23514", name, err)
		}
	}
}

func TestNameTranslation_MigrationReappliesCleanly(t *testing.T) {
	admin, _ := testkit.DBWithApp(t)
	ctx := principalContext(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	script, err := fs.ReadFile(migrations.FS, "0025_org_unit_translation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, string(script)); err != nil {
		t.Fatalf("re-applying 0025: %v", err)
	}
}

// An empty owner type lists every translation of the tenant, the read the organization package
// export makes; a named owner still lists only its own.
func TestNameTranslation_ListWithoutOwnerListsEveryTranslation(t *testing.T) {
	w := newTranslationWorld(t)
	w.put(t, "operating_unit", w.ou.ID, "de", "Abteilung")
	w.put(t, "warehouse", w.wh1.ID, "fr", "Entrepot")
	w.put(t, "warehouse", w.wh1.ID, "de", "Lager")
	all, err := w.s.ListNameTranslations(w.ctx, "", "")
	if err != nil || len(all) != 3 || all[0].OwnerType != "operating_unit" || all[2].Language != "fr" {
		t.Fatalf("all = %+v %v", all, err)
	}
	one, err := w.s.ListNameTranslations(w.ctx, "warehouse", w.wh1.ID)
	if err != nil || len(one) != 2 || one[0].Language != "de" {
		t.Fatalf("warehouse = %+v %v", one, err)
	}
}

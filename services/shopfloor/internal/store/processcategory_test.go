package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// FR-25.17 and BR-25.13 through Postgres as passage_app: a tenant with no row gets the built-in
// defaults; a row of a built-in code overrides it (destinations, reject flag, definitions); a custom
// code is routed and checked by its row; an inactive row refuses messages (SF-0165); an unknown code
// is SF-0164; a row is versioned (SF-0143), its code unique (SF-0142), tenant-scoped, and every save
// writes processcategory.saved.
func TestProcessCategory_MasterOverridesDefaults(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(301)
	ctx := testkit.Ctx(tenantID)

	def, err := s.ResolveProcessCategory(ctx, "quality-result")
	if err != nil || len(def.Destinations) != 3 || def.RejectOutOfLimit {
		t.Fatalf("default = %+v, %v", def, err)
	}
	if _, err := s.ResolveProcessCategory(ctx, "pi-weigh"); problemCode(err) != CodeCategoryNotFound {
		t.Fatalf("unknown = %v, want %s", err, CodeCategoryNotFound)
	}
	listed, err := s.ListProcessCategories(ctx, false, 10)
	if err != nil || len(listed) != len(domain.DefaultCategoryCodes) || !listed[0].BuiltIn {
		t.Fatalf("defaults listed = %d, %v", len(listed), err)
	}

	weigh, err := s.CreateProcessCategory(ctx, domain.CategoryInput{Code: "pi-weigh", Name: "Weigh-in", Destinations: []string{"ebr", "kafka"}, RejectOutOfLimit: true, Active: true,
		Characteristics: []domain.CharacteristicDef{{Name: "weight", Type: "numeric", Unit: ptr("kg"), Min: ptr("9.5"), Max: ptr("10.5"), Required: true}}}, "sup", idem("c1"))
	if err != nil || weigh.Version != 1 || len(weigh.Characteristics) != 1 || weigh.Characteristics[0].Max == nil || *weigh.Characteristics[0].Max != "10.5" {
		t.Fatalf("create = %+v, %v", weigh, err)
	}
	if _, err := s.CreateProcessCategory(ctx, domain.CategoryInput{Code: "pi-weigh", Name: "Again", Destinations: []string{"ebr"}, Active: true}, "sup", idem("c2")); problemCode(err) != CodeSetupCodeTaken {
		t.Fatalf("duplicate code = %v, want %s", err, CodeSetupCodeTaken)
	}
	cat, err := s.ResolveProcessCategory(ctx, "pi-weigh")
	if err != nil || !cat.RejectOutOfLimit || len(cat.Destinations) != 2 || len(cat.Characteristics) != 1 {
		t.Fatalf("custom category = %+v, %v", cat, err)
	}
	if _, err := domain.ApplyLimits(cat, []domain.Characteristic{{Name: "weight", Type: "numeric", Value: "12"}}); err == nil {
		t.Fatal("a value outside the defined limits was accepted by a rejecting category")
	}

	// A row for a built-in code replaces the default: quality-result now goes to quality alone.
	over, err := s.CreateProcessCategory(ctx, domain.CategoryInput{Code: "quality-result", Name: "Quality result", Destinations: []string{"quality"}, Active: true}, "sup", idem("c3"))
	if err != nil {
		t.Fatal(err)
	}
	cat, err = s.ResolveProcessCategory(ctx, "quality-result")
	if err != nil || len(cat.Destinations) != 1 || cat.Destinations[0] != "quality" {
		t.Fatalf("override = %+v, %v", cat, err)
	}
	listed, err = s.ListProcessCategories(ctx, false, 10)
	if err != nil || len(listed) != 2+len(domain.DefaultCategoryCodes)-1 {
		t.Fatalf("list with an override = %d rows, %v", len(listed), err)
	}
	for _, c := range listed {
		if c.Code == "quality-result" && c.BuiltIn {
			t.Fatal("the built-in quality-result is still listed beside its override")
		}
	}

	if _, err := s.UpdateProcessCategory(ctx, over.ID, 9, domain.CategoryInput{Name: "x", Destinations: []string{"quality"}, Active: true}, "sup"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version = %v, want %s", err, CodeVersionMismatch)
	}
	off, err := s.UpdateProcessCategory(ctx, over.ID, over.Version, domain.CategoryInput{Name: "Quality result", Destinations: []string{"quality", "ebr"}, Active: false}, "sup")
	if err != nil || off.Active || off.Version != 2 || off.Code != "quality-result" || len(off.Destinations) != 2 {
		t.Fatalf("deactivate = %+v, %v", off, err)
	}
	if _, err := s.ResolveProcessCategory(ctx, "quality-result"); problemCode(err) != CodeCategoryInactive {
		t.Fatalf("inactive = %v, want %s", err, CodeCategoryInactive)
	}
	if _, err := s.UpdateProcessCategory(ctx, ids.New(), 1, domain.CategoryInput{Name: "x", Destinations: []string{"ebr"}}, "sup"); problemCode(err) != CodeCategoryNotFound {
		t.Fatalf("unknown id = %v", err)
	}
	if got, err := s.GetProcessCategory(ctx, weigh.ID); err != nil || got.Code != "pi-weigh" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if active, err := s.ListProcessCategories(ctx, true, 10); err != nil || len(active) != 1+len(domain.DefaultCategoryCodes)-1 {
		t.Fatalf("active list = %d rows, %v", len(active), err)
	}

	// Another tenant sees none of it.
	other := testkit.Ctx(testkit.TenantID(302))
	if _, err := s.GetProcessCategory(other, weigh.ID); problemCode(err) != CodeCategoryNotFound {
		t.Fatalf("other tenant get = %v", err)
	}
	if _, err := s.ResolveProcessCategory(other, "pi-weigh"); problemCode(err) != CodeCategoryNotFound {
		t.Fatalf("other tenant resolve = %v", err)
	}
	if n := countEvents(t, admin, tenantID, "dev.passage.shopfloor.processcategory.saved.v1"); n != 3 {
		t.Fatalf("processcategory.saved events = %d, want 3 (two creates and an update)", n)
	}
}

// A message is routed to the destinations of its category row: a custom category with one
// destination creates one delivery.
func TestProcessCategory_RoutesMessageByRow(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(303)
	ctx := testkit.Ctx(tenantID)
	if _, err := s.CreateProcessCategory(ctx, domain.CategoryInput{Code: "pi-note", Name: "Note", Destinations: []string{"ebr"}, Active: true}, "sup", idem("n1")); err != nil {
		t.Fatal(err)
	}
	cat, err := s.ResolveProcessCategory(ctx, "pi-note")
	if err != nil {
		t.Fatal(err)
	}
	raw := domain.NewMessage{Source: domain.SourceManual, SourceMessageID: "note-1", CategoryCode: "pi-note", OrderID: ids.New(), OccurredAt: day(9, 0)}
	res, err := s.IngestProcessMessage(ctx, raw, cat, day(9, 0))
	if err != nil || len(res.Message.Deliveries) != 1 || res.Message.Deliveries[0].Destination != domain.DestEBR {
		t.Fatalf("routed = %+v, %v", res.Message.Deliveries, err)
	}

	// The stored message is found by its source key, which is how a replay is answered after its
	// category was deactivated; another key is not.
	if got, ok, err := s.StoredProcessMessage(ctx, domain.SourceManual, "note-1"); err != nil || !ok || got.ID != res.Message.ID {
		t.Fatalf("stored = %+v, %v, %v", got, ok, err)
	}
	if _, ok, err := s.StoredProcessMessage(ctx, domain.SourceManual, "note-2"); err != nil || ok {
		t.Fatalf("unknown key found = %v, %v", ok, err)
	}

	// Migration 0020 relaxed the category check to the code format, not to any text: a code that
	// is not lowercase letters, digits and hyphens, or longer than 32, is refused by the database.
	insert := func(code string) error {
		bg := context.Background()
		tx, err := app.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err := tx.Exec(bg, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(bg, `insert into process_message (tenant_id, id, source, source_message_id, category_code, order_id, phase_path,
			characteristics, occurred_at, received_at, status) values ($1, $2, 'manual', $3, $4, $5, '', '[]', now(), now(), 'received')`,
			tenantID, ids.New(), "raw-"+ids.New(), code, ids.New())
		return err
	}
	for _, bad := range []string{"Bad Code", "", "-lead", "x';drop", "abcdefghijabcdefghijabcdefghijabc"} {
		var pgErr *pgconn.PgError
		if err := insert(bad); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("category_code %q = %v, want a check violation", bad, err)
		}
	}
	for _, good := range []string{"pi-weigh", "phase-start"} {
		if err := insert(good); err != nil {
			t.Errorf("category_code %q = %v", good, err)
		}
	}
}

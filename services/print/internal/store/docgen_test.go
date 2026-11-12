package store_test

import (
	"context"
	"errors"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

func TestDocgen_TemplatesBlocksAndOutputRulesUnderRLS(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenantCtx(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)

	tpl := domain.DocumentTemplate{Code: "INV-STD", Name: "Standard", DocumentType: domain.DocCustomerInvoice, Default: true,
		Config: domain.TemplateConfig{Title: "Tax invoice", HideColumns: []string{"Unit"}, NoteBlocks: []string{"TERMS"}}}
	saved, err := s.PutTemplate(ctx, tpl, 0, "designer-1")
	if err != nil || saved.Version != 1 || saved.Config.Title != "Tax invoice" {
		t.Fatalf("put: %+v %v", saved, err)
	}
	tpl.Config.Title = "Invoice"
	if again, err := s.PutTemplate(ctx, tpl, 1, "designer-2"); err != nil || again.Version != 2 {
		t.Fatalf("second put: %+v %v", again, err)
	}
	// A save based on version 1 after version 2 was saved is refused, and nothing is stored.
	var p *httpx.Problem
	if _, err := s.PutTemplate(ctx, tpl, 1, "designer-1"); !errors.As(err, &p) || p.Status != 412 || p.Code != domain.CodeTemplateStale {
		t.Fatalf("stale If-Match: %v", err)
	}
	hist, err := s.ListTemplateVersions(ctx, "INV-STD")
	if err != nil || len(hist) != 2 || hist[0].Version != 2 || hist[0].SavedBy != "designer-2" || hist[0].Config.Title != "Invoice" || hist[1].Config.Title != "Tax invoice" {
		t.Fatalf("history: %+v %v", hist, err)
	}
	if _, err := s.ListTemplateVersions(ctx, "NOPE"); !errors.As(err, &p) || p.Status != 404 {
		t.Fatalf("history of an unknown template: %v", err)
	}
	if _, err := s.ListTemplateVersions(tenantCtx(2), "INV-STD"); err == nil {
		t.Fatal("another tenant reads the history")
	}
	// A second default of the type takes over the flag.
	other := tpl
	other.Code = "INV-ALT"
	if _, err := s.PutTemplate(ctx, other, 0, ""); err != nil {
		t.Fatal(err)
	}
	def, err := s.DefaultTemplate(ctx, domain.DocCustomerInvoice)
	if err != nil || def == nil || def.Code != "INV-ALT" {
		t.Fatalf("default = %+v %v", def, err)
	}
	if first, _ := s.GetTemplate(ctx, "INV-STD"); first.Default {
		t.Fatal("two defaults for one document type")
	}
	wrongType := tpl
	wrongType.DocumentType = domain.DocPurchaseOrder
	if _, err := s.PutTemplate(ctx, wrongType, 0, ""); err == nil {
		t.Fatal("the document type of a template changed")
	}
	if none, err := s.DefaultTemplate(ctx, domain.DocPackingSlip); err != nil || none != nil {
		t.Fatalf("default of a type without one = %+v %v", none, err)
	}

	for _, b := range []domain.TextBlock{{Code: "TERMS", Language: "en", Body: "Due {{days}} days."}, {Code: "TERMS", Language: "de", Body: "Zahlbar in {{days}} Tagen."}} {
		if _, err := s.PutTextBlock(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := s.TextBlockVersions(ctx, "TERMS")
	if err != nil || len(versions) != 2 || versions[0].Language != "de" || versions[0].Placeholders[0] != "days" {
		t.Fatalf("versions: %+v %v", versions, err)
	}

	rule := domain.OutputRule{DocumentType: domain.DocCustomerInvoice, AccountID: "22222222-0000-4000-8000-000000000001", CopyKind: domain.CopyOriginal, Channel: domain.ChannelEmail, Destination: "ap@contoso.example", Copies: 1, Sequence: 1}
	created, inserted, err := s.CreateOutputRule(ctx, rule, "k-1")
	if err != nil || !inserted || created.ID == "" {
		t.Fatalf("create rule: %+v %v %v", created, inserted, err)
	}
	if replay, inserted, err := s.CreateOutputRule(ctx, rule, "k-1"); err != nil || inserted || replay.ID != created.ID {
		t.Fatalf("replay: %+v %v %v", replay, inserted, err)
	}
	rule.Destination = "other@contoso.example"
	if _, _, err := s.CreateOutputRule(ctx, rule, "k-1"); err == nil {
		t.Fatal("a different body under the same key was accepted")
	}

	// Another tenant sees none of it.
	other2 := tenantCtx(2)
	if ts, _ := s.ListTemplates(other2, ""); len(ts) != 0 {
		t.Fatalf("tenant 2 templates: %+v", ts)
	}
	if bs, _ := s.ListTextBlocks(other2); len(bs) != 0 {
		t.Fatalf("tenant 2 blocks: %+v", bs)
	}
	if rs, _ := s.ListOutputRules(other2); len(rs) != 0 {
		t.Fatalf("tenant 2 rules: %+v", rs)
	}
	if err := s.DeleteOutputRule(other2, created.ID, true); err == nil {
		t.Fatal("tenant 2 deleted tenant 1's rule")
	}
	if err := s.DeleteOutputRule(ctx, created.ID, true); err != nil {
		t.Fatal(err)
	}
}

func TestDocgen_OutputRulesFollowTheLegalEntityScope(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	base := tenantCtx(1)
	if err := dbx.Migrate(base, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	const leOne, leTwo = "11111111-0000-4000-8000-0000000000e1", "11111111-0000-4000-8000-0000000000e2"
	as := func(le string) context.Context {
		p, _ := tenant.FromContext(base)
		p.LegalEntityID = le
		return tenant.With(base, p)
	}
	rule := func(le string) domain.OutputRule {
		return domain.OutputRule{DocumentType: domain.DocPackingSlip, LegalEntityID: le, CopyKind: domain.CopyOriginal, Channel: domain.ChannelArchive, Copies: 1, Sequence: 1}
	}
	one, _, err := s.CreateOutputRule(as(leOne), rule(leOne), "k1")
	if err != nil {
		t.Fatal(err)
	}
	global, _, err := s.CreateOutputRule(base, rule(""), "k2")
	if err != nil {
		t.Fatal(err)
	}
	// Writing a rule for another entity is refused by row-level security, whatever the API did.
	if _, _, err := s.CreateOutputRule(as(leTwo), rule(leOne), "k3"); err == nil {
		t.Fatal("entity two created a rule for entity one")
	}
	// Entity two sees the tenant-wide rule but not entity one's, and cannot delete it.
	got, err := s.ListOutputRules(as(leTwo))
	if err != nil || len(got) != 1 || got[0].ID != global.ID {
		t.Fatalf("entity two sees %+v %v", got, err)
	}
	if err := s.DeleteOutputRule(as(leTwo), one.ID, false); err == nil {
		t.Fatal("entity two deleted entity one's rule")
	}
	if err := s.DeleteOutputRule(as(leOne), global.ID, false); err == nil {
		t.Fatal("an entity administrator deleted the tenant-wide rule")
	}
	if err := s.DeleteOutputRule(as(leOne), one.ID, false); err != nil {
		t.Fatalf("entity one deleting its own rule: %v", err)
	}
	if err := s.DeleteOutputRule(base, global.ID, true); err != nil {
		t.Fatal(err)
	}
}

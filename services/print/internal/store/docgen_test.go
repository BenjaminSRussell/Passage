package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
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
	saved, err := s.PutTemplate(ctx, tpl)
	if err != nil || saved.Version != 1 || saved.Config.Title != "Tax invoice" {
		t.Fatalf("put: %+v %v", saved, err)
	}
	if again, err := s.PutTemplate(ctx, tpl); err != nil || again.Version != 2 {
		t.Fatalf("second put: %+v %v", again, err)
	}
	// A second default of the type takes over the flag.
	other := tpl
	other.Code = "INV-ALT"
	if _, err := s.PutTemplate(ctx, other); err != nil {
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
	if _, err := s.PutTemplate(ctx, wrongType); err == nil {
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

	rule := domain.OutputRule{DocumentType: domain.DocCustomerInvoice, AccountID: "cust-1", CopyKind: domain.CopyOriginal, Channel: domain.ChannelEmail, Destination: "ap@contoso.example", Copies: 1, Sequence: 1}
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
	if err := s.DeleteOutputRule(other2, created.ID); err == nil {
		t.Fatal("tenant 2 deleted tenant 1's rule")
	}
	if err := s.DeleteOutputRule(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
}

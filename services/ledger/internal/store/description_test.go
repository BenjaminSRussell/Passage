package store

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// withoutDescription posts a vendor invoice whose request omits the description
// and the line texts.
func withoutDescription(e env, key, amount string) PostInput {
	in := vendorInvoice(e.ledger.ID, key, amount)
	in.Description = ""
	return in
}

// tenantWide is a principal of the test tenant without a legal-entity scope:
// only such callers may maintain tenant templates.
func tenantWide(e env) env {
	e.ctx = principalCtx(e.tenant, "")
	return e
}

func putTemplate(t *testing.T, e env, in DescriptionTemplate) DescriptionTemplate {
	t.Helper()
	if in.Language == "" {
		in.Language = "en"
	}
	out, _, err := e.store.PutDescriptionTemplate(e.ctx, ids.New(), in)
	if err != nil {
		t.Fatalf("put template %+v: %v", in, err)
	}
	return out
}

func problemStatus(err error) (int, string) {
	var p *httpx.Problem
	if !errors.As(err, &p) {
		return 0, ""
	}
	return p.Status, p.Code
}

func TestPostgres_DescriptionTemplateCRUD(t *testing.T) {
	e, _ := setup(t)
	e = tenantWide(e)
	id := ids.New()
	in := DescriptionTemplate{Scope: ScopeTenant, SourceType: "manual_journal", Language: "en", Template: "Journal %1",
		FieldBindings: map[string]string{"1": "voucher"}}
	created, isNew, err := e.store.PutDescriptionTemplate(e.ctx, id, in)
	if err != nil || !isNew || created.ID != id || created.FieldBindings["1"] != "voucher" || created.LegalEntityID != nil {
		t.Fatalf("create: %+v new=%v err=%v", created, isNew, err)
	}
	in.Template = "Manual journal %1"
	replaced, isNew, err := e.store.PutDescriptionTemplate(e.ctx, id, in)
	if err != nil || isNew || replaced.Template != "Manual journal %1" {
		t.Fatalf("replace: %+v new=%v err=%v", replaced, isNew, err)
	}
	got, err := e.store.GetDescriptionTemplate(e.ctx, id)
	if err != nil || got.Template != replaced.Template {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, err := e.store.ListDescriptionTemplates(e.ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	_, err = e.store.GetDescriptionTemplate(e.ctx, ids.New())
	if status, code := problemStatus(err); status != http.StatusNotFound || code != "LEDGER_DESCRIPTION_TEMPLATE_NOT_FOUND" {
		t.Fatalf("missing template: %v", err)
	}
}

func TestPostgres_DescriptionTemplateScopeIsUnique(t *testing.T) {
	e, _ := setup(t)
	e = tenantWide(e)
	first := DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice", Language: "en", Template: "A %1",
		FieldBindings: map[string]string{"1": "source_id"}}
	putTemplate(t, e, first)
	_, _, err := e.store.PutDescriptionTemplate(e.ctx, ids.New(), first)
	if status, code := problemStatus(err); status != http.StatusConflict || code != "LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE" {
		t.Fatalf("a second tenant template for the same key must conflict, got %v", err)
	}
	// Another language, or the legal entity's own template, is a different key.
	putTemplate(t, e, DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice", Language: "de", Template: "B"})
	le := e.ledger.LegalEntityID
	putTemplate(t, e, DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le, SourceType: "vendor_invoice", Template: "C"})
	_, _, err = e.store.PutDescriptionTemplate(e.ctx, ids.New(), DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le,
		SourceType: "vendor_invoice", Language: "en", Template: "D"})
	if status, code := problemStatus(err); status != http.StatusConflict || code != "LEDGER_DESCRIPTION_TEMPLATE_DUPLICATE" {
		t.Fatalf("a second legal entity template for the same key must conflict, got %v", err)
	}
}

// AC-11.19: a tenant template for manual_journal "Journal %1 of %2" bound to
// {voucher} and {document_date} describes a manual journal posted without a description.
func TestPostgres_AC11_19_ManualJournalDefaultDescription(t *testing.T) {
	e, _ := setup(t)
	putTemplate(t, tenantWide(e), DescriptionTemplate{Scope: ScopeTenant, SourceType: "manual_journal",
		Template: "Journal %1 of %2", FieldBindings: map[string]string{"1": "voucher", "2": "document_date"}})
	in := withoutDescription(e, "GJ-1", "100.00")
	in.Source.System, in.Source.Type = "ledger", "manual_journal"
	docDate := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	in.DocumentDate = &docDate
	res, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	entry := res.Entries[0]
	want := "Journal " + entry.Voucher + " of 2026-03-31"
	if entry.Description != want {
		t.Fatalf("description %q, want %q", entry.Description, want)
	}
	stored, err := e.store.GetEntry(e.ctx, entry.ID)
	if err != nil || stored.Description != want {
		t.Fatalf("stored description %q (%v), want %q", stored.Description, err, want)
	}
	for _, ln := range stored.Lines {
		if ln.Text != want {
			t.Fatalf("line %d text %q, want the default description", ln.LineNumber, ln.Text)
		}
	}
	if len(res.Notices) != 0 {
		t.Fatalf("no notice expected, got %+v", res.Notices)
	}
}

func TestPostgres_DefaultDescriptionNeverReplacesExplicitText(t *testing.T) {
	e, _ := setup(t)
	putTemplate(t, tenantWide(e), DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice",
		Template: "Invoice %1 for %2 %3", FieldBindings: map[string]string{"1": "voucher", "2": "amount_txn", "3": "currency_txn"}})

	res, err := e.store.PostEntry(e.ctx, withoutDescription(e, "DESC-1", "1250.5"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	entry := res.Entries[0]
	if want := "Invoice " + entry.Voucher + " for 1250.50 USD"; entry.Description != want {
		t.Fatalf("description %q, want %q", entry.Description, want)
	}

	// An explicit description and explicit line text are kept; only the empty
	// line text takes the template.
	explicit := vendorInvoice(e.ledger.ID, "DESC-2", "5.00")
	explicit.Lines[0].Text = "Freight"
	got, err := e.store.PostEntry(e.ctx, explicit)
	if err != nil {
		t.Fatalf("post explicit: %v", err)
	}
	ent := got.Entries[0]
	if ent.Description != "Vendor invoice DESC-2" {
		t.Fatalf("explicit description changed: %q", ent.Description)
	}
	if ent.Lines[0].Text != "Freight" || ent.Lines[1].Text != "Invoice "+ent.Voucher+" for 5.00 USD" {
		t.Fatalf("line texts %q / %q", ent.Lines[0].Text, ent.Lines[1].Text)
	}

	// Without a template for the source type nothing is filled in.
	other := withoutDescription(e, "DESC-3", "1.00")
	other.Source.Type = "customer_invoice"
	plain, err := e.store.PostEntry(e.ctx, other)
	if err != nil {
		t.Fatalf("post untemplated: %v", err)
	}
	if plain.Entries[0].Description != "" || plain.Entries[0].Lines[0].Text != "" {
		t.Fatalf("untemplated entry got text: %+v", plain.Entries[0])
	}
}

func TestPostgres_DefaultDescriptionTruncationNotice(t *testing.T) {
	e, _ := setup(t)
	putTemplate(t, tenantWide(e), DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice",
		Template: strings.Repeat("x", 250) + "%1", FieldBindings: map[string]string{"1": "source_id"}})
	in := withoutDescription(e, "TRUNC-1", "2.00")
	res, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	d := res.Entries[0].Description
	if utf8.RuneCountInString(d) != domain.MaxDescriptionLength || !strings.HasSuffix(d, "…") {
		t.Fatalf("description has %d runes: %q", utf8.RuneCountInString(d), d)
	}
	if len(res.Notices) != 1 || res.Notices[0].Code != domain.CodeDescriptionTruncated {
		t.Fatalf("notices %+v, want LEDGER_DESCRIPTION_TRUNCATED", res.Notices)
	}
	// A replay returns the stored response, notice included.
	again, err := e.store.PostEntry(e.ctx, in)
	if err != nil || !again.Idempotency.Replayed || len(again.Notices) != 1 {
		t.Fatalf("replay: %+v %v", again, err)
	}
}

func TestPostgres_LegalEntityTemplateOverridesTenant(t *testing.T) {
	e, _ := setup(t)
	putTemplate(t, tenantWide(e), DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice",
		Template: "Tenant %1", FieldBindings: map[string]string{"1": "source_id"}})
	// Another legal entity's template never applies here.
	otherLE := ids.New()
	putTemplate(t, tenantWide(e), DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &otherLE,
		SourceType: "vendor_invoice", Template: "Other %1", FieldBindings: map[string]string{"1": "source_id"}})
	res, err := e.store.PostEntry(e.ctx, withoutDescription(e, "LE-0", "3.00"))
	if err != nil || res.Entries[0].Description != "Tenant LE-0" {
		t.Fatalf("tenant template must apply: %+v %v", res.Entries, err)
	}

	le := e.ledger.LegalEntityID
	putTemplate(t, e, DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le,
		SourceType: "vendor_invoice", Template: "Entity %1", FieldBindings: map[string]string{"1": "source_id"}})
	res, err = e.store.PostEntry(e.ctx, withoutDescription(e, "LE-1", "3.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if got := res.Entries[0].Description; got != "Entity LE-1" {
		t.Fatalf("the legal entity template must win, got %q", got)
	}
}

// A caller scoped to one legal entity sees tenant templates and its own, and
// may write only its own (FR-11.17 with the legal-entity scope rule).
func TestPostgres_DescriptionTemplateLegalEntityScope(t *testing.T) {
	e, _ := setup(t)
	wide := tenantWide(e)
	tenantTpl := putTemplate(t, wide, DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice", Template: "T"})
	otherLE := ids.New()
	otherTpl := putTemplate(t, wide, DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &otherLE,
		SourceType: "vendor_invoice", Template: "O"})
	le := e.ledger.LegalEntityID
	own := putTemplate(t, e, DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le, SourceType: "vendor_invoice", Template: "Own"})

	list, err := e.store.ListDescriptionTemplates(e.ctx)
	if err != nil || len(list) != 2 || list[0].ID != tenantTpl.ID || list[1].ID != own.ID {
		t.Fatalf("scoped list: %+v %v", list, err)
	}
	if all, err := wide.store.ListDescriptionTemplates(wide.ctx); err != nil || len(all) != 3 {
		t.Fatalf("tenant-wide list: %d %v", len(all), err)
	}
	if _, err := e.store.GetDescriptionTemplate(e.ctx, otherTpl.ID); err == nil {
		t.Fatal("another legal entity's template must be hidden")
	} else if status, _ := problemStatus(err); status != http.StatusNotFound {
		t.Fatalf("hidden template: %v", err)
	}

	forbidden := []struct {
		name string
		id   string
		in   DescriptionTemplate
	}{
		{"create tenant template", ids.New(), DescriptionTemplate{Scope: ScopeTenant, SourceType: "ap_payment", Language: "en", Template: "X"}},
		{"create for another entity", ids.New(), DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &otherLE,
			SourceType: "ap_payment", Language: "en", Template: "X"}},
		{"take over the tenant template", tenantTpl.ID, DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le,
			SourceType: "ap_payment", Language: "en", Template: "X"}},
		{"take over another entity's template", otherTpl.ID, DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le,
			SourceType: "ap_payment", Language: "en", Template: "X"}},
	}
	for _, tc := range forbidden {
		_, _, err := e.store.PutDescriptionTemplate(e.ctx, tc.id, tc.in)
		if status, code := problemStatus(err); status != http.StatusForbidden || code != "AUTH-0003" {
			t.Fatalf("%s: want 403 AUTH-0003, got %v", tc.name, err)
		}
	}
	if got, err := wide.store.GetDescriptionTemplate(wide.ctx, tenantTpl.ID); err != nil || got.Scope != ScopeTenant || got.Template != "T" {
		t.Fatalf("tenant template changed: %+v %v", got, err)
	}
	if got, err := wide.store.GetDescriptionTemplate(wide.ctx, otherTpl.ID); err != nil || got.Template != "O" {
		t.Fatalf("other entity's template changed: %+v %v", got, err)
	}
}

// A caller with no legal-entity claim writes tenant templates only with the
// tenant.administer duty, and is refused for any legal entity's template, as the
// database scope would also hide it. A scoped caller holding tenant.administer is
// still refused a tenant template.
func TestPostgres_DescriptionTemplateTenantWritesNeedTenantAdmin(t *testing.T) {
	e, _ := setup(t)
	le := e.ledger.LegalEntityID
	tenantTpl := DescriptionTemplate{Scope: ScopeTenant, SourceType: "vendor_invoice", Language: "en", Template: "T"}
	callers := []struct {
		name      string
		principal tenant.Principal
		wantOK    bool
	}{
		{"no claim without tenant.administer", tenant.Principal{TenantID: e.tenant, UserID: "test-user"}, false},
		{"scoped with tenant.administer", tenant.Principal{TenantID: e.tenant, LegalEntityID: le, UserID: "test-user", Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}}}, false},
		{"no claim with tenant.administer", tenant.Principal{TenantID: e.tenant, UserID: "test-user", Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}}}, true},
	}
	for _, c := range callers {
		_, _, err := e.store.PutDescriptionTemplate(tenant.With(e.ctx, c.principal), ids.New(), tenantTpl)
		status, code := problemStatus(err)
		if c.wantOK && err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !c.wantOK && (status != http.StatusForbidden || code != "AUTH-0003") {
			t.Fatalf("%s: want 403 AUTH-0003, got %v", c.name, err)
		}
		tenantTpl.SourceType += "x"
	}
	noAdmin := tenant.With(e.ctx, callers[0].principal)
	_, _, err := e.store.PutDescriptionTemplate(noAdmin, ids.New(), DescriptionTemplate{Scope: ScopeLegalEntity, LegalEntityID: &le,
		SourceType: "vendor_invoice", Language: "en", Template: "X"})
	if status, code := problemStatus(err); status != http.StatusForbidden || code != "AUTH-0003" {
		t.Fatalf("legal-entity template without scope: want 403 AUTH-0003, got %v", err)
	}
}

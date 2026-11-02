package target_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/target"
)

// The loader reports the legal entity of every accepted row of an entity the owning service scopes to one, so the
// cross-reference can keep an outbound lookup of another legal entity from resolving it; tenant-wide masters (party,
// product, item) and refused rows report none.
func TestLoadResultsCarryTheLegalEntityOfLegalEntityScopedRows(t *testing.T) {
	h, l := newHarness(t, "")
	party := item("P1", false, 0, map[string]any{"legalName": "Acme", "legalEntityId": le1})
	if res := load(t, l, "party", party); res[0].Code != "" || res[0].LegalEntityID != "" {
		t.Fatalf("party must be tenant-wide: %+v", res[0])
	}
	if res := load(t, l, "product", item("PR1", false, 0, map[string]any{"productNumber": "A", "name": "A", "baseUom": "EA", "legalEntityId": le1})); res[0].Code != "" || res[0].LegalEntityID != "" {
		t.Fatalf("product must be tenant-wide: %+v", res[0])
	}
	_ = h.xref.Put(ctx(), domain.XrefKey{SourceSystem: system, Entity: "party", SourceKey: "P1"}, domain.XrefEntry{TargetID: party.TargetID, ContentHash: "h", TargetVersion: 1})
	v := map[string]any{"partyKey": "P1", "accountNumber": "V-1", "vendorGroupId": ids.New(), "currencyCode": "EUR", "legalEntityId": le2}
	if res := load(t, l, "vendor", item("V1", false, 0, v)); res[0].Code != "" || res[0].LegalEntityID != le2 {
		t.Fatalf("vendor must carry its row's legal entity %s: %+v", le2, res[0])
	}
	if res := load(t, l, "vendor", item("V2", false, 0, map[string]any{"partyKey": "P1"})); res[0].Code == "" || res[0].LegalEntityID != "" {
		t.Fatalf("a refused row carries no legal entity: %+v", res[0])
	}

	// Open items load per legal entity: each accepted row reports its own.
	cust := ids.New()
	_ = h.xref.Put(ctx(), domain.XrefKey{SourceSystem: system, Entity: "customer", SourceKey: "C1"}, domain.XrefEntry{TargetID: cust, ContentHash: "h", LegalEntityID: le1})
	row := func(doc, le string) map[string]any {
		return map[string]any{"legalEntityId": le, "customerKey": "C1", "documentNumber": doc, "kind": "INVOICE", "side": "DEBIT", "documentDate": "2026-01-15", "dueDate": "2026-02-15", "currency": "USD", "originalAmount": "100.00", "openAmount": "40.50"}
	}
	res := load(t, l, "ar_open_item", item("AR-1", false, 0, row("INV-1", le1)), item("AR-2", false, 0, row("INV-2", le2)))
	if res[0].Code != "" || res[0].LegalEntityID != le1 {
		t.Fatalf("open item of legal entity 1 = %+v", res[0])
	}
	// The customer C1 was loaded into legal entity 1: a row of legal entity 2 cannot name it, and is told nothing
	// about where it exists.
	if res[1].Code != target.CodeRefUnresolved || res[1].LegalEntityID != "" {
		t.Fatalf("open item of legal entity 2 naming legal entity 1's customer = %+v", res[1])
	}
}

// RowLegalEntity answers the row's own legalEntityId only: the run's legal entity is not in the content hash, so it
// cannot say where an unchanged row was loaded.
func TestRowLegalEntityIsTheRowsOwn(t *testing.T) {
	_, l := newHarness(t, le1)
	cases := []struct {
		entity string
		fields map[string]any
		want   string
	}{
		{"customer", map[string]any{"legalEntityId": "00000000-0000-7000-8000-0000000000AA"}, "00000000-0000-7000-8000-0000000000aa"},
		{"customer", map[string]any{}, ""},
		{"vendor", map[string]any{"legalEntityId": "not-a-uuid"}, ""},
		{"party", map[string]any{"legalEntityId": le2}, ""},
	}
	for _, c := range cases {
		if got := l.RowLegalEntity(c.entity, c.fields); got != c.want {
			t.Errorf("RowLegalEntity(%s, %v) = %q, want %q", c.entity, c.fields, got, c.want)
		}
	}
}

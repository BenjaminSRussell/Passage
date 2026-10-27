package store

import (
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
)

// plStructure covers 6000..6999: DEPT is required on 6010 (AC-11.4), CC is
// limited to C200/C210 under D100, and both may be blank elsewhere.
func plStructure(status string) domain.Structure {
	return domain.Structure{Name: "PL", Version: 1, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Status: status,
		Segments: []string{"DEPT", "CC"}, AccountFilter: []string{"6000..6999"},
		Constraints: []domain.Constraint{
			{Segment: "DEPT", Under: map[string]string{"MAIN_ACCOUNT": "6010"}, Allowed: []string{"*"}},
			{Segment: "CC", Under: map[string]string{"DEPT": "D100"}, Allowed: []string{"C200", "C210", "blank"}},
		}}
}

// bsStructure covers the balance sheet (2000..2999) with no dimensions.
func bsStructure() domain.Structure {
	return domain.Structure{Name: "BS", Version: 1, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status: domain.StructureActive, AccountFilter: []string{"2000..2999"}}
}

func putStructure(t *testing.T, e env, s domain.Structure) (string, domain.Structure) {
	t.Helper()
	id := ids.New()
	out, created, err := e.store.PutStructure(e.ctx, e.ledger.ID, id, s)
	if err != nil || !created {
		t.Fatalf("put structure %s: created=%v err=%v", s.Name, created, err)
	}
	return id, out
}

func TestPostgres_StructureLifecycle(t *testing.T) {
	e, _ := setup(t)
	id, saved := putStructure(t, e, plStructure(domain.StructureDraft))
	if saved.ID != id || saved.Constraints[0].Under["MAIN_ACCOUNT"] != "6010" || !saved.EffectiveFrom.Equal(plStructure("").EffectiveFrom) {
		t.Fatalf("saved: %+v", saved)
	}

	// A draft changes freely; re-sending replaces (not creates).
	draft := plStructure(domain.StructureDraft)
	draft.Priority = 4
	got, created, err := e.store.PutStructure(e.ctx, e.ledger.ID, id, draft)
	if err != nil || created || got.Priority != 4 {
		t.Fatalf("edit draft: created=%v err=%v %+v", created, err, got)
	}

	// Activate, then any change but retirement is locked.
	active := draft
	active.Status = domain.StructureActive
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, id, active); err != nil {
		t.Fatalf("activate: %v", err)
	}
	changed := active
	changed.Segments = []string{"DEPT"}
	changed.Constraints = changed.Constraints[:1]
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, id, changed); problemCode(err) != domain.CodeStructureLocked {
		t.Fatalf("change active: want %s, got %v", domain.CodeStructureLocked, err)
	}
	if _, created, err := e.store.PutStructure(e.ctx, e.ledger.ID, id, active); err != nil || created {
		t.Fatalf("re-send active unchanged must be an idempotent replace: created=%v err=%v", created, err)
	}

	// The same name and version under another id is a 409.
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, ids.New(), draft); problemCode(err) != domain.CodeStructureVersionExists {
		t.Fatalf("duplicate version: got %v", err)
	}

	// An invalid definition lists every violation.
	bad := plStructure(domain.StructureDraft)
	bad.Version, bad.Segments = 2, []string{"PROJECT"}
	_, _, err = e.store.PutStructure(e.ctx, e.ledger.ID, ids.New(), bad)
	if codes := rejectionCodes(err); len(codes) < 2 || codes[0] != domain.CodeStructureInvalid {
		t.Fatalf("invalid definition: %v", err)
	}

	list, err := e.store.ListStructures(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || list[0].Status != domain.StructureActive {
		t.Fatalf("list: %v %+v", err, list)
	}
	if one, err := e.store.GetStructure(e.ctx, e.ledger.ID, id); err != nil || one.ID != id {
		t.Fatalf("get: %v", err)
	}
	other := newLedger(t, e, "GL2", false, false)
	if _, err := e.store.GetStructure(e.ctx, other.ID, id); problemCode(err) != domain.CodeStructureNotFound {
		t.Fatalf("structure read through another ledger: %v", err)
	}
	if _, _, err := e.store.PutStructure(e.ctx, other.ID, id, active); problemCode(err) != domain.CodeStructureNotFound {
		t.Fatalf("structure written through another ledger: %v", err)
	}
}

func TestPostgres_PostingValidatesAgainstStructures(t *testing.T) {
	e, _ := setup(t)
	// No structure yet: the ledger is open, so a line with only CC posts.
	open := vendorInvoice(e.ledger.ID, "OPEN-1", "10.00")
	open.Lines[0].Dimensions = map[string]string{"CC": "C999"}
	if _, err := e.store.PostEntry(e.ctx, open); err != nil {
		t.Fatalf("open ledger: %v", err)
	}

	// A draft does not govern postings.
	_, _ = putStructure(t, e, plStructure(domain.StructureDraft))
	open2 := vendorInvoice(e.ledger.ID, "OPEN-2", "10.00")
	if _, err := e.store.PostEntry(e.ctx, open2); err != nil {
		t.Fatalf("draft must not govern: %v", err)
	}

	// Only the P&L structure is active: 2100 is not covered.
	plID, _ := putStructure(t, e, func() domain.Structure { s := plStructure(domain.StructureActive); s.Version = 2; return s }())
	_, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "NOBS-1", "10.00"))
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{domain.CodeStructureNotFound}) {
		t.Fatalf("want LEDGER_STRUCTURE_NOT_FOUND, got %v", err)
	}
	putStructure(t, e, bsStructure())

	// AC-11.4: 6010 without DEPT is LEDGER_DIMENSION_REQUIRED with the line index; nothing is written.
	missing := vendorInvoice(e.ledger.ID, "REQ-1", "10.00")
	missing.Lines[0].Dimensions = map[string]string{"CC": "C200"}
	_, err = e.store.PostEntry(e.ctx, missing)
	var rej *domain.RejectedError
	if !errors.As(err, &rej) || len(rej.Violations) != 1 || rej.Violations[0].Code != domain.CodeDimensionRequired ||
		rej.Violations[0].LineIndex == nil || *rej.Violations[0].LineIndex != 0 {
		t.Fatalf("want LEDGER_DIMENSION_REQUIRED on line 0, got %v", err)
	}

	// The same check runs on POST /validate.
	res, err := e.store.ValidateEntry(e.ctx, missing)
	if err != nil || res.Valid() || res.Violations[0].Code != domain.CodeDimensionRequired {
		t.Fatalf("validate: %v %+v", err, res.Violations)
	}

	// Disallowed value and a dimension on a structure without segments.
	badValue := vendorInvoice(e.ledger.ID, "INV-1", "10.00")
	badValue.Lines[0].Dimensions = map[string]string{"DEPT": "D100", "CC": "C999"}
	badValue.Lines[1].Dimensions = map[string]string{"DEPT": "D100"}
	_, err = e.store.PostEntry(e.ctx, badValue)
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{domain.CodeDimensionInvalid, domain.CodeDimensionInvalid}) {
		t.Fatalf("want two LEDGER_DIMENSION_INVALID, got %v", err)
	}

	// A valid line posts and its combination records the structure version (FR-11.4).
	good := vendorInvoice(e.ledger.ID, "OK-1", "10.00")
	good.Lines[0].Dimensions = map[string]string{"DEPT": "D100", "CC": "C200"}
	posted, err := e.store.PostEntry(e.ctx, good)
	if err != nil {
		t.Fatalf("post valid: %v", err)
	}
	var structureID *string
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select c.account_structure_id::text from journal_line l
			join dimension_combination c on c.tenant_id = l.tenant_id and c.id = l.dimension_combination_id
			where l.entry_id = $1 and l.line_number = 1`, posted.Entries[0].ID).Scan(&structureID)
	})
	if err != nil || structureID == nil || *structureID != plID {
		t.Fatalf("combination structure id = %v (err %v), want %s", structureID, err, plID)
	}
	if posted.Entries[0].Lines[0].DisplayName != "6010-D100-C200" {
		t.Fatalf("display name %q", posted.Entries[0].Lines[0].DisplayName)
	}

	// Reversal reuses the original combinations, so it posts even though the
	// structure changed since (retire the P&L structure first).
	retired := plStructure(domain.StructureRetired)
	retired.Version = 2
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, plID, retired); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if _, err := reverseWith(t, e, posted.Entries[0].ID, "REV-OK-1", ""); err != nil {
		t.Fatalf("reverse after retirement: %v", err)
	}
}

func TestPostgres_DimensionValueRegister(t *testing.T) {
	e, _ := setup(t)
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	v, created, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, domain.DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Sales", ActiveFrom: &from})
	if err != nil || !created || v.ActiveFrom == nil || !v.ActiveFrom.Equal(from) {
		t.Fatalf("create: created=%v err=%v %+v", created, err, v)
	}

	// testPostingDate (2026-03-10) is before activeFrom.
	_, err = e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "INACT-1", "10.00"))
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{domain.CodeDimensionInactive}) {
		t.Fatalf("want LEDGER_DIMENSION_INACTIVE, got %v", err)
	}

	// Moving the start date earlier activates it; suspending refuses it again.
	early := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, created, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, domain.DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Sales", ActiveFrom: &early}); err != nil || created {
		t.Fatalf("replace: created=%v err=%v", created, err)
	}
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "ACT-1", "10.00")); err != nil {
		t.Fatalf("active value: %v", err)
	}
	if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, domain.DimensionValue{Attribute: "DEPT", Value: "D100", Name: "Sales", Suspended: true}); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "SUSP-1", "10.00")); rejectionCodes(err) == nil {
		t.Fatalf("suspended value must be refused, got %v", err)
	}

	if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, domain.DimensionValue{Attribute: "PROJECT", Value: "P1"}); rejectionCodes(err) == nil {
		t.Fatalf("attribute not on the ledger must be refused, got %v", err)
	}
	list, err := e.store.ListDimensionValues(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || !list[0].Suspended || list[0].ActiveFrom != nil {
		t.Fatalf("list: %v %+v", err, list)
	}
}

func TestPostgres_StructuresAreTenantAndLegalEntityScoped(t *testing.T) {
	e, _ := setup(t)
	id, _ := putStructure(t, e, bsStructure())
	if _, _, err := e.store.PutDimensionValue(e.ctx, e.ledger.ID, domain.DimensionValue{Attribute: "DEPT", Value: "D100"}); err != nil {
		t.Fatal(err)
	}
	otherTenant := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.ListStructures(otherTenant, e.ledger.ID); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("other tenant list structures: %v", err)
	}
	if _, err := e.store.GetStructure(otherTenant, e.ledger.ID, id); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("other tenant get structure: %v", err)
	}
	if _, err := e.store.ListDimensionValues(otherTenant, e.ledger.ID); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("other tenant list values: %v", err)
	}
	// Row-level security hides the rows even from a direct query.
	var n int
	err := dbx.WithTenantTx(otherTenant, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(otherTenant, `select (select count(*) from account_structure) + (select count(*) from dimension_value)`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("RLS leak: n=%d err=%v", n, err)
	}
	otherEntity := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetStructure(otherEntity, e.ledger.ID, id); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("other legal entity get structure: %v", err)
	}
	var p *httpx.Problem
	if _, _, err := e.store.PutStructure(otherEntity, e.ledger.ID, ids.New(), bsStructure()); !errors.As(err, &p) || p.Status != http.StatusNotFound {
		t.Fatalf("other legal entity put structure: %v", err)
	}
}

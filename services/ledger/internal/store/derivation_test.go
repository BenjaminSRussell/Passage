package store

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// The ledger of setup() has attributes DEPT and CC. The tests derive DEPT from (ACCOUNT, CC).
func deptLookup() domain.LookupTable {
	return domain.LookupTable{Name: "Department by account and centre", SourceFields: []string{domain.FieldAccount, "CC"}, Target: "DEPT",
		Rows: []domain.LookupRow{
			{Source: []string{"6010", "C200"}, Target: "PROD", ValidFrom: "2026-01-01"},
			{Source: []string{"6010", "*"}, Target: "ADMIN", ValidFrom: "2026-01-01"},
			{Source: []string{"6010", "C200"}, Target: "OLD", ValidFrom: "2025-01-01", ValidTo: strPtr("2025-12-31")},
		}}
}

func strPtr(s string) *string { return &s }

func deptStrategy(tableID string) domain.DerivationStrategy {
	return domain.DerivationStrategy{Name: "Cost of sales", Steps: []domain.DerivationStep{{Sequence: 10, Kind: domain.StepTableLookup,
		SourceFields: []string{domain.FieldAccount, "CC"}, Target: "DEPT", LookupTableID: tableID}}}
}

func activeStrategy(t *testing.T, e env) (domain.LookupTable, domain.DerivationStrategy) {
	t.Helper()
	tableID := ids.New()
	table, created, err := e.store.PutLookupTable(e.ctx, e.ledger.ID, tableID, 0, deptLookup())
	if err != nil || !created || table.RowCount != 3 || table.Version != 1 || len(table.Rows) != 3 {
		t.Fatalf("put lookup: %+v created=%v err=%v", table, created, err)
	}
	draft, created, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 1, 0, deptStrategy(tableID))
	if err != nil || !created || draft.Status != domain.StrategyDraft || draft.Steps[0].ID == "" {
		t.Fatalf("put strategy: %+v created=%v err=%v", draft, created, err)
	}
	active, err := e.store.ActivateDerivationStrategy(e.ctx, e.ledger.ID, 1, draft.LockVersion)
	if err != nil || active.Status != domain.StrategyActive || active.ActivatedBy != "test-user" {
		t.Fatalf("activate: %+v err=%v", active, err)
	}
	return table, active
}

func TestPostgres_AC11_15_DerivationFillsDimensionAndTracesIt(t *testing.T) {
	e, _ := setup(t)
	_, strategy := activeStrategy(t, e)
	stepID := strategy.Steps[0].ID

	in := vendorInvoice(e.ledger.ID, "DV-1", "50.00")
	in.Lines[0].Dimensions = map[string]string{"CC": "C200"}
	res, err := e.store.ValidateEntry(e.ctx, in)
	if err != nil || !res.Valid() || res.Lines[0].Dimensions["DEPT"] != "PROD" {
		t.Fatalf("validate: %+v err=%v", res, err)
	}
	if tr := res.Derivation[0]; len(tr) != 1 || tr[0].StepID != stepID || tr[0].After != "PROD" || len(tr[0].MatchedRow) != 2 {
		t.Fatalf("validate trace: %+v", res.Derivation)
	}

	posted, err := e.store.PostEntry(e.ctx, in)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	stored, err := e.store.GetEntry(e.ctx, posted.Entries[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	l := stored.Lines[0]
	if l.Dimensions["DEPT"] != "PROD" || l.Dimensions["CC"] != "C200" || len(l.DerivedByStepIDs) != 1 || l.DerivedByStepIDs[0] != stepID {
		t.Fatalf("stored line = %+v", l)
	}
	if got := stored.Lines[1].DerivedByStepIDs; len(got) != 0 {
		t.Fatalf("the credit line was not derived: %v", got)
	}
	if !strings.Contains(l.DisplayName, "PROD") {
		t.Fatalf("combination %q lacks the derived value", l.DisplayName)
	}

	// An explicit value wins over a step without overwrite; the wildcard row fills the rest.
	explicit := vendorInvoice(e.ledger.ID, "DV-2", "10.00")
	explicit.Lines[0].Dimensions = map[string]string{"CC": "C200", "DEPT": "D100"}
	other := vendorInvoice(e.ledger.ID, "DV-3", "10.00")
	other.Lines[0].Dimensions = map[string]string{"CC": "C999"}
	for key, want := range map[string]string{"DV-2": "D100", "DV-3": "ADMIN"} {
		in := explicit
		if key == "DV-3" {
			in = other
		}
		r, err := e.store.PostEntry(e.ctx, in)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got := r.Entries[0].Lines[0].Dimensions["DEPT"]; got != want {
			t.Errorf("%s: DEPT = %q, want %q", key, got, want)
		}
	}
}

func TestPostgres_StrategyLifecycleAndValidation(t *testing.T) {
	e, _ := setup(t)
	table, v1 := activeStrategy(t, e)

	// Versions stay dense; only drafts are replaced.
	if _, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 3, 0, deptStrategy(table.ID)); err == nil || problemCode(err) != "" || !strings.Contains(err.Error(), "LEDGER_DERIVATION_INVALID") {
		t.Fatalf("version 3 must be refused as not next: %v", err)
	}
	if _, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 1, 0, deptStrategy(table.ID)); problemCode(err) != CodeStrategyNotDraft {
		t.Fatalf("replace active: %v", err)
	}

	// A draft that reads the wrong fields saves, but cannot be activated (BR-11.14).
	bad := deptStrategy(table.ID)
	bad.Steps[0].SourceFields = []string{domain.FieldAccount}
	d2, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 2, 0, bad)
	if err != nil {
		t.Fatalf("draft save: %v", err)
	}
	if _, err := e.store.ActivateDerivationStrategy(e.ctx, e.ledger.ID, 2, 0); err == nil || !strings.Contains(err.Error(), "LEDGER_DERIVATION_INVALID") {
		t.Fatalf("activation must be refused: %v", err)
	}
	// Optimistic version on the draft.
	if _, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 2, d2.LockVersion+7, deptStrategy(table.ID)); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale lockVersion: %v", err)
	}
	fixed, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 2, d2.LockVersion, deptStrategy(table.ID))
	if err != nil || fixed.LockVersion != d2.LockVersion+1 {
		t.Fatalf("fix draft: %+v %v", fixed, err)
	}
	if _, err := e.store.ActivateDerivationStrategy(e.ctx, e.ledger.ID, 2, fixed.LockVersion); err != nil {
		t.Fatalf("activate v2: %v", err)
	}
	list, err := e.store.ListDerivationStrategies(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 2 || list[0].Version != 2 || list[0].Status != domain.StrategyActive || list[1].Status != domain.StrategyRetired {
		t.Fatalf("list after v2: %+v %v", list, err)
	}
	if _, err := e.store.ActivateDerivationStrategy(e.ctx, e.ledger.ID, 1, 0); problemCode(err) != CodeStrategyNotDraft {
		t.Fatalf("retired version: %v", err)
	}
	if got, err := e.store.GetDerivationStrategy(e.ctx, e.ledger.ID, 1); err != nil || got.Status != domain.StrategyRetired || got.ID != v1.ID {
		t.Fatalf("get v1: %+v %v", got, err)
	}
	if _, err := e.store.GetDerivationStrategy(e.ctx, e.ledger.ID, 9); problemCode(err) != CodeStrategyNotFound {
		t.Fatalf("missing version: %v", err)
	}
}

func TestPostgres_LookupTableRules(t *testing.T) {
	e, _ := setup(t)
	table, _ := activeStrategy(t, e)

	// A used table keeps its definition; rows can still be replaced and the version moves.
	changed := deptLookup()
	changed.SourceFields = []string{domain.FieldAccount}
	changed.Rows = nil
	if _, _, err := e.store.PutLookupTable(e.ctx, e.ledger.ID, table.ID, 0, changed); problemCode(err) != CodeLookupInUse {
		t.Fatalf("definition change of a used table: %v", err)
	}
	if _, _, err := e.store.PutLookupTable(e.ctx, e.ledger.ID, table.ID, 5, deptLookup()); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	more := deptLookup()
	more.Rows = more.Rows[:1]
	replaced, created, err := e.store.PutLookupTable(e.ctx, e.ledger.ID, table.ID, 1, more)
	if err != nil || created || replaced.Version != 2 || replaced.RowCount != 1 {
		t.Fatalf("replace rows: %+v created=%v err=%v", replaced, created, err)
	}
	// Name and content rules.
	dup := deptLookup()
	if _, _, err := e.store.PutLookupTable(e.ctx, e.ledger.ID, ids.New(), 0, dup); problemCode(err) != CodeLookupNameDuplicate {
		t.Fatalf("duplicate name: %v", err)
	}
	for name, tbl := range map[string]domain.LookupTable{
		"unknown target": {Name: "T1", SourceFields: []string{"CC"}, Target: "NOPE"},
		"unknown source": {Name: "T2", SourceFields: []string{"NOPE"}, Target: "DEPT"},
		"dup rows": {Name: "T3", SourceFields: []string{"CC"}, Target: "DEPT", Rows: []domain.LookupRow{
			{Source: []string{"C1"}, Target: "A", ValidFrom: "2026-01-01"}, {Source: []string{"C1"}, Target: "B", ValidFrom: "2026-01-01"}}},
	} {
		if _, _, err := e.store.PutLookupTable(e.ctx, e.ledger.ID, ids.New(), 0, tbl); err == nil || !strings.Contains(err.Error(), "entry rejected") {
			t.Errorf("%s: %v", name, err)
		}
	}
	list, err := e.store.ListLookupTables(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 1 || list[0].RowCount != 1 || len(list[0].Rows) != 0 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := e.store.GetLookupTable(e.ctx, e.ledger.ID, ids.New()); problemCode(err) != CodeLookupNotFound {
		t.Fatalf("missing table: %v", err)
	}
	// The strategy follows a row replacement at once: the dropped C200 row no longer matches.
	in := vendorInvoice(e.ledger.ID, "DV-9", "5.00")
	in.Lines[0].Dimensions = map[string]string{"CC": "C300"}
	r, err := e.store.PostEntry(e.ctx, in)
	if err != nil || r.Entries[0].Lines[0].Dimensions["DEPT"] != "" {
		t.Fatalf("after row replacement: %+v %v", r, err)
	}
}

func TestPostgres_DerivationEventsAndIsolation(t *testing.T) {
	e, _ := setup(t)
	activeStrategy(t, e)
	types := map[string]int{}
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(e.ctx, `select type from outbox`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			if err := rows.Scan(&typ); err != nil {
				return err
			}
			types[typ]++
		}
		return rows.Err()
	})
	if err != nil || types[ledgerevents.RulesChanged] != 2 {
		t.Fatalf("outbox = %v err=%v; want rules.changed for the table and the activation", types, err)
	}
	// Another tenant cannot see the ledger's rules, and the tables carry RLS.
	other := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.ListLookupTables(other, e.ledger.ID); problemStatusIs(err, http.StatusNotFound) == false {
		t.Fatalf("other tenant list: %v", err)
	}
	for _, table := range []string{"lookup_table", "lookup_table_row", "derivation_strategy"} {
		var n int
		if err := e.admin.QueryRow(e.ctx, `select count(*) from pg_class where relname = $1 and relrowsecurity and relforcerowsecurity`, table).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s lacks forced row-level security", table)
		}
	}
	// The posted line cannot be rewritten to hide the trace.
	if _, err := e.pool.Exec(e.ctx, `update journal_line set derived_by_step_ids = '{}'`); err == nil {
		t.Error("journal_line must stay append-only")
	}
}

func problemStatusIs(err error, status int) bool {
	got, _ := problemStatus(err)
	return got == status
}

// Submitting a batch runs the same derivation stage as posting, so a dimension the
// structure requires and the strategy fills is not reported missing (FR-11.22, AC-11.4).
func TestPostgres_BatchSubmitDerivesBeforeStructureValidation(t *testing.T) {
	e, _ := setup(t)
	putStructure(t, e, plStructure(domain.StructureActive))
	putStructure(t, e, bsStructure())
	putName(t, e, "GJ", JournalName{Active: true})
	in := batchOf(e, "GJ", "40.00")
	in.Lines[0].Dimensions = map[string]string{"CC": "C200"}
	b, err := e.store.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// Without a strategy DEPT is missing on 6010.
	if _, err := e.store.SubmitBatch(e.ctx, b.ID, 0, testToday); !slices.Contains(rejectionCodes(err), domain.CodeDimensionRequired) {
		t.Fatalf("without a strategy: %v", err)
	}
	_, strategy := activeStrategy(t, e)
	submitted, err := e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	if err != nil || submitted.Status != domain.BatchValidated {
		t.Fatalf("submit with derivation: %+v %v", submitted, err)
	}
	posted, err := postBatch(e, e.ctx, submitted, "BATCH-DERIVE", true)
	if err != nil {
		t.Fatalf("post batch: %v", err)
	}
	l := posted.Entries[0].Lines[0]
	if l.Dimensions["DEPT"] != "PROD" || len(l.DerivedByStepIDs) != 1 || l.DerivedByStepIDs[0] != strategy.Steps[0].ID {
		t.Fatalf("posted batch line = %+v", l)
	}
}

// Step ids identify a step in derived_by_step_ids, so a new version never reuses
// the ids of the version it was copied from, while a draft keeps its own ids.
func TestPostgres_StepIDsAreUniqueAcrossVersions(t *testing.T) {
	e, _ := setup(t)
	table, v1 := activeStrategy(t, e)
	copied := deptStrategy(table.ID)
	copied.Steps[0].ID = v1.Steps[0].ID
	v2, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 2, 0, copied)
	if err != nil {
		t.Fatal(err)
	}
	if v2.Steps[0].ID == v1.Steps[0].ID || !ids.Valid(v2.Steps[0].ID) {
		t.Fatalf("version 2 reused step id %s", v2.Steps[0].ID)
	}
	again, _, err := e.store.PutDerivationStrategy(e.ctx, e.ledger.ID, 2, v2.LockVersion, v2)
	if err != nil || again.Steps[0].ID != v2.Steps[0].ID {
		t.Fatalf("draft replace changed its step id: %+v %v", again, err)
	}
}

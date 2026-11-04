package store

import (
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func projectRule(structureID string) domain.AdvancedRule {
	return domain.AdvancedRule{StructureID: structureID, Name: "PROJECT_ON_D100", Priority: 1,
		Accounts: []string{"6000..6999"}, Dimensions: map[string]string{"DEPT": "D100"}, Segments: []string{"PROJECT"},
		Constraints: []domain.Constraint{{Segment: "PROJECT", Allowed: []string{"*"}}}}
}

// TestPostgres_AC11_18_AdvancedRule: "accounts 6000..6999 and DEPT = D100 add segment PROJECT (required)":
// a line on 6010/D100 without PROJECT is LEDGER_DIMENSION_REQUIRED; a line on 6010/D200 posts without it.
func TestPostgres_AC11_18_AdvancedRule(t *testing.T) {
	e, _ := setup(t)
	l, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.le, Code: "GL2", Name: "Project ledger", ChartCode: "STD",
		Currency: "USD", DimensionAttributes: []string{"DEPT", "CC", "PROJECT"}, FuturePostingDays: 366})
	if err != nil {
		t.Fatal(err)
	}
	pl := plStructure(domain.StructureDraft)
	pl.Segments = []string{"DEPT", "CC"}
	plID := ids.New()
	if _, _, err := e.store.PutStructure(e.ctx, l.ID, plID, 0, pl); err != nil {
		t.Fatal(err)
	}
	bs := bsStructure()
	if _, _, err := e.store.PutStructure(e.ctx, l.ID, ids.New(), 0, bs); err != nil {
		t.Fatal(err)
	}

	// Maintenance: create, replace at the lock version, stale tag, duplicate name, definition errors.
	ruleID := ids.New()
	rule, created, err := e.store.PutAdvancedRule(e.ctx, l.ID, ruleID, 0, projectRule(plID))
	if err != nil || !created || rule.LockVersion != 1 || rule.Segments[0] != "PROJECT" || rule.Dimensions["DEPT"] != "D100" {
		t.Fatalf("create: %+v created=%v err=%v", rule, created, err)
	}
	if _, _, err := e.store.PutAdvancedRule(e.ctx, l.ID, ruleID, 7, projectRule(plID)); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale If-Match: %v", err)
	}
	if _, _, err := e.store.PutAdvancedRule(e.ctx, l.ID, ids.New(), 0, projectRule(plID)); problemCode(err) != domain.CodeAdvancedRuleDuplicate {
		t.Fatalf("duplicate name: %v", err)
	}
	bad := projectRule(plID)
	bad.Name, bad.Segments = "OTHER", []string{"DEPT"}
	bad.Constraints = nil
	if _, _, err := e.store.PutAdvancedRule(e.ctx, l.ID, ids.New(), 0, bad); !slices.Contains(rejectionCodes(err), domain.CodeAdvancedRuleInvalid) {
		t.Fatalf("segment already in the structure: %v", err)
	}
	if _, _, err := e.store.PutAdvancedRule(e.ctx, l.ID, ids.New(), 0, projectRule(ids.New())); problemCode(err) != domain.CodeStructureNotFound {
		t.Fatalf("unknown structure: %v", err)
	}
	if _, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, ruleID, 0, projectRule(plID)); problemCode(err) != domain.CodeStructureNotFound {
		t.Fatalf("structure of another ledger: %v", err)
	}
	moved := projectRule(ids.New())
	if _, _, err := e.store.PutAdvancedRule(e.ctx, l.ID, ruleID, 0, moved); problemCode(err) != domain.CodeStructureNotFound {
		t.Fatalf("move to unknown structure: %v", err)
	}
	renamed := projectRule(plID)
	renamed.Priority = 3
	rule, created, err = e.store.PutAdvancedRule(e.ctx, l.ID, ruleID, 1, renamed)
	if err != nil || created || rule.LockVersion != 2 || rule.Priority != 3 {
		t.Fatalf("replace: %+v created=%v err=%v", rule, created, err)
	}
	if got, err := e.store.GetAdvancedRule(e.ctx, l.ID, ruleID); err != nil || got.LockVersion != 2 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := e.store.GetAdvancedRule(e.ctx, l.ID, ids.New()); problemCode(err) != domain.CodeAdvancedRuleNotFound {
		t.Fatalf("get unknown: %v", err)
	}
	if list, err := e.store.ListAdvancedRules(e.ctx, l.ID, plID); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list, err := e.store.ListAdvancedRules(e.ctx, l.ID, ids.New()); err != nil || len(list) != 0 {
		t.Fatalf("list other structure: %+v %v", list, err)
	}

	post := func(key, dept, project string) error {
		in := vendorInvoice(l.ID, key, "10.00")
		in.Lines[0].Dimensions = map[string]string{"DEPT": dept}
		if project != "" {
			in.Lines[0].Dimensions["PROJECT"] = project
		}
		_, err := e.store.PostEntry(e.ctx, in)
		return err
	}
	// While the structure is a draft neither it nor its rule governs: no active structure covers 6010.
	if codes := rejectionCodes(post("ADV-0", "D100", "")); !slices.Equal(codes, []string{domain.CodeStructureNotFound}) {
		t.Fatalf("draft structure: %v", codes)
	}
	// Activate the structure with its rule (draft -> active shares the activation step).
	active := pl
	active.Status = domain.StructureActive
	if _, _, err := e.store.PutStructure(e.ctx, l.ID, plID, structTag(e, l.ID, plID), active); err != nil {
		t.Fatalf("activate: %v", err)
	}
	// Rules of an active version are locked.
	changed := renamed
	changed.Priority = 4
	if _, _, err := e.store.PutAdvancedRule(e.ctx, l.ID, ruleID, 0, changed); problemCode(err) != domain.CodeStructureLocked {
		t.Fatalf("rule of an active structure: %v", err)
	}

	err = post("ADV-1", "D100", "")
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{domain.CodeDimensionRequired}) {
		t.Fatalf("6010/D100 without PROJECT: %v", err)
	}
	if err := post("ADV-2", "D200", ""); err != nil {
		t.Fatalf("6010/D200 must post without PROJECT: %v", err)
	}
	if err := post("ADV-3", "D100", "P1"); err != nil {
		t.Fatalf("6010/D100 with PROJECT: %v", err)
	}
	// A project on a line the rule does not match is not a segment of the structure.
	if codes := rejectionCodes(post("ADV-4", "D200", "P1")); !slices.Equal(codes, []string{domain.CodeDimensionInvalid}) {
		t.Fatalf("6010/D200 with PROJECT: %v", codes)
	}
	// The dry run applies the same rule.
	dry := vendorInvoice(l.ID, "ADV-5", "10.00")
	dry.Lines[0].Dimensions = map[string]string{"DEPT": "D100"}
	if res, err := e.store.ValidateEntry(e.ctx, dry); err != nil || res.Valid() || res.Violations[0].Code != domain.CodeDimensionRequired {
		t.Fatalf("validate: %v %+v", err, res.Violations)
	}
	// The ledger without the rule still posts 6010/D100 (and has no PROJECT attribute).
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "ADV-6", "10.00")); err != nil {
		t.Fatalf("other ledger: %v", err)
	}

	// rules.changed with kind advanced_rule went through the outbox, one per save.
	var n int
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and aggregate_type = 'advanced_rule'`, ledgerevents.RulesChanged).Scan(&n)
	})
	if err != nil || n != 2 {
		t.Fatalf("rules.changed events = %d err=%v", n, err)
	}
}

func TestPostgres_AdvancedRulesAreTenantAndLegalEntityScoped(t *testing.T) {
	e, _ := setup(t)
	draft := domain.Structure{Name: "PL", Version: 1, EffectiveFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status: domain.StructureDraft, Segments: []string{"DEPT"}, AccountFilter: []string{"6000..6999"}}
	id := ids.New()
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, id, 0, draft); err != nil {
		t.Fatal(err)
	}
	r := projectRule(id)
	r.Segments = []string{"CC"}
	r.Constraints = nil
	ruleID := ids.New()
	if _, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, ruleID, 0, r); err != nil {
		t.Fatal(err)
	}
	other := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.GetAdvancedRule(other, e.ledger.ID, ruleID); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("other tenant get: %v", err)
	}
	var n int
	err := dbx.WithTenantTx(other, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(other, `select count(*) from advanced_rule`).Scan(&n)
	})
	if err != nil || n != 0 {
		t.Fatalf("RLS leak: n=%d err=%v", n, err)
	}
	otherEntity := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetAdvancedRule(otherEntity, e.ledger.ID, ruleID); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("other legal entity get: %v", err)
	}
}

func advancedEvents(t *testing.T, e env, id string) int {
	t.Helper()
	var n int
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and aggregate_type = 'advanced_rule' and aggregate_id = $2`,
			ledgerevents.RulesChanged, id).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A PUT is idempotent: re-sending the stored content keeps the lockVersion and announces nothing,
// also once the structure version is active; a rule cannot move to another (draft) version.
func TestPostgres_AdvancedRuleRepeatedPutAndMove(t *testing.T) {
	e, _ := setup(t)
	pl := plStructure(domain.StructureDraft)
	pl.Segments, pl.Constraints = []string{"DEPT"}, nil
	plID, _ := putStructure(t, e, pl)
	other := plStructure(domain.StructureDraft)
	other.Name, other.Segments, other.Constraints = "PL2", []string{"DEPT"}, nil
	otherID, _ := putStructure(t, e, other)

	r := projectRule(plID)
	r.Segments = []string{"CC"}
	r.Constraints = []domain.Constraint{{Segment: "CC", Allowed: []string{"*"}}}
	id := ids.New()
	if _, created, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, id, 0, r); err != nil || !created {
		t.Fatalf("create: %v", err)
	}
	again, created, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, id, 1, r)
	if err != nil || created || again.LockVersion != 1 || advancedEvents(t, e, id) != 1 {
		t.Fatalf("repeat: %+v created=%v err=%v events=%d", again, created, err, advancedEvents(t, e, id))
	}
	moved := r
	moved.StructureID = otherID
	if _, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, id, 0, moved); !slices.Contains(rejectionCodes(err), domain.CodeAdvancedRuleInvalid) {
		t.Fatalf("move to another draft version: %v", err)
	}

	active := pl
	active.Status = domain.StructureActive
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, plID, structTag(e, e.ledger.ID, plID), active); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, id, 0, r); err != nil || got.LockVersion != 1 || advancedEvents(t, e, id) != 1 {
		t.Fatalf("unchanged re-send after activation must be a no-op: %+v %v", got, err)
	}
	changed := r
	changed.Priority = 4
	if _, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, id, 0, changed); problemCode(err) != domain.CodeStructureLocked {
		t.Fatalf("change after activation: %v", err)
	}
	if _, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, ids.New(), 0, func() domain.AdvancedRule { n := r; n.Name = "NEW"; return n }()); problemCode(err) != domain.CodeStructureLocked {
		t.Fatalf("new rule on an active version: %v", err)
	}
}

// Realignment (FR-11.24) validates its new side through the same structure stage, so the advanced
// rules of the governing version apply to it: moving D100 to D150 where "DEPT = D150 adds CC
// (required)" is refused, because the stored combinations carry no CC.
func TestPostgres_AdvancedRuleGovernsTheRealignmentNewSide(t *testing.T) {
	e := realignSetup(t)
	pl := plStructure(domain.StructureDraft)
	pl.Segments, pl.Constraints = []string{"DEPT"}, nil
	plID, _ := putStructure(t, e, pl)
	r := domain.AdvancedRule{StructureID: plID, Name: "CC_ON_D150", Dimensions: map[string]string{"DEPT": "D150"}, Segments: []string{"CC"},
		Constraints: []domain.Constraint{{Segment: "CC", Allowed: []string{"*"}}}}
	if _, _, err := e.store.PutAdvancedRule(e.ctx, e.ledger.ID, ids.New(), 0, r); err != nil {
		t.Fatal(err)
	}
	pl.Status = domain.StructureActive
	if _, _, err := e.store.PutStructure(e.ctx, e.ledger.ID, plID, structTag(e, e.ledger.ID, plID), pl); err != nil {
		t.Fatalf("activate: %v", err)
	}
	run, _, err := e.store.CreateRealignment(e.ctx, realignRequest(e, "ADV-RA"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.store.PostRealignment(e.ctx, realignAction(e, run, "REORG"))
	codes := rejectionCodes(err)
	if len(codes) == 0 || !slices.Contains(codes, domain.CodeDimensionRequired) {
		t.Fatalf("the new side must need CC: %v", err)
	}
}

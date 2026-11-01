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

// AC-11.14: lines on accounts 6000-6999 require DEPT when the amount exceeds 1,000.
func deptRule(severity string) domain.ValidationRule {
	return domain.ValidationRule{Sequence: 10, Name: "DEPT on large expenses", Level: domain.RuleLevelLine, Severity: severity, Active: true,
		Message: "Department is required on expense lines above 1,000",
		Prerequisite: []domain.RuleCondition{
			{Field: domain.FieldAccount, Op: domain.CondBetween, Values: []string{"6000", "6999"}},
			{Field: domain.FieldAmount, Op: domain.CondGt, Values: []string{"1000"}}},
		Checks: []domain.RuleCondition{{Field: "DEPT", Op: domain.CondNotBlank}}}
}

func noDept(in PostInput) PostInput {
	in.Lines[0].Dimensions = nil
	return in
}

func TestPostgres_AC11_14_ValidationRuleRefusesLine(t *testing.T) {
	e, _ := setup(t)
	rule, created, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, ids.New(), 0, deptRule(domain.SeverityError))
	if err != nil || !created || rule.LockVersion != 1 || !rule.Active || len(rule.Checks) != 1 || rule.Prerequisite[1].Values[0] != "1000" {
		t.Fatalf("put rule: %+v created=%v err=%v", rule, created, err)
	}
	big := noDept(vendorInvoice(e.ledger.ID, "VR-1", "1500.00"))
	res, err := e.store.ValidateEntry(e.ctx, big)
	if err != nil || res.Valid() || len(res.Violations) != 1 || res.Violations[0].Code != domain.CodeValidationRuleFail ||
		res.Violations[0].LineIndex == nil || *res.Violations[0].LineIndex != 0 || !strings.Contains(res.Violations[0].Message, rule.ID) {
		t.Fatalf("validate big: %+v err=%v", res, err)
	}
	if _, err := e.store.PostEntry(e.ctx, big); !slices.Contains(rejectionCodes(err), domain.CodeValidationRuleFail) {
		t.Fatalf("post big: %v", err)
	}
	// 500.00 passes the prerequisite filter; 1,500.00 with DEPT passes the check.
	if _, err := e.store.PostEntry(e.ctx, noDept(vendorInvoice(e.ledger.ID, "VR-2", "500.00"))); err != nil {
		t.Fatalf("post small: %v", err)
	}
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "VR-3", "1500.00")); err != nil {
		t.Fatalf("post with DEPT: %v", err)
	}
}

func TestPostgres_ValidationRuleWarningInactiveAndBatch(t *testing.T) {
	e, _ := setup(t)
	id := ids.New()
	warn, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, id, 0, deptRule(domain.SeverityWarning))
	if err != nil {
		t.Fatal(err)
	}
	big := noDept(vendorInvoice(e.ledger.ID, "VW-1", "1500.00"))
	res, err := e.store.ValidateEntry(e.ctx, big)
	if err != nil || !res.Valid() || len(res.Warnings) != 1 {
		t.Fatalf("warning must not invalidate: %+v err=%v", res, err)
	}
	posted, err := e.store.PostEntry(e.ctx, big)
	if err != nil || len(posted.Notices) != 1 || posted.Notices[0].Code != domain.CodeValidationRuleFail {
		t.Fatalf("posted with warning: %+v err=%v", posted, err)
	}
	// Switch to an error rule through a replace, then deactivate it.
	asError := deptRule(domain.SeverityError)
	updated, created, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, id, warn.LockVersion, asError)
	if err != nil || created || updated.LockVersion != 2 || updated.Severity != domain.SeverityError {
		t.Fatalf("replace: %+v created=%v err=%v", updated, created, err)
	}
	// A draft batch is checked at submit like a posting.
	putName(t, e, "GJ", JournalName{Active: true})
	in := batchOf(e, "GJ", "1500.00")
	in.Lines[0].Dimensions = nil
	b, err := e.store.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.SubmitBatch(e.ctx, b.ID, 0, testToday); !slices.Contains(rejectionCodes(err), domain.CodeValidationRuleFail) {
		t.Fatalf("submit: %v", err)
	}
	asError.Active = false
	off, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, id, updated.LockVersion, asError)
	if err != nil || off.Active {
		t.Fatalf("deactivate: %+v %v", off, err)
	}
	if _, err := e.store.SubmitBatch(e.ctx, b.ID, 0, testToday); err != nil {
		t.Fatalf("submit with inactive rule: %v", err)
	}
	if _, err := e.store.PostEntry(e.ctx, noDept(vendorInvoice(e.ledger.ID, "VW-2", "1500.00"))); err != nil {
		t.Fatalf("inactive rule must not run: %v", err)
	}
}

func TestPostgres_ValidationRuleMaintenance(t *testing.T) {
	e, _ := setup(t)
	id := ids.New()
	r, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, id, 0, deptRule(domain.SeverityError))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, id, 7, deptRule(domain.SeverityError)); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale If-Match: %v", err)
	}
	dup := deptRule(domain.SeverityError)
	if _, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, ids.New(), 0, dup); problemCode(err) != CodeValidationRuleDuplicate {
		t.Fatalf("duplicate name: %v", err)
	}
	bad := deptRule(domain.SeverityError)
	bad.Checks[0].Field = "NOPE"
	if _, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, ids.New(), 0, bad); !slices.Contains(rejectionCodes(err), CodeValidationRuleInvalid) {
		t.Fatalf("unknown field: %v", err)
	}
	got, err := e.store.GetValidationRule(e.ctx, e.ledger.ID, id)
	if err != nil || got.ID != r.ID || got.Name != r.Name {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := e.store.GetValidationRule(e.ctx, e.ledger.ID, ids.New()); problemCode(err) != CodeValidationRuleNotFound {
		t.Fatalf("missing: %v", err)
	}
	second := deptRule(domain.SeverityWarning)
	second.Name, second.Sequence = "Another", 5
	if _, _, err := e.store.PutValidationRule(e.ctx, e.ledger.ID, ids.New(), 0, second); err != nil {
		t.Fatal(err)
	}
	list, err := e.store.ListValidationRules(e.ctx, e.ledger.ID)
	if err != nil || len(list) != 2 || list[0].Name != "Another" {
		t.Fatalf("list order: %+v %v", list, err)
	}
	// Events, isolation and row-level security.
	n := 0
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select count(*) from outbox where type = $1 and aggregate_type = 'validation_rule'`, ledgerevents.RulesChanged).Scan(&n)
	})
	if err != nil || n != 2 {
		t.Fatalf("rules.changed events = %d err=%v", n, err)
	}
	other := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.ListValidationRules(other, e.ledger.ID); !problemStatusIs(err, http.StatusNotFound) {
		t.Fatalf("other tenant: %v", err)
	}
	// Another legal entity of the same tenant sees neither the ledger nor its rules.
	foreign := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetValidationRule(foreign, e.ledger.ID, id); !problemStatusIs(err, http.StatusNotFound) {
		t.Fatalf("other legal entity: %v", err)
	}
	seen := -1
	if err := dbx.WithTenantTx(foreign, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(foreign, `select count(*) from validation_rule`).Scan(&seen)
	}); err != nil || seen != 0 {
		t.Fatalf("legal-entity policy: %d rows visible, err=%v", seen, err)
	}
	var rls int
	if err := e.admin.QueryRow(e.ctx, `select count(*) from pg_class where relname = 'validation_rule' and relrowsecurity and relforcerowsecurity`).Scan(&rls); err != nil || rls != 1 {
		t.Fatal("validation_rule lacks forced row-level security")
	}
}

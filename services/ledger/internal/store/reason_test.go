package store

import (
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// newLedger creates a second ledger on the same chart with the given reversal parameters.
func newLedger(t *testing.T, e env, code string, reasonRequired, negativeEnabled bool) Ledger {
	t.Helper()
	l, err := e.store.CreateLedger(e.ctx, Ledger{LegalEntityID: e.ledger.LegalEntityID, Code: code, Name: "Ledger " + code,
		ChartCode: "STD", Currency: "USD", DimensionAttributes: []string{"DEPT", "CC"}, FuturePostingDays: 366,
		ReversalReasonRequired: reasonRequired, NegativePostingEnabled: negativeEnabled})
	if err != nil {
		t.Fatalf("create ledger %s: %v", code, err)
	}
	return l
}

// reverseWith reverses entry entryID with the given reason code (empty for none).
func reverseWith(t *testing.T, e env, entryID, key, code string) (EntryResult, error) {
	t.Helper()
	hash, _ := domain.HashPayload("reverse " + key)
	return e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: entryID, Key: key, Hash: hash, Actor: "test-user",
		TraceID: testTrace, Today: testToday, ReasonCode: code, ReasonComment: "test"})
}

// rejectionCodes returns the violation codes of a 422 rejection, or nil.
func rejectionCodes(err error) []string {
	var rej *domain.RejectedError
	if !errors.As(err, &rej) {
		return nil
	}
	var out []string
	for _, v := range rej.Violations {
		out = append(out, v.Code)
	}
	return out
}

func putReason(t *testing.T, e env, code string, in ReasonCode) {
	t.Helper()
	if _, _, err := e.store.PutReasonCode(e.ctx, code, in); err != nil {
		t.Fatalf("put reason %s: %v", code, err)
	}
}

func TestPostgres_ReasonCodeCRUD(t *testing.T) {
	e, _ := setup(t)
	in := ReasonCode{Name: "Input error", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true}
	created, isNew, err := e.store.PutReasonCode(e.ctx, "ERR01", in)
	if err != nil || !isNew || created.Code != "ERR01" {
		t.Fatalf("create: %+v new=%v err=%v", created, isNew, err)
	}
	in.Name = "Input error (renamed)"
	replaced, isNew, err := e.store.PutReasonCode(e.ctx, "ERR01", in)
	if err != nil || isNew || replaced.Name != "Input error (renamed)" {
		t.Fatalf("replace: %+v new=%v err=%v", replaced, isNew, err)
	}
	got, err := e.store.GetReasonCode(e.ctx, "ERR01")
	if err != nil || got.Name != replaced.Name || !got.Active {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, err := e.store.ListReasonCodes(e.ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	_, err = e.store.GetReasonCode(e.ctx, "MISSING")
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusNotFound || p.Code != "LEDGER_REASON_NOT_FOUND" {
		t.Fatalf("missing code: %v", err)
	}
}

func TestPostgres_ReasonCodesAreTenantScoped(t *testing.T) {
	e, _ := setup(t)
	putReason(t, e, "TEN01", ReasonCode{Name: "Tenant one", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true})
	other := tenant.With(e.ctx, tenant.Principal{TenantID: testkit.TenantID(2), LegalEntityID: ids.New(), UserID: "other",
		Duties: map[string]struct{}{}})
	list, err := e.store.ListReasonCodes(other)
	if err != nil || len(list) != 0 {
		t.Fatalf("another tenant must not see the code: %+v %v", list, err)
	}
}

func TestPostgres_ReversalRequiresReasonWhenConfigured(t *testing.T) {
	e, _ := setup(t)
	l := newLedger(t, e, "GLR", true, false)
	putReason(t, e, "ERR01", ReasonCode{Name: "Input error", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(l.ID, "REQ-1", "40.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	origID := orig.Entries[0].ID

	if _, err := reverseWith(t, e, origID, "R-NONE", ""); len(rejectionCodes(err)) == 0 || rejectionCodes(err)[0] != "LEDGER_REASON_REQUIRED" {
		t.Fatalf("missing reason: want LEDGER_REASON_REQUIRED, got %v", err)
	}
	if _, err := reverseWith(t, e, origID, "R-BAD", "NOPE"); len(rejectionCodes(err)) == 0 || rejectionCodes(err)[0] != "LEDGER_REASON_REQUIRED" {
		t.Fatalf("unknown reason: want LEDGER_REASON_REQUIRED, got %v", err)
	}
	rev, err := reverseWith(t, e, origID, "R-OK", "ERR01")
	if err != nil {
		t.Fatalf("reverse with reason: %v", err)
	}
	r := rev.Entries[0]
	if r.ReasonCode == nil || *r.ReasonCode != "ERR01" || r.ReasonComment == nil || *r.ReasonComment != "test" {
		t.Fatalf("reason must be stored on the reversing entry: %+v", r)
	}
	for _, ln := range r.Lines {
		if ln.NegativePosting {
			t.Fatal("a plain reversal must not be a negative posting")
		}
	}
}

func TestPostgres_NegativeReversalRestoresTurnover(t *testing.T) {
	e, _ := setup(t)
	l := newLedger(t, e, "GLN", true, true)
	putReason(t, e, "STORNO", ReasonCode{Name: "Storno", AppliesTo: []string{"ledger"}, NegativePostingAllowed: true,
		AlternativePostingDateAllowed: true, Active: true})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(l.ID, "NEG-1", "300.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	rev, err := reverseWith(t, e, orig.Entries[0].ID, "NEG-REV", "STORNO")
	if err != nil {
		t.Fatalf("negative reversal: %v", err)
	}
	for _, ln := range rev.Entries[0].Lines {
		if !ln.NegativePosting {
			t.Fatalf("storno lines must be flagged negative: %+v", ln)
		}
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: l.ID})
	if err != nil {
		t.Fatalf("trial balance: %v", err)
	}
	// The projection reduces the original side: turnover returns to the pre-posting zero.
	if tb.Debit != "0.00" || tb.Credit != "0.00" || !tb.Balanced {
		t.Fatalf("storno must return debit and credit turnover to zero: %+v", tb)
	}
	res, err := e.store.RebuildBalances(e.ctx, l.ID)
	if err != nil || res.RowsDiffering != 0 {
		t.Fatalf("rebuild must match the negative projection: %+v %v", res, err)
	}

	// The flag survives a read of the stored entry.
	stored, err := e.store.GetEntry(e.ctx, rev.Entries[0].ID)
	if err != nil {
		t.Fatalf("get reversal: %v", err)
	}
	if stored.ReasonCode == nil || *stored.ReasonCode != "STORNO" || len(stored.Lines) != 2 || !stored.Lines[0].NegativePosting {
		t.Fatalf("stored reversal lost its reason or storno flag: %+v", stored)
	}

	// journal.reversed carries the reason code (FR-11.15), written in the same transaction.
	var eventReason string
	err = dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `select payload->'data'->>'reasonCode' from outbox where type = $1`, ledgerevents.JournalReversed).Scan(&eventReason)
	})
	if err != nil || eventReason != "STORNO" {
		t.Fatalf("journal.reversed reasonCode = %q (err %v), want STORNO", eventReason, err)
	}

	// Re-instating the storno with a plain reason restores the original turnover:
	// the reinstating lines are ordinary postings, not negative ones.
	putReason(t, e, "REINST", ReasonCode{Name: "Re-instate", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: true})
	back, err := reverseWith(t, e, rev.Entries[0].ID, "NEG-BACK", "REINST")
	if err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	for _, ln := range back.Entries[0].Lines {
		if ln.NegativePosting {
			t.Fatalf("reinstating lines must not be negative: %+v", ln)
		}
	}
	tb, err = e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: l.ID})
	if err != nil {
		t.Fatalf("trial balance after reinstatement: %v", err)
	}
	if tb.Debit != "300.00" || tb.Credit != "300.00" || !tb.Balanced {
		t.Fatalf("reinstatement must restore the original turnover: %+v", tb)
	}
	if res, err := e.store.RebuildBalances(e.ctx, l.ID); err != nil || res.RowsDiffering != 0 {
		t.Fatalf("rebuild after reinstatement: %+v %v", res, err)
	}
}

func TestPostgres_ReversalReasonModuleAndCommentRules(t *testing.T) {
	e, _ := setup(t)
	l := newLedger(t, e, "GLM", true, false)
	putReason(t, e, "BANKONLY", ReasonCode{Name: "Bank only", AppliesTo: []string{"bank"}, AlternativePostingDateAllowed: true, Active: true})
	putReason(t, e, "NOTE", ReasonCode{Name: "Needs a note", AppliesTo: []string{"ledger"}, RequiresComment: true,
		AlternativePostingDateAllowed: true, Active: true})
	putReason(t, e, "OLD", ReasonCode{Name: "Retired", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: true, Active: false})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(l.ID, "MOD-1", "5.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	id := orig.Entries[0].ID
	if _, err := reverseWith(t, e, id, "MOD-BANK", "BANKONLY"); !slices.Equal(rejectionCodes(err), []string{"LEDGER_REASON_INVALID_MODULE"}) {
		t.Fatalf("bank-only code: got %v", err)
	}
	if _, err := reverseWith(t, e, id, "MOD-OLD", "OLD"); !slices.Equal(rejectionCodes(err), []string{"LEDGER_REASON_REQUIRED"}) {
		t.Fatalf("inactive code: got %v", err)
	}
	hash, _ := domain.HashPayload("no-comment")
	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: id, Key: "MOD-NOTE", Hash: hash, Actor: "test-user",
		TraceID: testTrace, Today: testToday, ReasonCode: "NOTE"})
	if !slices.Equal(rejectionCodes(err), []string{"LEDGER_REASON_REQUIRED"}) {
		t.Fatalf("code requiring a comment without one: got %v", err)
	}
	// Rejections write nothing: the entry can still be reversed once.
	if _, err := reverseWith(t, e, id, "MOD-OK", "NOTE"); err != nil {
		t.Fatalf("reverse with comment: %v", err)
	}
}

func TestPostgres_NegativeReasonRejectedWhenLedgerDisabled(t *testing.T) {
	e, _ := setup(t)
	putReason(t, e, "STORN", ReasonCode{Name: "Storno", AppliesTo: []string{"ledger"}, NegativePostingAllowed: true,
		AlternativePostingDateAllowed: true, Active: true})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "NEGOFF-1", "12.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_, err = reverseWith(t, e, orig.Entries[0].ID, "NEGOFF-REV", "STORN")
	if codes := rejectionCodes(err); len(codes) == 0 || codes[0] != "LEDGER_NEGATIVE_POSTING_DISABLED" {
		t.Fatalf("want LEDGER_NEGATIVE_POSTING_DISABLED, got %v", err)
	}
}

func TestPostgres_ReversalDateRuleFromReasonCode(t *testing.T) {
	e, _ := setup(t)
	putReason(t, e, "SAMED", ReasonCode{Name: "Same date", AppliesTo: []string{"ledger"}, AlternativePostingDateAllowed: false, Active: true})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "DATE-1", "9.00"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	hash, _ := domain.HashPayload("date-rule")
	other := testPostingDate.AddDate(0, 0, 1)
	_, err = e.store.ReverseEntry(e.ctx, ReverseInput{EntryID: orig.Entries[0].ID, Key: "DATE-REV", Hash: hash, Actor: "test-user",
		TraceID: testTrace, Today: testToday, PostingDate: &other, ReasonCode: "SAMED"})
	if codes := rejectionCodes(err); len(codes) == 0 || codes[0] != "LEDGER_DATE_INVALID" {
		t.Fatalf("a code that forbids alternative dates must refuse another date, got %v", err)
	}
}

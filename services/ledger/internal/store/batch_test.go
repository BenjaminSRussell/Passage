package store

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

func putName(t *testing.T, e env, code string, in JournalName) {
	t.Helper()
	in.Name = "Journal " + code
	if in.LinesLimit == 0 {
		in.LinesLimit = domain.DefaultBatchLinesLimit
	}
	if _, _, err := e.store.PutJournalName(e.ctx, code, nameTag(e, code), in); err != nil {
		t.Fatalf("put journal name %s: %v", code, err)
	}
}

// isProblem reports whether err is a problem with this status and code.
func isProblem(err error, status int, code string) bool {
	s, c := problemStatus(err)
	return s == status && c == code
}

func batchOf(e env, name, amount string) BatchInput {
	return BatchInput{
		LedgerID: e.ledger.ID, JournalName: name, Description: "Accrual", PostingDate: testPostingDate,
		Lines: []BatchLine{
			{Account: "6010", DebitCredit: "D", Amount: amount, Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"}},
			{Account: "2100", DebitCredit: "C", Amount: amount, Currency: "USD"},
		},
	}
}

func asUser(e env, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: e.tenant, LegalEntityID: e.le, UserID: user, Duties: testkit.ScopeDuties(e.le)})
}

func postBatch(e env, ctx context.Context, b Batch, key string, direct bool) (EntryResult, error) {
	return e.store.PostBatch(ctx, PostBatchInput{ID: b.ID, Key: key, Today: testToday, TraceID: testTrace, CanPostDirect: direct})
}

// batchEvents returns the from>to pairs of the batch's statuschanged events in order.
func batchEvents(t *testing.T, e env, batchID string) []string {
	t.Helper()
	var out []string
	err := dbx.WithTenantTx(e.ctx, e.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(e.ctx, `select aggregate_type, payload from outbox where type = $1 and aggregate_id = $2 order by created_at, id`,
			ledgerevents.BatchStatusChanged, batchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var agg string
			var raw []byte
			if err := rows.Scan(&agg, &raw); err != nil {
				return err
			}
			var evt struct {
				Data ledgerevents.BatchStatusChangedData `json:"data"`
			}
			if err := json.Unmarshal(raw, &evt); err != nil {
				return err
			}
			if agg != "journal_batch" || evt.Data.BatchID != batchID || evt.Data.LedgerID != e.ledger.ID || evt.Data.Actor == "" {
				t.Errorf("event envelope %s %+v", agg, evt.Data)
			}
			out = append(out, evt.Data.From+">"+evt.Data.To)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPostgres_BatchLifecycleWithoutWorkflow(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	b, err := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "125"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Status != domain.BatchDraft || b.Version != 1 || b.TotalDebit != "125.00" || b.TotalCredit != "125.00" ||
		b.LineCount != 2 || b.Currency != "USD" || b.LegalEntityID != e.le || b.CreatedBy != "test-user" || b.RequiresApproval {
		t.Fatalf("draft batch = %+v", b)
	}
	if len(b.Lines) != 2 || b.Lines[0].LineNumber != 1 || b.Lines[0].Amount != "125.00" || b.Lines[0].Dimensions["DEPT"] != "D100" {
		t.Fatalf("lines = %+v", b.Lines)
	}

	b, err = e.store.SubmitBatch(e.ctx, b.ID, 1, testToday)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if b.Status != domain.BatchValidated || b.SubmittedBy != "test-user" || b.Version != 2 {
		t.Fatalf("after submit (no workflow) = %s v%d by %q", b.Status, b.Version, b.SubmittedBy)
	}

	// A validated batch skips approval, so posting it needs "post without approval".
	if _, err := postBatch(e, e.ctx, b, "GJ-POST-1", false); !isProblem(err, http.StatusForbidden, "AUTH-0003") {
		t.Fatalf("direct post without the duty: got %v", err)
	}
	first, err := postBatch(e, e.ctx, b, "GJ-POST-1", true)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	entry := first.Entries[0]
	if first.Idempotency.Replayed || entry.Source.Type != "manual_journal" || entry.Source.System != "ledger" || entry.Source.ID != b.ID ||
		entry.DocumentID != b.ID || entry.Description != "Accrual" || entry.DocumentDate == nil || *entry.DocumentDate != "2026-03-10" ||
		entry.Totals.Debit != "125.00" || len(entry.Lines) != 2 {
		t.Fatalf("posted entry = %+v", entry)
	}

	replay, err := postBatch(e, e.ctx, b, "GJ-POST-1", true)
	if err != nil || !replay.Idempotency.Replayed || replay.Entries[0].ID != entry.ID {
		t.Fatalf("replay must return the stored entry: %+v %v", replay.Idempotency, err)
	}
	if _, err := postBatch(e, e.ctx, b, "GJ-POST-2", true); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("a posted batch must refuse another key, got %v", err)
	}

	posted, err := e.store.GetBatch(e.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != domain.BatchPosted || posted.PostedEntryID != entry.ID || posted.PostedBy != "test-user" || posted.PostedAt == nil {
		t.Fatalf("posted batch = %+v", posted)
	}
	if _, err := e.store.CancelBatch(e.ctx, b.ID, 0); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("posted batches are terminal, got %v", err)
	}
	desc := "late edit"
	if _, err := e.store.UpdateBatch(e.ctx, b.ID, 0, BatchPatch{Description: &desc}); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("a posted batch cannot be edited, got %v", err)
	}
	if got := batchEvents(t, e, b.ID); !slices.Equal(got, []string{"draft>validated", "validated>posted"}) {
		t.Fatalf("events = %v", got)
	}
	tb, err := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if err != nil || tb.Debit != "125.00" || !tb.Balanced {
		t.Fatalf("trial balance after the batch: %+v %v", tb, err)
	}
}

// AC-11.8: a clerk submits a batch of a journal name with approval; it is
// in_approval; after an approver (not the clerk) approves, the clerk posts.
func TestPostgres_AC11_8_BatchApproval(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "APPR", JournalName{Active: true, RequiresApproval: true})
	clerk, approver := asUser(e, "clerk"), asUser(e, "approver")
	b, err := e.store.CreateBatch(clerk, batchOf(e, "APPR", "80.00"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err = e.store.SubmitBatch(clerk, b.ID, 0, testToday); err != nil || b.Status != domain.BatchInApproval {
		t.Fatalf("submit: %s %v", b.Status, err)
	}
	if _, err := postBatch(e, clerk, b, "AP-1", true); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("a batch in approval cannot be posted, got %v", err)
	}
	desc := "edit"
	if _, err := e.store.UpdateBatch(clerk, b.ID, 0, BatchPatch{Description: &desc}); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("a batch in approval cannot be edited, got %v", err)
	}
	if _, err := e.store.DecideBatch(clerk, b.ID, 0, true); !isProblem(err, http.StatusForbidden, CodeSelfApproval) {
		t.Fatalf("the submitter must not approve, got %v", err)
	}
	if _, err := e.store.DecideBatch(approver, b.ID, b.Version-1, true); !isProblem(err, http.StatusPreconditionFailed, CodeVersionConflict) {
		t.Fatalf("a stale If-Match must be refused, got %v", err)
	}
	approved, err := e.store.DecideBatch(approver, b.ID, b.Version, true)
	if err != nil || approved.Status != domain.BatchApproved || approved.DecidedBy != "approver" {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if _, err := e.store.CancelBatch(clerk, b.ID, 0); !isProblem(err, http.StatusConflict, CodeBatchState) {
		t.Fatalf("an approved batch cannot be cancelled, got %v", err)
	}
	// An approved batch posts with "Post ledger journals" alone.
	res, err := postBatch(e, clerk, approved, "AP-1", false)
	if err != nil || len(res.Entries) != 1 {
		t.Fatalf("post approved: %v", err)
	}
	if got := batchEvents(t, e, b.ID); !slices.Equal(got, []string{"draft>validated", "validated>in_approval", "in_approval>approved", "approved>posted"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestPostgres_BatchSelfApprovalAndRejection(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "SELF", JournalName{Active: true, RequiresApproval: true, SelfApprovalAllowed: true})
	putName(t, e, "STRICT", JournalName{Active: true, RequiresApproval: true})
	b, _ := e.store.CreateBatch(e.ctx, batchOf(e, "SELF", "1.00"))
	b, _ = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	if b, err := e.store.DecideBatch(e.ctx, b.ID, 0, true); err != nil || b.Status != domain.BatchApproved {
		t.Fatalf("self-approval allowed by the journal name: %s %v", b.Status, err)
	}

	r, _ := e.store.CreateBatch(e.ctx, batchOf(e, "STRICT", "1.00"))
	r, _ = e.store.SubmitBatch(e.ctx, r.ID, 0, testToday)
	rejected, err := e.store.DecideBatch(asUser(e, "approver"), r.ID, 0, false)
	if err != nil || rejected.Status != domain.BatchRejected {
		t.Fatalf("reject: %s %v", rejected.Status, err)
	}
	// A rejected batch is corrected (back to draft, approval cleared) or cancelled.
	amount := "2.00"
	lines := batchOf(e, "STRICT", amount).Lines
	edited, err := e.store.UpdateBatch(e.ctx, r.ID, rejected.Version, BatchPatch{Lines: &lines})
	if err != nil || edited.Status != domain.BatchDraft || edited.DecidedBy != "" || edited.SubmittedBy != "" || edited.TotalDebit != "2.00" {
		t.Fatalf("edit rejected: %+v %v", edited, err)
	}
	if got := batchEvents(t, e, r.ID); !slices.Equal(got, []string{"draft>validated", "validated>in_approval", "in_approval>rejected", "rejected>draft"}) {
		t.Fatalf("events = %v", got)
	}
	cancelled, err := e.store.CancelBatch(e.ctx, r.ID, edited.Version)
	if err != nil || cancelled.Status != domain.BatchCancelled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestPostgres_BatchSubmitViolationsChangeNothing(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	in := batchOf(e, "GJ", "125.00")
	in.Lines[1].Amount = "124.99"
	in.Lines = append(in.Lines, BatchLine{Account: "9999", DebitCredit: "D", Amount: "0.01", Currency: "USD"})
	b, err := e.store.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	if codes := rejectionCodes(err); !slices.Contains(codes, "LEDGER_ACCOUNT_NOT_FOUND") {
		t.Fatalf("want violations for every failed rule, got %v", err)
	}
	after, err := e.store.GetBatch(e.ctx, b.ID)
	if err != nil || after.Status != domain.BatchDraft || after.Version != b.Version || after.SubmittedBy != "" {
		t.Fatalf("a refused submit must change nothing: %+v %v", after, err)
	}
	if got := batchEvents(t, e, b.ID); len(got) != 0 {
		t.Fatalf("no event for a refused submit, got %v", got)
	}
	unbalanced := batchOf(e, "GJ", "10.00")
	unbalanced.Lines[1].Amount = "9.00"
	u, _ := e.store.CreateBatch(e.ctx, unbalanced)
	if codes := rejectionCodes(func() error { _, err := e.store.SubmitBatch(e.ctx, u.ID, 0, testToday); return err }()); !slices.Equal(codes, []string{"LEDGER_UNBALANCED"}) {
		t.Fatalf("unbalanced batch: %v", codes)
	}
}

func TestPostgres_BatchEditReturnsToDraftWithVersionCheck(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	b, _ := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "10.00"))
	b, err := e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	if err != nil {
		t.Fatal(err)
	}
	text := "Corrected"
	if _, err := e.store.UpdateBatch(e.ctx, b.ID, b.Version-1, BatchPatch{Description: &text}); !isProblem(err, http.StatusPreconditionFailed, CodeVersionConflict) {
		t.Fatalf("stale version: got %v", err)
	}
	doc := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	edited, err := e.store.UpdateBatch(e.ctx, b.ID, b.Version, BatchPatch{Description: &text, DocumentDate: &doc})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if edited.Status != domain.BatchDraft || edited.Description != "Corrected" || edited.SubmittedBy != "" ||
		edited.Version <= b.Version || edited.DocumentDate == nil || *edited.DocumentDate != "2026-03-31" {
		t.Fatalf("edited batch = %+v", edited)
	}
	cleared, err := e.store.UpdateBatch(e.ctx, b.ID, edited.Version, BatchPatch{ClearDocumentDate: true})
	if err != nil || cleared.DocumentDate != nil || cleared.Description != "Corrected" {
		t.Fatalf("clear document date: %+v %v", cleared, err)
	}
	if got := batchEvents(t, e, b.ID); !slices.Equal(got, []string{"draft>validated", "validated>draft"}) {
		t.Fatalf("events = %v (a draft edit is not a transition)", got)
	}
}

// BR-11.10: a failed post leaves the batch approved with the error recorded,
// and writes no entry.
func TestPostgres_BatchPostFailureKeepsStateAndError(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "APPR", JournalName{Active: true, RequiresApproval: true})
	b, _ := e.store.CreateBatch(e.ctx, batchOf(e, "APPR", "10.00"))
	b, _ = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	b, err := e.store.DecideBatch(asUser(e, "approver"), b.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	suspended := "2026-01-01"
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "6010", accountTag(e, "STD", "6010"), Account{Name: "Freight expense", Type: "expense", Postable: true, SuspendedFrom: &suspended}); err != nil {
		t.Fatal(err)
	}
	_, err = postBatch(e, e.ctx, b, "FAIL-1", false)
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{"LEDGER_ACCOUNT_SUSPENDED"}) {
		t.Fatalf("want LEDGER_ACCOUNT_SUSPENDED, got %v", err)
	}
	after, err := e.store.GetBatch(e.ctx, b.ID)
	if err != nil || after.Status != domain.BatchApproved || !strings.Contains(after.LastError, "LEDGER_ACCOUNT_SUSPENDED") || after.PostedEntryID != "" {
		t.Fatalf("after a failed post: %+v %v", after, err)
	}
	tb, _ := e.store.TrialBalance(e.ctx, BalanceFilter{LedgerID: e.ledger.ID})
	if len(tb.Rows) != 0 {
		t.Fatalf("a failed post must write nothing: %+v", tb.Rows)
	}
	// Lift the suspension and retry with the same key: it posts and clears the error.
	if _, _, err := e.store.PutAccount(e.ctx, "STD", "6010", accountTag(e, "STD", "6010"), Account{Name: "Freight expense", Type: "expense", Postable: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := postBatch(e, e.ctx, after, "FAIL-1", false); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if posted, _ := e.store.GetBatch(e.ctx, b.ID); posted.Status != domain.BatchPosted || posted.LastError != "" {
		t.Fatalf("after retry: %+v", posted)
	}
}

func TestPostgres_BatchJournalNameControls(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "OFF", JournalName{Active: false})
	putName(t, e, "ONE", JournalName{Active: true, LinesLimit: 1})
	putName(t, e, "GJ", JournalName{Active: true})
	putName(t, e, "MIGR", JournalName{Active: true, AllowReconAccounts: true})

	if _, err := e.store.CreateBatch(e.ctx, batchOf(e, "OFF", "1.00")); !isProblem(err, http.StatusUnprocessableEntity, CodeJournalNameInvalid) {
		t.Fatalf("inactive journal name: got %v", err)
	}
	if _, err := e.store.CreateBatch(e.ctx, batchOf(e, "NOPE", "1.00")); !isProblem(err, http.StatusUnprocessableEntity, CodeJournalNameInvalid) {
		t.Fatalf("unknown journal name: got %v", err)
	}
	if _, err := e.store.CreateBatch(e.ctx, batchOf(e, "ONE", "1.00")); !isProblem(err, http.StatusUnprocessableEntity, CodeLineLimit) {
		t.Fatalf("lines limit: got %v", err)
	}
	other := batchOf(e, "GJ", "1.00")
	other.LedgerID = ids.New()
	if _, err := e.store.CreateBatch(e.ctx, other); problemCode(err) != "LEDGER_NOT_FOUND" {
		t.Fatalf("unknown ledger: got %v", err)
	}

	// BR-11.8: manual lines on a reconciliation account need allow_recon_accounts.
	recon := func(name string) BatchInput {
		in := batchOf(e, name, "5.00")
		in.Lines[1].Account = "2110"
		return in
	}
	g, _ := e.store.CreateBatch(e.ctx, recon("GJ"))
	_, err := e.store.SubmitBatch(e.ctx, g.ID, 0, testToday)
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{"LEDGER_RECON_ACCOUNT_MANUAL"}) {
		t.Fatalf("recon account under GJ: got %v", err)
	}
	m, _ := e.store.CreateBatch(e.ctx, recon("MIGR"))
	if m, err = e.store.SubmitBatch(e.ctx, m.ID, 0, testToday); err != nil {
		t.Fatalf("recon account under MIGR: %v", err)
	}
	if _, err := postBatch(e, e.ctx, m, "MIGR-1", true); err != nil {
		t.Fatalf("post under MIGR: %v", err)
	}

	// A journal name that gains a workflow: the validated batch must go through approval.
	v, _ := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "3.00"))
	v, _ = e.store.SubmitBatch(e.ctx, v.ID, 0, testToday)
	putName(t, e, "GJ", JournalName{Active: true, RequiresApproval: true})
	if _, err := postBatch(e, e.ctx, v, "GJ-WF", true); !isProblem(err, http.StatusConflict, CodeBatchApprovalRequired) {
		t.Fatalf("post after the name gained a workflow: got %v", err)
	}
	if v, err = e.store.SubmitBatch(e.ctx, v.ID, 0, testToday); err != nil || v.Status != domain.BatchInApproval {
		t.Fatalf("re-submit for approval: %s %v", v.Status, err)
	}
}

// FR-11.15 / AC-11.11: a journal name with reversal_reason_required demands a
// reason when an entry its batch posted is reversed, though the ledger does not.
func TestPostgres_AC11_11_JournalNameRequiresReversalReason(t *testing.T) {
	e, _ := setup(t)
	if e.ledger.ReversalReasonRequired {
		t.Fatal("the test ledger must not require reasons itself")
	}
	putName(t, e, "RR", JournalName{Active: true, ReversalReasonRequired: true})
	b, _ := e.store.CreateBatch(e.ctx, batchOf(e, "RR", "7.00"))
	b, _ = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	res, err := postBatch(e, e.ctx, b, "RR-1", true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reverseWith(t, e, res.Entries[0].ID, "RR-REV", "")
	if codes := rejectionCodes(err); !slices.Equal(codes, []string{"LEDGER_REASON_REQUIRED"}) {
		t.Fatalf("reversal without reason: got %v", err)
	}
	// An entry posted directly (no journal name) reverses without a reason on this ledger.
	direct, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "RR-D", "7.00"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reverseWith(t, e, direct.Entries[0].ID, "RR-D-REV", ""); err != nil {
		t.Fatalf("direct entry reversal: %v", err)
	}
}

// AC-11.19: a batch posted without description gets the manual_journal template.
func TestPostgres_AC11_19_BatchDefaultDescription(t *testing.T) {
	e, _ := setup(t)
	putTemplate(t, tenantWide(e), DescriptionTemplate{Scope: ScopeTenant, SourceType: "manual_journal",
		Template: "Journal %1 of %2", FieldBindings: map[string]string{"1": "voucher", "2": "document_date"}})
	putName(t, e, "GJ", JournalName{Active: true})
	in := batchOf(e, "GJ", "100.00")
	in.Description = ""
	doc := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	in.DocumentDate = &doc
	b, _ := e.store.CreateBatch(e.ctx, in)
	b, _ = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday)
	res, err := postBatch(e, e.ctx, b, "DESC-B", true)
	if err != nil {
		t.Fatal(err)
	}
	entry := res.Entries[0]
	if want := "Journal " + entry.Voucher + " of 2026-03-31"; entry.Description != want {
		t.Fatalf("description %q, want %q", entry.Description, want)
	}
}

func TestPostgres_BatchIsolationAndList(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	putName(t, e, "AJ", JournalName{Active: true})
	b, _ := e.store.CreateBatch(e.ctx, batchOf(e, "GJ", "1.00"))
	a, _ := e.store.CreateBatch(e.ctx, batchOf(e, "AJ", "2.00"))
	if _, err := e.store.SubmitBatch(e.ctx, a.ID, 0, testToday); err != nil {
		t.Fatal(err)
	}

	all, err := e.store.ListBatches(e.ctx, BatchFilter{LedgerID: e.ledger.ID})
	if err != nil || len(all) != 2 || all[0].ID != a.ID || all[1].Lines != nil || all[1].TotalDebit != "1.00" || all[1].LineCount != 2 {
		t.Fatalf("list newest first without lines: %+v %v", all, err)
	}
	drafts, _ := e.store.ListBatches(e.ctx, BatchFilter{Status: domain.BatchDraft})
	named, _ := e.store.ListBatches(e.ctx, BatchFilter{JournalName: "AJ"})
	if len(drafts) != 1 || drafts[0].ID != b.ID || len(named) != 1 || named[0].ID != a.ID {
		t.Fatalf("filters: drafts %d named %d", len(drafts), len(named))
	}

	otherTenant := principalCtx(testkit.TenantID(2), "")
	if _, err := e.store.GetBatch(otherTenant, b.ID); problemCode(err) != CodeBatchNotFound {
		t.Fatalf("another tenant must not see the batch, got %v", err)
	}
	if list, err := e.store.ListBatches(otherTenant, BatchFilter{}); err != nil || len(list) != 0 {
		t.Fatalf("another tenant lists %d batches (%v)", len(list), err)
	}
	if names, err := e.store.ListJournalNames(otherTenant); err != nil || len(names) != 0 {
		t.Fatalf("another tenant lists %d journal names (%v)", len(names), err)
	}
	otherEntity := principalCtx(e.tenant, ids.New())
	if _, err := e.store.GetBatch(otherEntity, b.ID); problemCode(err) != CodeBatchNotFound {
		t.Fatalf("a caller scoped to another legal entity must not see the batch, got %v", err)
	}
	for _, table := range []string{"journal_batch", "journal_batch_line"} {
		var n int
		err := dbx.WithTenantTx(otherEntity, e.pool, func(tx pgx.Tx) error {
			return tx.QueryRow(otherEntity, `select count(*) from `+table).Scan(&n)
		})
		if err != nil || n != 0 {
			t.Fatalf("%s: another legal entity sees %d rows (err %v)", table, n, err)
		}
	}
}

func TestPostgres_JournalNameCRUD(t *testing.T) {
	e, _ := setup(t)
	created, isNew, err := e.store.PutJournalName(e.ctx, "GJ", 0, JournalName{Name: "General", RequiresApproval: true, LinesLimit: 50, Active: true})
	if err != nil || !isNew || created.Code != "GJ" || !created.RequiresApproval || created.LinesLimit != 50 {
		t.Fatalf("create: %+v %v %v", created, isNew, err)
	}
	replaced, isNew, err := e.store.PutJournalName(e.ctx, "GJ", created.LockVersion, JournalName{Name: "General journal", LinesLimit: 10, Active: false})
	if err != nil || isNew || replaced.Name != "General journal" || replaced.RequiresApproval || replaced.Active {
		t.Fatalf("replace: %+v %v %v", replaced, isNew, err)
	}
	got, err := e.store.GetJournalName(e.ctx, "GJ")
	if err != nil || got.LinesLimit != 10 {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := e.store.GetJournalName(e.ctx, "NOPE"); problemCode(err) != "LEDGER_JOURNAL_NAME_NOT_FOUND" {
		t.Fatalf("unknown: %v", err)
	}
}

func TestPostgres_BatchStatisticalLinePostsWithQuantity(t *testing.T) {
	e, _ := setup(t)
	putName(t, e, "GJ", JournalName{Active: true})
	in := batchOf(e, "GJ", "5.00")
	in.Lines = append(in.Lines,
		BatchLine{Account: "6010", DebitCredit: "D", Amount: "0", Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"}, Quantity: "40.000000", Unit: "HR"},
		BatchLine{Account: "6010", DebitCredit: "C", Amount: "0", Currency: "USD", Dimensions: map[string]string{"DEPT": "D100"}, Quantity: "40.000000", Unit: "HR"})
	b, err := e.store.CreateBatch(e.ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := e.store.GetBatch(e.ctx, b.ID)
	if err != nil || got.Lines[2].Quantity != "40.000000" || got.Lines[2].Unit != "HR" || got.Lines[0].Quantity != "" {
		t.Fatalf("batch lines read back: %+v %v", got.Lines, err)
	}
	if b, err = e.store.SubmitBatch(e.ctx, b.ID, 0, testToday); err != nil {
		t.Fatalf("submit: %v", err)
	}
	res, err := postBatch(e, e.ctx, b, "STAT-B1", true)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if l := res.Entries[0].Lines[2]; l.Quantity != "40.000000" || l.Unit != "HR" {
		t.Fatalf("posted statistical line: %+v", l)
	}
}

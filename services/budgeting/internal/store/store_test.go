package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/budgeting/internal/domain"
	budgetevents "github.com/BenjaminSRussell/passage/services/budgeting/internal/events"
	"github.com/BenjaminSRussell/passage/services/budgeting/migrations"
)

// testCtx carries a principal with no legal-entity scope so RLS shows every row.
func testCtx(tenantID string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// setup migrates a fresh schema, creates a rule version and posts an Original
// budget of 100,000 on dept-10 / FY2026.
func setup(t *testing.T) (context.Context, *Postgres, *pgxpool.Pool, Rule) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := testCtx(testkit.TenantID(1), "budget.configure", "budget.maintain", "budget.check")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	_, raw, err := st.CreateRule(ctx, "rule-1", "h-rule", NewRule{LegalEntityID: ids.New(), Code: "DEPT", WarnPct: d("0"), BlockPct: d("5"),
		Currency: "USD", EffectiveFrom: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	var rule Rule
	if err := json.Unmarshal(raw, &rule); err != nil {
		t.Fatal(err)
	}
	_, _, err = st.PostRegisterEntry(ctx, "entry-1", "h-entry", RegisterRequest{RuleVersionID: rule.ID, EntryType: domain.EntryOriginal,
		Lines: []domain.RegisterLine{{DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d("100000")}}})
	if err != nil {
		t.Fatal(err)
	}
	return ctx, st, pool, rule
}

func checkReq(ruleID string, amount string) CheckRequest {
	return CheckRequest{RuleVersionID: ruleID, DocumentID: ids.New(), DocumentType: "PurchaseOrder",
		Lines: []CheckLine{{LineID: ids.New(), DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d(amount)}}}
}

func decodeCheck(t *testing.T, raw []byte) CheckResponse {
	t.Helper()
	var resp CheckResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// AC-38.4: two concurrent POs of 8,000 against 10,000 available: exactly one passes.
func TestCheck_ConcurrentLastDollar(t *testing.T) {
	ctx, st, _, rule := setup(t)
	// Actuals 60,000 come from journal events in a later slice; set them directly.
	if err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update budget_control_balance set actuals = 60000`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CheckBudget(ctx, "seed-30k", "h-seed", checkReq(rule.ID, "30000"), false); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make([]CheckResponse, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, raw, err := st.CheckBudget(ctx, "race-"+ids.New(), "h-race", checkReq(rule.ID, "8000"), false)
			if err != nil {
				t.Error(err)
				return
			}
			if err := json.Unmarshal(raw, &results[i]); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	passes := 0
	for _, r := range results {
		if r.Result == domain.Pass {
			passes++
		}
	}
	if passes != 1 {
		t.Fatalf("passes = %d, want exactly 1 (%+v)", passes, results)
	}
}

// AC-38.3 end to end: a 10 x 50.00 PO reservation relieved by a 10 x 52.00 invoice.
func TestRelieve_InvoiceExactness(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := checkReq(rule.ID, "500")
	_, raw, err := st.CheckBudget(ctx, "po-1", "h-po", req, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCheck(t, raw); got.Result != domain.Pass {
		t.Fatalf("PO check = %s", got.Result)
	}
	encID := decodeCheck(t, raw).Lines[0].EncumbranceID
	if encID != lookupEncumbrance(t, ctx, st, req.Lines[0].LineID) {
		t.Fatalf("check response encumbrance %q does not match the stored one", encID)
	}
	_, raw, err = st.RelieveEncumbrance(ctx, "inv-1", "h-inv", RelieveRequest{EncumbranceID: encID,
		InvoiceID: ids.New(), InvoiceAmount: d("520"), MatchedShare: d("500")})
	if err != nil {
		t.Fatal(err)
	}
	var rel RelieveResponse
	if err := json.Unmarshal(raw, &rel); err != nil {
		t.Fatal(err)
	}
	if !rel.Relieved.Equal(d("500")) || !rel.Excess.Equal(d("20")) || rel.Status != "Relieved" {
		t.Fatalf("relief = %+v, want relieved 500, excess 20, Relieved", rel)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
}

// lookupEncumbrance finds the encumbrance id written for a source line.
func lookupEncumbrance(t *testing.T, ctx context.Context, st *Postgres, lineID string) string {
	t.Helper()
	var id string
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select id::text from encumbrance where source_line_id = $1`, lineID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Idempotency: a replay returns the stored body; a reused key with another body is refused.
func TestCommand_IdempotencyReplayAndReuse(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := checkReq(rule.ID, "1000")
	_, first, err := st.CheckBudget(ctx, "key-replay", "hash-a", req, false)
	if err != nil {
		t.Fatal(err)
	}
	_, replay, err := st.CheckBudget(ctx, "key-replay", "hash-a", req, false)
	if err != nil {
		t.Fatal(err)
	}
	// The stored body is jsonb, so a replay is the same document, not the same bytes.
	if a, b := decodeCheck(t, first), decodeCheck(t, replay); a.ID != b.ID || a.Result != b.Result || len(a.Lines) != len(b.Lines) {
		t.Fatalf("replay differs:\n%s\n%s", first, replay)
	}
	if checks, err := st.ListChecks(ctx, rule.ID, req.DocumentID); err != nil || len(checks) != 1 {
		t.Fatalf("replay recorded a second check: %d (%v)", len(checks), err)
	}
	if _, _, err := st.CheckBudget(ctx, "key-replay", "hash-b", req, false); err == nil {
		t.Fatal("reused key with a different body was accepted")
	}
}

// assertEncumbranceInvariant checks PRD 7.38.4: Σ (reserved − relieved) of a
// balance's encumbrances equals the balance's encumbrances column.
func assertEncumbranceInvariant(t *testing.T, ctx context.Context, st *Postgres, ruleID string) {
	t.Helper()
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select b.control_dimension_key, b.encumbrances::text, b.pre_encumbrances::text,
			coalesce(sum(e.reserved - e.relieved - e.released) filter (where e.stage = 'encumbrance'), 0)::text,
			coalesce(sum(e.reserved - e.relieved - e.released) filter (where e.stage = 'pre'), 0)::text
			from budget_control_balance b left join encumbrance e on e.tenant_id = b.tenant_id and e.balance_id = b.id
			where b.rule_version_id = $1 group by b.tenant_id, b.id`, ruleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var dim, bal, pre, open, openPre string
			if err := rows.Scan(&dim, &bal, &pre, &open, &openPre); err != nil {
				return err
			}
			if !d(bal).Equal(d(open)) {
				t.Errorf("balance %s: encumbrances %s, open reservations %s", dim, bal, open)
			}
			if !d(pre).Equal(d(openPre)) {
				t.Errorf("balance %s: pre-encumbrances %s, open pre-encumbrance lines %s", dim, pre, openPre)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func availableOf(t *testing.T, ctx context.Context, st *Postgres, ruleID string) decimal.Decimal {
	t.Helper()
	rows, err := st.Available(ctx, ruleID, "dept-10", "FY2026")
	if err != nil || len(rows) != 1 {
		t.Fatalf("available: %v (%d rows)", err, len(rows))
	}
	return rows[0].Available
}

// AC-38.1 / AC-38.2 against the stored balance: fail reserves nothing, warn reserves.
func TestCheck_ThresholdsAgainstStoredBalance(t *testing.T) {
	ctx, st, _, rule := setup(t)
	if err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update budget_control_balance set actuals = 60000`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CheckBudget(ctx, "seed", "h", checkReq(rule.ID, "30000"), false); err != nil {
		t.Fatal(err)
	}
	_, raw, err := st.CheckBudget(ctx, "ac1", "h", checkReq(rule.ID, "12000"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCheck(t, raw); got.Result != domain.Fail || got.Lines[0].EncumbranceID != "" || !got.Lines[0].OverPct.Equal(d("20")) {
		t.Fatalf("AC-38.1: %+v", got)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("10000")) {
		t.Fatalf("a failed check reserved funds: available %s", got)
	}
	_, raw, err = st.CheckBudget(ctx, "ac2", "h", checkReq(rule.ID, "10400"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCheck(t, raw); got.Result != domain.Warn || got.Lines[0].EncumbranceID == "" {
		t.Fatalf("AC-38.2: %+v", got)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("-400")) {
		t.Fatalf("warn must reserve 10,400: available %s", got)
	}
	checks, err := st.ListChecks(ctx, rule.ID, "")
	if err != nil || len(checks) != 3 {
		t.Fatalf("recorded checks = %d (%v), want 3 including the failed one", len(checks), err)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)
}

// FR-38.4 step 4 and BR-38.3: a revision of the same line is checked for the
// net change only, lowering releases the difference, and the prior comes from
// the stored reservation.
func TestCheck_RevisionUsesStoredReservation(t *testing.T) {
	ctx, st, _, rule := setup(t)
	req := checkReq(rule.ID, "60000")
	if _, _, err := st.CheckBudget(ctx, "po-v1", "h", req, false); err != nil {
		t.Fatal(err)
	}
	// Raising to 90,000 needs only 30,000 more; 40,000 is available, so it passes.
	req.Lines[0].Amount = d("90000")
	_, raw, err := st.CheckBudget(ctx, "po-v2", "h", req, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCheck(t, raw); got.Result != domain.Pass || !got.Lines[0].Net.Equal(d("30000")) || !got.Lines[0].Prior.Equal(d("60000")) {
		t.Fatalf("raise: %+v", got.Lines[0])
	}
	// Lowering to 20,000 releases 70,000 and never fails, even with block 0.
	req.Lines[0].Amount = d("20000")
	_, raw, err = st.CheckBudget(ctx, "po-v3", "h", req, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeCheck(t, raw); got.Result != domain.Pass || !got.Lines[0].Net.Equal(d("-70000")) {
		t.Fatalf("lower: %+v", got.Lines[0])
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("80000")) {
		t.Fatalf("available after revisions = %s, want 80000", got)
	}
	encs, err := st.ListEncumbrances(ctx, rule.ID, "")
	if err != nil || len(encs) != 1 || !encs[0].Reserved.Equal(d("20000")) || len(encs[0].History) != 3 {
		t.Fatalf("encumbrances = %+v (%v)", encs, err)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)

	// The same line presented on another combination is refused, not double reserved.
	req.Lines[0].DimensionKey = "dept-20"
	if _, _, err := st.CheckBudget(ctx, "po-v4", "h", req, false); !hasCode(err, CodeLineMoved) {
		t.Fatalf("moved line: err = %v, want %s", err, CodeLineMoved)
	}
}

// A relief with no matched share changes nothing and keeps the encumbrance Open.
func TestRelieve_ZeroMatchKeepsOpen(t *testing.T) {
	ctx, st, _, rule := setup(t)
	_, raw, err := st.CheckBudget(ctx, "po", "h", checkReq(rule.ID, "500"), false)
	if err != nil {
		t.Fatal(err)
	}
	encID := decodeCheck(t, raw).Lines[0].EncumbranceID
	_, raw, err = st.RelieveEncumbrance(ctx, "inv-0", "h", RelieveRequest{EncumbranceID: encID, InvoiceID: ids.New(), InvoiceAmount: d("0"), MatchedShare: d("0")})
	if err != nil {
		t.Fatal(err)
	}
	var rel RelieveResponse
	if err := json.Unmarshal(raw, &rel); err != nil || rel.Status != domain.StatusOpen || !rel.Remaining.Equal(d("500")) {
		t.Fatalf("relief = %+v (%v)", rel, err)
	}
	if _, _, err := st.RelieveEncumbrance(ctx, "inv-x", "h", RelieveRequest{EncumbranceID: ids.New(), InvoiceID: ids.New()}); !hasCode(err, CodeEncumbranceNotFound) {
		t.Fatalf("unknown encumbrance: err = %v", err)
	}
}

// FR-38.5: closing a PO after a partial invoice releases exactly the open
// remainder back to available funds; the line is then Released, a second
// release, a re-check of the line and a consumption onto it are refused, and a
// late invoice relieves nothing (its whole amount is excess).
func TestRelease_ClosedPOGivesBackResidual(t *testing.T) {
	ctx, st, pool, rule := setup(t)
	req := checkReq(rule.ID, "500")
	_, raw, err := st.CheckBudget(ctx, "po", "h", req, false)
	if err != nil {
		t.Fatal(err)
	}
	encID := decodeCheck(t, raw).Lines[0].EncumbranceID
	if _, _, err := st.RelieveEncumbrance(ctx, "inv-1", "h", RelieveRequest{EncumbranceID: encID, InvoiceID: ids.New(), InvoiceAmount: d("320"), MatchedShare: d("320")}); err != nil {
		t.Fatal(err)
	}
	// Relief moves 320 out of encumbrances (actuals arrive from journal events).
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("99820")) {
		t.Fatalf("available before release = %s, want 99820", got)
	}
	poID := req.DocumentID
	status, raw, err := st.ReleaseEncumbrance(ctx, "rel-1", "h-rel", ReleaseRequest{EncumbranceID: encID, DocumentID: poID, Reason: ReleaseClosed})
	if err != nil || status != 200 {
		t.Fatalf("release: status %d err %v", status, err)
	}
	var rel ReleaseResponse
	if err := json.Unmarshal(raw, &rel); err != nil || !rel.Released.Equal(d("180")) || !rel.Relieved.Equal(d("320")) || rel.Status != domain.StatusReleased {
		t.Fatalf("release = %+v (%v)", rel, err)
	}
	// The 180 residual is back in available funds and no encumbrance is left.
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("100000")) {
		t.Fatalf("available after release = %s, want 100000", got)
	}
	assertEncumbranceInvariant(t, ctx, st, rule.ID)

	// A replay with the same key returns the stored answer and moves nothing.
	_, again, err := st.ReleaseEncumbrance(ctx, "rel-1", "h-rel", ReleaseRequest{EncumbranceID: encID, DocumentID: poID, Reason: ReleaseClosed})
	var replay ReleaseResponse
	if err != nil || json.Unmarshal(again, &replay) != nil || replay.EncumbranceID != encID || !replay.Released.Equal(rel.Released) || replay.Status != rel.Status {
		t.Fatalf("replay: %s (%v), want %+v", again, err, rel)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("100000")) {
		t.Fatalf("available after replay = %s, want 100000", got)
	}
	if _, _, err := st.ReleaseEncumbrance(ctx, "rel-2", "h", ReleaseRequest{EncumbranceID: encID, DocumentID: poID, Reason: ReleaseCancelled}); !hasCode(err, CodeNothingToRelease) {
		t.Fatalf("second release: err = %v, want %s", err, CodeNothingToRelease)
	}
	if _, _, err := st.CheckBudget(ctx, "po-v2", "h", req, false); !hasCode(err, CodeLineReleased) {
		t.Fatalf("re-check of a released line: err = %v, want %s", err, CodeLineReleased)
	}
	res := earmark(t, ctx, st, rule.ID, domain.FundsReservation, "2099-12-31", "100")
	if _, _, err := st.ConsumeReservation(ctx, "con-1", "h", ConsumeRequest{ReservationID: res.ID, LineID: res.Lines[0].LineID,
		DocumentType: "PurchaseOrder", DocumentID: poID, DocumentLineID: req.Lines[0].LineID, Amount: d("10")}); !hasCode(err, CodeLineReleased) {
		t.Fatalf("consumption onto a released line: err = %v, want %s", err, CodeLineReleased)
	}
	_, raw, err = st.RelieveEncumbrance(ctx, "inv-2", "h", RelieveRequest{EncumbranceID: encID, InvoiceID: ids.New(), InvoiceAmount: d("50"), MatchedShare: d("50")})
	if err != nil {
		t.Fatal(err)
	}
	var late RelieveResponse
	if err := json.Unmarshal(raw, &late); err != nil || !late.Relieved.IsZero() || !late.Excess.Equal(d("50")) || late.Status != domain.StatusReleased {
		t.Fatalf("late invoice = %+v (%v)", late, err)
	}
	got, err := st.GetEncumbrance(ctx, encID)
	if err != nil || !got.Released.Equal(d("180")) || !got.Remaining.IsZero() || got.Status != domain.StatusReleased || got.History[len(got.History)-1].Type != "Release" {
		t.Fatalf("encumbrance = %+v (%v)", got, err)
	}
	if list, err := st.ListEncumbrances(ctx, rule.ID, domain.StatusReleased); err != nil || len(list) != 1 {
		t.Fatalf("released filter = %+v (%v)", list, err)
	}
	var payload map[string]string
	if err := pool.QueryRow(ctx, `select payload->'data' from outbox where type = $1`, budgetevents.EncumbranceReleased).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["amount"] != "180" || payload["reason"] != ReleaseClosed || payload["document_id"] != poID {
		t.Fatalf("released event = %v", payload)
	}
	if _, _, err := st.ReleaseEncumbrance(ctx, "rel-x", "h", ReleaseRequest{EncumbranceID: ids.New(), DocumentID: poID, Reason: ReleaseCancelled}); !hasCode(err, CodeEncumbranceNotFound) {
		t.Fatalf("unknown encumbrance: err = %v", err)
	}
}

// Transfers net to zero (CO-0604) and posted entries are listed with their total.
func TestRegister_TransferAndList(t *testing.T) {
	ctx, st, _, rule := setup(t)
	bad := RegisterRequest{RuleVersionID: rule.ID, EntryType: domain.EntryTransfer, Lines: []domain.RegisterLine{
		{DimensionKey: "dept-10", SpanKey: "FY2026", Amount: d("-5000")}, {DimensionKey: "dept-20", SpanKey: "FY2026", Amount: d("4000")}}}
	if _, _, err := st.PostRegisterEntry(ctx, "tr-bad", "h", bad); !hasCode(err, domain.CodeTransferUnbalanced) {
		t.Fatalf("unbalanced transfer: err = %v", err)
	}
	bad.Lines[1].Amount = d("5000")
	if _, _, err := st.PostRegisterEntry(ctx, "tr-ok", "h", bad); err != nil {
		t.Fatal(err)
	}
	if got := availableOf(t, ctx, st, rule.ID); !got.Equal(d("95000")) {
		t.Fatalf("available after transfer out = %s", got)
	}
	entries, err := st.ListRegisterEntries(ctx, rule.ID)
	if err != nil || len(entries) != 2 || entries[0].EntryType != domain.EntryTransfer || !entries[0].Total.IsZero() || entries[0].Lines[0].DimensionKey == "" {
		t.Fatalf("entries = %+v (%v)", entries, err)
	}
	if _, err := st.ListRegisterEntries(ctx, ids.New()); !hasCode(err, CodeRuleNotFound) {
		t.Fatalf("unknown rule: err = %v", err)
	}
}

// Rule versions count up per legal entity and code.
func TestCreateRule_VersionsCountUp(t *testing.T) {
	ctx, st, _, rule := setup(t)
	_, raw, err := st.CreateRule(ctx, "rule-2", "h", NewRule{LegalEntityID: rule.LegalEntityID, Code: rule.Code, WarnPct: d("1"), BlockPct: d("2"),
		Currency: "USD", EffectiveFrom: "2026-07-01"})
	if err != nil {
		t.Fatal(err)
	}
	var next Rule
	if err := json.Unmarshal(raw, &next); err != nil || next.Version != 2 {
		t.Fatalf("second version = %+v (%v)", next, err)
	}
	rules, err := st.ListRules(ctx, rule.LegalEntityID)
	if err != nil || len(rules) != 2 || rules[0].Version != 2 {
		t.Fatalf("rules = %+v (%v)", rules, err)
	}
}

func hasCode(err error, code string) bool {
	var p *httpx.Problem
	return errors.As(err, &p) && p.Code == code
}

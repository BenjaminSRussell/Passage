package firing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

// The whole job against Postgres as the application role: a schedule saved in
// February fires on 1 April for March, the CSV in the library holds the March
// balance, the second pass finds nothing due, and a ledger the read model does
// not know ends as a recorded failure that no later pass repeats.
func TestRunner_EndToEndAgainstPostgres(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: Actor,
		Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	ledger, entity, unknown := ids.New(), ids.New(), ids.New()
	entry := domain.PostedEntry{LedgerID: ledger, LegalEntityID: entity, EntryID: ids.New(), PostingSequence: 1, PostingDate: "2026-03-15",
		PeriodKey: "2026-03", Voucher: "V1", SourceSystem: "ledger", Lines: []domain.SourceLine{
			{LineNumber: 1, Account: "1000", DebitCredit: "D", AmountAcct: decimal.RequireFromString("125.50"), SourceURI: "passage://payables/invoices/1", TraceID: "0123456789abcdef0123456789abcdef"},
			{LineNumber: 2, Account: "2000", DebitCredit: "C", AmountAcct: decimal.RequireFromString("125.50"), SourceURI: "passage://payables/invoices/1", TraceID: "0123456789abcdef0123456789abcdef"},
		}}
	if n, err := st.ApplyEntry(ctx, entry); err != nil || n != 1 {
		t.Fatalf("apply: %d %v", n, err)
	}
	def := domain.Definition{Code: "TB-E2E", Name: "Trial balance", Kind: domain.KindTrialBalance,
		Rows:    []domain.Row{{Code: "CASH", Kind: domain.RowAccountRange, Label: "Cash", AccountFrom: "1000", AccountTo: "1000", NormalBalance: "debit"}},
		Columns: []domain.Column{{Code: "CUR", Label: "Closing", PeriodExpr: "P", CurrencyType: "acct"}}}
	if _, _, err := st.SaveDraft(ctx, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "TB-E2E", "tester"); err != nil {
		t.Fatal(err)
	}
	sc := schedule("csv", "html")
	sc.Code, sc.Definition, sc.LedgerIDs = "E2E", "TB-E2E", []string{ledger, unknown}
	if _, _, err := st.SaveSchedule(ctx, sc, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `update report_schedule set updated_at = '2026-02-10T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}

	r := Runner{Store: st, Now: func() time.Time { return fire.Add(time.Minute) }, MaxLagSeconds: 30, RetentionYears: 7}
	res, err := r.Run(ctx)
	if err != nil || res != (Result{Due: 2, Completed: 1, Failed: 1}) {
		t.Fatalf("first pass %+v err %v", res, err)
	}
	if res, err := r.Run(ctx); err != nil || res.Due != 0 {
		t.Fatalf("second pass %+v err %v: both firings are recorded", res, err)
	}

	page, err := st.ListLibrary(ctx, store.LibraryQuery{Format: "csv"}, nil, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("library %+v err %v", page, err)
	}
	_, body, err := st.LibraryContent(ctx, page.Items[0].ID)
	if err != nil || !strings.Contains(string(body), "125.50") || !strings.Contains(string(body), "2026-03") {
		t.Fatalf("csv %q err %v: want the March balance", body, err)
	}
	if e := page.Items[0]; e.PeriodYear != 2026 || e.PeriodNo != 3 || e.LegalEntityID != entity || !e.RetainUntil.After(time.Date(2033, 3, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("entry %+v", e)
	}
	firings, err := st.ListFirings(ctx, "E2E", false, 10)
	if err != nil || len(firings) != 2 {
		t.Fatalf("firings %+v err %v", firings, err)
	}
	var failed store.Firing
	for _, f := range firings {
		if f.Status == store.FiringFailed {
			failed = f
		}
	}
	if failed.LedgerID != unknown || failed.ErrorCode != "REPORT-0031" || failed.Attempts != 2 {
		t.Fatalf("failed firing %+v: an unknown ledger is REPORT-0031 after the retry", failed)
	}
}

package store_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

func net(t *testing.T, l ledgerclient.Ledger, ledgerID, account string) decimal.Decimal {
	t.Helper()
	b, err := l.Balance(context.Background(), ledgerclient.BalanceQuery{LedgerID: ledgerID, Account: account})
	if err != nil {
		t.Fatal(err)
	}
	return b.Net()
}

// glScenario is the disposal proof asset: IFRS 10,000.00 with a 3,000.00 depreciation adjustment and a 1,000.00 impairment, TAX 10,000.00 with a
// posted MACRS 2026 run of 2,000.00, then a 25% sale for 2,000.00, a 33.33% scrap and the final sale for 3,500.00 (hand-derived in disposal_proof_test.go).
func glScenario(t *testing.T) (*store.Postgres, string, string) {
	s, _, ctx := setup(t)
	le := ids.New()
	asset := newAsset(t, s, ctx, le, "FA-GL-37")
	ifrs, err := s.AddBook(ctx, asset.ID, store.Book{BookCode: "IFRS", Currency: "USD", Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None",
		InServiceDate: day("2026-01-01"), Cost: d("10000"), SalvageValue: d("0")}, "gl-ifrs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBook(ctx, asset.ID, store.Book{BookCode: "TAX", Currency: "USD", Method: string(domain.MethodMACRS), ServiceLifePeriods: 5,
		Convention: "HalfYear", InServiceDate: day("2026-01-01"), Cost: d("10000"), SalvageValue: d("0")}, "gl-tax"); err != nil {
		t.Fatal(err)
	}
	for i, adj := range []store.AdjustmentInput{
		{BookID: ifrs.ID, Type: domain.AdjustmentDepreciation, Date: day("2026-12-31"), Amount: d("3000")},
		{BookID: ifrs.ID, Type: domain.AdjustmentImpairment, Date: day("2026-12-31"), Amount: d("1000")},
	} {
		if _, err := s.Adjust(ctx, asset.ID, adj, "gl-adj-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	run, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "TAX", Period: "2026"}, "gl-run-tax")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostRun(ctx, run.ID, run.Version); err != nil {
		t.Fatal(err)
	}
	return s, le, asset.ID
}

func disposeAll(t *testing.T, s *store.Postgres, assetID string) {
	_, _, ctx := setup(t)
	for _, st := range []struct {
		kind          domain.DisposalKind
		pct, proceeds string
		date          string
	}{{domain.DisposalSale, "25", "2000", "2027-02-01"}, {domain.DisposalScrap, "33.33", "0", "2027-03-01"}, {domain.DisposalSale, "100", "3500", "2027-04-01"}} {
		detail, err := s.GetAsset(ctx, assetID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Dispose(ctx, assetID, store.DisposalInput{Version: detail.Version, Kind: st.kind, Percentage: d(st.pct), Proceeds: d(st.proceeds), Date: day(st.date)}, "gl-disp-"+st.date); err != nil {
			t.Fatal(err)
		}
	}
}

// W37 AC2 and AC4: the acquisition, adjustments, depreciation run and the three disposals of both books are booked in a fake ledger, the disposal
// gain and loss land on the gain and loss accounts to the cent, and the register's cost and accumulated depreciation reconcile to the ledger
// accounts before and after the disposals (all zero once the asset is sold out).
func TestGL_DisposalsAndDepreciationAreBookedAndReconcile(t *testing.T) {
	s, le, assetID := glScenario(t)
	_, _, ctx := setup(t)
	fake := ledgerclient.NewFake()
	ledgers := map[string]string{"IFRS": ids.New(), "TAX": ids.New()}
	both := func(account string) decimal.Decimal {
		return net(t, fake, ledgers["IFRS"], account).Add(net(t, fake, ledgers["TAX"], account))
	}
	p := store.PlaceholderProfile
	req := func(book string) store.GLPostRequest {
		return store.GLPostRequest{LegalEntityID: le, LedgerID: ledgers[book], BookCode: book, Currency: "USD"}
	}
	rec := func(book string) store.GLReconciliation {
		r, err := s.ReconcileGL(ctx, fake, store.GLReconcileRequest{LegalEntityID: le, LedgerID: ledgers[book], BookCode: book, Currency: "USD"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	before := rec("IFRS")
	if before.Reconciled || before.Unposted != 3 || len(before.Accounts) != 2 || !before.Accounts[0].Subledger.Equal(d("10000")) {
		t.Fatalf("nothing booked yet: %+v", before)
	}
	res, err := s.PostToGL(ctx, fake, req("IFRS"))
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 3 || !res.Booked[0].Placeholder {
		t.Fatalf("IFRS booking: %+v %v", res, err)
	}
	if r := rec("IFRS"); !r.Reconciled || r.Unposted != 0 || !net(t, fake, ledgers["IFRS"], p.Cost).Equal(d("10000")) || !net(t, fake, ledgers["IFRS"], p.Accumulated).Equal(d("-4000")) {
		t.Fatalf("IFRS after adjustments: %+v", r)
	}
	if res, err = s.PostToGL(ctx, fake, req("TAX")); err != nil || len(res.Booked) != 2 {
		t.Fatalf("TAX booking (acquisition and the run): %+v %v", res, err)
	}
	for _, e := range fake.Entries() {
		if e.SourceType == "asset-depreciation-run" && (len(e.Lines) != 2 || !e.Lines[0].Amount.Equal(d("2000")) || !strings.HasSuffix(e.IdempotencyKey, ":TAX:USD")) {
			t.Fatalf("a run is one entry of expense against accumulated depreciation: %+v", e)
		}
	}
	if !both(p.DepreciationExpense).Equal(d("5000")) {
		t.Fatalf("depreciation expense %s, want 3,000.00 adjustment + 2,000.00 run", both(p.DepreciationExpense))
	}

	disposeAll(t, s, assetID)
	if r := rec("IFRS"); r.Reconciled || r.Unposted != 3 {
		t.Fatalf("unbooked disposals must show as a difference: %+v", r)
	}
	for _, book := range []string{"IFRS", "TAX"} {
		if res, err = s.PostToGL(ctx, fake, req(book)); err != nil || len(res.Booked) != 3 || len(res.Failed) != 0 {
			t.Fatalf("%s disposals: %+v %v", book, res, err)
		}
		if r := rec(book); !r.Reconciled || r.Unposted != 0 {
			t.Fatalf("%s after disposals: %+v", book, r)
		}
	}
	want := map[string]string{
		p.Cost: "0", p.Accumulated: "0", p.Proceeds: "11000", p.Gain: "-999.85", p.Loss: "3999.85", // IFRS gain 500.00 + 499.85, loss 1,499.85; TAX loss 1,999.80 + 500.20
		p.Impairment: "1000", p.AcquisitionClearing: "-20000", p.TransferClearing: "0",
	}
	for account, v := range want {
		if got := both(account); !got.Equal(d(v)) {
			t.Errorf("GL %s = %s, want %s", account, got, v)
		}
	}
	entries := len(fake.Entries())
	for _, book := range []string{"IFRS", "TAX"} {
		if again, err := s.PostToGL(ctx, fake, req(book)); err != nil || len(again.Booked) != 0 || len(fake.Entries()) != entries {
			t.Fatalf("a second %s run books nothing: %+v %v", book, again, err)
		}
	}
}

func TestGL_ARefusedJournalStaysUnbookedAndRetries(t *testing.T) {
	s, le, _ := glScenario(t)
	_, _, ctx := setup(t)
	fake, ledgerID := ledgerclient.NewFake(), ids.New()
	req := store.GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "TAX", Currency: "USD"}
	fake.FailPosts(&ledgerclient.Error{Op: "post", Status: http.StatusUnprocessableEntity, Code: ledgerclient.CodePeriodClosed})
	res, err := s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Booked) != 0 || len(res.Failed) != 2 || res.Failed[0].Code != ledgerclient.CodePeriodClosed {
		t.Fatalf("refused: %+v %v", res, err)
	}
	r, err := s.ReconcileGL(ctx, fake, store.GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "TAX", Currency: "USD"})
	if err != nil || r.Reconciled || r.Unposted != 2 {
		t.Fatalf("refused journals are unreconciled: %+v %v", r, err)
	}
	fake.FailPosts(nil)
	if res, err = s.PostToGL(ctx, fake, req); err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("retry: %+v %v", res, err)
	}
}

func TestGL_ProfileOverridesThePlaceholderAndOtherEntitiesAreIsolated(t *testing.T) {
	s, le, _ := glScenario(t)
	_, _, ctx := setup(t)
	fake, ledgerID := ledgerclient.NewFake(), ids.New()
	custom := store.PostingProfile{LegalEntityID: le, ClassCode: "VEH", Cost: "1601", Accumulated: "1691", DepreciationExpense: "6901", Impairment: "6911", Revaluation: "3401",
		AcquisitionClearing: "2291", Proceeds: "1291", Gain: "7201", Loss: "7211", TransferClearing: "1696"}
	saved, created, err := s.SavePostingProfile(ctx, custom, 0, "admin")
	if err != nil || !created || saved.Version != 1 {
		t.Fatalf("create: %+v %v %v", saved, created, err)
	}
	if _, _, err := s.SavePostingProfile(ctx, custom, 0, "admin"); err == nil {
		t.Fatal("replacing without If-Match must be refused")
	}
	if _, _, err := s.SavePostingProfile(ctx, custom, 5, "admin"); err == nil {
		t.Fatal("a stale If-Match must be refused")
	}
	custom.Cost = "1602"
	if saved, created, err = s.SavePostingProfile(ctx, custom, 1, "admin"); err != nil || created || saved.Version != 2 || saved.Cost != "1602" {
		t.Fatalf("replace: %+v %v %v", saved, created, err)
	}
	res, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "TAX", Currency: "USD"})
	if err != nil || len(res.Booked) != 2 || res.Booked[0].Placeholder || !net(t, fake, ledgerID, "1602").Equal(d("10000")) || !net(t, fake, ledgerID, store.PlaceholderProfile.Cost).IsZero() {
		t.Fatalf("custom accounts: %+v %v", res, err)
	}
	if other, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: ids.New(), LedgerID: ledgerID, BookCode: "TAX", Currency: "USD"}); err != nil || len(other.Booked) != 0 {
		t.Fatalf("another legal entity has nothing to book: %+v %v", other, err)
	}
	if list, err := s.ListPostingProfiles(ctx, le); err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
}

// The real ledger client signs a token per call: booking carries only ledger.entry.post and the reconciliation only ledger.balance.read,
// each within the assets -> ledger grant.
func TestGL_TheClientSignsOnlyTheDutyEachCallNeeds(t *testing.T) {
	s, le, _ := glScenario(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("assets", priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"assets": pub})
	seen := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, err := verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
		if err != nil || claims.Le != le {
			t.Errorf("token for %s %s: %v (le %s)", r.Method, r.URL.Path, err, claims.Le)
		}
		seen[r.Method+" "+r.URL.Path] = claims.Duties
		if r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"entries":[{"id":"` + ids.New() + `"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"currency":"USD","rows":[]}`))
	}))
	defer srv.Close()
	client, err := ledgerclient.New(srv.URL, "assets", signer, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: le, UserID: "accountant-1", Duties: testkit.ScopeDuties(le)})
	ledgerID := ids.New()
	if res, err := s.PostToGL(ctx, client, store.GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "TAX", Currency: "USD"}); err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 {
		t.Fatalf("booking: %+v %v", res, err)
	}
	if _, err := s.ReconcileGL(ctx, client, store.GLReconcileRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "TAX", Currency: "USD"}); err != nil {
		t.Fatal(err)
	}
	if d := seen["POST /v1/ledger/journal-entries"]; len(d) != 1 || d[0] != ledgerclient.DutyPost {
		t.Errorf("booking duties %v", d)
	}
	if d := seen["GET /v1/ledger/balances"]; len(d) != 1 || d[0] != ledgerclient.DutyBalanceRead {
		t.Errorf("reconciliation duties %v", d)
	}
}

type flakyLedger struct {
	ledgerclient.Ledger
	posts []ledgerclient.Entry
	err   error
}

func (f *flakyLedger) Post(ctx context.Context, e ledgerclient.Entry) (string, error) {
	f.posts = append(f.posts, e)
	if f.err != nil {
		return "", f.err
	}
	return f.Ledger.Post(ctx, e)
}

func TestGL_ARetryResendsTheStoredPayloadEvenAfterTheProfileChanged(t *testing.T) {
	s, le, _ := glScenario(t)
	_, _, ctx := setup(t)
	fl := &flakyLedger{Ledger: ledgerclient.NewFake(), err: &ledgerclient.Error{Op: "post", Status: http.StatusServiceUnavailable}}
	req := store.GLPostRequest{LegalEntityID: le, LedgerID: ids.New(), BookCode: "TAX", Currency: "USD"}
	if res, err := s.PostToGL(ctx, fl, req); err != nil || len(res.Failed) != 2 {
		t.Fatalf("outage: %+v %v", res, err)
	}
	if _, _, err := s.SavePostingProfile(ctx, store.PostingProfile{LegalEntityID: le, ClassCode: "ALL", Cost: "1601", Accumulated: "1691", DepreciationExpense: "6901", Impairment: "6911",
		Revaluation: "3401", AcquisitionClearing: "2291", Proceeds: "1291", Gain: "7201", Loss: "7211", TransferClearing: "1696"}, 0, "admin"); err != nil {
		t.Fatal(err)
	}
	first := append([]ledgerclient.Entry(nil), fl.posts...)
	fl.err, fl.posts = nil, nil
	if res, err := s.PostToGL(ctx, fl, req); err != nil || len(res.Booked) != 2 || len(fl.posts) != 2 {
		t.Fatalf("retry: %+v %v", res, err)
	}
	for _, e := range fl.posts {
		for _, f := range first {
			if f.IdempotencyKey == e.IdempotencyKey && (f.Lines[0].Account != e.Lines[0].Account || !f.Lines[0].Amount.Equal(e.Lines[0].Amount) || f.PostingDate != e.PostingDate) {
				t.Fatalf("a retry must resend the stored payload: %+v vs %+v", f, e)
			}
		}
		for _, l := range e.Lines {
			if l.Account == "1601" || l.Account == "6901" {
				t.Fatalf("the new profile must not rewrite a pending booking: %+v", e)
			}
		}
	}
}

func TestGL_ARunCoversOneCurrencyAndItsEntryKeyNamesBookAndCurrency(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	for _, c := range []struct{ number, currency string }{{"FA-USD", "USD"}, {"FA-EUR", "EUR"}} {
		a := newAsset(t, s, ctx, le, c.number)
		if _, err := s.AddBook(ctx, a.ID, store.Book{BookCode: "IFRS", Currency: c.currency, Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None",
			InServiceDate: day("2026-01-01"), Cost: d("6000"), SalvageValue: d("0")}, "b-"+c.number); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-01"}, "run-two-currencies"); problemCode(err) != domain.CodeInvalidDepreciation {
		t.Fatalf("a run with two currencies must be refused, got %v", err)
	}
	fake := ledgerclient.NewFake()
	for _, cur := range []string{"USD", "EUR"} {
		res, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: le, LedgerID: ids.New(), BookCode: "IFRS", Currency: cur})
		if err != nil || len(res.Failed) != 0 || len(res.Booked) != 1 {
			t.Fatalf("%s acquisition: %+v %v", cur, res, err)
		}
	}
	keys := map[string]bool{}
	for _, e := range fake.Entries() {
		if !strings.HasSuffix(e.IdempotencyKey, ":IFRS:"+e.Currency) || keys[e.IdempotencyKey] {
			t.Fatalf("key %q must name book and currency and be unique", e.IdempotencyKey)
		}
		keys[e.IdempotencyKey] = true
	}
}

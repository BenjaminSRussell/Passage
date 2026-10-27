package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
	"github.com/BenjaminSRussell/passage/services/assets/migrations"
)

func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, context.Context) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	// No legal-entity scope: the principal sees every entity in the tenant.
	return store.New(pool), pool, scoped(1, "")
}

func scoped(tenantN int, legalEntity string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantN), LegalEntityID: legalEntity, Duties: map[string]struct{}{}})
}

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func day(s string) store.Date {
	v, err := store.ParseDate(s)
	if err != nil {
		panic(err)
	}
	return v
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func newAsset(t *testing.T, s *store.Postgres, ctx context.Context, le, number string) store.Asset {
	t.Helper()
	a, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: le, AssetNumber: number, Name: "Van " + number, ClassCode: "VEH", Quantity: d("1")}, "asset-"+le+number)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func book(code, cost string) store.Book {
	return store.Book{BookCode: code, Currency: "USD", Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None",
		InServiceDate: day("2026-03-01"), Cost: d(cost), SalvageValue: d("0")}
}

func outboxTypes(t *testing.T, pool *pgxpool.Pool, ctx context.Context) map[string]int {
	t.Helper()
	out := map[string]int{}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select type from outbox`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var typ string
			if err := rows.Scan(&typ); err != nil {
				return err
			}
			out[typ]++
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStore_CreateAssetIdempotencyAndUniqueness(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	barcode := "BC-1"
	in := store.Asset{LegalEntityID: le, AssetNumber: "FA-1", Name: "Press", ClassCode: "MACHINE", Barcode: &barcode, Quantity: d("1")}
	first, err := s.CreateAsset(ctx, in, "k-1")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "NotYetAcquired" || first.Version != 1 || first.BookCount != 0 {
		t.Fatalf("new asset = %+v", first)
	}
	replay, err := s.CreateAsset(ctx, in, "k-1")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %s, %v; want %s", replay.ID, err, first.ID)
	}
	changed := in
	changed.Name = "Other"
	if _, err := s.CreateAsset(ctx, changed, "k-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse with another body: err = %v", err)
	}
	if _, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: le, AssetNumber: "FA-1", Name: "Dup", ClassCode: "MACHINE", Quantity: d("1")}, "k-2"); problemCode(err) != store.CodeAssetNumberDup {
		t.Fatalf("duplicate number: err = %v", err)
	}
	if _, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: le, AssetNumber: "FA-2", Name: "Dup", ClassCode: "MACHINE", Barcode: &barcode, Quantity: d("1")}, "k-3"); problemCode(err) != store.CodeAssetNumberDup {
		t.Fatalf("duplicate barcode: err = %v", err)
	}
	// The same number in another legal entity is a different asset.
	if _, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: ids.New(), AssetNumber: "FA-1", Name: "Elsewhere", ClassCode: "MACHINE", Quantity: d("1")}, "k-4"); err != nil {
		t.Fatalf("same number in another entity: %v", err)
	}
}

// TestStore_DisposalAcrossBooksReconciles covers AC-37.5 on two books and the
// subledger invariant: Σ transaction amounts per book equal the book's cost
// and accumulated depreciation projection.
func TestStore_DisposalAcrossBooksReconciles(t *testing.T) {
	s, pool, ctx := setup(t)
	asset := newAsset(t, s, ctx, ids.New(), "FA-1")
	ifrs, err := s.AddBook(ctx, asset.ID, book("IFRS", "10000"), "b-ifrs")
	if err != nil {
		t.Fatal(err)
	}
	if ifrs.NBV.Cmp(d("10000")) != 0 || ifrs.Status != "Open" {
		t.Fatalf("book after acquisition = nbv %s status %s", ifrs.NBV, ifrs.Status)
	}
	if _, err := s.AddBook(ctx, asset.ID, book("TAX", "9000"), "b-tax"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBook(ctx, asset.ID, book("TAX", "1"), "b-tax-2"); problemCode(err) != store.CodeBookDuplicate {
		t.Fatalf("duplicate book code: err = %v", err)
	}
	detail, err := s.GetAsset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "Open" || len(detail.Books) != 2 || detail.BookCount != 2 || detail.AcquiredDate == nil || detail.AcquiredDate.String() != "2026-03-01" {
		t.Fatalf("asset after acquisitions = %+v", detail)
	}

	if _, err := s.Dispose(ctx, asset.ID, store.DisposalInput{Version: detail.Version - 1, Kind: domain.DisposalSale, Percentage: d("50"), Proceeds: d("7000"), Date: day("2026-10-01")}, "stale"); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: err = %v", err)
	}
	if _, err := s.Dispose(ctx, asset.ID, store.DisposalInput{Version: detail.Version, Kind: domain.DisposalSale, Percentage: d("50"), Proceeds: d("7000"), Date: day("2026-02-01")}, "early"); problemCode(err) != domain.CodeInvalidDisposal {
		t.Fatalf("disposal before in-service: err = %v", err)
	}
	in := store.DisposalInput{Version: detail.Version, Kind: domain.DisposalSale, Percentage: d("50"), Proceeds: d("7000"), Date: day("2026-10-01")}
	disp, err := s.Dispose(ctx, asset.ID, in, "dispose-1")
	if err != nil {
		t.Fatal(err)
	}
	if disp.Asset.Status != "Open" || disp.Asset.Version != detail.Version+1 || len(disp.Books) != 2 || len(disp.Transactions) != 2 {
		t.Fatalf("partial disposal = %+v", disp)
	}
	wantGain := map[string]string{"IFRS": "2000", "TAX": "2500"} // 7,000 − 5,000 and 7,000 − 4,500
	for i, b := range disp.Books {
		tx := disp.Transactions[i]
		if tx.GainLoss == nil || tx.GainLoss.Cmp(d(wantGain[b.BookCode])) != 0 || tx.TransactionDate.String() != "2026-10-01" || tx.Type != "DisposalSale" {
			t.Errorf("book %s transaction = %+v", b.BookCode, tx)
		}
	}
	replay, err := s.Dispose(ctx, asset.ID, in, "dispose-1")
	if err != nil || len(replay.Transactions) != 2 || replay.Transactions[0].ID != disp.Transactions[0].ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}

	txs, err := s.ListTransactions(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 4 {
		t.Fatalf("transactions = %d, want 4", len(txs))
	}
	after, err := s.GetAsset(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range after.Books {
		cost, acc := decimal.Zero, decimal.Zero
		for _, tx := range txs {
			if tx.AssetBookID == b.ID {
				cost, acc = cost.Add(tx.CostAmount), acc.Add(tx.AccumulatedAmount)
			}
		}
		if !cost.Equal(b.Cost) || !acc.Equal(b.AccumulatedDepreciation) || !b.NBV.Equal(b.Cost.Sub(b.AccumulatedDepreciation)) {
			t.Errorf("book %s: Σ cost %s / Σ acc %s vs projection %s / %s", b.BookCode, cost, acc, b.Cost, b.AccumulatedDepreciation)
		}
	}

	full := store.DisposalInput{Version: after.Version, Kind: domain.DisposalScrap, Percentage: d("100"), Proceeds: d("0"), Date: day("2026-11-01")}
	scrapped, err := s.Dispose(ctx, asset.ID, full, "dispose-2")
	if err != nil {
		t.Fatal(err)
	}
	if scrapped.Asset.Status != "Scrapped" || scrapped.Books[0].Status != "Scrapped" || !scrapped.Books[0].Cost.IsZero() || !scrapped.Books[1].NBV.IsZero() {
		t.Fatalf("full scrap = %+v", scrapped)
	}
	full.Version = scrapped.Asset.Version
	if _, err := s.Dispose(ctx, asset.ID, full, "dispose-3"); problemCode(err) != domain.CodeAssetState {
		t.Fatalf("dispose a scrapped asset: err = %v", err)
	}
	if _, err := s.AddBook(ctx, asset.ID, book("LOCAL", "1"), "b-local"); problemCode(err) != domain.CodeAssetState {
		t.Fatalf("acquire on a scrapped asset: err = %v", err)
	}
	types := outboxTypes(t, pool, ctx)
	if types["dev.passage.assets.asset.acquired.v1"] != 2 || types["dev.passage.assets.asset.disposed.v1"] != 4 {
		t.Fatalf("outbox = %v", types)
	}
}

func TestStore_BookLimitAndAppendOnly(t *testing.T) {
	s, pool, ctx := setup(t)
	asset := newAsset(t, s, ctx, ids.New(), "FA-1")
	for i := 0; i < domain.MaxBooksPerAsset; i++ {
		if _, err := s.AddBook(ctx, asset.ID, book("B"+strconv.Itoa(i), "100"), "b-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddBook(ctx, asset.ID, book("B99", "100"), "b-99"); problemCode(err) != domain.CodeTooManyBooks {
		t.Fatalf("11th book: err = %v", err)
	}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update asset_transaction set cost_amount = 0`)
		return err
	})
	if err == nil {
		t.Fatal("asset_transaction accepted an UPDATE")
	}
	if _, err := s.GetAsset(ctx, ids.New()); problemCode(err) != store.CodeAssetNotFound {
		t.Fatalf("unknown asset: err = %v", err)
	}
	if _, err := s.GetBook(ctx, asset.ID, ids.New()); problemCode(err) != store.CodeBookNotFound {
		t.Fatalf("unknown book: err = %v", err)
	}
}

func TestStore_ListAssetsPagesAndFilters(t *testing.T) {
	s, _, ctx := setup(t)
	le, other := ids.New(), ids.New()
	for _, n := range []string{"FA-3", "FA-1", "FA-2"} {
		newAsset(t, s, ctx, le, n)
	}
	acquired := newAsset(t, s, ctx, other, "FA-9")
	if _, err := s.AddBook(ctx, acquired.ID, book("IFRS", "100"), "b-9"); err != nil {
		t.Fatal(err)
	}
	first, err := s.ListAssets(ctx, store.AssetFilter{LegalEntityID: le, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].AssetNumber != "FA-1" || first.Items[1].AssetNumber != "FA-2" || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second, err := s.ListAssets(ctx, store.AssetFilter{LegalEntityID: le, After: first.NextCursor, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].AssetNumber != "FA-3" || second.NextCursor != "" {
		t.Fatalf("second page = %+v", second)
	}
	open, err := s.ListAssets(ctx, store.AssetFilter{Status: "Open", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(open.Items) != 1 || open.Items[0].ID != acquired.ID || open.Items[0].BookCount != 1 {
		t.Fatalf("open assets = %+v", open)
	}
	// Another tenant sees nothing.
	if page, err := s.ListAssets(scoped(2, ""), store.AssetFilter{Limit: 50}); err != nil || len(page.Items) != 0 {
		t.Fatalf("other tenant = %+v, %v", page, err)
	}
}

func TestStore_LeaseRoundTripAndList(t *testing.T) {
	s, pool, ctx := setup(t)
	sched, err := domain.ComputeLease(domain.LeaseTerms{Payment: d("10000"), PaymentsPerYear: 1, TermMonths: 60, AnnualRate: d("0.05"), Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]store.LeaseLine, 0, len(sched.Lines))
	for _, l := range sched.Lines {
		lines = append(lines, store.LeaseLine{PeriodNo: l.PeriodNo, Opening: l.Opening, Interest: l.Interest, Payment: l.Payment, Closing: l.Closing, ROUDepreciation: l.ROUDepreciation})
	}
	in := store.Lease{LegalEntityID: ids.New(), LeaseNumber: "L-1", CommencementDate: day("2026-01-01"),
		TermMonths: 60, PaymentsPerYear: 1, PaymentAmount: d("10000"), PaymentTiming: "arrears", IBRRate: d("0.05"), Currency: "USD",
		Liability: sched.Liability, ROUAsset: sched.ROUAsset, Schedule: lines}
	lease, err := s.CreateLease(ctx, in, "key-lease")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateLease(ctx, in, "key-lease")
	if err != nil || replay.ID != lease.ID {
		t.Fatalf("replay = %s, %v", replay.ID, err)
	}
	if _, err := s.CreateLease(ctx, in, "key-lease-2"); problemCode(err) != store.CodeLeaseNumberDup {
		t.Fatalf("duplicate lease number: err = %v", err)
	}
	read, err := s.GetLease(ctx, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.Schedule) != 5 || !read.Schedule[4].Closing.IsZero() || read.Liability.Cmp(d("43294.77")) != 0 || read.CommencementDate.String() != "2026-01-01" {
		t.Fatalf("lease read back = liability %s, %d lines", read.Liability, len(read.Schedule))
	}
	raw, _ := json.Marshal(read)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	if wire["commencementDate"] != "2026-01-01" {
		t.Errorf("commencementDate on the wire = %v, want a YYYY-MM-DD date", wire["commencementDate"])
	}
	page, err := s.ListLeases(ctx, store.LeaseFilter{Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].Schedule != nil {
		t.Fatalf("lease list = %+v, %v", page, err)
	}
	if _, err := s.GetLease(ctx, ids.New()); problemCode(err) != store.CodeLeaseNotFound {
		t.Fatalf("unknown lease: err = %v", err)
	}
	if types := outboxTypes(t, pool, ctx); types["dev.passage.assets.lease.recognized.v1"] != 1 {
		t.Fatalf("outbox = %v", types)
	}
}

// TestStore_RowLevelSecurity runs as the non-bypass application role: every
// table enables and forces RLS, and a legal-entity scope hides other entities.
func TestStore_RowLevelSecurity(t *testing.T) {
	if os.Getenv(testkit.EnvURL) == "" || os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s and %s are needed for the RLS test", testkit.EnvURL, testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := admin.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, grant := range []string{
		"grant usage on schema " + schema + " to passage_app",
		"grant select, insert, update, delete on all tables in schema " + schema + " to passage_app",
	} {
		if _, err := admin.Exec(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"asset", "asset_book", "asset_transaction", "lease", "lease_schedule_line", "outbox", "idempotency_keys"} {
		var enabled, forced bool
		if err := admin.QueryRow(ctx, `select relrowsecurity, relforcerowsecurity from pg_class where relname = $1 and relnamespace = $2::regnamespace`, table, schema).Scan(&enabled, &forced); err != nil {
			t.Fatal(table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v", table, enabled, forced)
		}
	}
	s := store.New(app)
	leA, leB := ids.New(), ids.New()
	a := newAsset(t, s, scoped(1, leA), leA, "FA-A")
	newAsset(t, s, scoped(1, leB), leB, "FA-B")
	if page, err := s.ListAssets(scoped(1, leA), store.AssetFilter{Limit: 50}); err != nil || len(page.Items) != 1 || page.Items[0].ID != a.ID {
		t.Fatalf("scope A sees %+v, %v", page, err)
	}
	if _, err := s.GetAsset(scoped(1, leB), a.ID); problemCode(err) != store.CodeAssetNotFound {
		t.Fatalf("scope B reads A: err = %v", err)
	}
	if page, err := s.ListAssets(scoped(2, ""), store.AssetFilter{Limit: 50}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 sees %+v, %v", page, err)
	}
	if _, err := s.CreateAsset(scoped(1, leA), store.Asset{LegalEntityID: leB, AssetNumber: "FA-X", Name: "Out of scope", ClassCode: "VEH", Quantity: d("1")}, "x"); err == nil {
		t.Fatal("insert outside the legal-entity scope was accepted")
	}
}

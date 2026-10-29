package store_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/assets/internal/domain"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// reconcile checks the subledger invariant on every book of an asset: the
// signed transactions sum to the book's cost and accumulated depreciation.
func reconcile(t *testing.T, s *store.Postgres, ctx context.Context, assetID string) {
	t.Helper()
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	txs, err := s.ListTransactions(ctx, assetID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range a.Books {
		cost, acc := decimal.Zero, decimal.Zero
		for _, tx := range txs {
			if tx.AssetBookID == b.ID {
				cost, acc = cost.Add(tx.CostAmount), acc.Add(tx.AccumulatedAmount)
			}
		}
		if !cost.Equal(b.Cost) || !acc.Equal(b.AccumulatedDepreciation) {
			t.Errorf("asset %s book %s: transactions %s / %s, projections %s / %s", a.AssetNumber, b.BookCode, cost, acc, b.Cost, b.AccumulatedDepreciation)
		}
	}
}

func impairmentOf(t *testing.T, pool *pgxpool.Pool, ctx context.Context, bookID string) decimal.Decimal {
	t.Helper()
	var v decimal.Decimal
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select coalesce(sum(impairment_amount), 0) from asset_transaction where asset_book_id = $1`, bookID).Scan(&v)
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func bookByCode(t *testing.T, books []store.Book, code string) store.Book {
	t.Helper()
	for _, b := range books {
		if b.BookCode == code {
			return b
		}
	}
	t.Fatalf("no %s book in %+v", code, books)
	return store.Book{}
}

// TestStore_SplitMovesEveryBookAndContinuesTheSchedule covers FR-37.7 and
// BR-37.7 on a real database: a 60/40 split of an asset with a depreciated and
// impaired IFRS book and a TAX book.
func TestStore_SplitMovesEveryBookAndContinuesTheSchedule(t *testing.T) {
	s, pool, ctx := setup(t)
	le := ids.New()
	src := newAsset(t, s, ctx, le, "FA-S")
	ifrs, err := s.AddBook(ctx, src.ID, monthly("IFRS"), "b-ifrs")
	if err != nil {
		t.Fatal(err)
	}
	tax := monthly("TAX")
	tax.Cost = d("1000")
	if _, err := s.AddBook(ctx, src.ID, tax, "b-tax"); err != nil {
		t.Fatal(err)
	}
	run, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-01"}, "run-jan")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostRun(ctx, run.ID, run.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Adjust(ctx, src.ID, store.AdjustmentInput{BookID: ifrs.ID, Type: domain.AdjustmentImpairment, Date: day("2026-02-01"), Amount: d("200")}, "imp"); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetAsset(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	in := store.TransferInput{Version: before.Version, Kind: domain.TransferKindSplit, Date: day("2026-02-15"), Parts: []store.TransferPart{
		{AssetNumber: "FA-S1", Name: "Van front", Percentage: d("60")},
		{AssetNumber: "FA-S2", Name: "Van rear", Percentage: d("40")},
	}}
	if _, err := s.Transfer(ctx, src.ID, store.TransferInput{Version: before.Version - 1, Kind: in.Kind, Date: in.Date, Parts: in.Parts}, "split-stale"); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	res, err := s.Transfer(ctx, src.ID, in, "split-1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != domain.TransferKindSplit || len(res.Targets) != 2 || res.Source == nil || res.Source.Status != "Transferred" || res.Source.Version != before.Version+1 {
		t.Fatalf("split = %+v", res)
	}
	// IFRS: cost 1,200.00, accumulated 100.00 + 200.00 impairment = 300.00.
	want := []struct{ ifrsCost, ifrsAcc, imp, taxCost, qty string }{{"720", "180", "120", "600", "0.6"}, {"480", "120", "80", "400", "0.4"}}
	for i, target := range res.Targets {
		ib, tb := bookByCode(t, target.Books, "IFRS"), bookByCode(t, target.Books, "TAX")
		w := want[i]
		if !ib.Cost.Equal(d(w.ifrsCost)) || !ib.AccumulatedDepreciation.Equal(d(w.ifrsAcc)) || !tb.Cost.Equal(d(w.taxCost)) || !tb.AccumulatedDepreciation.IsZero() {
			t.Errorf("target %d books IFRS %s/%s TAX %s/%s", i, ib.Cost, ib.AccumulatedDepreciation, tb.Cost, tb.AccumulatedDepreciation)
		}
		if deref(ib.LastDepreciatedPeriod) != "2026-01" || tb.LastDepreciatedPeriod != nil || ib.InServiceDate.String() != "2026-01-01" {
			t.Errorf("target %d keeps the source schedule position: IFRS %v TAX %v in service %s", i, ib.LastDepreciatedPeriod, tb.LastDepreciatedPeriod, ib.InServiceDate)
		}
		if got := impairmentOf(t, pool, ctx, ib.ID); !got.Equal(d(w.imp)) {
			t.Errorf("target %d impairment = %s, want %s", i, got, w.imp)
		}
		if target.Asset.Status != "Open" || target.Asset.LegalEntityID != le || target.Asset.ClassCode != "VEH" || !target.Asset.Quantity.Equal(d(w.qty)) {
			t.Errorf("target %d asset = %+v", i, target.Asset)
		}
		reconcile(t, s, ctx, target.Asset.ID)
	}
	after, err := s.GetAsset(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range after.Books {
		if b.Status != "Transferred" || !b.Cost.IsZero() || !b.AccumulatedDepreciation.IsZero() {
			t.Errorf("source book %s after the split = %+v", b.BookCode, b)
		}
	}
	reconcile(t, s, ctx, src.ID)
	if got := impairmentOf(t, pool, ctx, ifrs.ID); !got.IsZero() {
		t.Errorf("source impairment after the split = %s, want 0", got)
	}
	if got := outboxTypes(t, pool, ctx)["dev.passage.assets.asset.split.v1"]; got != 1 {
		t.Errorf("asset.split events = %d, want 1", got)
	}

	// Idempotency: the same key replays, a different request with it is refused.
	if replay, err := s.Transfer(ctx, src.ID, in, "split-1"); err != nil || replay.ID != res.ID {
		t.Fatalf("replay = %v, %v", replay.ID, err)
	}
	changed := in
	changed.Parts = []store.TransferPart{{AssetNumber: "FA-S1", Name: "Van front", Percentage: d("50")}, {AssetNumber: "FA-S2", Name: "Van rear", Percentage: d("50")}}
	if _, err := s.Transfer(ctx, src.ID, changed, "split-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	// BR-37.2: the Transferred source takes no further transfer.
	if _, err := s.Transfer(ctx, src.ID, store.TransferInput{Version: after.Version, Kind: domain.TransferKindTransfer, Date: day("2026-03-01"),
		Parts: []store.TransferPart{{AssetNumber: "FA-S3", Name: "Van", Percentage: d("100")}}}, "again"); problemCode(err) != domain.CodeAssetState {
		t.Fatalf("transfer of a Transferred asset: %v", err)
	}

	// The next run picks up the target books where the source stopped and
	// leaves the Transferred source book out.
	feb, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "IFRS", Period: "2026-02"}, "run-feb")
	if err != nil {
		t.Fatal(err)
	}
	if feb.AssetCount != 2 || feb.ExceptionCount != 0 || len(feb.Lines) != 2 {
		t.Fatalf("February run = %+v", feb)
	}
	for _, l := range feb.Lines {
		if l.AssetID == src.ID || l.Status != "Due" || !l.Amount.IsPositive() {
			t.Errorf("February line = %+v", l)
		}
	}

	// BR-37.5 counts the targets, which carry the basis, not the emptied source.
	mq, err := s.MidQuarterTest(ctx, store.MidQuarterFilter{LegalEntityID: le, BookCode: "TAX", Year: 2026})
	if err != nil || mq.BookCount != 2 {
		t.Fatalf("mid-quarter test after the split = %+v, %v", mq, err)
	}

	for _, id := range []string{src.ID, res.Targets[1].Asset.ID} {
		list, err := s.ListTransfers(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 1 || list[0].ID != res.ID || list[0].SourceNumber != "FA-S" || len(list[0].Targets) != 2 || list[0].Targets[1].Asset.AssetNumber != "FA-S2" {
			t.Fatalf("transfers of %s = %+v", id, list)
		}
	}
}

// TestStore_TransferToAnotherLegalEntity covers the intercompany transfer of
// FR-37.7 (without the GL settlement, a wave-40 handoff) and its guards.
func TestStore_TransferToAnotherLegalEntity(t *testing.T) {
	s, pool, ctx := setup(t)
	le1, le2 := ids.New(), ids.New()
	src := newAsset(t, s, ctx, le1, "FA-T")
	if _, err := s.AddBook(ctx, src.ID, monthly("IFRS"), "b-ifrs"); err != nil {
		t.Fatal(err)
	}
	newAsset(t, s, ctx, le2, "FA-DUP")
	a, err := s.GetAsset(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	part := func(number, le string) []store.TransferPart {
		return []store.TransferPart{{AssetNumber: number, Name: "Van", Percentage: d("100"), LegalEntityID: le}}
	}
	cases := []struct {
		name string
		in   store.TransferInput
		code string
	}{
		{"number taken in the target entity", store.TransferInput{Version: a.Version, Kind: domain.TransferKindTransfer, Date: day("2026-05-01"), Parts: part("FA-DUP", le2)}, store.CodeAssetNumberDup},
		{"before the in-service date", store.TransferInput{Version: a.Version, Kind: domain.TransferKindTransfer, Date: day("2025-12-31"), Parts: part("FA-T2", le2)}, domain.CodeInvalidTransfer},
		{"split into another entity", store.TransferInput{Version: a.Version, Kind: domain.TransferKindSplit, Date: day("2026-05-01"),
			Parts: []store.TransferPart{{AssetNumber: "A", Name: "A", Percentage: d("50"), LegalEntityID: le2}, {AssetNumber: "B", Name: "B", Percentage: d("50")}}}, domain.CodeInvalidTransfer},
		{"transfer of part of the asset", store.TransferInput{Version: a.Version, Kind: domain.TransferKindTransfer, Date: day("2026-05-01"),
			Parts: []store.TransferPart{{AssetNumber: "A", Name: "A", Percentage: d("40")}}}, domain.CodeSplitPercentage},
	}
	for i, tc := range cases {
		if _, err := s.Transfer(ctx, src.ID, tc.in, "bad-"+strconv.Itoa(i)); problemCode(err) != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
	}
	res, err := s.Transfer(ctx, src.ID, store.TransferInput{Version: a.Version, Kind: domain.TransferKindTransfer, Date: day("2026-05-01"), Parts: part("FA-T2", le2)}, "ic-1")
	if err != nil {
		t.Fatal(err)
	}
	target := res.Targets[0]
	if target.Asset.LegalEntityID != le2 || target.Books[0].LegalEntityID != le2 || !target.Books[0].Cost.Equal(d("1200")) || !target.Percentage.Equal(d("100")) {
		t.Fatalf("target = %+v", target)
	}
	if got := outboxTypes(t, pool, ctx)["dev.passage.assets.asset.transferred.v1"]; got != 1 {
		t.Errorf("asset.transferred events = %d, want 1", got)
	}
	// A principal scoped to the target entity sees the new asset, not the source.
	if _, err := s.GetAsset(scoped(1, le2), target.Asset.ID); err != nil {
		t.Fatalf("target in its own entity: %v", err)
	}
	reconcile(t, s, ctx, src.ID)
	reconcile(t, s, ctx, target.Asset.ID)
}

// TestStore_GroupAssetSammelposten covers FR-37.16, BR-37.11 and AC-37.10: 40
// DE assets of EUR 600.00 acquired in 2026 pooled on the DE-TAX book; the
// group's FY2026 charge is 24,000.00 / 5 = 4,800.00 and the members take no
// own charge in a run.
func TestStore_GroupAssetSammelposten(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	deTax := store.Book{BookCode: "DE-TAX", Currency: "EUR", Method: "StraightLine", ServiceLifePeriods: 36, Convention: "None",
		InServiceDate: day("2026-03-01"), Cost: d("600"), SalvageValue: d("0")}
	newGWG := func(number, class string) store.Asset {
		a, err := s.CreateAsset(ctx, store.Asset{LegalEntityID: le, AssetNumber: number, Name: "Laptop " + number, ClassCode: class, Quantity: d("1")}, "a-"+number)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddBook(ctx, a.ID, deTax, "b-"+number); err != nil {
			t.Fatal(err)
		}
		return a
	}
	var members []string
	for i := 1; i <= 40; i++ {
		members = append(members, newGWG("GWG-"+strconv.Itoa(100+i), "GWG").ID)
	}
	ifrs := monthly("IFRS")
	if _, err := s.AddBook(ctx, members[0], ifrs, "b-ifrs"); err != nil {
		t.Fatal(err)
	}
	in := store.GroupAssetInput{LegalEntityID: le, GroupCode: "SP-2026", Name: "Sammelposten 2026", ClassCode: "GWG", BookCode: "DE-TAX", TaxYear: 2026,
		Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None", MemberAssetIDs: members}
	g, err := s.CreateGroupAsset(ctx, in, "group-1")
	if err != nil {
		t.Fatal(err)
	}
	if !g.Basis.Equal(d("24000")) || g.MemberCount != 40 || len(g.Members) != 40 || g.Currency != "EUR" || g.InServiceDate.String() != "2026-01-01" {
		t.Fatalf("group = %+v", g)
	}
	if len(g.Schedule) != 5 || g.Schedule[0].Year != "2026" || !g.Schedule[0].Amount.Equal(d("4800")) {
		t.Fatalf("schedule = %+v", g.Schedule)
	}
	if replay, err := s.CreateGroupAsset(ctx, in, "group-1"); err != nil || replay.ID != g.ID {
		t.Fatalf("replay = %v, %v", replay.ID, err)
	}

	// The member's DE-TAX book names the group; its IFRS book does not.
	first, err := s.GetAsset(ctx, members[0])
	if err != nil {
		t.Fatal(err)
	}
	if b := bookByCode(t, first.Books, "DE-TAX"); b.GroupCode == nil || *b.GroupCode != "SP-2026" || *b.GroupAssetID != g.ID {
		t.Fatalf("DE-TAX book = %+v", b)
	}
	if b := bookByCode(t, first.Books, "IFRS"); b.GroupAssetID != nil {
		t.Fatalf("IFRS book names a group: %+v", b)
	}

	fresh := newGWG("GWG-900", "GWG")
	it := newGWG("IT-1", "IT")
	other := in
	other.GroupCode = "SP-2026B"
	cases := []struct {
		name    string
		code    string
		members []string
		group   string
	}{
		{"member of another group (BR-37.11)", "AST-0535", []string{fresh.ID, members[3]}, "SP-2026B"},
		{"other asset class", domain.CodeInvalidGroup, []string{it.ID}, "SP-2026B"},
		{"no DE-TAX book", domain.CodeInvalidGroup, []string{newAsset(t, s, ctx, le, "GWG-NOBOOK").ID}, "SP-2026B"},
		{"unknown asset", store.CodeAssetNotFound, []string{ids.New()}, "SP-2026B"},
		{"group code taken", store.CodeGroupCodeDup, []string{fresh.ID}, "SP-2026"},
	}
	for i, tc := range cases {
		other.MemberAssetIDs, other.GroupCode = tc.members, tc.group
		if _, err := s.CreateGroupAsset(ctx, other, "bad-group-"+strconv.Itoa(i)); problemCode(err) != tc.code {
			t.Errorf("%s: %v, want %s", tc.name, err, tc.code)
		}
	}
	wrongYear := other
	wrongYear.GroupCode, wrongYear.TaxYear, wrongYear.MemberAssetIDs = "SP-2025", 2025, []string{fresh.ID}
	if _, err := s.CreateGroupAsset(ctx, wrongYear, "bad-year"); problemCode(err) != domain.CodeInvalidGroup {
		t.Errorf("member outside the tax year: %v", err)
	}

	// FR-37.16: no own charge for a member book; the other books still post.
	run, err := s.ProposeRun(ctx, store.RunInput{LegalEntityID: le, BookCode: "DE-TAX", Period: "2026-03"}, "run-de")
	if err != nil {
		t.Fatal(err)
	}
	grouped := 0
	for _, l := range run.Lines {
		if l.ExceptionCode != nil && *l.ExceptionCode == domain.CodeDepreciatedAtGroup && l.Amount.IsZero() {
			grouped++
		} else if l.Status != "Due" {
			t.Errorf("non-member line = %+v", l)
		}
	}
	if grouped != 40 || run.AssetCount != 2 || !run.TotalAmount.Equal(d("33.34")) {
		t.Fatalf("DE-TAX run: %d member exceptions, %d due, total %s (want 40, 2, 33.34)", grouped, run.AssetCount, run.TotalAmount)
	}
	posted, err := s.PostRun(ctx, run.ID, run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if posted.AssetCount != 2 || !posted.TotalAmount.Equal(d("33.34")) {
		t.Fatalf("posted DE-TAX run = %+v", posted)
	}
	if b, err := s.GetAsset(ctx, members[5]); err != nil || !bookByCode(t, b.Books, "DE-TAX").AccumulatedDepreciation.IsZero() {
		t.Fatalf("member accumulated after the run = %v, %v", b, err)
	}

	// The basis is the members' current cost: a 50% sale of a member lowers it.
	m := members[1]
	ma, err := s.GetAsset(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dispose(ctx, m, store.DisposalInput{Version: ma.Version, Kind: domain.DisposalSale, Percentage: d("50"), Proceeds: d("250"), Date: day("2026-04-01")}, "sell-half"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetGroupAsset(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Basis.Equal(d("23700")) || got.MemberCount != 40 {
		t.Fatalf("basis after the sale = %s (%d members), want 23700", got.Basis, got.MemberCount)
	}
	page, err := s.ListGroupAssets(ctx, store.GroupFilter{LegalEntityID: le, Limit: 50})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != g.ID || !page.Items[0].Basis.Equal(d("23700")) {
		t.Fatalf("list = %+v, %v", page, err)
	}
	if _, err := s.GetGroupAsset(ctx, ids.New()); problemCode(err) != store.CodeGroupNotFound {
		t.Fatalf("unknown group: %v", err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

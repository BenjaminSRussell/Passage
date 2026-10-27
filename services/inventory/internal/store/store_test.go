package store_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
	"github.com/BenjaminSRussell/passage/services/inventory/migrations"
)

const (
	inventoryAccount  = "1300"
	adjustmentAccount = "5100"
	openingAccount    = "3900"
)

// setup migrates a throwaway schema as the owner and returns a store on the
// application role (NOSUPERUSER, NOBYPASSRLS), so every query runs under RLS.
// It also returns the owner pool for assertions that read across tenants.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), app, admin
}

// entityCtx is a principal scoped to one legal entity of a tenant.
func entityCtx(tenantN int, legalEntityID string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: testkit.TenantID(tenantN), LegalEntityID: legalEntityID, UserID: ids.New(), Duties: map[string]struct{}{},
	})
}

// tenantWideCtx is a principal with no legal-entity narrowing ('*' scope).
func tenantWideCtx(tenantN int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantN), UserID: ids.New(), Duties: map[string]struct{}{}})
}

func dims(extra ...string) domain.Dimensions {
	d := domain.Dimensions{domain.DimSite: "S1", domain.DimWarehouse: "W1"}
	for i := 0; i+1 < len(extra); i += 2 {
		d[extra[i]] = extra[i+1]
	}
	return d
}

func line(product, qty, cost string, d domain.Dimensions) domain.JournalLine {
	return domain.JournalLine{ProductID: product, Dims: d, Qty: decimal.RequireFromString(qty),
		EnteredQty: decimal.RequireFromString(qty), EnteredUnit: "EA", CostPrice: decimal.RequireFromString(cost)}
}

func header(ctx context.Context, typ domain.JournalType, currency string) domain.Journal {
	p, _ := tenant.FromContext(ctx)
	j := domain.Journal{LegalEntityID: p.LegalEntityID, Type: typ, PostingDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Currency: currency}
	if typ == domain.JournalMovement {
		j.OffsetAccountID = "500100"
	}
	return j
}

// newJournal creates a DRAFT journal of the given type in USD and adds its lines.
func newJournal(ctx context.Context, s *store.Postgres, typ domain.JournalType, lines ...domain.JournalLine) (domain.Journal, error) {
	return newJournalIn(ctx, s, typ, "USD", lines...)
}

func newJournalIn(ctx context.Context, s *store.Postgres, typ domain.JournalType, currency string, lines ...domain.JournalLine) (domain.Journal, error) {
	created, _, err := s.CreateJournal(ctx, header(ctx, typ, currency), ids.New())
	if err != nil {
		return domain.Journal{}, err
	}
	return s.AddJournalLines(ctx, created.ID, lines)
}

func postParams() store.PostParams {
	return store.PostParams{InventoryAccount: inventoryAccount, AdjustmentAccount: adjustmentAccount, OpeningAccount: openingAccount}
}

// post creates and posts a journal, failing the test on any error.
func post(t *testing.T, ctx context.Context, s *store.Postgres, typ domain.JournalType, lines ...domain.JournalLine) domain.Journal {
	t.Helper()
	j, err := newJournal(ctx, s, typ, lines...)
	if err != nil {
		t.Fatal(err)
	}
	posted, err := s.PostJournal(ctx, j.ID, j.Version, postParams())
	if err != nil {
		t.Fatal(err)
	}
	return posted
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

// onHandFor returns the product total (physical, value) as the store sees it.
func onHandFor(t *testing.T, s *store.Postgres, ctx context.Context, product string) (decimal.Decimal, decimal.Decimal) {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"product"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) == 0 {
		return decimal.Zero, decimal.Zero
	}
	if len(res.Rows) != 1 {
		t.Fatalf("product %s has %d groups, want 1", product, len(res.Rows))
	}
	return res.Rows[0].PhysicalInvent, res.Rows[0].PostedValue
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestOpeningBalancePostsOnHandGLAndEvents(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	posted := post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims()), line(product, "50", "3.333333", dims()))
	if posted.Status != domain.JournalPosted || posted.PostedAt == nil || posted.Version != 3 {
		t.Fatalf("status %s postedAt %v version %d, want POSTED, set, 3", posted.Status, posted.PostedAt, posted.Version)
	}
	// 50 x 3.333333 = 166.66665, rounded to 166.67; value is 200 + 166.67.
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("150")) || !value.Equal(dec("366.67")) {
		t.Fatalf("on-hand %s / %s, want 150 / 366.67", phys, value)
	}
	gl := posted.GLEntry
	if gl == nil || !gl.Balanced() || !gl.Net(inventoryAccount).Equal(dec("366.67")) || !gl.Net(openingAccount).Equal(dec("-366.67")) {
		t.Fatalf("GL entry %+v, want Dr %s / Cr %s 366.67", gl, inventoryAccount, openingAccount)
	}
	counts := map[string]int{}
	rows, err := admin.Query(context.Background(), `select type, count(*) from outbox where tenant_id = $1 group by type`, testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var typ string
		var n int
		if err := rows.Scan(&typ, &n); err != nil {
			t.Fatal(err)
		}
		counts[typ] = n
	}
	rows.Close()
	want := map[string]int{"dev.passage.inventory.transaction.posted.v1": 2, "dev.passage.inventory.onhand.changed.v1": 1, "dev.passage.inventory.journal.posted.v1": 1}
	for typ, n := range want {
		if counts[typ] != n {
			t.Errorf("outbox %s = %d, want %d (all: %v)", typ, counts[typ], n, counts)
		}
	}
	// Posting again is a no-op that returns the posted journal and adds no events.
	again, err := s.PostJournal(ctx, posted.ID, 1, postParams())
	if err != nil || again.Status != domain.JournalPosted {
		t.Fatalf("repost: %v %s", err, again.Status)
	}
	var total int
	_ = admin.QueryRow(context.Background(), `select count(*) from outbox where tenant_id = $1`, testkit.TenantID(1)).Scan(&total)
	if total != 4 {
		t.Fatalf("repost wrote events: %d rows, want 4", total)
	}
}

func TestPostRequiresCurrentVersionAndDraftLines(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	j, err := newJournal(ctx, s, domain.JournalOpeningBalance, line(ids.New(), "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version-1, postParams()); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v, want %s", err, store.CodeConflictVersion)
	}
	if _, err := s.PostJournal(ctx, j.ID, j.Version, postParams()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{line(ids.New(), "1", "1", dims())}); problemCode(err) != store.CodeJournalState {
		t.Fatalf("lines on a posted journal: %v, want %s", err, store.CodeJournalState)
	}
	empty, _, err := s.CreateJournal(ctx, header(ctx, domain.JournalAdjustment, "USD"), ids.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, empty.ID, empty.Version, postParams()); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("journal without lines: %v, want %s", err, domain.CodeInvalid)
	}
}

func TestNegativeIssueRejectedAndNothingWritten(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "5", "1", dims()))
	adj, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-6", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.PostJournal(ctx, adj.ID, adj.Version, postParams())
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != domain.CodeNegativePhysical || p.Status != http.StatusUnprocessableEntity {
		t.Fatalf("want 422 %s, got %v", domain.CodeNegativePhysical, err)
	}
	if phys, _ := onHandFor(t, s, ctx, product); !phys.Equal(dec("5")) {
		t.Fatalf("on-hand changed after rejected issue: %s", phys)
	}
	var txns int
	_ = admin.QueryRow(context.Background(), `select count(*) from inventory_transaction where product_id = $1`, product).Scan(&txns)
	if got, _ := s.GetJournal(ctx, adj.ID); txns != 1 || got.Status != domain.JournalDraft {
		t.Fatalf("rejected post left %d transactions and status %s; want 1 and DRAFT", txns, got.Status)
	}
	// The same issue posts when the policy allows negative stock, and a later
	// receipt into the negative row is accepted under the forbid policy.
	allow := postParams()
	allow.Negative = domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if _, err := s.PostJournal(ctx, adj.ID, adj.Version, allow); err != nil {
		t.Fatalf("allowed negative: %v", err)
	}
	post(t, ctx, s, domain.JournalAdjustment, line(product, "0.5", "1", dims()))
	res, _ := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product})
	if len(res.Rows) != 1 || !res.Rows[0].PhysicalInvent.Equal(dec("-0.5")) || !res.Rows[0].Negative {
		t.Fatalf("negative row not flagged: %+v", res.Rows)
	}
}

// TestConcurrentIssuesNeverGoNegative is AC1 at reduced scale: with 100 units on
// hand, 300 concurrent single-unit issues yield exactly 100 successes and 200
// INV-NEG-001 rejections, and on-hand ends at zero. The 10k-issue k6 run is a
// performance test outside this slice.
func TestConcurrentIssuesNeverGoNegative(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "1", dims()))
	const attempts = 300
	var ok, rejected, other atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "-1", "1", dims()))
			if err != nil {
				other.Add(1)
				return
			}
			_, err = s.PostJournal(ctx, j.ID, j.Version, postParams())
			switch {
			case err == nil:
				ok.Add(1)
			case problemCode(err) == domain.CodeNegativePhysical:
				rejected.Add(1)
			default:
				other.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 100 || rejected.Load() != attempts-100 || other.Load() != 0 {
		t.Fatalf("ok=%d rejected=%d other=%d", ok.Load(), rejected.Load(), other.Load())
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.IsZero() || !value.IsZero() {
		t.Fatalf("on-hand %s / %s, want 0 / 0", phys, value)
	}
}

// Issues are valued at the row's running average, not at the cost typed on
// the issue line, and the issue that empties the row clears its value.
func TestIssuesValuedAtRunningAverage(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "3", "3.333333", dims())) // 10.00
	first := post(t, ctx, s, domain.JournalAdjustment, line(product, "-1", "999", dims()))
	if net := first.GLEntry.Net(inventoryAccount); !net.Equal(dec("-3.33")) {
		t.Fatalf("first issue credits inventory %s, want 3.33 at the running average", net.Neg())
	}
	if net := first.GLEntry.Net(adjustmentAccount); !net.Equal(dec("3.33")) {
		t.Fatalf("adjustment account %s, want Dr 3.33", net)
	}
	last := post(t, ctx, s, domain.JournalAdjustment, line(product, "-2", "0", dims()))
	if net := last.GLEntry.Net(inventoryAccount); !net.Equal(dec("-6.67")) {
		t.Fatalf("last issue credits %s, want the remaining 6.67", net.Neg())
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.IsZero() || !value.IsZero() {
		t.Fatalf("on-hand %s / %s, want 0 / 0", phys, value)
	}
}

// TestProjectionReconcilesWithTransactionsAndLedger is the reconciliation of
// PRD 3.16.9 at small scale: per row, posted quantity and value equal the sums
// over transactions, and the GL nets on the inventory account equal the total
// posted value.
func TestProjectionReconcilesWithTransactionsAndLedger(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	products := []string{ids.New(), ids.New()}
	ledger := decimal.Zero
	for i, p := range products {
		for _, j := range []domain.Journal{
			post(t, ctx, s, domain.JournalOpeningBalance, line(p, "40", "2.125", dims()), line(p, "10", "2", dims("batch", "B1"))),
			post(t, ctx, s, domain.JournalMovement, line(p, []string{"-7", "-3"}[i], "0", dims()), line(p, "4", "1.1", dims("batch", "B1"))),
			post(t, ctx, s, domain.JournalAdjustment, line(p, "-1.5", "0", dims("batch", "B1"))),
		} {
			ledger = ledger.Add(j.GLEntry.Net(inventoryAccount))
		}
	}
	rows, err := admin.Query(context.Background(), `select o.posted_qty::text, o.posted_value::text,
		coalesce(sum(t.qty), 0)::text, coalesce(sum(t.cost_amount_posted), 0)::text
		from on_hand o left join inventory_transaction t on t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id
		  and t.product_id = o.product_id and t.dimension_set_hash = o.dimension_set_hash
		where o.tenant_id = $1 group by o.tenant_id, o.legal_entity_id, o.product_id, o.dimension_set_hash`, testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n, value := 0, decimal.Zero
	for rows.Next() {
		var pq, pv, sq, sv string
		if err := rows.Scan(&pq, &pv, &sq, &sv); err != nil {
			t.Fatal(err)
		}
		if !dec(pq).Equal(dec(sq)) || !dec(pv).Equal(dec(sv)) {
			t.Fatalf("projection %s / %s != transactions %s / %s", pq, pv, sq, sv)
		}
		value = value.Add(dec(pv))
		n++
	}
	if n != 2*len(products) {
		t.Fatalf("checked %d rows, want %d", n, 2*len(products))
	}
	if !value.Equal(ledger) {
		t.Fatalf("inventory value %s != GL inventory net %s", value, ledger)
	}
}

// AC-16.5: a serial already in physical stock cannot be received again, at any
// warehouse of the legal entity or twice in one journal; moving it is fine.
func TestDuplicateSerialReceiptRejected(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "1", "50", dims("serial", "S-001")))

	other := dims("serial", "S-001")
	other[domain.DimWarehouse] = "W2"
	dup, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "50", other))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, dup.ID, dup.Version, postParams()); problemCode(err) != domain.CodeDuplicateSerial {
		t.Fatalf("receipt of a serial in stock: %v, want %s", err, domain.CodeDuplicateSerial)
	}
	twice, err := newJournal(ctx, s, domain.JournalAdjustment,
		line(product, "1", "50", dims("serial", "S-002")), line(product, "1", "50", dims("serial", "S-002", "location", "L9")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, twice.ID, twice.Version, postParams()); problemCode(err) != domain.CodeDuplicateSerial {
		t.Fatalf("same serial twice in one journal: %v, want %s", err, domain.CodeDuplicateSerial)
	}
	// A movement that issues S-001 at W1 and receives it at W2 keeps one unit in stock.
	move := post(t, ctx, s, domain.JournalMovement, line(product, "-1", "0", dims("serial", "S-001")), line(product, "1", "50", other))
	if move.Status != domain.JournalPosted {
		t.Fatalf("serial move: %s", move.Status)
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.Equal(dec("1")) || !value.Equal(dec("50")) {
		t.Fatalf("after move %s / %s, want 1 / 50", phys, value)
	}
}

func TestLinesRequireSiteAndValidDimensions(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	j, _, err := s.CreateJournal(ctx, header(ctx, domain.JournalAdjustment, "USD"), ids.New())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		dims domain.Dimensions
		code string
	}{
		"site missing":           {domain.Dimensions{domain.DimWarehouse: "W1"}, domain.CodeDimensionMandatory},
		"unknown dimension":      {domain.Dimensions{domain.DimSite: "S1", "colour": "red"}, domain.CodeInvalid},
		"vendor-owned stock":     {domain.Dimensions{domain.DimSite: "S1", domain.DimOwner: "V1"}, domain.CodeOwnershipValue},
		"malformed segment":      {domain.Dimensions{domain.DimSite: "S1", domain.DimSegment: "P1"}, domain.CodeInvalid},
		"license plate reserved": {domain.Dimensions{domain.DimSite: "S1", domain.DimLicensePlate: "LP"}, domain.CodeInvalid},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{line(ids.New(), "1", "1", c.dims)}); problemCode(err) != c.code {
				t.Fatalf("got %v, want %s", err, c.code)
			}
		})
	}
	if got, _ := s.GetJournal(ctx, j.ID); len(got.Lines) != 0 || got.Version != 1 {
		t.Fatalf("rejected lines changed the journal: %d lines, version %d", len(got.Lines), got.Version)
	}
}

func TestLineClientKeysMakeBulkAddReplaySafe(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	j, _, err := s.CreateJournal(ctx, header(ctx, domain.JournalAdjustment, "USD"), ids.New())
	if err != nil {
		t.Fatal(err)
	}
	product := ids.New()
	a, b := line(product, "1", "2", dims()), line(product, "3", "2", dims())
	a.ClientKey, b.ClientKey = "row-1", "row-2"
	first, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{a, b})
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{a, b})
	if err != nil || len(replay.Lines) != 2 || replay.Version != first.Version {
		t.Fatalf("replay: %v, %d lines, version %d (want 2 lines, version %d)", err, len(replay.Lines), replay.Version, first.Version)
	}
	changed := a
	changed.Qty, changed.EnteredQty = dec("9"), dec("9")
	if _, err := s.AddJournalLines(ctx, j.ID, []domain.JournalLine{changed}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse with other values: %v, want %s", err, store.CodeIdempotencyReuse)
	}
}

func TestCreateJournalIdempotencyAndScope(t *testing.T) {
	s, _, _ := setup(t)
	le := ids.New()
	ctx := entityCtx(1, le)
	h := header(ctx, domain.JournalMovement, "USD")
	first, inserted, err := s.CreateJournal(ctx, h, "key-1")
	if err != nil || !inserted {
		t.Fatalf("create: %v inserted=%v", err, inserted)
	}
	again, inserted, err := s.CreateJournal(ctx, h, "key-1")
	if err != nil || inserted || again.ID != first.ID {
		t.Fatalf("replay: %v inserted=%v id %s, want %s", err, inserted, again.ID, first.ID)
	}
	h.Currency = "EUR"
	if _, _, err := s.CreateJournal(ctx, h, "key-1"); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v, want %s", err, store.CodeIdempotencyReuse)
	}
	foreign := header(ctx, domain.JournalAdjustment, "USD")
	foreign.LegalEntityID = ids.New()
	if _, _, err := s.CreateJournal(ctx, foreign, "key-2"); problemCode(err) != store.CodeOutOfScope {
		t.Fatalf("journal for another legal entity: %v, want %s", err, store.CodeOutOfScope)
	}
}

// Two legal entities that use the same dimension values keep separate on-hand
// rows; a principal sees only its own entity, a tenant-wide one sees both.
func TestLegalEntitiesKeepSeparateOnHand(t *testing.T) {
	s, _, _ := setup(t)
	leA, leB := ids.New(), ids.New()
	ctxA, ctxB := entityCtx(1, leA), entityCtx(1, leB)
	product := ids.New()
	post(t, ctxA, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	post(t, ctxB, s, domain.JournalOpeningBalance, line(product, "4", "1", dims()))
	if phys, _ := onHandFor(t, s, ctxA, product); !phys.Equal(dec("10")) {
		t.Fatalf("entity A sees %s, want 10", phys)
	}
	adj, err := newJournal(ctxB, s, domain.JournalAdjustment, line(product, "-5", "0", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctxB, adj.ID, adj.Version, postParams()); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("entity B issued from entity A's stock: %v", err)
	}
	res, err := s.ListOnHand(tenantWideCtx(1), store.OnHandFilter{ProductID: product, GroupBy: []string{"legal_entity"}})
	if err != nil || len(res.Rows) != 2 {
		t.Fatalf("tenant-wide view: %v, %d groups, want 2", err, len(res.Rows))
	}
	if _, err := s.GetJournal(ctxA, adj.ID); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("entity A read entity B's journal: %v", err)
	}
}

func TestPostingInAnotherCurrencyIsRejected(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	eur, err := newJournalIn(ctx, s, domain.JournalAdjustment, "EUR", line(product, "1", "1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostJournal(ctx, eur.ID, eur.Version, postParams()); problemCode(err) != store.CodeCurrencyMismatch {
		t.Fatalf("EUR posting onto a USD row: %v, want %s", err, store.CodeCurrencyMismatch)
	}
}

func TestRLSIsolatesTenants(t *testing.T) {
	s, app, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	posted := post(t, ctx, s, domain.JournalOpeningBalance, line(product, "2", "1", dims()))
	other := tenantWideCtx(2)
	if _, err := s.GetJournal(other, posted.ID); problemCode(err) != store.CodeJournalNotFound {
		t.Fatalf("tenant 2 read tenant 1's journal: %v", err)
	}
	if res, err := s.ListOnHand(other, store.OnHandFilter{}); err != nil || len(res.Rows) != 0 {
		t.Fatalf("tenant 2 sees on-hand: %v %+v", err, res.Rows)
	}
	if page, err := s.ListJournals(other, store.JournalFilter{}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 lists journals: %v %d", err, len(page.Items))
	}
	// Without a tenant setting the application role reads nothing: the policy
	// either matches no row or (on a pooled session where the setting is '')
	// fails the uuid cast. Both fail closed.
	var n int
	if err := app.QueryRow(context.Background(), `select count(*) from inventory_transaction`).Scan(&n); err == nil && n != 0 {
		t.Fatalf("unscoped read returned %d rows", n)
	}
}

func TestTransactionsAreAppendOnly(t *testing.T) {
	s, app, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	post(t, ctx, s, domain.JournalOpeningBalance, line(ids.New(), "2", "1", dims()))
	tx, err := app.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `savepoint s`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `update inventory_transaction set qty = qty * 2`); err == nil {
		t.Fatal("quantity update must be refused")
	}
	if _, err := tx.Exec(context.Background(), `rollback to savepoint s`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `delete from inventory_transaction`); err == nil {
		t.Fatal("delete must be refused")
	}
}

func TestListJournalsFiltersAndPages(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "5", "1", dims()))
	for i := 0; i < 3; i++ {
		if _, err := newJournal(ctx, s, domain.JournalAdjustment, line(product, "1", "1", dims())); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		page, err := s.ListJournals(ctx, store.JournalFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range page.Items {
			if seen[j.ID] {
				t.Fatalf("journal %s listed twice", j.ID)
			}
			seen[j.ID] = true
			if j.LineCount != 1 || len(j.Lines) != 0 {
				t.Fatalf("summary %+v: want line count 1 and no lines", j)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
		if pages > 3 {
			t.Fatal("paging does not end")
		}
	}
	if len(seen) != 4 {
		t.Fatalf("listed %d journals, want 4", len(seen))
	}
	drafts, err := s.ListJournals(ctx, store.JournalFilter{Status: domain.JournalDraft, Type: domain.JournalAdjustment})
	if err != nil || len(drafts.Items) != 3 {
		t.Fatalf("draft adjustments: %v, %d", err, len(drafts.Items))
	}
	if _, err := s.ListJournals(ctx, store.JournalFilter{Cursor: "%%%"}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestOnHandGroupsAndFilters(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance,
		line(product, "10", "1", dims("batch", "B1")), line(product, "5", "1", dims("batch", "B2")),
		line(product, "2", "1", domain.Dimensions{domain.DimSite: "S2", domain.DimWarehouse: "W9"}))
	bySite, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{"site", "site"}})
	if err != nil || len(bySite.Rows) != 2 || bySite.Rows[0].Group["site"] != "S1" || !bySite.Rows[0].PhysicalInvent.Equal(dec("15")) ||
		bySite.Rows[0].Currency != "USD" {
		t.Fatalf("by site: %v %+v", err, bySite.Rows)
	}
	batch, err := s.ListOnHand(ctx, store.OnHandFilter{BatchID: "B2", GroupBy: []string{"warehouse", "batch"}})
	if err != nil || len(batch.Rows) != 1 || batch.Rows[0].Group["batch"] != "B2" || !batch.Rows[0].PhysicalInvent.Equal(dec("5")) {
		t.Fatalf("batch filter: %v %+v", err, batch.Rows)
	}
	limited, err := s.ListOnHand(ctx, store.OnHandFilter{GroupBy: []string{"batch"}, Limit: 1})
	if err != nil || len(limited.Rows) != 1 || !limited.Truncated {
		t.Fatalf("limit: %v %+v", err, limited)
	}
}

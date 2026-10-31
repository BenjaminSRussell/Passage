package store_test

// Wave 16 proof, AC2 (issue #16, PRD 3.16 test plan "Rebuild equality",
// fixture inv-1m-txn): the on-hand projection equals SUM(transactions) after
// 1M transactions, by rebuild-and-compare.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// EnvAC2Transactions sets the AC2 fixture size; the issue's scale is 1000000.
const EnvAC2Transactions = "PASSAGE_INVENTORY_AC2_TXNS"

// ac2Book is the generator's own record of one projection row: quantities
// summed from what it asked the store to do and the store accepted. It never
// reads the projection or the engine's valuation.
type ac2Book struct {
	le, product string
	dims        domain.Dimensions
	hash        []byte
	posted      decimal.Decimal // SOLD / PURCHASED quantity
	registered  decimal.Decimal
	arrived     decimal.Decimal
	reserved    decimal.Decimal
}

// free is what the generator lets itself issue, block, transfer or reserve:
// financially posted stock not reserved. It is stricter than the engine's
// available physical quantity, which also counts REGISTERED receipts.
func (b *ac2Book) free() decimal.Decimal { return b.posted.Sub(b.reserved) }

// availPhysical is the engine's availability as FR-16.3 defines it, from the
// books: posted + registered - reserved (no RECEIVED / DEDUCTED / PICKED here).
func (b *ac2Book) availPhysical() decimal.Decimal { return b.posted.Add(b.registered).Sub(b.reserved) }

// ac2Reg is a receipt registered through transactions:apply.
type ac2Reg struct {
	id   string
	le   int
	book *ac2Book
	qty  decimal.Decimal
}

func sortBooks(bs []*ac2Book) {
	slices.SortFunc(bs, func(a, b *ac2Book) int {
		return strings.Compare(a.product+string(a.hash), b.product+string(b.hash))
	})
}

// ac2Worker drives the store for a disjoint set of products, so its books are
// exact without coordination.
type ac2Worker struct {
	t        *testing.T
	s        *store.Postgres
	ctxs     []context.Context // one principal per legal entity
	rng      *rand.Rand
	zipf     *rand.Zipf
	products []string
	books    map[string]*ac2Book
	budget   int // transactions still to write
	written  int
	stats    map[string]int
	regs     []ac2Reg // registrations not yet unregistered
	resIDs   []string // active reservation ids
	resBook  map[string]*ac2Book
	resQty   map[string]decimal.Decimal
	base     time.Time
	errs     []error
}

var ac2Warehouses = []string{"W1", "W2", "W3"}

func (w *ac2Worker) book(le int, product string, d domain.Dimensions) *ac2Book {
	n, err := d.Normalize()
	if err != nil {
		w.t.Fatal(err)
	}
	h := n.Hash()
	k := strconv.Itoa(le) + "|" + product + "|" + string(h)
	b := w.books[k]
	if b == nil {
		b = &ac2Book{le: legalEntity(w.ctxs[le]), product: product, dims: n, hash: h}
		w.books[k] = b
	}
	return b
}

// randomDims picks one of a product's few dimension sets (site, warehouse and,
// for every third product, a batch).
func (w *ac2Worker) randomDims(productIdx int, warehouse string) domain.Dimensions {
	site := []string{"S1", "S2"}[w.rng.IntN(2)]
	if warehouse == "" {
		warehouse = ac2Warehouses[w.rng.IntN(len(ac2Warehouses))]
	}
	d := domain.Dimensions{domain.DimSite: site, domain.DimWarehouse: warehouse}
	if productIdx%3 == 0 {
		d[domain.DimBatch] = fmt.Sprintf("B%d", 1+w.rng.IntN(4))
	}
	return d
}

func (w *ac2Worker) qty() decimal.Decimal {
	return decimal.New(int64(1+w.rng.IntN(5000)), -2) // 0.01 to 50.00
}

func (w *ac2Worker) cost() decimal.Decimal {
	return decimal.New(int64(50+w.rng.IntN(999950)), -4) // 0.0050 to 99.9999
}

func (w *ac2Worker) date() time.Time { return w.base.AddDate(0, 0, w.rng.IntN(365)) }

// pickProduct draws a product index Zipf-distributed over the worker's products.
func (w *ac2Worker) pickProduct() int { return int(w.zipf.Uint64()) }

// stockedRows lists the books of a legal entity with free posted stock and no
// inventory status (blocked rows are not issued from).
func (w *ac2Worker) stockedRows(le int, warehouse string) []*ac2Book {
	var out []*ac2Book
	want := legalEntity(w.ctxs[le])
	for _, b := range w.books {
		if b.le == want && b.free().Sign() > 0 && b.dims[domain.DimStatus] == "" && (warehouse == "" || b.dims[domain.DimWarehouse] == warehouse) {
			out = append(out, b)
		}
	}
	// Map iteration order is random; sort for a reproducible pick.
	sortBooks(out)
	return out
}

func (w *ac2Worker) fail(err error) {
	w.errs = append(w.errs, err)
}

// postJournal creates and posts a journal; apply runs only if it posted.
func (w *ac2Worker) postJournal(le int, typ domain.JournalType, lines []domain.JournalLine, txns int, apply func()) {
	ctx := w.ctxs[le]
	h := header(ctx, typ, "USD")
	h.PostingDate = w.date()
	j, _, err := w.s.CreateJournal(ctx, h, ids.New())
	if err != nil {
		w.fail(fmt.Errorf("create %s journal: %w", typ, err))
		return
	}
	if j, err = w.s.AddJournalLines(ctx, j.ID, lines); err != nil {
		w.fail(fmt.Errorf("add %d lines to a %s journal: %w", len(lines), typ, err))
		return
	}
	if _, err := w.s.PostJournal(ctx, j.ID, j.Version, consignmentParams()); err != nil {
		w.fail(fmt.Errorf("%s journal of %d lines: %w", typ, len(lines), err))
		return
	}
	apply()
	w.written += txns
	w.budget -= txns
	w.stats[string(typ)]++
}

// receiptsAndIssues builds a MOVEMENT / ADJUSTMENT / OPENING_BALANCE journal of
// n lines: receipts on random rows and, unless receiptsOnly, issues of at most
// the free stock the books (with the journal's earlier lines) say a row has.
func (w *ac2Worker) receiptsAndIssues(le int, typ domain.JournalType, n int, receiptsOnly bool) {
	pending := map[*ac2Book]decimal.Decimal{}
	var lines []domain.JournalLine
	stocked := w.stockedRows(le, "")
	for len(lines) < n {
		if !receiptsOnly && len(stocked) > 0 && w.rng.IntN(100) < 45 {
			b := stocked[w.rng.IntN(len(stocked))]
			free := b.free().Add(pending[b])
			if free.Sign() <= 0 {
				continue
			}
			q := decimal.Min(free, w.qty())
			if w.rng.IntN(10) == 0 {
				q = free // empty the row now and then (the whole-value branch)
			}
			pending[b] = pending[b].Sub(q)
			lines = append(lines, line(b.product, q.Neg().String(), "0", b.dims))
			continue
		}
		pi := w.pickProduct()
		d := w.randomDims(pi, "")
		b := w.book(le, w.products[pi], d)
		q := w.qty()
		pending[b] = pending[b].Add(q)
		lines = append(lines, line(b.product, q.String(), w.cost().String(), d))
	}
	w.postJournal(le, typ, lines, len(lines), func() {
		for b, q := range pending {
			b.posted = b.posted.Add(q)
		}
	})
}

// transferJournal moves stock of up to n distinct rows from one warehouse to
// another of the same site (two transactions per line).
func (w *ac2Worker) transferJournal(le, n int) {
	from := ac2Warehouses[w.rng.IntN(len(ac2Warehouses))]
	to := ac2Warehouses[(slices.Index(ac2Warehouses, from)+1+w.rng.IntN(len(ac2Warehouses)-1))%len(ac2Warehouses)]
	src := w.stockedRows(le, from)
	w.rng.Shuffle(len(src), func(i, j int) { src[i], src[j] = src[j], src[i] })
	if len(src) > n {
		src = src[:n]
	}
	if len(src) == 0 {
		return
	}
	type move struct {
		from, to *ac2Book
		q        decimal.Decimal
	}
	var moves []move
	var lines []domain.JournalLine
	for _, b := range src {
		q := decimal.Min(b.free(), w.qty())
		dest := domain.Dimensions{}
		for k, v := range b.dims {
			dest[k] = v
		}
		dest[domain.DimWarehouse] = to
		moves = append(moves, move{b, w.bookFor(b, dest), q})
		lines = append(lines, transferLine(b.product, q.String(), b.dims, dest))
	}
	w.postJournal(le, domain.JournalTransfer, lines, 2*len(lines), func() {
		for _, m := range moves {
			m.from.posted = m.from.posted.Sub(m.q)
			m.to.posted = m.to.posted.Add(m.q)
		}
	})
}

// bookFor is the book of another dimension set of b's product and legal entity.
func (w *ac2Worker) bookFor(b *ac2Book, d domain.Dimensions) *ac2Book {
	for i, c := range w.ctxs {
		if legalEntity(c) == b.le {
			return w.book(i, b.product, d)
		}
	}
	w.t.Fatal("unknown legal entity")
	return nil
}

func (w *ac2Worker) leOf(b *ac2Book) int {
	for i, c := range w.ctxs {
		if legalEntity(c) == b.le {
			return i
		}
	}
	return 0
}

// overIssue asks for 0.01 more than a row's available physical stock (by the
// books); the store must refuse it with INV-NEG-001 and write nothing.
func (w *ac2Worker) overIssue(le int) {
	stocked := w.stockedRows(le, "")
	if len(stocked) == 0 {
		return
	}
	b := stocked[w.rng.IntN(len(stocked))]
	ctx := w.ctxs[le]
	ask := b.availPhysical().Add(decimal.New(1, -2))
	j, err := newJournal(ctx, w.s, domain.JournalAdjustment, line(b.product, ask.Neg().String(), "0", b.dims))
	if err != nil {
		w.fail(err)
		return
	}
	if _, err := w.s.PostJournal(ctx, j.ID, j.Version, consignmentParams()); problemCode(err) != domain.CodeNegativePhysical {
		w.fail(fmt.Errorf("over-issue of %s: %v, want %s", ask, err, domain.CodeNegativePhysical))
		return
	}
	w.stats["refused over-issue"]++
}

func (w *ac2Worker) block(le int) {
	stocked := w.stockedRows(le, "")
	if len(stocked) == 0 {
		return
	}
	b := stocked[w.rng.IntN(len(stocked))]
	q := decimal.Min(b.free(), w.qty())
	blocked := domain.Dimensions{}
	for k, v := range b.dims {
		blocked[k] = v
	}
	blocked[domain.DimStatus] = domain.StockBlocked
	if _, _, err := w.s.CreateBlocking(w.ctxs[le], store.BlockingInput{IdempotencyKey: ids.New(), LegalEntityID: b.le, ProductID: b.product,
		Dims: b.dims, Qty: q, Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: w.date()}); err != nil {
		w.fail(fmt.Errorf("block %s: %w", q, err))
		return
	}
	b.posted = b.posted.Sub(q)
	t := w.book(le, b.product, blocked)
	t.posted = t.posted.Add(q)
	w.written += 2
	w.budget -= 2
	w.stats["blocking"]++
}

// register registers an expected receipt at REGISTERED through
// transactions:apply and unregisters an earlier one half of the time.
func (w *ac2Worker) register(le int) {
	pi := w.pickProduct()
	d := w.randomDims(pi, "")
	b := w.book(le, w.products[pi], d)
	q := w.qty()
	change, _, err := w.s.CreateTransaction(w.ctxs[le], store.CreateTransactionInput{LegalEntityID: b.le, SourceType: "RECEIPT_LINE",
		SourceID: ids.New(), Reference: "GRN", ProductID: b.product, Dims: d, Qty: q, EnteredQty: q, EnteredUnit: "EA",
		Status: domain.StatusRegistered, Currency: "USD", CostPrice: w.cost(), Date: w.date(), IdempotencyKey: ids.New()})
	if err != nil {
		w.fail(fmt.Errorf("register: %w", err))
		return
	}
	b.registered = b.registered.Add(q)
	w.regs = append(w.regs, ac2Reg{id: change.TransactionID, le: le, book: b, qty: q})
	w.written++
	w.budget--
	w.stats["registration"]++
	if len(w.regs) > 1 && w.rng.IntN(2) == 0 {
		w.unregister(w.rng.IntN(len(w.regs) - 1))
	}
}

// unregister moves the i-th open registration back to ARRIVED (AC-16.8).
func (w *ac2Worker) unregister(i int) {
	r := w.regs[i]
	if _, _, err := w.s.ApplyStatusStep(w.ctxs[r.le], store.StatusStepInput{TransactionID: r.id, Command: domain.CommandUnregister,
		IdempotencyKey: ids.New()}); err != nil {
		w.fail(fmt.Errorf("unregister: %w", err))
		return
	}
	r.book.registered = r.book.registered.Sub(r.qty)
	r.book.arrived = r.book.arrived.Add(r.qty)
	w.regs = append(w.regs[:i], w.regs[i+1:]...)
	w.stats["unregister"]++
}

// reserve reserves part of a row's free stock (MANUAL) and releases an earlier
// reservation a third of the time.
func (w *ac2Worker) reserve(le int) {
	stocked := w.stockedRows(le, "")
	if len(stocked) == 0 {
		return
	}
	b := stocked[w.rng.IntN(len(stocked))]
	q := decimal.Min(b.free(), w.qty())
	res, err := reserve(w.ctxs[le], w.s, b.product, q.String(), domain.PolicyManual, b.dims, ids.New())
	if err != nil {
		w.fail(fmt.Errorf("reserve %s of %s: %w", q, b.free(), err))
		return
	}
	b.reserved = b.reserved.Add(q)
	id := res.Reservations[0].ID
	w.resIDs = append(w.resIDs, id)
	w.resBook[id], w.resQty[id] = b, q
	w.stats["reservation"]++
	if len(w.resIDs) > 1 && w.rng.IntN(3) == 0 {
		i := w.rng.IntN(len(w.resIDs) - 1)
		rid := w.resIDs[i]
		rb := w.resBook[rid]
		if _, err := w.s.ReleaseReservation(w.ctxs[w.leOf(rb)], rid); err != nil {
			w.fail(fmt.Errorf("release: %w", err))
			return
		}
		rb.reserved = rb.reserved.Sub(w.resQty[rid])
		w.resIDs = append(w.resIDs[:i], w.resIDs[i+1:]...)
		w.stats["release"]++
	}
}

func (w *ac2Worker) run(maxLines int) {
	// Opening balances first, so issues have stock to draw on.
	for le := range w.ctxs {
		w.receiptsAndIssues(le, domain.JournalOpeningBalance, min(w.budget/10+1, maxLines, w.budget), true)
	}
	for w.budget > 0 && len(w.errs) == 0 {
		le := w.rng.IntN(len(w.ctxs))
		n := 1 + w.rng.IntN(maxLines)
		switch k := w.rng.IntN(100); {
		case k < 40:
			w.receiptsAndIssues(le, domain.JournalMovement, min(n, w.budget), false)
		case k < 65:
			w.receiptsAndIssues(le, domain.JournalAdjustment, min(n, w.budget), false)
		case k < 70:
			w.receiptsAndIssues(le, domain.JournalOpeningBalance, min(n, w.budget), true)
		case k < 82:
			if w.budget >= 2 {
				w.transferJournal(le, min(n, w.budget/2))
			}
		case k < 86:
			if w.budget >= 2 {
				w.block(le)
			}
		case k < 92:
			w.register(le)
		case k < 98:
			w.reserve(le)
		default:
			w.overIssue(le)
		}
	}
}

// TestAC2ProjectionEqualsSumOfTransactions is AC2 and the PRD's inv-1m-txn
// rebuild check. Sixteen workers, each owning a disjoint slice of the products,
// write exactly N transactions (default 40,000; PASSAGE_INVENTORY_AC2_TXNS=1000000
// for the issue's scale) through the store over two legal entities and twelve
// months of posting dates: opening balances, MOVEMENT and ADJUSTMENT journals
// mixing receipts and issues (issues drawn Zipf-skewed over a few hundred to
// 2,000 products, some emptying a row), TRANSFER journals between warehouses,
// blockings, registrations through transactions:apply with half of them
// unregistered, MANUAL reservations with a third released, and over-issues the
// store must refuse. Every worker keeps its own books of what it asked for and
// the store accepted. Checks, in order:
//
//   - the ledger holds exactly N transactions;
//   - RebuildOnHand compares every row and finds no difference;
//   - an SQL re-aggregation of inventory_transaction and active reservations
//     (independent of the rebuild code) equals every stored row bucket by
//     bucket, and no transaction group lacks a row;
//   - every book equals its stored row (posted, registered, arrived, reserved);
//   - per legal entity, the posted on-hand value equals the inventory-account
//     net summed over the stored GL entries of the posted journals.
func TestAC2ProjectionEqualsSumOfTransactions(t *testing.T) {
	n := 40000
	if v := os.Getenv(EnvAC2Transactions); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1000 {
			t.Fatalf("%s=%q: want an integer >= 1000", EnvAC2Transactions, v)
		}
	}
	s, app, admin := setup(t)
	const workers = 16
	hot := wideStore(t, app, workers+4)
	ctxs := []context.Context{entityCtx(1, ids.New()), entityCtx(1, ids.New())}
	products := min(max(n/500, 64), 2000)
	maxLines := min(max(n/400, 20), 500)

	ws := make([]*ac2Worker, workers)
	for i := range ws {
		var mine []string
		for p := i; p < products; p += workers {
			mine = append(mine, ids.New())
		}
		rng := rand.New(rand.NewPCG(1616, uint64(i)))
		ws[i] = &ac2Worker{t: t, s: hot, ctxs: ctxs, rng: rng, zipf: rand.NewZipf(rng, 1.1, 1, uint64(len(mine)-1)), products: mine,
			books: map[string]*ac2Book{}, budget: n / workers, stats: map[string]int{}, resBook: map[string]*ac2Book{},
			resQty: map[string]decimal.Decimal{}, base: time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)}
	}
	ws[workers-1].budget += n % workers
	began := time.Now()
	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func(w *ac2Worker) {
			defer wg.Done()
			w.run(maxLines)
		}(w)
	}
	wg.Wait()
	loaded := time.Since(began)
	written, stats := 0, map[string]int{}
	for i, w := range ws {
		if len(w.errs) > 0 {
			t.Fatalf("worker %d: %v", i, w.errs[0])
		}
		written += w.written
		for k, v := range w.stats {
			stats[k] += v
		}
	}
	if written != n {
		t.Fatalf("generator wrote %d transactions, want %d", written, n)
	}
	bg := context.Background()
	var txns, rows int
	if err := admin.QueryRow(bg, `select (select count(*) from inventory_transaction where tenant_id = $1),
		(select count(*) from on_hand where tenant_id = $1)`, testkit.TenantID(1)).Scan(&txns, &rows); err != nil {
		t.Fatal(err)
	}
	if txns != n {
		t.Fatalf("ledger holds %d transactions, want %d", txns, n)
	}

	began = time.Now()
	rep, err := s.RebuildOnHand(tenantWideCtx(1), false)
	rebuilt := time.Since(began)
	if err != nil || rep.DiffCount != 0 || rep.Checked != rows {
		t.Fatalf("rebuild: %v checked %d of %d rows, %d differ: %+v", err, rep.Checked, rows, rep.DiffCount, rep.Diffs)
	}

	var mismatched, orphanGroups int
	if err := admin.QueryRow(bg, ac2Reaggregate, testkit.TenantID(1)).Scan(&mismatched, &orphanGroups); err != nil {
		t.Fatal(err)
	}
	if mismatched != 0 || orphanGroups != 0 {
		t.Fatalf("SQL re-aggregation: %d rows differ from SUM(transactions), %d transaction groups have no row", mismatched, orphanGroups)
	}

	checkAC2Books(t, admin, ws)

	for _, ctx := range ctxs {
		var value, gl string
		if err := admin.QueryRow(bg, `select
			(select coalesce(sum(posted_value), 0) from on_hand where tenant_id = $1 and legal_entity_id = $2)::text,
			(select coalesce(sum((l->>'debit')::numeric - (l->>'credit')::numeric), 0) from inventory_journal j,
			  jsonb_array_elements(j.gl_entry->'lines') l
			 where j.tenant_id = $1 and j.legal_entity_id = $2 and j.status = 'POSTED' and l->>'account' = $3)::text`,
			testkit.TenantID(1), legalEntity(ctx), inventoryAccount).Scan(&value, &gl); err != nil {
			t.Fatal(err)
		}
		if !dec(value).Equal(dec(gl)) {
			t.Fatalf("legal entity %s: posted on-hand value %s, GL inventory net %s", legalEntity(ctx), value, gl)
		}
	}
	t.Logf("AC2: %d transactions over %d products (%d on-hand rows) written in %s; rebuild-and-compare in %s, 0 differences; operations %v",
		n, products, rows, loaded.Round(time.Second), rebuilt.Round(time.Millisecond), stats)
}

// ac2Reaggregate re-derives every bucket the transactions and reservations
// imply with plain SQL (receipt statuses add, issue statuses subtract, as PRD
// FR-16.3 defines the buckets) and counts stored rows that differ, and
// transaction groups with no stored row.
const ac2Reaggregate = `
with t as (
  select legal_entity_id, product_id, dimension_set_hash,
    sum(qty) filter (where coalesce(issue_status, receipt_status) in ('SOLD', 'PURCHASED')) posted_qty,
    sum(coalesce(cost_amount_posted, 0)) posted_value,
    sum(qty) filter (where receipt_status = 'RECEIVED') received,
    sum(qty) filter (where receipt_status = 'REGISTERED') registered,
    sum(qty) filter (where receipt_status = 'ARRIVED') arrived,
    sum(qty) filter (where receipt_status = 'ORDERED') ordered,
    -sum(qty) filter (where issue_status = 'DEDUCTED') deducted,
    -sum(qty) filter (where issue_status = 'PICKED') picked,
    -sum(qty) filter (where issue_status = 'RESERVED_ORDERED') reserved_ordered,
    -sum(qty) filter (where issue_status = 'ON_ORDER') on_order,
    -sum(qty) filter (where issue_status = 'RESERVED_PHYSICAL') reserved_physical_txn
  from inventory_transaction where tenant_id = $1
  group by legal_entity_id, product_id, dimension_set_hash),
r as (
  select legal_entity_id, product_id, dimension_set_hash, sum(qty) reserved
  from inventory_reservation where tenant_id = $1 and status = 'ACTIVE'
  group by legal_entity_id, product_id, dimension_set_hash)
select
  (select count(*) from on_hand o
     left join t using (legal_entity_id, product_id, dimension_set_hash)
     left join r using (legal_entity_id, product_id, dimension_set_hash)
   where o.tenant_id = $1 and (
     o.posted_qty <> coalesce(t.posted_qty, 0) or o.posted_value <> coalesce(t.posted_value, 0)
     or o.received <> coalesce(t.received, 0) or o.registered <> coalesce(t.registered, 0)
     or o.arrived <> coalesce(t.arrived, 0) or o.ordered <> coalesce(t.ordered, 0)
     or o.deducted <> coalesce(t.deducted, 0) or o.picked <> coalesce(t.picked, 0)
     or o.reserved_ordered <> coalesce(t.reserved_ordered, 0) or o.on_order <> coalesce(t.on_order, 0)
     or o.reserved_physical <> coalesce(r.reserved, 0) + coalesce(t.reserved_physical_txn, 0))),
  (select count(*) from t where not exists (select 1 from on_hand o where o.tenant_id = $1
     and o.legal_entity_id = t.legal_entity_id and o.product_id = t.product_id and o.dimension_set_hash = t.dimension_set_hash))`

// checkAC2Books compares every worker book with its stored row.
func checkAC2Books(t *testing.T, admin *pgxpool.Pool, ws []*ac2Worker) {
	t.Helper()
	type stored struct{ posted, registered, arrived, reserved decimal.Decimal }
	got := map[string]stored{}
	rows, err := admin.Query(context.Background(), `select legal_entity_id::text, product_id::text, dimension_set_hash,
		posted_qty::text, registered::text, arrived::text, reserved_physical::text from on_hand where tenant_id = $1`, testkit.TenantID(1))
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var le, product, p, rg, ar, rs string
		var h []byte
		if err := rows.Scan(&le, &product, &h, &p, &rg, &ar, &rs); err != nil {
			t.Fatal(err)
		}
		got[le+"|"+product+"|"+string(h)] = stored{dec(p), dec(rg), dec(ar), dec(rs)}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, w := range ws {
		for _, b := range w.books {
			st, ok := got[b.le+"|"+b.product+"|"+string(b.hash)]
			if !ok {
				if b.posted.IsZero() && b.registered.IsZero() && b.arrived.IsZero() && b.reserved.IsZero() {
					continue // a row the generator only drew, never wrote
				}
				t.Fatalf("book %s %v has no stored row", b.product, b.dims)
			}
			if !st.posted.Equal(b.posted) || !st.registered.Equal(b.registered) || !st.arrived.Equal(b.arrived) || !st.reserved.Equal(b.reserved) {
				t.Fatalf("row %s %v stored posted/registered/arrived/reserved %s/%s/%s/%s, books %s/%s/%s/%s", b.product, b.dims,
					st.posted, st.registered, st.arrived, st.reserved, b.posted, b.registered, b.arrived, b.reserved)
			}
			checked++
		}
	}
	if checked != len(got) {
		t.Fatalf("books cover %d of %d stored rows", checked, len(got))
	}
}

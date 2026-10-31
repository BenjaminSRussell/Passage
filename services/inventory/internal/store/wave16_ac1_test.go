package store_test

// Wave 16 proof, AC1 (issue #16, PRD AC-16.1): concurrent issues against one SKU
// never drive on-hand negative when the policy forbids it.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// wideStore returns a store on its own application-role pool of conns
// connections, so many postings contend for the row lock at the same moment
// instead of queueing on the default (CPU-sized) pool.
func wideStore(t *testing.T, app *pgxpool.Pool, conns int32) *store.Postgres {
	t.Helper()
	cfg := app.Config()
	cfg.MaxConns, cfg.MinConns = conns, conns
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return store.New(pool)
}

// draftsInParallel creates n DRAFT journals with workers goroutines; build
// returns the lines of journal i.
func draftsInParallel(t *testing.T, ctx context.Context, s *store.Postgres, typ domain.JournalType, n, workers int,
	build func(i int) []domain.JournalLine) []domain.Journal {
	t.Helper()
	out := make([]domain.Journal, n)
	errs := make([]error, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				out[i], errs[i] = newJournal(ctx, s, typ, build(i)...)
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("draft %d: %v", i, err)
		}
	}
	return out
}

// TestAC1TenThousandConcurrentIssuesOfOneSKU is PRD AC-16.1 at the issue's full
// scale: with 1,000 units on hand and the forbid policy, 10,000 single-unit
// issue journals (prepared as drafts first) are released at once through a
// barrier against one SKU and one dimension set. Exactly 1,000 post, 9,000 are
// refused with INV-NEG-001, nothing else fails (no deadlock, no serialization
// error), on-hand ends at 0 quantity and 0 value, only the 1,000 posted issues
// wrote transactions, and the projection equals a rebuild from the ledger.
// Expected counts are the AC's literals (1,000 units / 1 unit per issue).
func TestAC1TenThousandConcurrentIssuesOfOneSKU(t *testing.T) {
	s, app, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "1000", "2.5", dims()))

	const issues = 10000
	drafts := draftsInParallel(t, ctx, s, domain.JournalAdjustment, issues, 32, func(int) []domain.JournalLine {
		return []domain.JournalLine{line(product, "-1", "0", dims())}
	})
	hot := wideStore(t, app, 64)
	var ok, rejected, other atomic.Int64
	var firstOther atomic.Value
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, j := range drafts {
		wg.Add(1)
		go func(j domain.Journal) {
			defer wg.Done()
			<-start
			_, err := hot.PostJournal(ctx, j.ID, j.Version, postParams())
			switch {
			case err == nil:
				ok.Add(1)
			case problemCode(err) == domain.CodeNegativePhysical:
				rejected.Add(1)
			default:
				other.Add(1)
				firstOther.CompareAndSwap(nil, err.Error())
			}
		}(j)
	}
	began := time.Now()
	close(start)
	wg.Wait()
	t.Logf("AC1: %d issues in %s: posted=%d refused INV-NEG-001=%d other=%d", issues, time.Since(began).Round(time.Millisecond),
		ok.Load(), rejected.Load(), other.Load())
	if ok.Load() != 1000 || rejected.Load() != 9000 || other.Load() != 0 {
		t.Fatalf("posted=%d refused=%d other=%d (first other: %v), want 1000 / 9000 / 0", ok.Load(), rejected.Load(), other.Load(), firstOther.Load())
	}
	if phys, value := onHandFor(t, s, ctx, product); !phys.IsZero() || !value.IsZero() {
		t.Fatalf("on-hand %s / %s, want 0 / 0", phys, value)
	}
	var issueTxns, postedJournals, draftJournals int
	if err := admin.QueryRow(context.Background(), `select
		(select count(*) from inventory_transaction where tenant_id = $1 and product_id = $2 and issue_status is not null),
		(select count(*) from inventory_journal where tenant_id = $1 and journal_type = 'ADJUSTMENT' and status = 'POSTED'),
		(select count(*) from inventory_journal where tenant_id = $1 and journal_type = 'ADJUSTMENT' and status = 'DRAFT')`,
		testkit.TenantID(1), product).Scan(&issueTxns, &postedJournals, &draftJournals); err != nil {
		t.Fatal(err)
	}
	if issueTxns != 1000 || postedJournals != 1000 || draftJournals != 9000 {
		t.Fatalf("issue transactions %d, posted journals %d, drafts %d; want 1000 / 1000 / 9000", issueTxns, postedJournals, draftJournals)
	}
	if rep, err := s.RebuildOnHand(tenantWideCtx(1), false); err != nil || rep.DiffCount != 0 || rep.Checked != 1 {
		t.Fatalf("rebuild after the race: %v %+v", err, rep)
	}
}

// ac1Row is one dimension set of the SKU in the mixed race and its opening stock.
type ac1Row struct {
	dims    domain.Dimensions
	opening decimal.Decimal
}

// TestAC1MixedConcurrentIssuesAndReservationsNeverGoNegative is the adversarial
// form of AC1. One SKU sits in four dimension sets (two warehouses x two
// batches, fractional opening stock). 3,000 issue journals of one to three
// lines over distinct sets, with lines in random order (the posting must lock
// in key order or deadlock), and 300 FIFO reservations race for the stock
// together. Demand exceeds the 10,000 units of stock (the total is logged), so
// rows drain unevenly and some refusals happen while other rows still hold
// stock. Issues and reservations only ever lower availability, so the final
// availability is the minimum over the race; the checks are:
//
//   - nothing fails except INV-NEG-001 / INV-RES-001 (no deadlock or 40001);
//   - every row ends with physical and available stock >= 0;
//   - per row, opening - sum of the posted issue lines (the test's own books)
//     equals the stored posted quantity, and the sum of the granted
//     reservations equals reserved_physical;
//   - every refused journal has a line whose row ends with less available than
//     the line asked for, and every refused reservation asked for more than the
//     SKU ends with (availability never comes back, so a refusal was not
//     premature);
//   - the projection equals a rebuild from the ledger.
//
// The seed is fixed; the success rate is whatever the race yields and is logged.
func TestAC1MixedConcurrentIssuesAndReservationsNeverGoNegative(t *testing.T) {
	s, app, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	rows := []ac1Row{
		{dims("warehouse", "W1", "batch", "B1"), dec("3125")},
		{dims("warehouse", "W1", "batch", "B2"), dec("2500")},
		{dims("warehouse", "W2", "batch", "B1"), dec("1997.75")},
		{dims("warehouse", "W2", "batch", "B2"), dec("2376.25")},
	}
	var opening []domain.JournalLine
	for _, r := range rows {
		opening = append(opening, line(product, r.opening.String(), "4", r.dims))
	}
	post(t, ctx, s, domain.JournalOpeningBalance, opening...)

	rng := rand.New(rand.NewPCG(16, 1))
	sizes := []string{"0.25", "0.5", "1", "1", "2", "3", "7.5"}
	type issueLine struct {
		row int
		qty decimal.Decimal
	}
	const journals, reservations = 3000, 300
	plans := make([][]issueLine, journals)
	for i := range plans {
		picked := rng.Perm(len(rows))[:1+rng.IntN(3)] // distinct rows, random order
		for _, r := range picked {
			plans[i] = append(plans[i], issueLine{r, dec(sizes[rng.IntN(len(sizes))])})
		}
	}
	resQty := make([]decimal.Decimal, reservations)
	for i := range resQty {
		resQty[i] = dec(sizes[rng.IntN(len(sizes))])
	}
	demand := decimal.Zero
	for _, p := range plans {
		for _, l := range p {
			demand = demand.Add(l.qty)
		}
	}
	for _, q := range resQty {
		demand = demand.Add(q)
	}
	if demand.LessThanOrEqual(dec("10000")) {
		t.Fatalf("demand %s does not exceed the stock; the race would not test the policy", demand)
	}
	drafts := draftsInParallel(t, ctx, s, domain.JournalAdjustment, journals, 32, func(i int) []domain.JournalLine {
		var ls []domain.JournalLine
		for _, l := range plans[i] {
			ls = append(ls, line(product, l.qty.Neg().String(), "0", rows[l.row].dims))
		}
		return ls
	})

	hot := wideStore(t, app, 64)
	journalOK := make([]bool, journals)
	resGranted := make([][]domain.Reservation, reservations)
	var refusedJ, refusedR, other atomic.Int64
	var firstOther atomic.Value
	start := make(chan struct{})
	var wg sync.WaitGroup
	order := rng.Perm(journals + reservations) // interleave the two kinds of demand
	for _, k := range order {
		wg.Add(1)
		go func(k int) {
			defer wg.Done()
			<-start
			if k < journals {
				_, err := hot.PostJournal(ctx, drafts[k].ID, drafts[k].Version, postParams())
				switch {
				case err == nil:
					journalOK[k] = true
				case problemCode(err) == domain.CodeNegativePhysical:
					refusedJ.Add(1)
				default:
					other.Add(1)
					firstOther.CompareAndSwap(nil, err.Error())
				}
				return
			}
			i := k - journals
			res, err := hot.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: resQty[i],
				DemandType: domain.DemandSalesOrder, DemandID: fmt.Sprintf("SO-%d", i), Policy: domain.PolicyFIFO,
				RequiredDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: fmt.Sprintf("ac1-res-%d", i)})
			switch {
			case err == nil:
				resGranted[i] = res.Reservations
			case problemCode(err) == domain.CodeReserveShortfall:
				refusedR.Add(1)
			default:
				other.Add(1)
				firstOther.CompareAndSwap(nil, err.Error())
			}
		}(k)
	}
	close(start)
	wg.Wait()
	if other.Load() != 0 {
		t.Fatalf("%d operations failed with something other than a shortfall; first: %v", other.Load(), firstOther.Load())
	}

	// The test's own books: opening minus posted issue lines, and granted reservations.
	wantQty := make([]decimal.Decimal, len(rows))
	wantReserved := make([]decimal.Decimal, len(rows))
	byHash := map[string]int{}
	for i, r := range rows {
		wantQty[i], wantReserved[i] = r.opening, decimal.Zero
		n, _ := r.dims.Normalize()
		byHash[string(n.Hash())] = i
	}
	postedJ := 0
	for i, okJ := range journalOK {
		if !okJ {
			continue
		}
		postedJ++
		for _, l := range plans[i] {
			wantQty[l.row] = wantQty[l.row].Sub(l.qty)
		}
	}
	granted := 0
	for _, rs := range resGranted {
		if rs != nil {
			granted++
		}
		for _, r := range rs {
			n, _ := r.Dims.Normalize()
			idx, ok := byHash[string(n.Hash())]
			if !ok {
				t.Fatalf("reservation on an unknown dimension set %v", r.Dims)
			}
			wantReserved[idx] = wantReserved[idx].Add(r.Qty)
		}
	}

	finalAvail := make([]decimal.Decimal, len(rows))
	total := decimal.Zero
	for i, r := range rows {
		n, _ := r.dims.Normalize()
		var posted, reserved, received, registered, deducted, picked string
		if err := admin.QueryRow(context.Background(), `select posted_qty::text, reserved_physical::text, received::text, registered::text,
			deducted::text, picked::text from on_hand where tenant_id = $1 and product_id = $2 and dimension_set_hash = $3`,
			testkit.TenantID(1), product, n.Hash()).Scan(&posted, &reserved, &received, &registered, &deducted, &picked); err != nil {
			t.Fatal(err)
		}
		physical := dec(posted).Add(dec(received)).Add(dec(registered)).Sub(dec(deducted)).Sub(dec(picked))
		finalAvail[i] = physical.Sub(dec(reserved))
		total = total.Add(finalAvail[i])
		if physical.Sign() < 0 || finalAvail[i].Sign() < 0 {
			t.Fatalf("row %d ended negative: physical %s available %s", i, physical, finalAvail[i])
		}
		if !dec(posted).Equal(wantQty[i]) || !dec(reserved).Equal(wantReserved[i]) {
			t.Fatalf("row %d: stored posted %s reserved %s, books say %s / %s", i, posted, reserved, wantQty[i], wantReserved[i])
		}
	}
	for i, okJ := range journalOK {
		if okJ {
			continue
		}
		justified := false
		for _, l := range plans[i] {
			if finalAvail[l.row].LessThan(l.qty) {
				justified = true
			}
		}
		if !justified {
			t.Fatalf("journal %d was refused but every row still has the quantity it asked for: %+v (final %v)", i, plans[i], finalAvail)
		}
	}
	for i, rs := range resGranted {
		if rs == nil && !total.LessThan(resQty[i]) {
			t.Fatalf("reservation %d of %s was refused but %s is still available", i, resQty[i], total)
		}
	}
	if rep, err := s.RebuildOnHand(tenantWideCtx(1), false); err != nil || rep.DiffCount != 0 || rep.Checked != len(rows) {
		t.Fatalf("rebuild after the race: %v %+v", err, rep)
	}
	left := make([]string, len(finalAvail))
	for i, a := range finalAvail {
		left[i] = a.String()
	}
	sort.Strings(left)
	t.Logf("AC1 mixed: demand %s against 10000; journals posted %d / refused %d of %d; reservations granted %d / refused %d of %d; available left per row %v",
		demand, postedJ, refusedJ.Load(), journals, granted, refusedR.Load(), reservations, left)
}

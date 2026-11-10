package store_test

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/issueclaim"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// #33 AC2: claims of every kind and inventory's own issues are weighed
// together against held stock, and a unit is counted once however many steps
// (pick, ship, the delivery booking) move it.

func sortedStrings(s []string) []string {
	slices.Sort(s)
	return s
}

func refClaim(ctx context.Context, product, key, kind, qty, ref string) store.IssueClaimInput {
	c := claimOf(ctx, product, key, kind, qty)
	c.IssueRef = ref
	return c
}

var deliverySources sync.Map

// deliver books a sales issue at W1 as the postings consumer books a DELIVERED
// line: ref is the order line's ref, empty for an issue that names none.
// A repeat of key (a redelivered event) names the same origin.
func deliver(ctx context.Context, s *store.Postgres, product, key string, qty decimal.Decimal, ref string) error {
	q := qty.Neg()
	src, _ := deliverySources.LoadOrStore(product+"|"+key, ids.New())
	_, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{LegalEntityID: legalEntityOf(ctx), SourceType: "SALES_LINE", SourceID: src.(string),
		ProductID: product, Dims: dims(), Qty: q, EnteredQty: q, EnteredUnit: "EA", Status: domain.StatusDeducted, Currency: "USD",
		CostPrice: dec("1"), IdempotencyKey: key, IssueRef: ref})
	return err
}

// journalMove posts a MOVEMENT journal of qty (negative: an issue) at W1.
func journalMove(ctx context.Context, s *store.Postgres, product string, qty decimal.Decimal) error {
	j, err := newJournal(ctx, s, domain.JournalMovement, line(product, qty.String(), "1", dims()))
	if err != nil {
		return err
	}
	_, err = s.PostJournal(ctx, j.ID, j.Version, postParams())
	return err
}

func roomOf(t testing.TB, ctx context.Context, s *store.Postgres, product string) store.IssueRoom {
	t.Helper()
	r, err := s.GetIssueRoom(ctx, legalEntityOf(ctx), product, "W1", "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func wantRoom(t *testing.T, got store.IssueRoom, physical, held, outstanding string) {
	t.Helper()
	if !got.Physical.Equal(dec(physical)) || !got.Held.Equal(dec(held)) || !got.Outstanding.Equal(dec(outstanding)) {
		t.Fatalf("room %s physical / %s held / %s outstanding, want %s / %s / %s", got.Physical, got.Held, got.Outstanding, physical, held, outstanding)
	}
}

func TestIssueClaim_APickAndTheShipOfWhatItPickedCountOnce(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "6", "qo-1", dims())); err != nil {
		t.Fatal(err)
	}
	l1, l2 := issueclaim.SalesLineRef(ids.New()), issueclaim.SalesLineRef(ids.New())
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick", "PICK", "3", l1)); err != nil {
		t.Fatalf("pick of 3 of the free 4: %v", err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "ship", "SHIP", "3", l1)); err != nil {
		t.Fatalf("ship of the 3 the line picked: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "6", "3")
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "consume", "CONSUME", "2")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("consume of 2 with 1 free: %v, want %s", err, domain.CodeQualityHold)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick-2", "PICK", "2", l2)); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("pick of 2 for another line with 1 free: %v, want %s", err, domain.CodeQualityHold)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "ship-more", "SHIP", "1", l1)); err != nil {
		t.Fatalf("ship of 1 more of the line, the 1 free: %v", err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick-more", "PICK", "1", l1)); err != nil {
		t.Fatalf("a late pick of the unit the line shipped: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "6", "4")
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "replayed", "SHIP", "1", l2)); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("nothing is free: %v", err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "ship", "SHIP", "3", l2)); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("a key reused for another line: %v, want %s", err, store.CodeIdempotencyReuse)
	}
}

func TestIssueClaim_TheDeliveryBookingSettlesTheClaimsOfItsLine(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "6", "qo-1", dims())); err != nil {
		t.Fatal(err)
	}
	l := issueclaim.SalesLineRef(ids.New())
	for _, kind := range []string{"PICK", "SHIP"} {
		if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, kind, kind, "2", l)); err != nil {
			t.Fatalf("%s of 2: %v", kind, err)
		}
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "6", "2")
	if err := deliver(ctx, s, product, "d1", dec("2"), l); err != nil {
		t.Fatalf("delivery of the shipped 2: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "6", "0")
	if err := deliver(ctx, s, product, "d1", dec("2"), l); err != nil {
		t.Fatalf("replay of the delivery: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "6", "0")
	// Counted twice, the 2 would be gone from physical and still claimed,
	// and the free 2 refused.
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "consume", "CONSUME", "2")); err != nil {
		t.Fatalf("consume of the 2 still free: %v", err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "consume-more", "CONSUME", "1")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("consume beyond the free 2: %v, want %s", err, domain.CodeQualityHold)
	}
	// A claim cannot withdraw what inventory booked.
	if err := s.ReleaseIssueClaim(ctx, "issued:d1||"+l); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "6", "2")
}

// A hold placed after a pick may cover the staged units: nothing more moves
// until the usage decision, claims or inventory issues alike, but the booking
// of a delivery that already left (it settles its claims) is not refused.
func TestIssueClaim_AHoldPlacedAfterThePickStopsTheShipOfWhatWasPicked(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	l := issueclaim.SalesLineRef(ids.New())
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick", "PICK", "6", l)); err != nil {
		t.Fatal(err)
	}
	res, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "5", "qo-1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "ship", "SHIP", "2", l)); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("ship of staged units a later hold may cover: %v, want %s", err, domain.CodeQualityHold)
	}
	if err := journalMove(ctx, s, product, dec("-1")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("journal issue with nothing free: %v, want %s", err, domain.CodeQualityHold)
	}
	if err := deliver(ctx, s, product, "d1", dec("2"), l); err != nil {
		t.Fatalf("booking of a delivery of the line: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "5", "4")
	if _, err := s.ReleaseHold(ctx, res.Holds[0].ID, res.Holds[0].Version, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "ship", "SHIP", "2", l)); err != nil {
		t.Fatalf("ship after the usage decision: %v", err)
	}
}

// Stock issued for a line that never claimed it settles nothing, so a later
// claim of the same line still counts in full.
func TestIssueClaim_AnIssueWithoutAClaimDoesNotAbsorbALaterClaimOfItsLine(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "4", "qo-1", dims())); err != nil {
		t.Fatal(err)
	}
	l := issueclaim.SalesLineRef(ids.New())
	if err := deliver(ctx, s, product, "d1", dec("2"), l); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick", "PICK", "3", l)); err != nil {
		t.Fatalf("pick of 3 of the free 4: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "4", "3")
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "ship", "SHIP", "2")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("ship of 2 with 1 free: %v, want %s", err, domain.CodeQualityHold)
	}
}

func TestIssueClaim_InventoryIssuesCannotReachHeldStockBehindClaims(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	res, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "4", "qo-1", dims()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "consume", "CONSUME", "4")); err != nil {
		t.Fatal(err)
	}
	if err := journalMove(ctx, s, product, dec("-3")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("journal issue of 3 with 2 free behind the claims: %v, want %s", err, domain.CodeQualityHold)
	}
	if err := deliver(ctx, s, product, "d1", dec("3"), ""); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("sales issue of 3 with 2 free: %v, want %s", err, domain.CodeQualityHold)
	}
	if _, _, err := s.CreateBlocking(ctx, store.BlockingInput{IdempotencyKey: "b1", LegalEntityID: legalEntityOf(ctx), ProductID: product, Dims: dims(),
		Qty: dec("3"), Unit: "EA", Target: domain.StockBlocked, Reason: "QUALITY", PhysicalDate: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)}); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("blocking of 3 with 2 free: %v, want %s", err, domain.CodeQualityHold)
	}
	if err := journalMove(ctx, s, product, dec("-2")); err != nil {
		t.Fatalf("journal issue of the free 2: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "4", "4")
	if _, err := s.ReleaseHold(ctx, res.Holds[0].ID, res.Holds[0].Version, nil); err != nil {
		t.Fatal(err)
	}
	if err := journalMove(ctx, s, product, dec("-1")); err != nil {
		t.Fatalf("journal issue after the usage decision: %v", err)
	}
}

// Claims and journal issues racing for the free quantity behind one hold:
// together they take exactly what is free, never held stock.
func TestIssueClaim_ConcurrentClaimsAndJournalIssuesTakeOnlyTheFreeQuantity(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "14", "qo-1", dims())); err != nil {
		t.Fatal(err)
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = s.ClaimIssue(ctx, claimOf(ctx, product, fmt.Sprintf("c%d", i), "SHIP", "1"))
			} else {
				err = journalMove(ctx, s, product, dec("-1"))
			}
			switch {
			case err == nil:
				admitted.Add(1)
			case problemCode(err) != domain.CodeQualityHold:
				t.Errorf("movement %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 6 {
		t.Fatalf("%d movements admitted against 6 free units", admitted.Load())
	}
	if room := roomOf(t, ctx, s, product); !room.Free().IsZero() {
		t.Fatalf("free %s after the race, want 0", room.Free())
	}
}

// issueModel is the hold rule as the property test states it: physical and
// held stock of the one row, the claims taken and what inventory booked for
// each ref.
type issueModel struct {
	physical, held decimal.Decimal
	claims         map[string]issueclaim.Claim
	issued         map[string]decimal.Decimal
}

func (m *issueModel) refOutstanding(ref string, extra *issueclaim.Claim) decimal.Decimal {
	per := map[issueclaim.Kind]decimal.Decimal{}
	for _, c := range m.claims {
		if c.Ref == ref {
			per[c.Kind] = per[c.Kind].Add(c.Qty)
		}
	}
	if extra != nil {
		per[extra.Kind] = per[extra.Kind].Add(extra.Qty)
	}
	most := decimal.Zero
	for _, q := range per {
		if ref == "" {
			most = most.Add(q)
		} else {
			most = decimal.Max(most, q)
		}
	}
	return decimal.Max(decimal.Zero, most.Sub(m.issued[ref]))
}

// settledBy is what an issue of qty settles: nothing for an issue that names no
// ref, else no more than the ref's own open claims. Claims without a ref and
// claims of other refs are never settled by an issue.
func (m *issueModel) settledBy(qty decimal.Decimal, ref string) map[string]decimal.Decimal {
	if ref == "" {
		return nil
	}
	return map[string]decimal.Decimal{ref: decimal.Min(qty, m.refOutstanding(ref, nil))}
}

// outstanding is what the claims stand for that inventory has not booked:
// unreferenced claims in full, and per ref the largest per-kind total less
// what was issued for it. extra is a claim being weighed.
func (m *issueModel) outstanding(extra *issueclaim.Claim) decimal.Decimal {
	total := decimal.Zero
	refs := map[string]bool{}
	for _, c := range m.claims {
		refs[c.Ref] = true
	}
	if extra != nil {
		refs[extra.Ref] = true
	}
	for ref := range refs {
		var e *issueclaim.Claim
		if extra != nil && extra.Ref == ref {
			e = extra
		}
		total = total.Add(m.refOutstanding(ref, e))
	}
	return total
}

func (m *issueModel) free() decimal.Decimal { return m.physical.Sub(m.held).Sub(m.outstanding(nil)) }

// admitsClaim is the hold rule for a claim: with stock held, nothing may be
// left free below zero after it.
func (m *issueModel) admitsClaim(freeAfter decimal.Decimal) bool {
	return !m.held.IsPositive() || !freeAfter.IsNegative()
}

// admitsIssue is the rule for inventory's own issues: the same, except that
// an issue that leaves free stock where it was (the booking of a delivery its
// claims let out) passes.
func (m *issueModel) admitsIssue(freeAfter decimal.Decimal) bool {
	return m.admitsClaim(freeAfter) || !freeAfter.LessThan(m.free())
}

// issueOutcome is what an issue of qty (settling ref, if set) should meet:
// the row's own checks first (physical below held, then negative stock),
// then the claims. ok is false with the code expected.
func (m *issueModel) issueOutcome(qty decimal.Decimal, ref string) (settled map[string]decimal.Decimal, code string) {
	after := m.physical.Sub(qty)
	switch {
	case m.held.IsPositive() && after.LessThan(m.held):
		return nil, domain.CodeQualityHold
	case after.Sub(m.held).IsNegative():
		return nil, domain.CodeNegativePhysical
	}
	settled = m.settledBy(qty, ref)
	total := decimal.Zero
	for _, q := range settled {
		total = total.Add(q)
	}
	if !m.admitsIssue(after.Sub(m.held).Sub(m.outstanding(nil).Sub(total))) {
		return nil, domain.CodeQualityHold
	}
	return settled, ""
}

// TestHoldProperty_MixedMovementsNeverReachHeldStock interleaves picks, ships,
// consumptions (with and without a ref, some withdrawn), journal issues and
// receipts, delivery bookings that settle a line's claims, and holds placed
// (on free stock, as at receipt, or over stock already claimed, as after a
// pick) and released. After every step the store must agree with the model
// (physical, held and outstanding), available + held = on hand on the row,
// and no movement may take free stock (on hand less held less the claims not
// yet issued) below zero, or lower it further when a hold over claimed stock
// left it there, while anything is held. Every movement is refused exactly
// when the model says it would reach held stock: no false refusal of free
// stock, and a unit picked and then shipped, or claimed and then booked, is
// counted once.
func TestHoldProperty_MixedMovementsNeverReachHeldStock(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	le := legalEntityOf(ctx)
	kinds := []issueclaim.Kind{issueclaim.Pick, issueclaim.Ship, issueclaim.Consume}
	rapid.Check(t, func(rt *rapid.T) {
		product := ids.New()
		refs := []string{"", issueclaim.SalesLineRef(ids.New()), issueclaim.SalesLineRef(ids.New()), issueclaim.ProductionItemRef(ids.New(), product)}
		post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
		m := &issueModel{physical: dec("20"), claims: map[string]issueclaim.Claim{}, issued: map[string]decimal.Decimal{}}
		seq := 0
		key := func() string {
			seq++
			return fmt.Sprintf("%s-%d", product, seq)
		}
		check := func(step string, movement bool, freeBefore decimal.Decimal) {
			room, err := s.GetIssueRoom(ctx, le, product, "W1", "")
			if err != nil {
				rt.Fatalf("%s: %v", step, err)
			}
			if !room.Physical.Equal(m.physical) || !room.Held.Equal(m.held) || !room.Outstanding.Equal(m.outstanding(nil)) {
				rt.Fatalf("%s: store %s physical / %s held / %s outstanding, model %s / %s / %s", step, room.Physical, room.Held, room.Outstanding,
					m.physical, m.held, m.outstanding(nil))
			}
			if movement && m.held.IsPositive() && m.free().LessThan(decimal.Min(decimal.Zero, freeBefore)) {
				rt.Fatalf("%s: held stock reached: %s held, %s physical, %s claimed and not issued (free was %s)", step, room.Held, room.Physical,
					room.Outstanding, freeBefore)
			}
			r := stockByLocation(t, ctx, s, product)[""]
			if !r.avail.Add(r.held).Add(r.reserved).Equal(r.physical) || !r.held.Equal(m.held) {
				rt.Fatalf("%s: row available %s + held %s + reserved %s != on hand %s", step, r.avail, r.held, r.reserved, r.physical)
			}
		}
		steps := rapid.IntRange(1, 30).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			qty := decimal.NewFromInt(int64(rapid.IntRange(1, 8).Draw(rt, "qty")))
			freeBefore := m.free()
			op := rapid.IntRange(0, 7).Draw(rt, "op")
			switch op {
			case 0: // a claim of any kind, with or without a ref
				c := issueclaim.Claim{Kind: kinds[rapid.IntRange(0, 2).Draw(rt, "kind")], Qty: qty, Ref: refs[rapid.IntRange(0, len(refs)-1).Draw(rt, "ref")], Key: key()}
				want := m.admitsClaim(m.physical.Sub(m.held).Sub(m.outstanding(&c)))
				_, err := s.ClaimIssue(ctx, refClaim(ctx, product, c.Key, string(c.Kind), c.Qty.String(), c.Ref))
				switch {
				case want && err != nil:
					rt.Fatalf("%s %s ref %q with %s free: refused %v", c.Kind, c.Qty, c.Ref, m.free(), err)
				case !want && problemCode(err) != domain.CodeQualityHold:
					rt.Fatalf("%s %s ref %q with %s free: %v, want %s", c.Kind, c.Qty, c.Ref, m.free(), err, domain.CodeQualityHold)
				case want:
					m.claims[c.Key] = c
				}
			case 1: // a movement that did not commit withdraws its claim
				if len(m.claims) == 0 {
					continue
				}
				keys := make([]string, 0, len(m.claims))
				for k := range m.claims {
					keys = append(keys, k)
				}
				k := rapid.SampledFrom(sortedStrings(keys)).Draw(rt, "withdraw")
				if err := s.ReleaseIssueClaim(ctx, k); err != nil {
					rt.Fatal(err)
				}
				delete(m.claims, k)
			case 2, 3: // a journal issue, or a delivery booking (op 3) that settles its line
				ref := ""
				if op == 3 {
					ref = refs[rapid.IntRange(0, len(refs)-1).Draw(rt, "ref")]
				}
				settled, code := m.issueOutcome(qty, ref)
				open := m.refOutstanding(ref, nil)
				var err error
				if op == 2 {
					err = journalMove(ctx, s, product, qty.Neg())
					if err == nil {
						if got := roomOf(t, ctx, s, product).Free(); !got.Equal(freeBefore.Sub(qty)) {
							rt.Fatalf("an unrelated journal issue of %s took free stock from %s to %s", qty, freeBefore, got)
						}
					}
				} else {
					err = deliver(ctx, s, product, key(), qty, ref)
				}
				switch {
				case code == "" && err != nil:
					rt.Fatalf("issue %s ref %q with %s free, %s physical, %s held: refused %v", qty, ref, m.free(), m.physical, m.held, err)
				case code != "" && problemCode(err) != code:
					rt.Fatalf("issue %s ref %q with %s free, %s physical, %s held: %v, want %s", qty, ref, m.free(), m.physical, m.held, err, code)
				case code == "":
					if m.held.IsPositive() && !freeBefore.IsPositive() && (ref == "" || qty.GreaterThan(open)) {
						rt.Fatalf("issue %s ref %q passed with %s free and %s held, %s open under its ref", qty, ref, freeBefore, m.held, open)
					}
					m.physical = m.physical.Sub(qty)
					for r, q := range settled {
						m.issued[r] = m.issued[r].Add(q)
					}
				}
			case 4: // a receipt
				if err := journalMove(ctx, s, product, qty); err != nil {
					rt.Fatal(err)
				}
				m.physical = m.physical.Add(qty)
			case 5, 7: // quality holds what is free (5), or what the row has, claimed or not (7)
				free := decimal.Min(m.free(), m.physical.Sub(m.held))
				if op == 7 {
					free = m.physical.Sub(m.held)
				}
				if !free.IsPositive() {
					continue
				}
				q := decimal.Min(qty, free)
				if _, err := s.PlaceHold(ctx, holdInput(ctx, product, key(), q.String(), "qo", dims())); err != nil {
					rt.Fatalf("hold %s of %s free: %v", q, free, err)
				}
				m.held = m.held.Add(q)
			case 6: // the usage decision releases a hold, in part or whole
				active, err := s.ListHolds(ctx, store.HoldFilter{ProductID: product, State: domain.HoldActive})
				if err != nil {
					rt.Fatal(err)
				}
				if len(active) == 0 {
					continue
				}
				h := active[rapid.IntRange(0, len(active)-1).Draw(rt, "hold")]
				q := decimal.Min(qty, h.Held())
				part := &q
				if q.Equal(h.Held()) {
					part = nil
				}
				if _, err := s.ReleaseHold(ctx, h.ID, h.Version, part); err != nil {
					rt.Fatalf("release %s of hold %s: %v", q, h.ID, err)
				}
				m.held = m.held.Sub(q)
			}
			check(fmt.Sprintf("step %d op %d", i, op), op <= 3, freeBefore)
		}
	})
}

// A pick without a document carries no ref: no issue settles it, so a delivery
// of another line never releases it and never moves the stock it stands for. It
// is released explicitly.
func TestIssueClaim_ADeliveryNeverSettlesAnotherLinesRefLessPick(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "5", "qo-1", dims())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick-b", "PICK", "5", "")); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "5", "5")
	a := issueclaim.SalesLineRef(ids.New())
	if err := deliver(ctx, s, product, "d-a", dec("5"), a); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("delivery of 5 with nothing free and no claim of its own: %v, want %s", err, domain.CodeQualityHold)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "5", "5")
	if err := s.ReleaseIssueClaim(ctx, "pick-b"); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "5", "0")
}

// A production consumption booked for its order item settles the item's
// CONSUME claim; a scrap journal settles no claim and takes free stock.
func TestIssueClaim_AConsumptionCarryingItsRefSettlesItsClaim(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	item := issueclaim.ProductionItemRef(ids.New(), product)
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "consume", "CONSUME", "5", item)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick", "PICK", "2", "")); err != nil {
		t.Fatal(err)
	}
	if err := deliver(ctx, s, product, "c1", dec("5"), item); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "15", "0", "2")
}

func TestIssueClaim_UnlinkedIssuesAndInScopeMovesSettleNothing(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	item := issueclaim.ProductionItemRef(ids.New(), product)
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "consume", "CONSUME", "5", item)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, "pick", "PICK", "4", "")); err != nil {
		t.Fatal(err)
	}
	if err := journalMove(ctx, s, product, dec("-3")); err != nil {
		t.Fatalf("scrap of 3 of the free stock: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "17", "0", "9")
	if free := roomOf(t, ctx, s, product).Free(); !free.Equal(dec("8")) {
		t.Fatalf("free %s after the scrap, want 20 - 9 claimed - 3 scrapped = 8", free)
	}
	if err := deliver(ctx, s, product, "u1", dec("3"), ""); err != nil {
		t.Fatalf("registration naming no ref: %v", err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "14", "0", "9")
	// An issue of 6 naming the item's ref settles the item's 5 and nothing of
	// the pick without a ref.
	if err := deliver(ctx, s, product, "c1", dec("6"), item); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "8", "0", "4")
	post(t, ctx, s, domain.JournalTransfer, transferLine(product, "2", dims(), dims("location", "B")))
	wantRoom(t, roomOf(t, ctx, s, product), "8", "0", "4")
}

func TestIssueClaim_AKeyInTheSettlementNamespaceIsRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	if _, err := s.ClaimIssue(ctx, claimOf(ctx, product, "issued:d1||", "PICK", "1")); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("a claim keyed like a settlement: %v, want %s", err, domain.CodeInvalid)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "10", "0", "0")
}

func pickAndShip(t *testing.T, ctx context.Context, s *store.Postgres, product, qty, ref string) {
	t.Helper()
	for _, kind := range []string{"PICK", "SHIP"} {
		if _, err := s.ClaimIssue(ctx, refClaim(ctx, product, kind+ref, kind, qty, ref)); err != nil {
			t.Fatalf("%s of %s: %v", kind, qty, err)
		}
	}
}

func TestIssueClaim_APickLinkedToItsLineIsSettledWithTheShipByTheDelivery(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	l := issueclaim.SalesLineRef(ids.New())
	pickAndShip(t, ctx, s, product, "5", l)
	wantRoom(t, roomOf(t, ctx, s, product), "20", "0", "5")
	if err := deliver(ctx, s, product, "d1", dec("5"), l); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "15", "0", "0")
	if free := roomOf(t, ctx, s, product).Free(); !free.Equal(dec("15")) {
		t.Fatalf("free %s, want 15", free)
	}
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "15", "qo-1", dims())); err != nil {
		t.Fatalf("hold of the 15 free units: %v", err)
	}
	if err := journalMove(ctx, s, product, dec("-1")); problemCode(err) != domain.CodeQualityHold {
		t.Fatalf("journal issue with everything held: %v, want %s", err, domain.CodeQualityHold)
	}
}

func TestIssueClaim_APartialDeliveryLeavesTheRestOfTheLinesPickAndShipOutstanding(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	l := issueclaim.SalesLineRef(ids.New())
	pickAndShip(t, ctx, s, product, "5", l)
	if err := deliver(ctx, s, product, "d1", dec("3"), l); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "17", "0", "2")
	if err := deliver(ctx, s, product, "d2", dec("2"), l); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "15", "0", "0")
}

func TestIssueClaim_AnotherLinesDeliveryLeavesThisLinesPickAlone(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "20", "1", dims()))
	l1, l2 := issueclaim.SalesLineRef(ids.New()), issueclaim.SalesLineRef(ids.New())
	pickAndShip(t, ctx, s, product, "5", l1)
	if err := deliver(ctx, s, product, "d2", dec("4"), l2); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "16", "0", "5")
	if err := deliver(ctx, s, product, "d1", dec("5"), l1); err != nil {
		t.Fatal(err)
	}
	wantRoom(t, roomOf(t, ctx, s, product), "11", "0", "0")
}

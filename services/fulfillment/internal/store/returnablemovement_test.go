package store_test

import (
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// The movements behind a balance replay to it: post, cancel, repost, a return, and a
// correction up and down each leave one row whose balanceAfter is the running balance
// (FR-28.16). Adjustments need a returnable type and never take the balance below zero.
func TestReturnableMovements_ReplayToTheBalance(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "PALLET", TareKg: d("25"), Returnable: true, PackagingMaterial: strp("WOOD")})
	saveType(t, s, ctx, store.ContainerType{Code: "CARTON", TareKg: d("1")})
	f := released(t, s, ctx, 5)
	line := f.shipment.Lines[0]
	customer := f.shipment.CustomerID
	saveRule(t, s, ctx, store.PackingRule{ProductID: line.ProductID, ContainerType: "PALLET", QtyPerContainer: d("1"), MaxWeightKg: d("1000")})
	picked := pickAll(t, s, ctx, f.shipment)
	res, err := s.AutoPack(ctx, picked.ID, picked.Version, nil, idem("ap"))
	if err != nil || len(res.Containers) != 5 {
		t.Fatalf("AutoPack = %d containers, %v; want 5 pallets", len(res.Containers), err)
	}
	slip, err := s.PostPackingSlip(ctx, picked.ID, res.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "EUR", Costs: costs(f.shipment, "1")}, idem("post"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	sh, _ := s.GetShipment(ctx, picked.ID)
	slip2, err := s.PostPackingSlip(ctx, picked.ID, sh.Version, store.PostInput{PostingDate: today(), Currency: "EUR", Costs: costs(f.shipment, "1")}, idem("repost"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: customer, ContainerType: "PALLET", Qty: d("2")}, idem("ret")); err != nil {
		t.Fatal(err)
	}
	up, err := s.AdjustReturnable(ctx, store.AdjustInput{CustomerID: customer, ContainerType: "PALLET", Delta: d("4"), Reason: "Count at the customer", Reference: "sheet 9"}, idem("up"))
	if err != nil || !up.BalanceAfter.Equal(d("7")) || up.Kind != store.MovementAdjusted || deref(up.Reason) != "Count at the customer" || deref(up.Reference) != "sheet 9" || deref(up.Actor) != "user-test" {
		t.Fatalf("adjust +4 = %+v, %v; want ADJUSTED balance 7 by user-test", up, err)
	}
	down, err := s.AdjustReturnable(ctx, store.AdjustInput{CustomerID: customer, ContainerType: "PALLET", Delta: d("-3"), Reason: "Lost in transit"}, idem("down"))
	if err != nil || !down.BalanceAfter.Equal(d("4")) || !down.Qty.Equal(d("-3")) {
		t.Fatalf("adjust -3 = %+v, %v; want balance 4", down, err)
	}

	items, err := s.ListReturnableBalances(ctx, customer, 10)
	if err != nil || len(items) != 1 || !items[0].Balance.Equal(d("4")) {
		t.Fatalf("balances = %+v, %v; want 4", items, err)
	}
	moves, err := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{store.MovementAdjusted, store.MovementAdjusted, store.MovementReturned, store.MovementShipped, store.MovementShipmentCancelled, store.MovementShipped}
	if len(moves) != len(wantKinds) {
		t.Fatalf("%d movements, want %d: %+v", len(moves), len(wantKinds), moves)
	}
	for i, k := range wantKinds {
		if moves[i].Kind != k {
			t.Fatalf("movement %d kind = %s, want %s (newest first)", i, moves[i].Kind, k)
		}
	}
	// Oldest first, every balanceAfter is the sum of the signed quantities so far.
	running := d("0")
	for i := len(moves) - 1; i >= 0; i-- {
		running = running.Add(moves[i].Qty)
		if !moves[i].BalanceAfter.Equal(running) {
			t.Fatalf("movement %d balanceAfter = %s, want running sum %s", i, moves[i].BalanceAfter, running)
		}
	}
	if !running.Equal(items[0].Balance) {
		t.Fatalf("replayed balance %s, stored %s", running, items[0].Balance)
	}
	// A slip movement names its slip.
	if deref(moves[3].Reference) != slip2.ID || deref(moves[4].Reference) != slip.ID {
		t.Fatalf("slip references = %v / %v, want %s / %s", moves[3].Reference, moves[4].Reference, slip2.ID, slip.ID)
	}
	if got, _ := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, Kind: store.MovementAdjusted, Limit: 50}); len(got) != 2 {
		t.Fatalf("ADJUSTED filter = %d rows, want 2", len(got))
	}
	if got, _ := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, ContainerType: "CARTON", Limit: 50}); len(got) != 0 {
		t.Fatalf("CARTON filter = %d rows, want 0", len(got))
	}
	if got, _ := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, Limit: 2}); len(got) != 2 {
		t.Fatalf("limit 2 = %d rows", len(got))
	}
	if got, _ := s.ListReturnableMovements(ctxFor(2, ""), store.MovementFilter{Limit: 50}); len(got) != 0 {
		t.Fatalf("tenant 2 sees %d movements", len(got))
	}

	// The events carry the movement.
	if got := outboxData(t, admin, fulevents.ReturnableAdjusted, "{qty}"); got != "-3" {
		t.Fatalf("adjusted event qty = %q, want -3", got)
	}
	if got := outboxData(t, admin, fulevents.ReturnableAdjusted, "{reason}"); got != "Lost in transit" {
		t.Fatalf("adjusted event reason = %q", got)
	}
	if got := outboxData(t, admin, fulevents.ReturnableReturned, "{balance_after}"); got != "3" {
		t.Fatalf("returned event balance_after = %q, want 3", got)
	}

	// Refusals write nothing.
	if _, err := s.AdjustReturnable(ctx, store.AdjustInput{CustomerID: customer, ContainerType: "PALLET", Delta: d("-5"), Reason: "Too many"}, idem("neg")); problemCode(err) != store.CodeRTPNegative {
		t.Fatalf("adjust below zero = %v, want FUL-0015", err)
	}
	for _, code := range []string{"CARTON", "NOPE"} {
		if _, err := s.AdjustReturnable(ctx, store.AdjustInput{CustomerID: customer, ContainerType: code, Delta: d("1"), Reason: "Odd"}, idem("nr")); problemCode(err) != store.CodeNotReturnable {
			t.Fatalf("adjust %s = %v, want FUL-0069", code, err)
		}
	}
	if got, _ := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, Limit: 50}); len(got) != len(wantKinds) {
		t.Fatalf("refused adjustments left %d movements, want %d", len(got), len(wantKinds))
	}
}

// A first adjustment creates the balance (an opening balance for a customer with no
// shipments) and a replayed key moves nothing twice.
func TestAdjustReturnable_OpeningBalanceAndReplay(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "CRATE", TareKg: d("2"), Returnable: true})
	customer := ids.New()
	key := idem("open")
	in := store.AdjustInput{CustomerID: customer, ContainerType: "CRATE", Delta: d("12"), Reason: "Opening balance from the legacy system"}
	first, err := s.AdjustReturnable(ctx, in, key)
	if err != nil || !first.BalanceAfter.Equal(d("12")) {
		t.Fatalf("opening balance = %+v, %v; want 12", first, err)
	}
	second, err := s.AdjustReturnable(ctx, in, key)
	if err != nil || second.ID != first.ID {
		t.Fatalf("replay = %+v, %v; want the stored movement %s", second, err, first.ID)
	}
	items, _ := s.ListReturnableBalances(ctx, customer, 10)
	if len(items) != 1 || !items[0].Balance.Equal(d("12")) {
		t.Fatalf("balances = %+v, want one of 12", items)
	}
	if got, _ := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, Limit: 10}); len(got) != 1 {
		t.Fatalf("replay wrote %d movements, want 1", len(got))
	}
	// An opening balance can be returned against and cannot be returned beyond.
	if b, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: customer, ContainerType: "CRATE", Qty: d("5")}, idem("r5")); err != nil || !b.Balance.Equal(d("7")) {
		t.Fatalf("return 5 = %+v, %v; want 7", b, err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Overlapping corrections and return receipts of one balance each wait for the row lock,
// so the history, oldest first, still replays to the balance step by step: every
// balanceAfter is the running sum of the signed quantities, including when the first
// corrections race to create the balance row.
func TestReturnableMovements_ReplayUnderConcurrency(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "CRATE", TareKg: d("2"), Returnable: true})
	customer := ids.New()
	run := func(jobs []func() error) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, len(jobs))
		for _, job := range jobs {
			wg.Add(1)
			go func(job func() error) {
				defer wg.Done()
				errs <- job()
			}(job)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	adjust := func(delta string) func() error {
		return func() error {
			_, err := s.AdjustReturnable(ctx, store.AdjustInput{CustomerID: customer, ContainerType: "CRATE", Delta: d(delta), Reason: "Count"}, idem("c"))
			return err
		}
	}
	ret := func() error {
		_, err := s.ReturnPackaging(ctx, store.ReturnInput{CustomerID: customer, ContainerType: "CRATE", Qty: d("1")}, idem("r"))
		return err
	}
	// No balance row yet: eight opening corrections race to create it.
	var first []func() error
	for range 8 {
		first = append(first, adjust("2"))
	}
	run(first)
	var mixed []func() error
	for range 6 {
		mixed = append(mixed, adjust("-1"), adjust("1"), ret)
	}
	run(mixed)

	items, err := s.ListReturnableBalances(ctx, customer, 10)
	if err != nil || len(items) != 1 || !items[0].Balance.Equal(d("10")) {
		t.Fatalf("balances = %+v, %v; want 16 - 6 + 6 - 6 = 10", items, err)
	}
	moves, err := s.ListReturnableMovements(ctx, store.MovementFilter{CustomerID: customer, Limit: 100})
	if err != nil || len(moves) != 26 {
		t.Fatalf("%d movements, %v; want 26", len(moves), err)
	}
	running := d("0")
	for i := len(moves) - 1; i >= 0; i-- {
		running = running.Add(moves[i].Qty)
		if !moves[i].BalanceAfter.Equal(running) {
			t.Fatalf("movement %d (%s %s) balanceAfter = %s, want running sum %s", len(moves)-1-i, moves[i].Kind, moves[i].Qty, moves[i].BalanceAfter, running)
		}
	}
	if !running.Equal(items[0].Balance) {
		t.Fatalf("replayed balance %s, stored %s", running, items[0].Balance)
	}
}

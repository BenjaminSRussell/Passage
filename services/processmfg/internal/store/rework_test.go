package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/domain"
	pmfevents "github.com/BenjaminSRussell/passage/services/processmfg/internal/events"
	"github.com/BenjaminSRussell/passage/services/processmfg/internal/store"
)

// reworkFormula is a 100 kg rework formula that makes item.
func reworkFormula(t *testing.T, s *store.Postgres, item, code string) store.Formula {
	t.Helper()
	f, _, err := s.CreateFormula(ctxFor(1, ""), store.Formula{Code: code, Name: "Rework", ItemID: item, Size: dec("100"), Unit: "kg", Lines: []store.FormulaLine{
		{LineNumber: 1, ItemID: ids.New(), Type: domain.LineIngredient, Qty: dec("20"), Scalable: true},
	}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func newRework(f store.Formula, b store.Batch, keep bool) store.NewRework {
	p := domain.DefaultReworkParams()
	p.KeepBatchNumber = keep
	return store.NewRework{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("80"), SourceBatchID: b.ID, SourceVersion: b.Version,
		SourceQty: dec("100"), InputQty: dec("80"), OutputQty: dec("78"), Params: p}
}

// finish runs the rework's batch order to reported, with the quantity out.
func finish(t *testing.T, s *store.Postgres, r store.ReworkOrder, qty string) {
	t.Helper()
	o, err := s.GetBatchOrder(ctxFor(1, ""), r.BatchOrderID)
	if err != nil {
		t.Fatal(err)
	}
	o = advance(t, s, o, domain.CmdRelease)
	o = advance(t, s, o, domain.CmdStart)
	if _, _, err := s.ReportBatchOrder(ctxFor(1, ""), o.ID, store.ReportInput{MainQty: dec(qty)}, o.Version, store.Idem{Key: "rep" + o.ID, Fingerprint: "r"}); err != nil {
		t.Fatal(err)
	}
}

// AC-24.18: a rework that keeps the batch number puts the batch back on the
// quarantine disposition once the order reports its output; nothing earlier.
func TestRework_KeptNumberReturnsToQuarantine(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	b := register(t, s, item, "L-1")
	f := reworkFormula(t, s, item, "RW-1")

	r, created, err := s.CreateReworkOrder(ctx, newRework(f, b, true), store.Idem{Key: "rw1", Fingerprint: "1"})
	if err != nil || !created || r.Status != "planned" || r.Version != 1 || r.OutputDisposition != "quarantine" || !r.QualityOrderRequired ||
		r.OrderStatus != "planned" || r.SourceBatchNumber != "L-1" || !r.InputQty.Equal(dec("80")) || r.VarianceType != "rework" {
		t.Fatalf("create created=%v %+v err=%v", created, r, err)
	}
	if n := len(outboxEvents(t, admin, pmfevents.ReworkOrderCreated)); n != 1 || len(outboxEvents(t, admin, pmfevents.BatchOrderCreated)) != 1 {
		t.Fatalf("created events %d", n)
	}
	// The rework takes stock out of the batch, so a change based on the version
	// read before it is stale.
	if _, _, err := s.ChangeDisposition(ctx, b.ID, "on-hold", "late", b.Version, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("disposition change on the version before the rework: %v", err)
	}

	// Replays; a second rework of the same batch is refused while this one is open.
	again, created, err := s.CreateReworkOrder(ctx, newRework(f, b, true), store.Idem{Key: "rw1", Fingerprint: "1"})
	if err != nil || created || again.ID != r.ID || len(outboxEvents(t, admin, pmfevents.ReworkOrderCreated)) != 1 {
		t.Fatalf("replay created=%v err=%v", created, err)
	}
	cur, err := s.GetBatch(ctx, b.ID)
	if err != nil || cur.Version != b.Version+1 {
		t.Fatalf("the rework moves the batch version: %+v %v", cur, err)
	}
	if _, _, err := s.CreateReworkOrder(ctx, newRework(f, cur, true), store.Idem{Key: "rw2", Fingerprint: "2"}); problemCode(err) != store.CodeReworkOpen {
		t.Fatalf("second open rework: %v", err)
	}

	// The output cannot come back before the order is reported as finished.
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "", r.Version, false, store.Idem{Key: "rc0", Fingerprint: "0"}); problemCode(err) != store.CodeReworkNotReported {
		t.Fatalf("receive before report: %v", err)
	}
	finish(t, s, r, "78")
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "L-2", r.Version, false, store.Idem{Key: "rc1", Fingerprint: "1"}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("number given though kept: %v", err)
	}
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "", r.Version+4, false, store.Idem{Key: "rc2", Fingerprint: "2"}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}
	got, changed, err := s.ReceiveReworkOrder(ctx, r.ID, "", r.Version, false, store.Idem{Key: "rc3", Fingerprint: "3"})
	if err != nil || !changed || got.Status != "received" || got.Version != 2 || got.OutputBatchID == nil || *got.OutputBatchID != b.ID ||
		got.ReceivedQty == nil || !got.ReceivedQty.Equal(dec("78")) || got.OrderStatus != "reported" {
		t.Fatalf("receive changed=%v %+v err=%v", changed, got, err)
	}
	after, err := s.GetBatch(ctx, b.ID)
	if err != nil || after.Disposition != "quarantine" || after.Version != b.Version+2 || len(after.Dispositions) != 1 {
		t.Fatalf("batch after %+v err=%v", after, err)
	}
	var reason string
	if err := admin.QueryRow(context.Background(), `select reason from batch_disposition_history where batch_id = $1`, b.ID).Scan(&reason); err != nil || reason != "Rework "+r.OrderNumber {
		t.Fatalf("reason %q %v", reason, err)
	}
	var ev pmfevents.ReworkOrderReceivedData
	evts := outboxEvents(t, admin, pmfevents.ReworkOrderReceived)
	if len(evts) != 1 || json.Unmarshal(evts[0].Data, &ev) != nil || ev.Qty != "78" || ev.OutputBatchID != b.ID || ev.Disposition != "quarantine" || !ev.KeepBatchNumber {
		t.Fatalf("received event %d %+v", len(evts), ev)
	}
	// AC-24.18: a kept number links no other batch, the genealogy shows the rework order.
	if g, err := s.GetGenealogy(ctx, b.ID); err != nil || len(g.Parents) != 0 || len(g.Children) != 0 ||
		len(g.Reworks) != 1 || g.Reworks[0].ID != r.ID || g.Reworks[0].Status != "received" || g.Reworks[0].OrderNumber != r.OrderNumber {
		t.Fatalf("genealogy of a kept number: %+v %v", g, err)
	}

	// Received once; a replay of the receive returns the first answer.
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "", got.Version, false, store.Idem{Key: "rc4", Fingerprint: "4"}); problemCode(err) != store.CodeReworkReceived {
		t.Fatalf("second receive: %v", err)
	}
	rep, changed, err := s.ReceiveReworkOrder(ctx, r.ID, "", r.Version, false, store.Idem{Key: "rc3", Fingerprint: "3"})
	if err != nil || changed || rep.ID != r.ID || len(outboxEvents(t, admin, pmfevents.ReworkOrderReceived)) != 1 {
		t.Fatalf("receive replay changed=%v err=%v", changed, err)
	}
	// With the first rework received, the batch can be reworked again.
	next := newRework(f, after, true)
	if _, _, err := s.CreateReworkOrder(ctx, next, store.Idem{Key: "rw3", Fingerprint: "3"}); err != nil {
		t.Fatalf("rework again: %v", err)
	}
}

// A rework that does not keep the number registers a new batch with a genealogy
// link to the source; a number already in use refuses and leaves the rework open.
func TestRework_NewNumberLinksGenealogy(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	b := register(t, s, item, "L-1")
	register(t, s, item, "L-TAKEN")
	f := reworkFormula(t, s, item, "RW-1")
	in := newRework(f, b, false)
	in.Params.RequireQualityOrder = false
	in.Params.OutputDisposition = "available"
	in.Params.Capitalize = true
	r, _, err := s.CreateReworkOrder(ctx, in, store.Idem{Key: "rw1", Fingerprint: "1"})
	if err != nil || r.VarianceType != "capitalized" || r.OutputDisposition != "available" || r.QualityOrderRequired {
		t.Fatalf("create %+v err=%v", r, err)
	}
	finish(t, s, r, "75")
	// An output planned as available releases the batch: the disposition duty is needed.
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "L-1R", r.Version, false, store.Idem{Key: "rn", Fingerprint: "n"}); problemCode(err) != "AUTH-0003" {
		t.Fatalf("release without the disposition duty: %v", err)
	}
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "", r.Version, true, store.Idem{}); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("number missing: %v", err)
	}
	if _, _, err := s.ReceiveReworkOrder(ctx, r.ID, "L-TAKEN", r.Version, true, store.Idem{}); problemCode(err) != store.CodeBatchDuplicate {
		t.Fatalf("number taken: %v", err)
	}
	if open, err := s.GetReworkOrder(ctx, r.ID); err != nil || open.Status != "planned" || open.Version != 1 {
		t.Fatalf("still open: %+v %v", open, err)
	}
	got, _, err := s.ReceiveReworkOrder(ctx, r.ID, "L-1R", r.Version, true, store.Idem{Key: "rc", Fingerprint: "c"})
	if err != nil || got.OutputBatchID == nil || *got.OutputBatchID == b.ID || got.OutputBatchNumber == nil || *got.OutputBatchNumber != "L-1R" {
		t.Fatalf("receive %+v err=%v", got, err)
	}
	nb, err := s.GetBatch(ctx, *got.OutputBatchID)
	if err != nil || nb.Disposition != "available" || nb.BatchOrderID == nil || *nb.BatchOrderID != r.BatchOrderID {
		t.Fatalf("new batch %+v err=%v", nb, err)
	}
	g, err := s.GetGenealogy(ctx, nb.ID)
	if err != nil || len(g.Parents) != 1 || g.Parents[0].BatchID != b.ID || g.Parents[0].Relation != "rework" || !g.Parents[0].Qty.Equal(dec("80")) ||
		len(g.Reworks) != 1 || g.Reworks[0].ID != r.ID {
		t.Fatalf("genealogy %+v err=%v", g, err)
	}
	if g, err := s.GetGenealogy(ctx, b.ID); err != nil || len(g.Children) != 1 || g.Children[0].BatchID != nb.ID || len(g.Reworks) != 1 {
		t.Fatalf("source genealogy %+v err=%v", g, err)
	}
	// The source keeps its disposition; only the create moved its version.
	if src, err := s.GetBatch(ctx, b.ID); err != nil || src.Version != b.Version+1 || src.Disposition != "available" {
		t.Fatalf("source untouched %+v err=%v", src, err)
	}
	var ev pmfevents.ReworkOrderReceivedData
	evts := outboxEvents(t, admin, pmfevents.ReworkOrderReceived)
	if len(evts) != 1 || json.Unmarshal(evts[0].Data, &ev) != nil || ev.BatchNumber != "L-1R" || ev.Qty != "75" || ev.KeepBatchNumber || ev.VarianceType != "capitalized" {
		t.Fatalf("event %d %+v", len(evts), ev)
	}
}

// Refusals write nothing: no batch order, no rework, no event.
func TestRework_RefusalsWriteNothing(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	b := register(t, s, item, "L-1")
	f := reworkFormula(t, s, item, "RW-1")
	other := reworkFormula(t, s, ids.New(), "RW-X")

	tooMuch := newRework(f, b, true)
	tooMuch.InputQty = dec("100.000001")
	stale := newRework(f, b, true)
	stale.SourceVersion = 9
	wrongItem := newRework(other, b, true)
	available := newRework(f, b, true)
	available.Params.OutputDisposition = "available"
	foreign := newRework(f, b, true)
	foreign.LegalEntityID = entityB
	for name, tc := range map[string]struct {
		in   store.NewRework
		code string
	}{
		"more than the batch":              {tooMuch, domain.CodeInvalid},
		"stale batch version":              {stale, store.CodeVersionConflict},
		"formula of another item":          {wrongItem, domain.CodeInvalid},
		"available behind a quality order": {available, domain.CodeInvalid},
		"batch of another legal entity":    {foreign, domain.CodeInvalid},
	} {
		if _, _, err := s.CreateReworkOrder(ctx, tc.in, store.Idem{Key: "k-" + name, Fingerprint: name}); problemCode(err) != tc.code {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, q := range []string{`select count(*) from rework_order`, `select count(*) from batch_order`, `select count(*) from outbox where type = '` + pmfevents.ReworkOrderCreated + `'`,
		`select count(*) from idempotency_keys where key like 'k-%'`} {
		var n int
		if err := admin.QueryRow(context.Background(), q).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s = %d %v", q, n, err)
		}
	}
	if src, err := s.GetBatch(ctx, b.ID); err != nil || src.Version != b.Version {
		t.Errorf("a refused rework moved the batch version: %+v %v", src, err)
	}
	if _, _, err := s.CreateReworkOrder(ctx, store.NewRework{LegalEntityID: entityA, FormulaID: f.ID, BatchSize: dec("80"), SourceBatchID: ids.New(), SourceVersion: 1,
		SourceQty: dec("100"), InputQty: dec("80"), OutputQty: dec("78"), Params: domain.DefaultReworkParams()}, store.Idem{}); problemCode(err) != store.CodeBatchNotFound {
		t.Errorf("unknown batch: %v", err)
	}
}

// Another tenant sees none of it.
func TestRework_TenantIsolation(t *testing.T) {
	s, _ := setup(t)
	item := ids.New()
	b := register(t, s, item, "L-1")
	r, _, err := s.CreateReworkOrder(ctxFor(1, ""), newRework(reworkFormula(t, s, item, "RW-1"), b, true), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetReworkOrder(ctxFor(2, ""), r.ID); problemCode(err) != store.CodeReworkNotFound {
		t.Fatalf("other tenant read: %v", err)
	}
	list, err := s.ListReworkOrders(ctxFor(2, ""), store.ReworkFilter{Limit: 10})
	if err != nil || len(list) != 0 {
		t.Fatalf("other tenant list %v %v", list, err)
	}
	mine, err := s.ListReworkOrders(ctxFor(1, ""), store.ReworkFilter{Status: "planned", SourceBatchID: b.ID, Limit: 10})
	if err != nil || len(mine) != 1 || mine[0].ID != r.ID {
		t.Fatalf("list %v %v", mine, err)
	}
}

// Two creates of the same batch and two receives of the same rework at once:
// the batch and rework rows are locked and re-checked inside the transaction,
// so exactly one of each wins and the other is refused with a problem.
func TestRework_ConcurrentCreatesAndReceives(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	item := ids.New()
	b := register(t, s, item, "L-1")
	f := reworkFormula(t, s, item, "RW-1")

	race := func(run func(i int) error) []error {
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				errs[i] = run(i)
			}(i)
		}
		wg.Wait()
		return errs
	}
	oneWins := func(what string, errs []error, codes ...string) {
		t.Helper()
		won := 0
		for _, err := range errs {
			if err == nil {
				won++
				continue
			}
			ok := false
			for _, c := range codes {
				ok = ok || problemCode(err) == c
			}
			if !ok {
				t.Fatalf("%s: unexpected refusal %v", what, err)
			}
		}
		if won != 1 {
			t.Fatalf("%s: %d won, errors %v", what, won, errs)
		}
	}

	made := make([]string, 2)
	errs := race(func(i int) error {
		r, _, err := s.CreateReworkOrder(ctx, newRework(f, b, true), store.Idem{Key: fmt.Sprintf("c%d", i), Fingerprint: fmt.Sprint(i)})
		made[i] = r.ID
		return err
	})
	oneWins("create", errs, store.CodeVersionConflict, store.CodeReworkOpen)
	id := made[0]
	if errs[0] != nil {
		id = made[1]
	}
	r, err := s.GetReworkOrder(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	finish(t, s, r, "78")
	errs = race(func(i int) error {
		_, _, err := s.ReceiveReworkOrder(ctx, r.ID, "", r.Version, false, store.Idem{Key: fmt.Sprintf("r%d", i), Fingerprint: fmt.Sprint(i)})
		return err
	})
	oneWins("receive", errs, store.CodeVersionConflict, store.CodeReworkReceived)
	for q, want := range map[string]int{
		`select count(*) from rework_order`:                                                1,
		`select count(*) from outbox where type = '` + pmfevents.ReworkOrderReceived + `'`: 1,
		`select count(*) from batch_disposition_history`:                                   1,
	} {
		var n int
		if err := admin.QueryRow(context.Background(), q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d, want %d (%v)", q, n, want, err)
		}
	}
}

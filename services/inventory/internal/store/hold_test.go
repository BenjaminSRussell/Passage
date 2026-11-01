package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

func legalEntityOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.LegalEntityID
}

func holdInput(ctx context.Context, product, key, qty, ref string, d domain.Dimensions) store.HoldInput {
	return store.HoldInput{IdempotencyKey: key, LegalEntityID: legalEntityOf(ctx), ProductID: product, Dims: d, Qty: dec(qty),
		Reason: "QUALITY", SourceType: "QUALITY_ORDER", SourceRef: ref}
}

type stockRow struct{ physical, avail, held, reserved decimal.Decimal }

func stockByLocation(t testing.TB, ctx context.Context, s *store.Postgres, product string) map[string]stockRow {
	t.Helper()
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: product, GroupBy: []string{domain.DimLocation}})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]stockRow{}
	for _, r := range res.Rows {
		out[r.Group[domain.DimLocation]] = stockRow{physical: r.PhysicalInvent, avail: r.AvailPhysical, held: r.HeldPhysical, reserved: r.ReservedPhysical}
	}
	return out
}

func issue(ctx context.Context, s *store.Postgres, product, qty string, d domain.Dimensions) error {
	j, err := newJournal(ctx, s, domain.JournalMovement, line(product, "-"+qty, "0", d))
	if err != nil {
		return err
	}
	_, err = s.PostJournal(ctx, j.ID, j.Version, postParams())
	return err
}

func TestHoldLeavesStockOutOfAvailabilityAndIssues(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "100", "2", dims("batch", "B1")))

	res, err := s.PlaceHold(ctx, holdInput(ctx, product, "h1", "30", "qo-1", dims("batch", "B1")))
	if err != nil || len(res.Holds) != 1 || !res.Holds[0].Held().Equal(dec("30")) {
		t.Fatalf("place: %v %+v", err, res)
	}
	row := stockByLocation(t, ctx, s, product)[""]
	if !row.physical.Equal(dec("100")) || !row.avail.Equal(dec("70")) || !row.held.Equal(dec("30")) {
		t.Fatalf("physical %s avail %s held %s, want 100 / 70 / 30", row.physical, row.avail, row.held)
	}
	if err := issue(ctx, s, product, "80", dims("batch", "B1")); problemCode(err) != domain.CodeNegativePhysical {
		t.Fatalf("issuing into the held quantity: %v, want %s", err, domain.CodeNegativePhysical)
	}
	if err := issue(ctx, s, product, "70", dims("batch", "B1")); err != nil {
		t.Fatalf("issuing the available quantity: %v", err)
	}
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "h2", "1", "qo-2", dims("batch", "B1"))); problemCode(err) != domain.CodeHoldExceeds {
		t.Fatalf("holding stock that is all held already: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, res.Holds[0].ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if row = stockByLocation(t, ctx, s, product)[""]; !row.avail.Equal(dec("30")) || !row.held.IsZero() {
		t.Fatalf("after release avail %s held %s, want 30 / 0", row.avail, row.held)
	}
	counts := map[string]int{}
	rows, err := admin.Query(context.Background(), `select type, count(*) from outbox where tenant_id = $1 and type like '%hold%' group by type`, testkit.TenantID(1))
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
	if counts["dev.passage.inventory.hold.placed.v1"] != 1 || counts["dev.passage.inventory.hold.released.v1"] != 1 {
		t.Fatalf("hold events %v, want one placed and one released", counts)
	}
	if rep, err := s.RebuildOnHand(ctx, false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild after holds: %v %+v", err, rep)
	}
}

func TestHoldQuantityRulesAndPartialPlacement(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims("batch", "B1")))
	if _, err := s.PlaceHold(ctx, holdInput(ctx, product, "k1", "11", "x", dims("batch", "B1"))); problemCode(err) != domain.CodeHoldExceeds {
		t.Fatalf("over-hold: %v", err)
	}
	in := holdInput(ctx, product, "k2", "11", "x", dims("batch", "B1"))
	in.AllowPartial = true
	res, err := s.PlaceHold(ctx, in)
	if err != nil || !res.Holds[0].Qty.Equal(dec("10")) || !res.Shortfall.Equal(dec("1")) {
		t.Fatalf("partial placement: %v %+v", err, res)
	}
	replay, err := s.PlaceHold(ctx, in)
	if err != nil || !replay.Replayed || replay.Holds[0].ID != res.Holds[0].ID {
		t.Fatalf("replay: %v %+v", err, replay)
	}
	changed := in
	changed.Qty = dec("5")
	if _, err := s.PlaceHold(ctx, changed); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	for name, bad := range map[string]store.HoldInput{
		"restricted stock": holdInput(ctx, product, "k3", "1", "x", dims("batch", "B1", "inventory_status", "BLOCKED")),
		"no site":          holdInput(ctx, product, "k4", "1", "x", domain.Dimensions{"batch": "B1"}),
		"no stock at all":  holdInput(ctx, product, "k5", "1", "x", dims("batch", "NOPE")),
	} {
		if _, err := s.PlaceHold(ctx, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := s.ReleaseHold(ctx, res.Holds[0].ID, 1, ptr(dec("11"))); problemCode(err) != domain.CodeHoldReleaseQty {
		t.Fatalf("release above held: %v", err)
	}
	if _, err := s.ReleaseHold(ctx, res.Holds[0].ID, 7, nil); problemCode(err) != store.CodeConflictVersion {
		t.Fatalf("stale version: %v", err)
	}
	part, err := s.ReleaseHold(ctx, res.Holds[0].ID, 1, ptr(dec("4")))
	if err != nil || part.State != domain.HoldActive || !part.Held().Equal(dec("6")) || part.Version != 2 {
		t.Fatalf("partial release: %v %+v", err, part)
	}
	done, err := s.ReleaseHold(ctx, res.Holds[0].ID, 2, nil)
	if err != nil || done.State != domain.HoldReleased || done.ReleasedAt == nil {
		t.Fatalf("full release: %v %+v", err, done)
	}
	if again, err := s.ReleaseHold(ctx, res.Holds[0].ID, 2, nil); err != nil || again.Version != done.Version {
		t.Fatalf("releasing a released hold changes nothing: %v %+v", err, again)
	}
}

func ptr(d decimal.Decimal) *decimal.Decimal { return &d }

func TestWholeLotHoldTakesEveryRowOfTheLotAndNothingElse(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "60", "1", dims("batch", "B1")), line(product, "40", "1", dims("batch", "B1", "location", "L2")),
		line(product, "25", "1", dims("batch", "B2")))
	in := store.HoldInput{IdempotencyKey: "lot", LegalEntityID: legalEntityOf(ctx), ProductID: product, Dims: domain.Dimensions{"batch": "B1"}, WholeLot: true,
		Reason: "RECALL", SourceType: "QUALITY_ORDER", SourceRef: "qo-lot"}
	res, err := s.PlaceHold(ctx, in)
	if err != nil || len(res.Holds) != 2 {
		t.Fatalf("whole lot: %v %+v", err, res)
	}
	got := stockByLocation(t, ctx, s, product)
	if !got["L2"].avail.IsZero() || !got["L2"].held.Equal(dec("40")) {
		t.Fatalf("row L2 %+v", got["L2"])
	}
	if !got[""].held.Equal(dec("60")) || !got[""].avail.Equal(dec("25")) {
		t.Fatalf("the unlocated row holds B1 (60) and keeps B2 (25) available: %+v", got[""])
	}
	if replay, err := s.PlaceHold(ctx, in); err != nil || !replay.Replayed || len(replay.Holds) != 2 {
		t.Fatalf("replay: %v %+v", err, replay)
	}
	in.IdempotencyKey = "lot-again"
	if _, err := s.PlaceHold(ctx, in); problemCode(err) != domain.CodeHoldExceeds {
		t.Fatalf("a lot with nothing available: %v", err)
	}
	released, err := s.ReleaseHoldsBySource(ctx, "QUALITY_ORDER", "qo-lot", dec("50"))
	if err != nil {
		t.Fatal(err)
	}
	var total decimal.Decimal
	for _, h := range released {
		total = total.Add(h.ReleasedQty)
	}
	if !total.Equal(dec("50")) {
		t.Fatalf("keeping 50 of 100 releases 50, released %s", total)
	}
	if again, err := s.ReleaseHoldsBySource(ctx, "QUALITY_ORDER", "qo-lot", dec("50")); err != nil || len(again) != 0 {
		t.Fatalf("stating the same level again releases nothing: %v %+v", err, again)
	}
	if none, err := s.ReleaseHoldsBySource(ctx, "QUALITY_ORDER", "unknown", decimal.Zero); err != nil || len(none) != 0 {
		t.Fatalf("a source without holds: %v %+v", err, none)
	}
	if _, err := s.ReleaseHoldsBySource(ctx, "QUALITY_ORDER", "qo-lot", decimal.Zero); err != nil {
		t.Fatal(err)
	}
	for loc, r := range stockByLocation(t, ctx, s, product) {
		if !r.held.IsZero() {
			t.Errorf("row %q still holds %s", loc, r.held)
		}
	}
}

func TestHoldsAreScopedToTheLegalEntityAndTenant(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(product, "10", "1", dims()))
	res, err := s.PlaceHold(ctx, holdInput(ctx, product, "k", "5", "x", dims()))
	if err != nil {
		t.Fatal(err)
	}
	other := entityCtx(1, ids.New())
	if _, err := s.ReleaseHold(other, res.Holds[0].ID, 1, nil); problemCode(err) != domain.CodeHoldNotFound {
		t.Fatalf("another legal entity released the hold: %v", err)
	}
	if got, err := s.ListHolds(other, store.HoldFilter{}); err != nil || len(got) != 0 {
		t.Fatalf("another legal entity listed %d holds: %v", len(got), err)
	}
	if out, err := s.ReleaseHoldsBySource(entityCtx(2, legalEntityOf(ctx)), "QUALITY_ORDER", "x", decimal.Zero); err != nil || len(out) != 0 {
		t.Fatalf("another tenant released holds: %v %+v", err, out)
	}
	foreign := holdInput(other, product, "k2", "1", "x", dims())
	foreign.LegalEntityID = legalEntityOf(ctx)
	if _, err := s.PlaceHold(other, foreign); err == nil {
		t.Fatal("a hold was placed on a legal entity outside the caller's scope")
	}
	if got, err := s.ListHolds(ctx, store.HoldFilter{SourceType: "QUALITY_ORDER", SourceRef: "x", State: domain.HoldActive}); err != nil || len(got) != 1 {
		t.Fatalf("listing by source: %v %d", err, len(got))
	}
}

func TestHoldProperty_AvailablePlusHeldIsOnHandAndNeverNegative(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	le := legalEntityOf(ctx)
	rowDims := []domain.Dimensions{dims("batch", "B1"), dims("batch", "B1", "location", "L2")}
	rowLocation := []string{"", "L2"}
	sources := []string{"s1", "s2"}
	rapid.Check(t, func(rt *rapid.T) {
		product := ids.New()
		post(t, ctx, s, domain.JournalOpeningBalance, line(product, "60", "1", rowDims[0]), line(product, "40", "1", rowDims[1]))
		physical := []decimal.Decimal{dec("60"), dec("40")}
		seq := 0

		check := func(step string) []domain.Hold {
			rows := stockByLocation(t, ctx, s, product)
			active, err := s.ListHolds(ctx, store.HoldFilter{ProductID: product, State: domain.HoldActive})
			if err != nil {
				rt.Fatalf("%s: list holds: %v", step, err)
			}
			heldBySet := map[string]decimal.Decimal{}
			for _, h := range active {
				heldBySet[h.Dims[domain.DimLocation]] = heldBySet[h.Dims[domain.DimLocation]].Add(h.Held())
			}
			for i, loc := range rowLocation {
				r := rows[loc]
				if r.avail.Sign() < 0 {
					rt.Fatalf("%s: row %q available %s is negative", step, loc, r.avail)
				}
				if !r.avail.Add(r.held).Add(r.reserved).Equal(r.physical) {
					rt.Fatalf("%s: row %q available %s + held %s + reserved %s != on-hand %s", step, loc, r.avail, r.held, r.reserved, r.physical)
				}
				if !r.held.Equal(heldBySet[loc]) {
					rt.Fatalf("%s: row %q held bucket %s != active holds %s", step, loc, r.held, heldBySet[loc])
				}
				if !r.physical.Equal(physical[i]) {
					rt.Fatalf("%s: row %q on-hand %s, model %s", step, loc, r.physical, physical[i])
				}
			}
			return active
		}

		steps := rapid.IntRange(1, 25).Draw(rt, "steps")
		for i := 0; i < steps; i++ {
			row := rapid.IntRange(0, 1).Draw(rt, "row")
			qty := rapid.IntRange(1, 70).Draw(rt, "qty")
			avail := stockByLocation(t, ctx, s, product)[rowLocation[row]].avail
			switch op := rapid.IntRange(0, 4).Draw(rt, "op"); op {
			case 0:
				partial := rapid.Bool().Draw(rt, "partial")
				in := store.HoldInput{IdempotencyKey: fmt.Sprintf("%s-%d", product, seq), LegalEntityID: le, ProductID: product, Dims: rowDims[row],
					Qty: decimal.NewFromInt(int64(qty)), AllowPartial: partial, Reason: "QUALITY", SourceType: "QUALITY_ORDER", SourceRef: sources[rapid.IntRange(0, 1).Draw(rt, "src")]}
				seq++
				_, err := s.PlaceHold(ctx, in)
				switch fits := avail.GreaterThanOrEqual(in.Qty); {
				case fits && err != nil, !fits && partial && avail.Sign() > 0 && err != nil:
					rt.Fatalf("place %d on %s available: %v", qty, avail, err)
				case !fits && !(partial && avail.Sign() > 0) && problemCode(err) != domain.CodeHoldExceeds:
					rt.Fatalf("place %d on %s available: %v, want %s", qty, avail, err, domain.CodeHoldExceeds)
				}
			case 1:
				in := store.HoldInput{IdempotencyKey: fmt.Sprintf("%s-%d", product, seq), LegalEntityID: le, ProductID: product, Dims: domain.Dimensions{"batch": "B1"},
					WholeLot: true, Reason: "RECALL", SourceType: "QUALITY_ORDER", SourceRef: sources[rapid.IntRange(0, 1).Draw(rt, "src")]}
				seq++
				others := stockByLocation(t, ctx, s, product)
				_, err := s.PlaceHold(ctx, in)
				if anything := others[""].avail.Sign() > 0 || others["L2"].avail.Sign() > 0; anything != (err == nil) {
					rt.Fatalf("whole lot with available %s / %s: %v", others[""].avail, others["L2"].avail, err)
				}
			case 2:
				active := check("before release")
				if len(active) == 0 {
					break
				}
				h := active[rapid.IntRange(0, len(active)-1).Draw(rt, "hold")]
				var part *decimal.Decimal
				if rapid.Bool().Draw(rt, "partial") {
					q := decimal.NewFromInt(int64(rapid.IntRange(1, int(h.Held().IntPart())).Draw(rt, "release")))
					part = &q
				}
				if _, err := s.ReleaseHold(ctx, h.ID, h.Version, part); err != nil {
					rt.Fatalf("release %s: %v", h.ID, err)
				}
			case 3:
				ref := sources[rapid.IntRange(0, 1).Draw(rt, "src")]
				keep := decimal.NewFromInt(int64(rapid.IntRange(0, 50).Draw(rt, "keep")))
				if _, err := s.ReleaseHoldsBySource(ctx, "QUALITY_ORDER", ref, keep); err != nil {
					rt.Fatalf("release source %s keep %s: %v", ref, keep, err)
				}
				left, _ := s.ListHolds(ctx, store.HoldFilter{ProductID: product, SourceType: "QUALITY_ORDER", SourceRef: ref, State: domain.HoldActive})
				var held decimal.Decimal
				for _, h := range left {
					held = held.Add(h.Held())
				}
				if held.GreaterThan(keep) {
					rt.Fatalf("source %s keeps %s after keep %s", ref, held, keep)
				}
			default:
				err := issue(ctx, s, product, fmt.Sprint(qty), rowDims[row])
				if want := avail.GreaterThanOrEqual(decimal.NewFromInt(int64(qty))); want != (err == nil) {
					rt.Fatalf("issue %d from %s available: %v", qty, avail, err)
				} else if err == nil {
					physical[row] = physical[row].Sub(decimal.NewFromInt(int64(qty)))
				} else if problemCode(err) != domain.CodeNegativePhysical {
					rt.Fatalf("issue %d refused with %v, want %s", qty, err, domain.CodeNegativePhysical)
				}
			}
			check(fmt.Sprintf("step %d", i))
		}
	})
	if rep, err := s.RebuildOnHand(ctx, false); err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild after the random sequences: %v %+v", err, rep)
	}
}

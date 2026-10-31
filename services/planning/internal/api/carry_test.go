package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// FirmedSupply serves firmedSupply for plan MAIN at siteID.
func (f *fakeStore) FirmedSupply(_ context.Context, code, site string, from time.Time) ([]store.FirmedSupply, error) {
	f.lastFirmedFrom = from
	if f.firmedErr != nil {
		return nil, f.firmedErr
	}
	if code != "MAIN" || site != siteID {
		return []store.FirmedSupply{}, nil
	}
	return f.firmedSupply, nil
}

const (
	firmedOrderID = "00000000-0000-0000-0000-0000000000f1"
	otherItemID   = "00000000-0000-0000-0000-0000000000ff"
)

// firmedFG is one firmed order of qty for the finished good, delivering on
// 2026-11-30 (day 28 of the run), and one of an item outside the snapshot.
func firmedFG(qty string) []store.FirmedSupply {
	day := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	return []store.FirmedSupply{
		{OrderID: firmedOrderID, ItemID: fgID, OrderType: "production", Qty: decimal.RequireFromString(qty), DeliveryDate: day},
		{OrderID: "00000000-0000-0000-0000-0000000000f2", ItemID: otherItemID, OrderType: "purchase", Qty: decimal.RequireFromString("5"), DeliveryDate: day},
	}
}

var carryBody = strings.Replace(runBody, `"items"`, `"carryForward":true,"items"`, 1)

// A firmed order is supply of the next run (FR-22.1): carrying it forward
// covers the demand (120 + 10 safety - 20 on hand = 110), pegs the sales line
// to the firmed order, and the run plans no second order. The order of an
// item outside the snapshot is not planned.
func TestRun_CarryForwardCountsFirmedOrdersAsSupply(t *testing.T) {
	st := &fakeStore{firmedSupply: firmedFG("110")}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", carryBody, "planning.run", true, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if n := len(st.last.Result.Orders); n != 0 {
		t.Fatalf("orders = %+v; the firmed 110 covers the demand", st.last.Result.Orders)
	}
	if st.lastFirmedFrom.Format("2006-01-02") != "2026-11-02" {
		t.Fatalf("firmed supply read from %v; want the plan date", st.lastFirmedFrom)
	}
	pegged := decimal.Zero
	for _, p := range st.last.Result.Pegging {
		if p.ItemID == otherItemID {
			t.Fatalf("an item outside the snapshot was planned: %+v", p)
		}
		if p.SupplyRef == "FP:"+firmedOrderID && p.DemandRef == "SO-1" {
			pegged = pegged.Add(p.Qty)
		}
	}
	if !pegged.IsPositive() {
		t.Fatalf("SO-1 is not pegged to the firmed order: %+v", st.last.Result.Pegging)
	}
	if st.last.ItemsPlanned != 1 {
		t.Fatalf("items planned %d", st.last.ItemsPlanned)
	}
}

// A firmed order that covers part of the demand leaves only the rest to plan.
func TestRun_CarryForwardPlansOnlyTheShortfall(t *testing.T) {
	st := &fakeStore{firmedSupply: firmedFG("60")}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", carryBody, "planning.run", true, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	if o := st.last.Result.Orders; len(o) != 1 || !o[0].Qty.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("orders = %+v; want one order of 110 - 60 = 50", o)
	}
}

// Without the flag the run is unchanged and does not read the firmed orders.
func TestRun_WithoutCarryForwardPlansAgain(t *testing.T) {
	st := &fakeStore{firmedSupply: firmedFG("110")}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d", rec.Code)
	}
	if n := len(st.last.Result.Orders); n != 1 || !st.lastFirmedFrom.IsZero() {
		t.Fatalf("orders %d, firmed read at %v", n, st.lastFirmedFrom)
	}
}

// The flag is part of the idempotency fingerprint: the same key with and
// without it is a reuse of the key, not a replay.
func TestRun_CarryForwardIsPartOfTheFingerprint(t *testing.T) {
	st := &fakeStore{firmedSupply: firmedFG("110")}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", runBody, "planning.run", true, "k1"); rec.Code != http.StatusCreated {
		t.Fatalf("first: %d", rec.Code)
	}
	first := st.last.InputHash
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", carryBody, "planning.run", true, "k1")
	if rec.Code != http.StatusUnprocessableEntity || st.last.InputHash == first {
		t.Fatalf("same key with carryForward: status %d; want 422 reuse", rec.Code)
	}
}

func TestRun_CarryForwardStoreFailureIsReported(t *testing.T) {
	st := &fakeStore{firmedErr: context.DeadlineExceeded}
	if rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", carryBody, "planning.run", true, "k1"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if st.last.PlanCode != "" {
		t.Fatal("the run was stored although the firmed supply could not be read")
	}
}

// A simulation takes the same snapshot (FR-22.15): with the flag, the
// scenario's demand of 144 is netted against the firmed 110 as well.
func TestScenarioRun_CarryForwardCountsFirmedOrders(t *testing.T) {
	st := &fakeStore{firmedSupply: firmedFG("110")}
	do(t, st, http.MethodPost, scenarioPath, upBody, dutySim, true, "")
	if rec := do(t, st, http.MethodPost, scenarioPath+"/UP20:run", carryBody, dutySim, true, "r1"); rec.Code != http.StatusCreated {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	// 144 + 10 safety - 20 on hand - 110 firmed = 24.
	if o := st.lastSim.Result.Orders; len(o) != 1 || !o[0].Qty.Equal(decimal.NewFromInt(24)) {
		t.Fatalf("orders = %+v; want one order of 24", o)
	}
	for _, p := range st.lastSim.Result.Pegging {
		if p.SupplyRef == "FP:"+firmedOrderID {
			return
		}
	}
	t.Fatalf("no pegging to the firmed order: %+v", st.lastSim.Result.Pegging)
}

// addFirmedSupply places the receipt on its delivery day relative to asOf.
func TestAddFirmedSupply_DayAndRefType(t *testing.T) {
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	items := []domain.Item{{ID: fgID}}
	addFirmedSupply(items, asOf, firmedFG("7"))
	r := items[0].Receipts
	if len(r) != 1 || r[0].Date != 28 || r[0].RefType != domain.RefPlanned || r[0].Ref != "FP:"+firmedOrderID || !r[0].Qty.Equal(decimal.NewFromInt(7)) {
		t.Fatalf("receipts = %+v", r)
	}
}

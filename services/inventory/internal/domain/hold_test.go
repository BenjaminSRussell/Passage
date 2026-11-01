package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func holdRequest(mutate func(*HoldRequest)) HoldRequest {
	r := HoldRequest{ProductID: "p1", Dims: Dimensions{DimSite: "S1", DimBatch: "B1"}, Qty: decimal.NewFromInt(5), Reason: "QUALITY", SourceType: "QUALITY_ORDER", SourceRef: "o1"}
	mutate(&r)
	return r
}

func TestValidateHoldRequest(t *testing.T) {
	cases := map[string]struct {
		mutate func(*HoldRequest)
		code   string
	}{
		"valid":                   {func(*HoldRequest) {}, ""},
		"whole lot with batch":    {func(r *HoldRequest) { r.WholeLot, r.Qty = true, decimal.Zero }, ""},
		"whole lot without batch": {func(r *HoldRequest) { r.WholeLot = true; delete(r.Dims, DimBatch) }, CodeInvalid},
		"zero quantity":           {func(r *HoldRequest) { r.Qty = decimal.Zero }, CodeInvalid},
		"too many decimals":       {func(r *HoldRequest) { r.Qty = decimal.RequireFromString("1.0000001") }, CodeInvalid},
		"no site":                 {func(r *HoldRequest) { delete(r.Dims, DimSite) }, CodeDimensionMandatory},
		"restricted stock":        {func(r *HoldRequest) { r.Dims[DimStatus] = StockBlocked }, CodeBlockedReserve},
		"vendor-owned stock":      {func(r *HoldRequest) { r.Dims[DimOwner] = "v1" }, CodeOwnershipValue},
		"unknown reason":          {func(r *HoldRequest) { r.Reason = "BORED" }, CodeInvalid},
		"lowercase source type":   {func(r *HoldRequest) { r.SourceType = "order" }, CodeInvalid},
		"empty source reference":  {func(r *HoldRequest) { r.SourceRef = "" }, CodeInvalid},
		"unknown dimension":       {func(r *HoldRequest) { r.Dims["colour"] = "x" }, CodeInvalid},
		"reserved dimension":      {func(r *HoldRequest) { r.Dims[DimLicensePlate] = "LP1" }, CodeInvalid},
		"over-long source ref":    {func(r *HoldRequest) { r.SourceRef = string(make([]byte, 61)) }, CodeInvalid},
	}
	for name, c := range cases {
		if got := domainCode(ValidateHoldRequest(holdRequest(c.mutate), 6)); got != c.code {
			t.Errorf("%s: code %q, want %q", name, got, c.code)
		}
	}
}

func TestPlanHold(t *testing.T) {
	d := decimal.NewFromInt
	for name, c := range map[string]struct {
		qty, avail     int64
		partial        bool
		held, shortage int64
		code           string
	}{
		"fits":            {5, 10, false, 5, 0, ""},
		"exactly all":     {10, 10, false, 10, 0, ""},
		"more than avail": {11, 10, false, 0, 11, CodeHoldExceeds},
		"partial":         {11, 10, true, 10, 1, ""},
		"nothing":         {1, 0, true, 0, 1, CodeHoldExceeds},
		"negative stock":  {1, -3, true, 0, 1, CodeHoldExceeds},
	} {
		held, shortfall, err := PlanHold(d(c.qty), d(c.avail), c.partial)
		if domainCode(err) != c.code || !held.Equal(d(c.held)) || !shortfall.Equal(d(c.shortage)) {
			t.Errorf("%s: held %s shortfall %s err %v", name, held, shortfall, err)
		}
	}
}

func TestHeldQuantityLeavesAvailability(t *testing.T) {
	o := OnHand{PostedQty: decimal.NewFromInt(100), ReservedPhysical: decimal.NewFromInt(10), HeldPhysical: decimal.NewFromInt(30)}
	if !o.AvailPhysical().Equal(decimal.NewFromInt(60)) || !o.PhysicalInvent().Equal(decimal.NewFromInt(100)) {
		t.Fatalf("avail %s physical %s, want 60 / 100", o.AvailPhysical(), o.PhysicalInvent())
	}
	rebuilt := o
	rebuilt.HeldPhysical = decimal.NewFromInt(20)
	if got := Diff(o, rebuilt); len(got) != 1 || got[0] != "held_physical" {
		t.Fatalf("diff %v", got)
	}
}

func TestCheckHoldRelease(t *testing.T) {
	h := Hold{ID: "h1", Qty: decimal.NewFromInt(10), ReleasedQty: decimal.NewFromInt(4), State: HoldActive}
	for name, c := range map[string]struct {
		hold Hold
		qty  int64
		code string
	}{
		"within held": {h, 6, ""}, "above held": {h, 7, CodeHoldReleaseQty}, "zero": {h, 0, CodeHoldReleaseQty},
		"released hold": {Hold{ID: "h2", Qty: h.Qty, ReleasedQty: h.Qty, State: HoldReleased}, 1, CodeHoldState},
	} {
		if got := domainCode(CheckHoldRelease(c.hold, decimal.NewFromInt(c.qty))); got != c.code {
			t.Errorf("%s: %q, want %q", name, got, c.code)
		}
	}
}

func TestHeldQuantityNeverExceedsPhysicalEvenWhenNegativeStockIsAllowed(t *testing.T) {
	before := OnHand{PostedQty: decimal.NewFromInt(10), HeldPhysical: decimal.NewFromInt(8)}
	after := before
	after.PostedQty = decimal.NewFromInt(5)
	allow := NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true}
	if after.CheckNegative(before, allow) == nil {
		t.Fatal("an issue that leaves less physical stock than is held must be refused under any policy")
	}
	after.PostedQty = decimal.NewFromInt(8)
	if err := after.CheckNegative(before, allow); err != nil {
		t.Fatalf("issuing down to the held quantity: %v", err)
	}
}

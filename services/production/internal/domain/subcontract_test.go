package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestSubcontractRequests_OnlySubcontractOperationsInRouteOrder(t *testing.T) {
	tests := []struct {
		name string
		ops  []Operation
		want []int
	}{
		{name: "no operations", ops: nil, want: []int{}},
		{name: "in-house only", ops: []Operation{{OperNumber: 10}, {OperNumber: 20}}, want: []int{}},
		{name: "one subcontract", ops: []Operation{{OperNumber: 10}, {OperNumber: 20, Subcontract: true}}, want: []int{20}},
		{name: "two subcontracts keep route order", ops: []Operation{
			{OperNumber: 10, Subcontract: true}, {OperNumber: 20}, {OperNumber: 30, Subcontract: true},
		}, want: []int{10, 30}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubcontractRequests(tt.ops)
			if got == nil || len(got) != len(tt.want) {
				t.Fatalf("got %v, want operations %v", got, tt.want)
			}
			for i, req := range got {
				if req.OperNumber != tt.want[i] {
					t.Fatalf("request %d is operation %d, want %d", i, req.OperNumber, tt.want[i])
				}
			}
		})
	}
}

// The price on the operation is per unit of process quantity: the request
// carries it unchanged and the amount is price × quantity at cost scale.
func TestSubcontractRequests_UnitPriceAndAmount(t *testing.T) {
	ops := []Operation{{OperNumber: 20, ResourceID: "vendor-res", ProcessQty: d("102.040817"), SubcontractPrice: d("1.255"), Subcontract: true}}
	got := SubcontractRequests(ops)
	if len(got) != 1 {
		t.Fatalf("got %d requests, want 1", len(got))
	}
	req := got[0]
	if req.ResourceID != "vendor-res" || !req.Qty.Equal(d("102.040817")) || !req.UnitPrice.Equal(d("1.255")) {
		t.Fatalf("request = %+v", req)
	}
	// 102.040817 × 1.255 = 128.06122533… → 128.0612
	if !req.Amount.Equal(d("128.0612")) {
		t.Fatalf("amount = %s, want 128.0612", req.Amount)
	}
}

// Property: the requests' amounts are exactly the estimate's subcontract cost rows,
// so what purchasing is asked to buy is what the order planned (FR-21.3, FR-21.10).
func TestSubcontractRequests_MatchTheEstimate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 6).Draw(t, "ops")
		ops := make([]Operation, n)
		for i := range ops {
			ops[i] = Operation{
				OperNumber:       (i + 1) * 10,
				ResourceID:       "res",
				ProcessQty:       decimal.New(int64(rapid.IntRange(1, 1_000_000).Draw(t, "qty")), -3),
				SubcontractPrice: decimal.New(int64(rapid.IntRange(0, 10_000_000).Draw(t, "price")), -4),
				Subcontract:      rapid.Bool().Draw(t, "subcontract"),
			}
		}
		planned := decimal.Zero
		for _, row := range Estimate(nil, ops) {
			if row.Group == GroupSubcontract {
				planned = planned.Add(row.Amount)
			}
		}
		requested := decimal.Zero
		for _, req := range SubcontractRequests(ops) {
			requested = requested.Add(req.Amount)
		}
		if !planned.Equal(requested) {
			t.Fatalf("estimate plans %s of subcontract, requests ask for %s", planned, requested)
		}
	})
}

func TestPlanSubcontractChanges(t *testing.T) {
	vendor, other := "v-1", "v-2"
	was := func(n int, res string, qty, price string) RequestedOp {
		return RequestedOp{OperNumber: n, ResourceID: res, Qty: decimal.RequireFromString(qty), UnitPrice: decimal.RequireFromString(price)}
	}
	now := func(n int, res string, sub bool, qty, price string) Operation {
		return Operation{OperNumber: n, ResourceID: res, Subcontract: sub, ProcessQty: decimal.RequireFromString(qty), SubcontractPrice: decimal.RequireFromString(price)}
	}
	tests := []struct {
		name       string
		before     []RequestedOp
		now        []Operation
		cancelled  bool
		due        bool
		wantAction SubcontractAction
		wantReason string
		wantNone   bool
		amount     string
	}{
		{name: "unchanged", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, true, "100", "1.25")}, wantNone: true},
		{name: "quantity", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, true, "80", "1.25")}, wantAction: SubcontractChange, amount: "100"},
		{name: "price", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, true, "100", "2")}, wantAction: SubcontractChange, amount: "200"},
		{name: "removed", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: nil, wantAction: SubcontractCancel, wantReason: CancelOpRemoved},
		{name: "turned off", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, false, "100", "1.25")}, wantAction: SubcontractCancel, wantReason: CancelSubcontractOff},
		{name: "other vendor", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, other, true, "100", "1.25")}, wantAction: SubcontractCancel, wantReason: CancelVendorChanged},
		{name: "order cancelled", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, true, "100", "1.25")}, cancelled: true, wantAction: SubcontractCancel, wantReason: CancelOrderCancelled},
		{name: "nothing requested", before: nil, now: []Operation{now(20, vendor, true, "100", "1.25")}, cancelled: true, wantNone: true},
		{name: "due date only", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, true, "100", "1.25")}, due: true, wantAction: SubcontractChange, amount: "125"},
		{name: "due date on a cancelled order", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: []Operation{now(20, vendor, true, "100", "1.25")}, cancelled: true, due: true, wantAction: SubcontractCancel, wantReason: CancelOrderCancelled},
		{name: "due date with the operation removed", before: []RequestedOp{was(20, vendor, "100", "1.25")}, now: nil, due: true, wantAction: SubcontractCancel, wantReason: CancelOpRemoved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PlanSubcontractChanges(tt.before, tt.now, tt.cancelled, tt.due)
			if tt.wantNone {
				if len(got) != 0 {
					t.Fatalf("want nothing, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Action != tt.wantAction || got[0].Reason != tt.wantReason {
				t.Fatalf("got %+v", got)
			}
			if tt.amount != "" && !got[0].Amount.Equal(decimal.RequireFromString(tt.amount)) {
				t.Fatalf("amount %s, want %s", got[0].Amount, tt.amount)
			}
		})
	}
}

// Whatever the order becomes, an operation is changed, cancelled or left alone, and an
// order that was not asked to buy anything owes purchasing nothing.
func TestPlanSubcontractChanges_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 4).Draw(t, "n")
		before := make([]RequestedOp, n)
		var now []Operation
		for i := range before {
			qty := decimal.NewFromInt(int64(rapid.IntRange(1, 50).Draw(t, "qty")))
			price := decimal.NewFromInt(int64(rapid.IntRange(1, 9).Draw(t, "price")))
			before[i] = RequestedOp{OperNumber: (i + 1) * 10, ResourceID: "v", Qty: qty, UnitPrice: price}
			if rapid.Bool().Draw(t, "kept") {
				now = append(now, Operation{OperNumber: (i + 1) * 10, ResourceID: "v", Subcontract: rapid.Bool().Draw(t, "sub"),
					ProcessQty: decimal.NewFromInt(int64(rapid.IntRange(1, 50).Draw(t, "newqty"))), SubcontractPrice: price})
			}
		}
		cancelled := rapid.Bool().Draw(t, "cancelled")
		due := rapid.Bool().Draw(t, "due")
		got := PlanSubcontractChanges(before, now, cancelled, due)
		if len(got) > len(before) {
			t.Fatalf("more plans than requests: %+v", got)
		}
		for _, pl := range got {
			if cancelled && pl.Action != SubcontractCancel {
				t.Fatalf("a cancelled order must cancel: %+v", pl)
			}
			if pl.Action == SubcontractCancel && pl.Reason == "" {
				t.Fatalf("a cancel names its reason: %+v", pl)
			}
		}
		if (cancelled || due) && len(got) != len(before) {
			t.Fatalf("a cancelled order or a moved due date reaches every request: %d of %d", len(got), len(before))
		}
		if len(PlanSubcontractChanges(nil, now, cancelled, due)) != 0 {
			t.Fatal("nothing was requested")
		}
	})
}

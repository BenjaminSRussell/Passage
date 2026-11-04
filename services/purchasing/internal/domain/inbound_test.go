package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func inbDay(t testing.TB, s string) Day {
	t.Helper()
	d, err := ParseDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func inbLine(t testing.TB, no int, qty, received, due string) InboundLine {
	return InboundLine{LineNo: no, ProductID: "p", Qty: decimal.RequireFromString(qty), ReceivedQty: decimal.RequireFromString(received), DeliveryDate: inbDay(t, due)}
}

func TestPlanPlanningMessage_PostponeAdvance(t *testing.T) {
	line := inbLine(t, 1, "100", "0", "2027-03-10")
	for _, tc := range []struct {
		name, action, date string
		outcome            string
		code               string
	}{
		{"postpone later", ActionPostpone, "2027-03-17", InboundApplied, ""},
		{"advance earlier", ActionAdvance, "2027-03-03", InboundApplied, ""},
		{"postpone to an earlier date", ActionPostpone, "2027-03-03", InboundRefused, CodeInboundDate},
		{"advance to a later date", ActionAdvance, "2027-03-17", InboundRefused, CodeInboundDate},
		{"already due then", ActionPostpone, "2027-03-10", InboundIgnored, CodeInboundDate},
		{"no date", ActionAdvance, "", InboundRefused, CodeInboundDate},
	} {
		req := InboundRequest{Action: tc.action}
		if tc.date != "" {
			req.Date = inbDay(t, tc.date)
		}
		got := PlanPlanningMessage(req, []InboundLine{line})
		if got.Outcome != tc.outcome || got.Code != tc.code {
			t.Errorf("%s: %+v", tc.name, got)
		}
		if got.Outcome == InboundApplied && (!got.SetDate || got.NewDate.String() != tc.date || got.LineNo != 1 || got.SetQty || got.CancelLine) {
			t.Errorf("%s: change %+v", tc.name, got)
		}
	}
}

func TestPlanPlanningMessage_Quantities(t *testing.T) {
	line := inbLine(t, 2, "100", "30", "2027-03-10")
	dec := decimal.RequireFromString
	for _, tc := range []struct {
		name, action, qty string
		outcome           string
		newQty            string
		cancel            bool
		code              string
	}{
		{"decrease by 40", ActionDecrease, "40", InboundApplied, "60", false, ""},
		{"increase by 25.5", ActionIncrease, "25.5", InboundApplied, "125.5", false, ""},
		{"decrease below zero", ActionDecrease, "101", InboundRefused, "", false, CodeInboundQuantity},
		{"decrease to zero with receipts", ActionDecrease, "100", InboundRefused, "", false, CodeInboundQuantity},
		{"decrease with no quantity", ActionDecrease, "0", InboundRefused, "", false, CodeInboundQuantity},
		{"increase with no quantity", ActionIncrease, "0", InboundRefused, "", false, CodeInboundQuantity},
		{"cancel", ActionCancel, "100", InboundApplied, "", true, ""},
		{"shortage is not for a PO", "shortage", "5", InboundIgnored, "", false, CodeInboundNotApplicable},
		{"delay is not for a PO", "delay", "5", InboundIgnored, "", false, CodeInboundNotApplicable},
	} {
		got := PlanPlanningMessage(InboundRequest{Action: tc.action, Qty: dec(tc.qty)}, []InboundLine{line})
		if got.Outcome != tc.outcome || got.Code != tc.code || got.CancelLine != tc.cancel {
			t.Errorf("%s: %+v", tc.name, got)
			continue
		}
		if tc.newQty != "" && (!got.SetQty || !got.NewQty.Equal(dec(tc.newQty))) {
			t.Errorf("%s: new quantity %+v", tc.name, got)
		}
	}
}

func TestPlanPlanningMessage_LineChoice(t *testing.T) {
	a := inbLine(t, 1, "100", "0", "2027-03-10")
	b := inbLine(t, 2, "40", "0", "2027-03-20")
	a.WarehouseID, b.WarehouseID = "w1", "w2"
	pp := func(req InboundRequest, lines ...InboundLine) InboundPlan { return PlanPlanningMessage(req, lines) }

	// No open line of the item, or none in the message's warehouse, is nothing to do.
	if got := pp(InboundRequest{Action: ActionCancel}); got.Outcome != InboundIgnored || got.Code != CodeInboundNoLine {
		t.Errorf("no lines: %+v", got)
	}
	if got := pp(InboundRequest{Action: ActionCancel, WarehouseID: "w9"}, a, b); got.Outcome != InboundIgnored || got.Code != CodeInboundNoLine {
		t.Errorf("other warehouse: %+v", got)
	}
	// The warehouse scope picks the line; the site scope (no warehouse) sees both and is ambiguous.
	if got := pp(InboundRequest{Action: ActionCancel, WarehouseID: "w2"}, a, b); got.Outcome != InboundApplied || got.LineNo != 2 {
		t.Errorf("warehouse scope: %+v", got)
	}
	if got := pp(InboundRequest{Action: ActionDecrease, Qty: decimal.NewFromInt(5)}, a, b); got.Outcome != InboundRefused || got.Code != CodeInboundAmbiguous {
		t.Errorf("ambiguous decrease: %+v", got)
	}
	// A date move is narrowed by the open quantity of the supply, which planning sends as qty.
	date := inbDay(t, "2027-04-01")
	if got := pp(InboundRequest{Action: ActionPostpone, Qty: decimal.NewFromInt(40), Date: date}, a, b); got.Outcome != InboundApplied || got.LineNo != 2 {
		t.Errorf("narrowed by open quantity: %+v", got)
	}
	if got := pp(InboundRequest{Action: ActionPostpone, Qty: decimal.NewFromInt(7), Date: date}, a, b); got.Outcome != InboundRefused || got.Code != CodeInboundAmbiguous {
		t.Errorf("no quantity match: %+v", got)
	}
	// A line with no warehouse of its own is not excluded by the message's warehouse.
	c := inbLine(t, 3, "10", "0", "2027-03-10")
	if got := pp(InboundRequest{Action: ActionCancel, WarehouseID: "w1"}, c); got.Outcome != InboundApplied || got.LineNo != 3 {
		t.Errorf("line without warehouse: %+v", got)
	}
}

func TestPlanDropShipClose(t *testing.T) {
	dec := decimal.RequireFromString
	line := inbLine(t, 1, "10", "0", "2027-03-10")
	for _, tc := range []struct {
		name, closed string
		line         InboundLine
		outcome      string
		newQty       string
		cancel       bool
		code         string
	}{
		{"close 4 of 10", "4", line, InboundApplied, "6", false, ""},
		{"close all of an unreceived line cancels it", "10", line, InboundApplied, "", true, ""},
		{"close more than the line", "11", line, InboundRefused, "", false, CodeInboundQuantity},
		{"close all of a received line", "10", inbLine(t, 1, "10", "3", "2027-03-10"), InboundRefused, "", false, CodeInboundQuantity},
		{"nothing closed", "0", line, InboundIgnored, "", false, CodeInboundQuantity},
	} {
		got := PlanDropShipClose(InboundRequest{Action: ActionCloseRemainder, Qty: dec(tc.closed)}, tc.line)
		if got.Outcome != tc.outcome || got.Code != tc.code || got.CancelLine != tc.cancel {
			t.Errorf("%s: %+v", tc.name, got)
			continue
		}
		if tc.newQty != "" && (!got.SetQty || !got.NewQty.Equal(dec(tc.newQty))) {
			t.Errorf("%s: %+v", tc.name, got)
		}
	}
}

// A decrease followed by an increase of the same quantity restores the line, and a plan never leaves
// a line with a quantity of zero or less.
func TestPlanQuantity_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		qty := decimal.New(rapid.Int64Range(1, 1_000_000).Draw(t, "qty"), -3)
		delta := decimal.New(rapid.Int64Range(1, 1_200_000).Draw(t, "delta"), -3)
		line := InboundLine{LineNo: 1, Qty: qty, ReceivedQty: decimal.Zero}
		down := PlanPlanningMessage(InboundRequest{Action: ActionDecrease, Qty: delta}, []InboundLine{line})
		switch down.Outcome {
		case InboundApplied:
			if down.SetQty {
				if !down.NewQty.IsPositive() || !down.NewQty.Add(delta).Equal(qty) {
					t.Fatalf("decrease %s by %s gave %+v", qty, delta, down)
				}
				back := PlanPlanningMessage(InboundRequest{Action: ActionIncrease, Qty: delta}, []InboundLine{{LineNo: 1, Qty: down.NewQty}})
				if !back.NewQty.Equal(qty) {
					t.Fatalf("increase back = %+v want %s", back, qty)
				}
			} else if !down.CancelLine || !delta.Equal(qty) {
				t.Fatalf("decrease %s by %s applied as %+v", qty, delta, down)
			}
		case InboundRefused:
			if delta.LessThan(qty) {
				t.Fatalf("decrease %s by %s refused: %+v", qty, delta, down)
			}
		default:
			t.Fatalf("decrease outcome %+v", down)
		}
	})
}

func TestPlanDropShipCancel(t *testing.T) {
	line := InboundLine{LineNo: 2, Qty: decimal.RequireFromString("10"), ReceivedQty: decimal.Zero}
	cases := map[string]struct {
		qty     string
		outcome string
		cancel  bool
		newQty  string
	}{
		"part of the line":   {"4", InboundApplied, false, "6"},
		"all of the line":    {"10", InboundApplied, true, ""},
		"more than the line": {"11", InboundRefused, false, ""},
		"nothing left":       {"0", InboundIgnored, false, ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := PlanDropShipCancel(InboundRequest{Action: ActionCancelDropShip, Qty: decimal.RequireFromString(c.qty)}, line)
			if p.Outcome != c.outcome || p.CancelLine != c.cancel {
				t.Fatalf("plan = %+v", p)
			}
			if c.newQty != "" && (!p.SetQty || !p.NewQty.Equal(decimal.RequireFromString(c.newQty))) {
				t.Fatalf("new qty = %s, want %s", p.NewQty, c.newQty)
			}
		})
	}
	received := line
	received.ReceivedQty = decimal.RequireFromString("3")
	if p := PlanDropShipCancel(InboundRequest{Qty: decimal.RequireFromString("10")}, received); p.Outcome != InboundRefused || p.Code != CodeInboundQuantity {
		t.Fatalf("a line with receipts cannot go to zero: %+v", p)
	}
}

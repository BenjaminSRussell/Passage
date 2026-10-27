package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var allStatuses = []Status{Created, Estimated, Scheduled, Released, Started, ReportedAsFinished, Ended, Cancelled}
var allCommands = []Command{CmdEstimate, CmdSchedule, CmdRelease, CmdStart, CmdReportFinished, CmdEnd, CmdCancel, CmdResetStatus}

// legal is the complete transition table of BR-21.1; every other pair is illegal.
// `:estimate` after Created is a re-estimate that keeps the status (FR-21.3).
var legal = map[Status]map[Command]Status{
	Created:            {CmdEstimate: Estimated, CmdCancel: Cancelled},
	Estimated:          {CmdEstimate: Estimated, CmdSchedule: Scheduled, CmdCancel: Cancelled, CmdResetStatus: Created},
	Scheduled:          {CmdEstimate: Scheduled, CmdRelease: Released, CmdCancel: Cancelled, CmdResetStatus: Estimated},
	Released:           {CmdEstimate: Released, CmdStart: Started, CmdCancel: Cancelled, CmdResetStatus: Scheduled},
	Started:            {CmdReportFinished: ReportedAsFinished, CmdEnd: Ended, CmdResetStatus: Released},
	ReportedAsFinished: {CmdReportFinished: ReportedAsFinished, CmdEnd: Ended, CmdResetStatus: Started},
	Ended:              {},
	Cancelled:          {},
}

func TestTransition_Table(t *testing.T) {
	for _, from := range allStatuses {
		for _, cmd := range allCommands {
			want, isLegal := legal[from][cmd]
			got, err := Transition(from, cmd)
			if !isLegal {
				if err == nil {
					t.Errorf("%s via %s: want illegal, got %s", from, cmd, got)
				}
				continue
			}
			if err != nil || got != want {
				t.Errorf("%s via %s: want %s, got %s (%v)", from, cmd, want, got, err)
			}
		}
	}
}

func TestTransition_IllegalCarriesCode(t *testing.T) {
	_, err := Transition(Created, CmdStart)
	de, ok := err.(*Error)
	if !ok || de.Code != CodeIllegalTransition {
		t.Fatalf("want %s, got %v", CodeIllegalTransition, err)
	}
}

func TestTransition_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.SampledFrom(allStatuses).Draw(t, "from")
		cmd := rapid.SampledFrom(allCommands).Draw(t, "cmd")
		want, isLegal := legal[from][cmd]
		got, err := Transition(from, cmd)
		if !isLegal {
			if err == nil {
				t.Fatalf("illegal %s via %s accepted as %s", from, cmd, got)
			}
			return
		}
		if err != nil || got != want {
			t.Fatalf("legal %s via %s: want %s, got %s (%v)", from, cmd, want, got, err)
		}
	})
}

func TestCheckEnd(t *testing.T) {
	zero, some := decimal.Zero, decimal.NewFromInt(60)
	tests := []struct {
		name string
		from Status
		raf  decimal.Decimal
		duty bool
		code string
	}{
		{"reported order ends without duty", ReportedAsFinished, some, false, ""},
		{"started with output must RAF first", Started, some, true, CodeNotRAFed},
		{"zero output needs the duty", Started, zero, false, CodeZeroOutput},
		{"zero output with duty ends", Started, zero, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckEnd(tt.from, tt.raf, tt.duty)
			if tt.code == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if de, ok := err.(*Error); !ok || de.Code != tt.code {
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}
}

func TestResetTo(t *testing.T) {
	for _, from := range allStatuses {
		for _, to := range allStatuses {
			got, err := ResetTo(from, to)
			fromRank, toRank := rank(from), rank(to)
			allowed := fromRank > 0 && toRank >= 0 && toRank < fromRank
			if allowed != (err == nil) {
				t.Errorf("reset %s → %s: allowed=%v, err=%v", from, to, allowed, err)
			}
			if allowed && got != to {
				t.Errorf("reset %s → %s returned %s", from, to, got)
			}
			if !allowed {
				if de, ok := err.(*Error); !ok || de.Code != CodeIllegalTransition {
					t.Errorf("reset %s → %s: want %s, got %v", from, to, CodeIllegalTransition, err)
				}
			}
		}
	}
	// FR-21.9 named cases.
	if _, err := ResetTo(Ended, Started); err == nil {
		t.Error("reset from Ended accepted")
	}
	if got, err := ResetTo(ReportedAsFinished, Created); err != nil || got != Created {
		t.Errorf("multi-step reset to Created: %s, %v", got, err)
	}
}

func TestStatusValid(t *testing.T) {
	for _, s := range allStatuses {
		if !s.Valid() {
			t.Errorf("%s reported invalid", s)
		}
	}
	for _, s := range []Status{"", "created", "Closed"} {
		if s.Valid() {
			t.Errorf("%q reported valid", s)
		}
	}
}

func TestCheckReportFinished(t *testing.T) {
	tests := []struct {
		name                        string
		order, raf, errQ, good, bad string
		tol                         string
		code                        string
	}{
		{"full quantity", "100", "0", "0", "100", "0", "0", ""},
		{"partial quantity", "100", "0", "0", "60", "0", "0", ""},
		{"good plus error up to the order", "100", "0", "0", "97", "3", "0", ""},
		{"over by one unit without tolerance", "100", "0", "0", "101", "0", "0", CodeRAFExceeds},
		{"error quantity counts against remaining", "100", "0", "0", "100", "1", "0", CodeRAFExceeds},
		{"over-delivery inside a 5% tolerance", "100", "0", "0", "105", "0", "5", ""},
		{"over-delivery beyond a 5% tolerance", "100", "0", "0", "105.000001", "0", "5", CodeRAFExceeds},
		{"earlier reports reduce remaining", "100", "60", "2", "39", "0", "0", CodeRAFExceeds},
		{"zero good quantity is refused (BR-21.1 qty_raf > 0)", "100", "0", "0", "0", "5", "0", CodeInvalidInput},
		{"negative error quantity is refused", "100", "0", "0", "10", "-1", "0", CodeInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckReportFinished(d(tt.order), d(tt.raf), d(tt.errQ), d(tt.good), d(tt.bad), d(tt.tol))
			if tt.code == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			if de, ok := err.(*Error); !ok || de.Code != tt.code {
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}
}

// Reported quantities never pass the tolerance limit, however they are split.
func TestCheckReportFinished_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		order := decimal.New(rapid.Int64Range(1, 1_000_000).Draw(t, "order"), -2)
		tol := decimal.New(rapid.Int64Range(0, 100).Draw(t, "tol"), 0)
		good := decimal.New(rapid.Int64Range(1, 2_000_000).Draw(t, "good"), -2)
		bad := decimal.New(rapid.Int64Range(0, 100_000).Draw(t, "bad"), -2)
		err := CheckReportFinished(order, decimal.Zero, decimal.Zero, good, bad, tol)
		limit := order.Mul(hundred.Add(tol)).Div(hundred)
		if within := !good.Add(bad).GreaterThan(limit); within != (err == nil) {
			t.Fatalf("order %s tol %s good %s bad %s: within=%v err=%v", order, tol, good, bad, within, err)
		}
	})
}

func TestCheckEdit(t *testing.T) {
	for _, s := range allStatuses {
		qtyErr := CheckEdit(s, true, false)
		if wantOK := s == Created || s == Estimated; wantOK != (qtyErr == nil) {
			t.Errorf("quantity edit on %s: err=%v", s, qtyErr)
		}
		dateErr := CheckEdit(s, false, true)
		if wantOK := s == Created || s == Estimated || s == Scheduled || s == Released; wantOK != (dateErr == nil) {
			t.Errorf("delivery date edit on %s: err=%v", s, dateErr)
		}
		for _, err := range []error{qtyErr, dateErr} {
			if de, ok := err.(*Error); err != nil && (!ok || de.Code != CodeNotEditable) {
				t.Errorf("%s: want %s, got %v", s, CodeNotEditable, err)
			}
		}
	}
}

package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

type stubScreen struct {
	status map[string]string
	err    error
}

func (s stubScreen) Screen(_ context.Context, p ScreeningParty) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if st, ok := s.status[p.Name]; ok {
		return st, nil
	}
	return ScreenClear, nil
}

func TestScreenParties(t *testing.T) {
	parties := []ScreeningParty{
		{Role: "SOLD_TO", Name: "Acme"},
		{Role: "SHIP_TO", Name: "Hit Co"},
	}
	cases := []struct {
		name       string
		provider   ScreeningProvider
		crossBord  bool
		want       string
		wantCodes  []string
		wantStatus []string
	}{
		{"domestic without provider is clear", nil, false, DecisionClear, []string{"", ""}, []string{ScreenClear, ScreenClear}},
		{"cross-border without provider warns TMS-0134", nil, true, DecisionWarn,
			[]string{CodeNotScreened, CodeNotScreened}, []string{ScreenNotScreened, ScreenNotScreened}},
		{"potential match holds TMS-0133", stubScreen{status: map[string]string{"Hit Co": ScreenPotentialMatch}}, true, DecisionHold,
			[]string{"", CodeScreeningHold}, []string{ScreenClear, ScreenPotentialMatch}},
		{"blocked holds", stubScreen{status: map[string]string{"Acme": ScreenBlocked}}, false, DecisionHold,
			[]string{CodeScreeningHold, ""}, []string{ScreenBlocked, ScreenClear}},
		{"provider error is pending and holds", stubScreen{err: errors.New("timeout")}, true, DecisionHold,
			[]string{CodeScreeningHold, CodeScreeningHold}, []string{ScreenPending, ScreenPending}},
		{"unknown status is pending", stubScreen{status: map[string]string{"Acme": "MAYBE"}}, false, DecisionHold,
			[]string{CodeScreeningHold, ""}, []string{ScreenPending, ScreenClear}},
		{"all clear", stubScreen{}, true, DecisionClear, []string{"", ""}, []string{ScreenClear, ScreenClear}},
		{"provider not screened warns", stubScreen{status: map[string]string{"Acme": ScreenNotScreened}}, false, DecisionWarn,
			[]string{CodeNotScreened, ""}, []string{ScreenNotScreened, ScreenClear}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ScreenParties(context.Background(), c.provider, parties, c.crossBord)
			if got.Decision != c.want {
				t.Fatalf("decision = %s, want %s", got.Decision, c.want)
			}
			for i, p := range got.Parties {
				if p.Status != c.wantStatus[i] || p.Code != c.wantCodes[i] {
					t.Errorf("party %d = %s/%s, want %s/%s", i, p.Status, p.Code, c.wantStatus[i], c.wantCodes[i])
				}
			}
		})
	}
}

func TestValidateScreeningParties(t *testing.T) {
	if f := ValidateScreeningParties(nil); f["parties"] == "" {
		t.Error("empty party list must be rejected")
	}
	bad := []ScreeningParty{{Role: "BUYER", Name: " "}, {Role: "CARRIER", Name: "X", Country: "usa"}}
	f := ValidateScreeningParties(bad)
	for _, k := range []string{"parties[0].role", "parties[0].name", "parties[1].country"} {
		if f[k] == "" {
			t.Errorf("missing field error %s in %v", k, f)
		}
	}
	if f := ValidateScreeningParties([]ScreeningParty{{Role: "CARRIER", Name: "Ok", Country: "US"}}); len(f) != 0 {
		t.Errorf("valid party rejected: %v", f)
	}
}

func TestTransferFor(t *testing.T) {
	cases := []struct {
		incoterm string
		event    string
		git      bool
	}{
		{"FOB", TransferOriginShipment, true},
		{"fob", TransferOriginShipment, true},
		{"EXW", TransferOriginShipment, true},
		{"CIF", TransferOriginShipment, true},
		{"DAP", TransferDestinationReceipt, false},
		{"DPU", TransferDestinationReceipt, false},
		{"DDP", TransferDestinationReceipt, false},
	}
	for _, c := range cases {
		r, err := TransferFor(c.incoterm)
		if err != nil || r.Event != c.event || r.GoodsInTransit != c.git {
			t.Errorf("TransferFor(%s) = %+v, %v; want event %s git %v", c.incoterm, r, err, c.event, c.git)
		}
		if c.git && r.Code != CodeOwnershipMoved {
			t.Errorf("TransferFor(%s) code = %q, want %s", c.incoterm, r.Code, CodeOwnershipMoved)
		}
	}
	if _, err := TransferFor("XYZ"); !errors.Is(err, ErrUnknownIncoterm) {
		t.Errorf("unknown incoterm error = %v", err)
	}
}

func TestClassifyArrival(t *testing.T) {
	appt := time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		arrived time.Time
		status  string
		delta   int
		code    string
	}{
		{appt.Add(-61 * time.Minute), ArrivalEarly, -61, CodeArrivalOutsideWindow},
		{appt.Add(-60 * time.Minute), ArrivalOnTime, -60, ""},
		{appt.Add(30 * time.Minute), ArrivalOnTime, 30, ""},
		{appt.Add(61 * time.Minute), ArrivalLate, 61, CodeArrivalOutsideWindow},
	}
	for _, c := range cases {
		got := ClassifyArrival(appt, c.arrived, DefaultGateToleranceMin)
		if got.Status != c.status || got.DeltaMin != c.delta || got.Code != c.code {
			t.Errorf("ClassifyArrival(%v) = %+v; want %s %d %q", c.arrived, got, c.status, c.delta, c.code)
		}
	}
}

func TestCheckSeal(t *testing.T) {
	if err := CheckSeal("778813", "778812"); err == nil {
		t.Fatal("mismatched seal must be blocked")
	} else {
		var re *Error
		if !errors.As(err, &re) || re.Code != CodeSealMismatch {
			t.Errorf("seal error = %v, want %s", err, CodeSealMismatch)
		}
	}
	if err := CheckSeal(" ab12 ", "AB12"); err != nil {
		t.Errorf("same seal ignoring case and space rejected: %v", err)
	}
	if err := CheckSeal("", "AB12"); err == nil {
		t.Error("missing declared seal must be blocked")
	}
}

func TestCheckWalkIn(t *testing.T) {
	if err := CheckWalkIn(false, false); err == nil {
		t.Error("walk-in without appointment must be rejected when not allowed")
	}
	if err := CheckWalkIn(false, true); err != nil {
		t.Errorf("walk-in allowed: %v", err)
	}
	if err := CheckWalkIn(true, false); err != nil {
		t.Errorf("appointment present: %v", err)
	}
}

func TestComputeDetention_Example(t *testing.T) {
	in := time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC)
	out := time.Date(2026, 3, 2, 11, 20, 0, 0, time.UTC)
	d := ComputeDetention(in, out, 120, decimal.RequireFromString("75.00"))
	if d.DwellMin != 200 || d.DetentionMin != 80 || d.Hours != 2 {
		t.Fatalf("detention = %+v, want dwell 200 detention 80 hours 2", d)
	}
	if !d.Charge.Equal(decimal.RequireFromString("150.00")) {
		t.Errorf("charge = %s, want 150.00", d.Charge)
	}
}

func TestComputeDetention_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		dwell := rapid.IntRange(0, 5000).Draw(t, "dwell")
		free := rapid.IntRange(0, 1440).Draw(t, "free")
		rate := rapid.IntRange(0, 500).Draw(t, "rate")
		in := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		d := ComputeDetention(in, in.Add(time.Duration(dwell)*time.Minute), free, decimal.NewFromInt(int64(rate)))
		if dwell <= free {
			if d.DetentionMin != 0 || !d.Charge.IsZero() {
				t.Fatalf("within free time: %+v", d)
			}
			return
		}
		want := dwell - free
		if d.DetentionMin != want || d.Hours*60 < want || (d.Hours-1)*60 >= want {
			t.Fatalf("hours must be ceil(%d/60), got %+v", want, d)
		}
		if !d.Charge.Equal(decimal.NewFromInt(int64(rate * d.Hours))) {
			t.Fatalf("charge %s != %d x %d", d.Charge, rate, d.Hours)
		}
	})
}

// slowScreen answers only after its context is done, like a provider that hangs.
type slowScreen struct{}

func (slowScreen) Screen(ctx context.Context, _ ScreeningParty) (string, error) {
	<-ctx.Done()
	return ScreenClear, nil
}

// TestScreenParties_TimeoutIsPending: a provider that answers only after the
// deadline is SCREENING_PENDING, never CLEAR (BR-34.17 "never auto-clears").
func TestScreenParties_TimeoutIsPending(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	got := ScreenParties(ctx, slowScreen{}, []ScreeningParty{{Role: "SHIP_TO", Name: "Slow"}}, true)
	if got.Decision != DecisionHold || got.Parties[0].Status != ScreenPending {
		t.Fatalf("timeout = %+v, want HOLD with SCREENING_PENDING", got)
	}
}

func TestCrossBorder(t *testing.T) {
	cases := []struct {
		countries []string
		want      bool
	}{
		{nil, false},
		{[]string{"US", "", "US"}, false},
		{[]string{"", "DE"}, false},
		{[]string{"US", "CA"}, true},
	}
	for _, c := range cases {
		var parties []ScreeningParty
		for _, country := range c.countries {
			parties = append(parties, ScreeningParty{Role: "SHIP_TO", Name: "x", Country: country})
		}
		if got := CrossBorder(parties); got != c.want {
			t.Errorf("CrossBorder(%v) = %v, want %v", c.countries, got, c.want)
		}
	}
}

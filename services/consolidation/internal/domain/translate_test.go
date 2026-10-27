package domain

import (
	"errors"
	"testing"
	"time"
)

// TestTranslate_AC406 is AC-40.6: EUR 700,000 at 1.05, net income EUR 150,000
// at 1.08 and closing EUR 850,000 at 1.10 give CTA 38,000.00 USD.
func TestTranslate_AC406(t *testing.T) {
	got := Translate(
		Statement{OpeningNetAssets: d("700000"), NetIncome: d("150000"), ClosingNetAssets: d("850000")},
		Rates{Closing: d("1.10"), Average: d("1.08"), Opening: d("1.05"), Historical: d("1.05")},
	)
	if !got.OpeningNetAssets.Equal(d("735000")) || !got.NetIncome.Equal(d("162000")) || !got.ClosingNetAssets.Equal(d("935000")) {
		t.Fatalf("translated = %+v", got)
	}
	if got.CTA.StringFixed(2) != "38000.00" {
		t.Fatalf("CTA = %s; want 38000.00", got.CTA.StringFixed(2))
	}
}

func TestNCIShare_Table(t *testing.T) {
	cases := []struct {
		amount, own, want string
	}{
		{"38000", "80", "7600"},
		{"38000", "100", "0"},
		{"1000", "60", "400"},
	}
	for _, c := range cases {
		if got := NCIShare(d(c.amount), d(c.own)); !got.Equal(d(c.want)) {
			t.Fatalf("NCIShare(%s, %s%%) = %s; want %s", c.amount, c.own, got, c.want)
		}
	}
}

func TestValidateOwnership_Table(t *testing.T) {
	day := func(m time.Month, dd int) time.Time { return time.Date(2026, m, dd, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name string
		ivs  []OwnershipInterval
		ok   bool
	}{
		{"single holding", []OwnershipInterval{{From: day(1, 1), Percent: d("80")}}, true},
		{"sequential holdings", []OwnershipInterval{
			{From: day(1, 1), To: day(6, 30), Percent: d("60")},
			{From: day(7, 1), Percent: d("100")},
		}, true},
		{"overlap above 100", []OwnershipInterval{
			{From: day(1, 1), Percent: d("60")},
			{From: day(3, 1), Percent: d("50")},
		}, false},
		{"percent above 100", []OwnershipInterval{{From: day(1, 1), Percent: d("101")}}, false},
		{"interval ends before start", []OwnershipInterval{{From: day(5, 1), To: day(4, 1), Percent: d("10")}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateOwnership(c.ivs)
			if (err == nil) != c.ok {
				t.Fatalf("err = %v; want ok=%v", err, c.ok)
			}
			var de *Error
			if err != nil && !c.ok && !errors.As(err, &de) {
				t.Fatalf("err %T is not a domain error", err)
			}
		})
	}
}

// TestTranslateUnit_AC406WithNCI is AC-40.6 through the rounded unit
// translation, plus BR-40.6: with 80 % ownership the NCI carries 20 % of the
// CTA (7,600.00) and the group 30,400.00.
func TestTranslateUnit_AC406WithNCI(t *testing.T) {
	got, err := TranslateUnit(
		Statement{OpeningNetAssets: d("700000"), NetIncome: d("150000"), ClosingNetAssets: d("850000")},
		Rates{Closing: d("1.10"), Average: d("1.08"), Opening: d("1.05")},
		d("80"), "USD",
	)
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]struct{ got, want string }{
		"opening": {got.OpeningNetAssets.StringFixed(2), "735000.00"},
		"income":  {got.NetIncome.StringFixed(2), "162000.00"},
		"closing": {got.ClosingNetAssets.StringFixed(2), "935000.00"},
		"cta":     {got.CTA.StringFixed(2), "38000.00"},
		"nci":     {got.NCIShareOfCTA.StringFixed(2), "7600.00"},
		"group":   {got.GroupShareOfCTA.StringFixed(2), "30400.00"},
	}
	for name, c := range checks {
		if c.got != c.want {
			t.Fatalf("%s = %s; want %s", name, c.got, c.want)
		}
	}
}

// TestTranslateUnit_RoundedLinesReconcile checks the roll-forward stays exact
// after rounding: opening + income + CTA = closing, and NCI + group = CTA.
func TestTranslateUnit_RoundedLinesReconcile(t *testing.T) {
	got, err := TranslateUnit(
		Statement{OpeningNetAssets: d("1234567.89"), NetIncome: d("-98765.43"), ClosingNetAssets: d("1111111.11")},
		Rates{Closing: d("1.0937"), Average: d("1.0811"), Opening: d("1.0649")},
		d("66.6667"), "USD",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OpeningNetAssets.Add(got.NetIncome).Add(got.CTA).Equal(got.ClosingNetAssets) {
		t.Fatalf("roll-forward broken: %+v", got)
	}
	if !got.NCIShareOfCTA.Add(got.GroupShareOfCTA).Equal(got.CTA) {
		t.Fatalf("CTA split broken: %+v", got)
	}
	if got.CTA.Exponent() < -2 || got.NCIShareOfCTA.Exponent() < -2 {
		t.Fatalf("amounts not rounded to cents: %+v", got)
	}
}

func TestTranslateUnit_Rejects(t *testing.T) {
	s := Statement{OpeningNetAssets: d("1"), NetIncome: d("1"), ClosingNetAssets: d("2")}
	r := Rates{Closing: d("1.1"), Average: d("1.1"), Opening: d("1.1")}
	cases := []struct {
		name string
		r    Rates
		own  string
		cur  string
	}{
		{"missing opening rate", Rates{Closing: r.Closing, Average: r.Average}, "80", "USD"},
		{"zero ownership", r, "0", "USD"},
		{"ownership above 100", r, "100.01", "USD"},
		{"bad currency", r, "80", "USDX"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var de *Error
			if _, err := TranslateUnit(s, c.r, d(c.own), c.cur); !errors.As(err, &de) || de.Code != CodeInvalid {
				t.Fatalf("err = %v; want COM-0001", err)
			}
		})
	}
}

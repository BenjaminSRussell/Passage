package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

const phone = "+18005551234"

func dgLine(un, class string, qty string, category int) DGLine {
	return DGLine{UNNumber: un, ProperShippingName: "TEST SUBSTANCE", Class: class, PackingGroup: "II", Quantity: dec(qty), TransportCategory: category}
}

func ruleIDs(fs []DGFinding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.RuleID)
	}
	return out
}

// TestCheckDG_AC3413: a class 1.1D explosive with a class 3 flammable liquid
// under 49 CFR fails with the segregation rule and TMS-0130.
func TestCheckDG_AC3413(t *testing.T) {
	req := DGRequest{Regulation: Reg49CFR, EmergencyResponsePhone: phone, Lines: []DGLine{
		dgLine("UN0081", "1.1D", "50", 1), dgLine("UN1203", "3", "200", 2),
	}}
	result, points, findings := CheckDG(req)
	if result != DGFail || !points.IsZero() {
		t.Fatalf("result = %s points %s, want FAIL and 0 under 49CFR", result, points)
	}
	if len(findings) != 1 || findings[0].RuleID != RuleSeg49CFR || findings[0].Code != CodeDGSegregation {
		t.Fatalf("findings = %+v", findings)
	}
	// The same pair under ADR names the ADR table.
	// The same pair under ADR names the ADR table, and 50 × 50 + 200 × 3 =
	// 3,100 points also warn.
	req.Regulation = RegADR
	result, points, findings = CheckDG(req)
	if got := ruleIDs(findings); result != DGFail || !points.Equal(dec("3100")) || len(got) != 2 || got[0] != RuleSegADR || got[1] != RuleADRPoints {
		t.Fatalf("ADR: %s %s %v", result, points, got)
	}
}

// TestCheckDG_AC3414: 400 L of a category-2 substance is 1,200 points and
// raises TMS-0131; 300 L is 900 points and raises nothing.
func TestCheckDG_AC3414(t *testing.T) {
	cases := []struct {
		qty, points, result string
		warn                bool
	}{
		{"400", "1200", DGWarn, true},
		{"300", "900", DGPass, false},
		{"333.334", "1000.002", DGWarn, true},
		{"333.333", "999.999", DGPass, false},
	}
	for _, c := range cases {
		req := DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1993", "3", c.qty, 2)}}
		result, points, findings := CheckDG(req)
		if !points.Equal(dec(c.points)) || result != c.result {
			t.Fatalf("%s L: points %s result %s, want %s %s", c.qty, points, result, c.points, c.result)
		}
		if got := len(findings) == 1 && findings[0].Code == CodeADRPoints; got != c.warn {
			t.Fatalf("%s L: findings = %+v", c.qty, findings)
		}
	}
	// Exactly 1,000 is not above the threshold.
	if r, p, _ := CheckDG(DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1993", "3", "1000", 3)}}); r != DGPass || !p.Equal(dec("1000")) {
		t.Fatalf("1,000 points = %s %s, want PASS", r, p)
	}
}

func TestCheckDG_Rules(t *testing.T) {
	lq := dgLine("UN1263", "3", "5", 2)
	lq.LimitedQuantity = true
	cases := []struct {
		name   string
		req    DGRequest
		result string
		rules  []string
	}{
		{"category 4 counts zero points", DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN3077", "9", "50000", 4)}}, DGPass, nil},
		{"category 1 multiplies by 50", DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1051", "6.1", "21", 1)}}, DGWarn, []string{RuleADRPoints}},
		{"category 0 always needs full ADR", DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1051", "6.1", "1", 0)}}, DGWarn, []string{RuleADRCategory0}},
		{"limited quantity is marking only and skips segregation", DGRequest{Regulation: Reg49CFR, Lines: []DGLine{dgLine("UN0081", "1.1D", "1", 1), lq}, EmergencyResponsePhone: phone}, DGWarn, []string{RuleLimitedQuantity}},
		{"only limited quantities need no emergency phone", DGRequest{Regulation: RegADR, Lines: []DGLine{lq}}, DGWarn, []string{RuleLimitedQuantity}},
		{"regulated goods without an emergency phone fail", DGRequest{Regulation: Reg49CFR, Lines: []DGLine{dgLine("UN1203", "3", "10", 2)}}, DGFail, []string{RuleEmergencyPhone}},
		{"division 1.4 is not in the prohibited subset", DGRequest{Regulation: Reg49CFR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN0012", "1.4S", "1", 4), dgLine("UN1203", "3", "1", 2)}}, DGPass, nil},
		{"two non-explosive classes may share a load", DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1203", "3", "100", 2), dgLine("UN1789", "8", "100", 3)}}, DGPass, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, _, findings := CheckDG(c.req)
			got := ruleIDs(findings)
			if result != c.result || len(got) != len(c.rules) {
				t.Fatalf("result %s rules %v, want %s %v", result, got, c.result, c.rules)
			}
			for i := range got {
				if got[i] != c.rules[i] {
					t.Fatalf("rules %v, want %v", got, c.rules)
				}
			}
		})
	}
}

// TestCheckDG_Properties: ADR points are Σ quantity × multiplier over the
// regulated lines, a load with an explosive 1.1 or 1.2 and a class 2 to 8
// fails, and the result never improves when a line is added.
func TestCheckDG_Properties(t *testing.T) {
	classes := []string{"1.1D", "1.2E", "1.4S", "2.1", "3", "4.1", "5.1", "6.1", "8", "9"}
	rank := map[string]int{DGPass: 0, DGWarn: 1, DGFail: 2}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "n")
		req := DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone}
		want := dec("0")
		explosive, other := false, false
		for i := 0; i < n; i++ {
			class := rapid.SampledFrom(classes).Draw(t, "class")
			cat := rapid.IntRange(1, 4).Draw(t, "cat")
			qty := rapid.IntRange(1, 2000).Draw(t, "qty")
			l := dgLine("UN1000", class, "1", cat)
			l.Quantity = decimal.NewFromInt(int64(qty))
			req.Lines = append(req.Lines, l)
			want = want.Add(l.Quantity.Mul(decimal.NewFromInt(ADRMultiplier(cat))))
			switch DivisionOf(class) {
			case "1.1", "1.2":
				explosive = true
			case "1.4", "9":
				// 1.4 depends on compatibility groups and class 9 is not in the
				// prohibited reference subset (see prohibited in dg.go).
			default:
				other = true
			}
		}
		result, points, _ := CheckDG(req)
		if !points.Equal(want) {
			t.Fatalf("points %s, want %s", points, want)
		}
		if explosive && other && result != DGFail {
			t.Fatalf("explosive mixed load = %s", result)
		}
		more := req
		more.Lines = append(append([]DGLine(nil), req.Lines...), dgLine("UN1993", "3", "1", 3))
		if r2, _, _ := CheckDG(more); rank[r2] < rank[result] {
			t.Fatalf("adding a line improved %s to %s", result, r2)
		}
	})
}

func TestValidateDGRequest(t *testing.T) {
	ok := DGRequest{Regulation: RegADR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1203", "3", "1.5", 2)}}
	if f := ValidateDGRequest(ok); len(f) != 0 {
		t.Fatalf("valid request: %v", f)
	}
	bad := DGRequest{Regulation: "IMDG", EmergencyResponsePhone: "555-1234", Lines: []DGLine{{
		UNNumber: "1203", ProperShippingName: " ", Class: "3D", PackingGroup: "IV", Quantity: dec("0"), TransportCategory: 5,
	}}}
	f := ValidateDGRequest(bad)
	for _, k := range []string{"regulation", "emergencyResponsePhone", "lines[0].unNumber", "lines[0].properShippingName", "lines[0].class", "lines[0].packingGroup", "lines[0].quantity", "lines[0].transportCategory"} {
		if f[k] == "" {
			t.Errorf("missing field error %s in %v", k, f)
		}
	}
	if f := ValidateDGRequest(DGRequest{Regulation: RegADR}); f["lines"] == "" {
		t.Fatalf("empty lines accepted: %v", f)
	}
}

func TestShippingPapers_CarryEmergencyPhoneInOrder(t *testing.T) {
	req := DGRequest{Regulation: Reg49CFR, EmergencyResponsePhone: phone, Lines: []DGLine{dgLine("UN1203", "3", "10", 2), dgLine("UN1789", "8", "5", 3)}}
	req.Lines[0].ProperShippingName = "  GASOLINE "
	papers := ShippingPapers(req)
	if len(papers) != 2 || papers[0].UNNumber != "UN1203" || papers[0].ProperShippingName != "GASOLINE" || papers[1].UNNumber != "UN1789" {
		t.Fatalf("papers = %+v", papers)
	}
	for _, p := range papers {
		if p.EmergencyResponsePhone != phone || p.PackingGroup != "II" {
			t.Fatalf("paper line = %+v", p)
		}
	}
}

func TestDecide_BR344(t *testing.T) {
	cases := []struct {
		action, status, reason, next string
		fails                        bool
	}{
		{ActionApprove, MatchMatched, "", BillApproved, false},
		{ActionApprove, MatchVariance, "accepted fuel correction", BillApproved, false},
		{ActionApprove, MatchVariance, " ", "", true},
		{ActionApprove, BillDisputed, "carrier credited 5.50", BillApproved, false},
		{ActionApprove, BillDisputed, "", "", true},
		{ActionApprove, BillUnmatched, "x", "", true},
		{ActionApprove, BillApproved, "x", "", true},
		{ActionApprove, BillSettled, "x", "", true},
		{ActionDispute, MatchVariance, "fuel charged twice", BillDisputed, false},
		{ActionDispute, MatchVariance, "", "", true},
		{ActionDispute, MatchMatched, "x", "", true},
		{ActionDispute, BillDisputed, "x", "", true},
		{ActionDispute, BillApproved, "x", "", true},
	}
	for _, c := range cases {
		next, err := Decide(c.action, c.status, c.reason)
		if c.fails {
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeBillState {
				t.Fatalf("%s %s %q: err %v, want %s", c.action, c.status, c.reason, err, CodeBillState)
			}
			continue
		}
		if err != nil || next != c.next {
			t.Fatalf("%s %s: %s %v, want %s", c.action, c.status, next, err, c.next)
		}
	}
}

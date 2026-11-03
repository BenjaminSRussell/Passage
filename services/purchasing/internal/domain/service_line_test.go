package domain_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func svcItem(desc, qty, unit, price string) domain.OutlineNode {
	return domain.OutlineNode{Kind: domain.OutlineItem, Description: desc, Qty: dd(qty), Unit: unit, UnitPrice: dd(price)}
}

func svcCodeOf(t *testing.T, err error) string {
	t.Helper()
	var de *domain.Error
	if !errors.As(err, &de) {
		t.Fatalf("want a domain error, got %v", err)
	}
	return de.Code
}

// AC-17.17: an outline "Maintenance" of 3 service items.
func TestBuildOutline_LevelSumsItsItems(t *testing.T) {
	total := dd("1050.26")
	tree := []domain.OutlineNode{{Kind: domain.OutlineLevel, Description: "Maintenance", Total: &total, Children: []domain.OutlineNode{
		svcItem("Inspection", "3", "h", "100"), svcItem("Lubrication", "1", "ea", "250.255"), svcItem("Report", "2", "ea", "250")}}}
	out, err := domain.BuildOutline(tree, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != 4 || out.Rows[0].Kind != domain.OutlineLevel || out.Rows[1].ParentSeq != 1 || out.Rows[3].Seq != 4 || out.Rows[3].Depth != 2 {
		t.Fatalf("rows %+v", out.Rows)
	}
	// 250.255 rounds half-even to 250.26 at two minor units; 300 + 250.26 + 500 = 1050.26.
	if !out.Rows[2].Amount.Equal(dd("250.26")) || !out.PlannedTotal.Equal(dd("1050.26")) || !out.Rows[0].Amount.Equal(dd("1050.26")) {
		t.Errorf("amounts %v planned %s", out.Rows, out.PlannedTotal)
	}
}

func TestBuildOutline_StatedTotalMismatchIsPRCSRV004(t *testing.T) {
	total := dd("999")
	tree := []domain.OutlineNode{{Kind: domain.OutlineLevel, Description: "Maintenance", Total: &total, Children: []domain.OutlineNode{svcItem("A", "1", "h", "100")}}}
	_, err := domain.BuildOutline(tree, 2)
	if svcCodeOf(t, err) != domain.CodeServiceOutline || !strings.Contains(err.Error(), "100") || !strings.Contains(err.Error(), "999") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildOutline_Rejections(t *testing.T) {
	two := dd("2")
	deep := svcItem("x", "1", "h", "1")
	level := func(c ...domain.OutlineNode) domain.OutlineNode {
		return domain.OutlineNode{Kind: domain.OutlineLevel, Description: "L", Children: c}
	}
	for i := 0; i < domain.MaxOutlineDepth; i++ {
		deep = level(deep)
	}
	many := make([]domain.OutlineNode, domain.MaxOutlineNodes+1)
	for i := range many {
		many[i] = svcItem("x", "1", "h", "1")
	}
	tests := map[string][]domain.OutlineNode{
		"unknown kind":         {{Kind: "TEXT", Description: "x"}},
		"item without text":    {{Kind: domain.OutlineItem, Qty: dd("1"), Unit: "h"}},
		"item qty zero":        {svcItem("x", "0", "h", "1")},
		"item qty 7 decimals":  {svcItem("x", "0.0000001", "h", "1")},
		"item no unit":         {svcItem("x", "1", " ", "1")},
		"item negative price":  {svcItem("x", "1", "h", "-1")},
		"item with children":   {{Kind: domain.OutlineItem, Description: "x", Qty: dd("1"), Unit: "h", Children: []domain.OutlineNode{svcItem("y", "1", "h", "1")}}},
		"item states a total":  {{Kind: domain.OutlineItem, Description: "x", Qty: dd("1"), Unit: "h", Total: &two}},
		"level without text":   {{Kind: domain.OutlineLevel, Children: []domain.OutlineNode{svcItem("y", "1", "h", "1")}}},
		"level without kids":   {{Kind: domain.OutlineLevel, Description: "L"}},
		"nested too deep":      {deep},
		"more than 200 nodes":  many,
		"description too long": {svcItem(strings.Repeat("x", 501), "1", "h", "1")},
	}
	for name, tree := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := domain.BuildOutline(tree, 2)
			if svcCodeOf(t, err) != domain.CodeValidation {
				t.Fatalf("got %v", err)
			}
		})
	}
	if out, err := domain.BuildOutline(nil, 2); err != nil || len(out.Rows) != 0 || !out.PlannedTotal.IsZero() {
		t.Errorf("an empty outline is valid: %+v %v", out, err)
	}
}

// Whatever tree is given, every level's amount is the sum of its children and the planned total
// the sum of the top-level nodes (BR-17.19).
func TestBuildOutline_SumInvariantProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		leaf := func(label string) domain.OutlineNode {
			return svcItem("s", dd2(rapid.Int64Range(1, 5000).Draw(t, label+"q")).String(), "h", dd2(rapid.Int64Range(0, 100000).Draw(t, label+"p")).String())
		}
		var tree []domain.OutlineNode
		for i := 0; i < rapid.IntRange(0, 4).Draw(t, "tops"); i++ {
			if rapid.Bool().Draw(t, "level") {
				kids := []domain.OutlineNode{}
				for j := 0; j < rapid.IntRange(1, 4).Draw(t, "kids"); j++ {
					kids = append(kids, leaf("k"))
				}
				tree = append(tree, domain.OutlineNode{Kind: domain.OutlineLevel, Description: "L", Children: kids})
			} else {
				tree = append(tree, leaf("t"))
			}
		}
		out, err := domain.BuildOutline(tree, 2)
		if err != nil {
			t.Fatal(err)
		}
		sums := map[int]decimal.Decimal{}
		top := decimal.Zero
		for _, r := range out.Rows {
			if r.ParentSeq == 0 {
				top = top.Add(r.Amount)
			} else {
				sums[r.ParentSeq] = sums[r.ParentSeq].Add(r.Amount)
			}
		}
		for _, r := range out.Rows {
			if r.Kind == domain.OutlineLevel && !r.Amount.Equal(sums[r.Seq]) {
				t.Fatalf("level %d amount %s, children %s", r.Seq, r.Amount, sums[r.Seq])
			}
		}
		if !top.Equal(out.PlannedTotal) {
			t.Fatalf("planned %s, top-level %s", out.PlannedTotal, top)
		}
		// Rebuilding the tree from the stored rows gives the same rows (model copy).
		again, err := domain.BuildOutline(domain.ToTree(out.Rows), 2)
		if err != nil || len(again.Rows) != len(out.Rows) || !again.PlannedTotal.Equal(out.PlannedTotal) {
			t.Fatalf("round trip: %v %+v", err, again)
		}
	})
}

func dd2(n int64) decimal.Decimal { return decimal.New(n, -2) }

func TestCheckUnplanned_AC1717(t *testing.T) {
	limit := dd("2000")
	got, err := domain.CheckUnplanned(limit, dd("1900"), dd("200"), nil, 1)
	if err != nil || got.Allowed || got.Code != domain.CodeServiceLimit || got.Detail != "Unplanned services 2100 exceed limit 2000 on PO line 1" ||
		!got.Remaining.Equal(dd("100")) {
		t.Fatalf("200 over 1900 of 2000: %+v %v", got, err)
	}
	got, err = domain.CheckUnplanned(limit, dd("1900"), dd("100"), nil, 1)
	if err != nil || !got.Allowed || got.Code != "" || !got.ValueRemaining.Equal(dd("100")) || !got.Remaining.Equal(dd("100")) {
		t.Fatalf("100 over 1900: %+v %v", got, err)
	}
	// A limit of 0 allows no unplanned service at all.
	if got, err := domain.CheckUnplanned(decimal.Zero, decimal.Zero, dd("0.01"), nil, 1); err != nil || got.Allowed || got.Code != domain.CodeServiceLimit {
		t.Errorf("limit 0: %+v %v", got, err)
	}
	// A limit lowered below what is already entered leaves nothing, never a negative remainder.
	if got, err := domain.CheckUnplanned(dd("500"), dd("600"), decimal.Zero, nil, 1); err != nil || got.Allowed ||
		!got.ValueRemaining.IsZero() || !got.Remaining.IsZero() {
		t.Errorf("entered above the limit: %+v %v", got, err)
	}
	if _, err := domain.CheckUnplanned(limit, decimal.Zero, dd("-1"), nil, 1); svcCodeOf(t, err) != domain.CodeValidation {
		t.Errorf("negative entry: %v", err)
	}
	if _, err := domain.CheckUnplanned(limit, dd("-1"), dd("1"), nil, 1); svcCodeOf(t, err) != domain.CodeValidation {
		t.Errorf("negative entered: %v", err)
	}
}

func TestCheckUnplanned_ContractLimitBoundsTheTotal(t *testing.T) {
	rem := dd("1500")
	got, err := domain.CheckUnplanned(dd("2000"), dd("1400"), dd("200"), &rem, 4)
	if err != nil || got.Allowed || got.Code != domain.CodeServiceLimit || !strings.Contains(got.Detail, "contract line") ||
		!got.ValueRemaining.Equal(dd("600")) || !got.Remaining.Equal(dd("100")) {
		t.Fatalf("contract limit: %+v %v", got, err)
	}
	if got, err := domain.CheckUnplanned(dd("2000"), dd("1400"), dd("100"), &rem, 4); err != nil || !got.Allowed || got.ContractRemaining == nil {
		t.Fatalf("within both: %+v %v", got, err)
	}
	// The value limit binds first when it is the smaller.
	if got, err := domain.CheckUnplanned(dd("1450"), dd("1400"), dd("60"), &rem, 4); err != nil || got.Allowed ||
		strings.Contains(got.Detail, "contract") || !got.Remaining.Equal(dd("50")) {
		t.Fatalf("value limit below the contract: %+v %v", got, err)
	}
}

func TestContractRemaining(t *testing.T) {
	value := domain.AgreementLine{CommitmentType: domain.CommitValue, CommittedAmount: dd("10000"), ReleasedAmount: dd("2500.50")}
	if got := domain.ContractRemaining(value, 2); !got.Equal(dd("7499.50")) {
		t.Errorf("value commitment: %s", got)
	}
	qty := domain.AgreementLine{CommitmentType: domain.CommitQuantity, CommittedQty: dd("100"), ReleasedQty: dd("40"), UnitPrice: dd("10"), DiscountPct: dd("10")}
	if got := domain.ContractRemaining(qty, 2); !got.Equal(dd("540")) {
		t.Errorf("quantity commitment: 60 x 9.00 = %s", got)
	}
	over := domain.AgreementLine{CommitmentType: domain.CommitValue, CommittedAmount: dd("100"), ReleasedAmount: dd("150")}
	if got := domain.ContractRemaining(over, 2); !got.IsZero() {
		t.Errorf("an overrun leaves nothing: %s", got)
	}
}

func TestValidateServiceTerms(t *testing.T) {
	ok := domain.ServiceTerms{ValueLimit: dd("2000"), ExpectedValue: dd("500")}
	if err := domain.ValidateServiceTerms(ok, dd("900"), dd("1000")); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		terms         domain.ServiceTerms
		planned, line string
		code          string
	}{
		"expected above limit": {domain.ServiceTerms{ValueLimit: dd("100"), ExpectedValue: dd("101")}, "0", "1000", domain.CodeServicePlanned},
		"planned above line":   {ok, "1000.01", "1000", domain.CodeServicePlanned},
		"negative limit":       {domain.ServiceTerms{ValueLimit: dd("-1")}, "0", "1000", domain.CodeValidation},
		"limit 7 decimals":     {domain.ServiceTerms{ValueLimit: dd("1.0000001")}, "0", "1000", domain.CodeValidation},
	} {
		t.Run(name, func(t *testing.T) {
			if err := domain.ValidateServiceTerms(c.terms, dd(c.planned), dd(c.line)); svcCodeOf(t, err) != c.code {
				t.Fatalf("got %v, want %s", err, c.code)
			}
		})
	}
}

func TestCheckServiceTarget(t *testing.T) {
	for _, s := range []string{domain.POStatusDraft, domain.POStatusRejected} {
		if err := domain.CheckServiceTarget(s, "OPEN"); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
	for _, s := range []string{domain.POStatusInReview, domain.POStatusApproved, domain.POStatusConfirmed, domain.POStatusCancelled} {
		if err := domain.CheckServiceTarget(s, "OPEN"); svcCodeOf(t, err) != domain.CodeServiceTarget {
			t.Errorf("%s: %v", s, err)
		}
	}
	if err := domain.CheckServiceTarget(domain.POStatusDraft, domain.POLineCancelled); svcCodeOf(t, err) != domain.CodeServiceTarget {
		t.Errorf("cancelled line: %v", err)
	}
}

func TestCheckModelApplies(t *testing.T) {
	m := domain.ModelService{Code: "M", LegalEntityID: "le1", Currency: "USD", Status: domain.ModelActive}
	if err := domain.CheckModelApplies(m, "le1", "USD"); err != nil {
		t.Fatal(err)
	}
	inactive := m
	inactive.Status = domain.ModelInactive
	for name, c := range map[string]struct {
		m       domain.ModelService
		le, cur string
	}{"inactive": {inactive, "le1", "USD"}, "other entity": {m, "le2", "USD"}, "other currency": {m, "le1", "EUR"}} {
		if err := domain.CheckModelApplies(c.m, c.le, c.cur); svcCodeOf(t, err) != domain.CodeServiceModel {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCheckContractLine(t *testing.T) {
	a := domain.Agreement{Number: "AGR-1", LegalEntityID: "le1", VendorID: "v1", Currency: "USD", Status: domain.AgreementEffective}
	if err := domain.CheckContractLine(a, "le1", "v1", "USD"); err != nil {
		t.Fatal(err)
	}
	held := a
	held.Status = "ON_HOLD"
	if err := domain.CheckContractLine(held, "le1", "v1", "USD"); svcCodeOf(t, err) != domain.CodeServicePlanned {
		t.Errorf("on hold: %v", err)
	}
	for _, c := range [][3]string{{"le2", "v1", "USD"}, {"le1", "v2", "USD"}, {"le1", "v1", "EUR"}} {
		if err := domain.CheckContractLine(a, c[0], c[1], c[2]); svcCodeOf(t, err) != domain.CodeServicePlanned {
			t.Errorf("%v: %v", c, err)
		}
	}
}

func TestValidModelCode(t *testing.T) {
	for code, want := range map[string]bool{"MAINT-1": true, "A_B": true, "": false, "lower": false, "HAS SPACE": false, strings.Repeat("A", 21): false, strings.Repeat("A", 20): true} {
		if domain.ValidModelCode(code) != want {
			t.Errorf("%q: want %v", code, want)
		}
	}
}

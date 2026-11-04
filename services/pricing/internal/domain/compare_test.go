package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func TestCompare_DiscountStackingVersusExclusionGroup(t *testing.T) {
	stacked := proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc), step(30, domain.TypeMultiline))
	best := proc(2, step(10, domain.TypePrice), grouped(20, domain.TypeLineDisc, "BEST"), grouped(30, domain.TypeMultiline, "BEST"))
	cmp := domain.Compare(resolveWith(stacked), resolveWith(best))
	if cmp.Identical || cmp.ChangedLines != 1 {
		t.Fatalf("versions price the line differently: %+v", cmp)
	}
	l := cmp.Lines[0]
	// 100 less 8% then 12% = 80.96; BEST keeps 12% only = 88.00.
	if !l.LineNetA.Equal(d("80.96")) || !l.LineNetB.Equal(d("88")) || !l.LineNetDelta.Equal(d("7.04")) {
		t.Fatalf("line nets %s -> %s delta %s; want 80.96 -> 88 delta 7.04", l.LineNetA, l.LineNetB, l.LineNetDelta)
	}
	if len(l.OnlyA) != 1 || len(l.OnlyB) != 0 {
		t.Fatalf("version A applied one more condition than B: onlyA=%v onlyB=%v", l.OnlyA, l.OnlyB)
	}
	if !cmp.OrderNetDelta.Equal(d("7.04")) {
		t.Fatalf("order net delta = %s; want 7.04", cmp.OrderNetDelta)
	}
}

func TestCompare_SameProcedureIsIdentical(t *testing.T) {
	p := proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc))
	cmp := domain.Compare(resolveWith(p), resolveWith(p))
	if !cmp.Identical || cmp.ChangedLines != 0 || !cmp.OrderNetDelta.IsZero() {
		t.Fatalf("a procedure compared with itself must be identical: %+v", cmp)
	}
}

func TestCompare_ErrorOnOneSideIsChanged(t *testing.T) {
	// Version B is priced against records that hold no price, so the line fails (PRC-0001) while A prices it.
	p := proc(1, step(10, domain.TypePrice))
	a := resolveWith(p)
	b := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Procedure: p})
	cmp := domain.Compare(a, b)
	if !cmp.Lines[0].Changed || cmp.Lines[0].ErrorB != domain.CodePriceNotFound || cmp.Lines[0].ErrorA != "" {
		t.Fatalf("a missing price on one side must show as a change with its code: %+v", cmp.Lines[0])
	}
}

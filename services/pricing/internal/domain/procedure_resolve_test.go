package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

func discRecords() []domain.Record {
	price := rec("price", domain.TypePrice, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "100.00")
	line := rec("line8", domain.TypeLineDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "0")
	line.DiscPct1 = d("8")
	multi := rec("multi12", domain.TypeMultiline, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "0")
	multi.DiscPct1 = d("12")
	return []domain.Record{price, line, multi}
}

func proc(version int, steps ...domain.ProcedureStep) *domain.Procedure {
	return &domain.Procedure{ID: "proc-1", Name: "AX_STANDARD", Version: version, Status: domain.ProcedureActive, Steps: steps}
}

func step(no int, t domain.ConditionType) domain.ProcedureStep {
	return domain.ProcedureStep{StepNo: no, ConditionType: t}
}

func grouped(no int, t domain.ConditionType, mode string) domain.ProcedureStep {
	s := step(no, t)
	s.ExclusionGroup, s.ExclusionMode = "DISC", mode
	return s
}

func resolveWith(p *domain.Procedure) domain.Result {
	return domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: discRecords(), Procedure: p})
}

func TestProcedure_NoGroupStacksBothDiscounts(t *testing.T) {
	res := resolveWith(proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc), step(30, domain.TypeMultiline)))
	if got := res.Lines[0].LineNet; !got.Equal(d("80.96")) {
		t.Fatalf("stacked net = %s; want 80.96", got)
	}
	if res.Procedure == nil || res.Procedure.Name != "AX_STANDARD" || res.Procedure.Version != 1 {
		t.Fatalf("result must name the procedure version, got %+v", res.Procedure)
	}
	if a := res.Lines[0].Applied; a[1].StepNo != 20 || a[2].StepNo != 30 {
		t.Fatalf("applied conditions must carry their step numbers: %+v", a)
	}
}

func TestProcedure_ExclusionGroupBestKeepsLowestNet(t *testing.T) {
	// AC-26.6: BEST with LINE_DISC 8% and 12% keeps only 12%.
	res := resolveWith(proc(1, step(10, domain.TypePrice), grouped(20, domain.TypeLineDisc, domain.ExclusionBest), grouped(30, domain.TypeMultiline, domain.ExclusionBest)))
	lr := res.Lines[0]
	if !lr.LineNet.Equal(d("88")) || len(lr.Applied) != 2 || lr.Applied[1].RecordID != "multi12" {
		t.Fatalf("BEST: net = %s applied %+v; want 88 via multi12 only", lr.LineNet, lr.Applied)
	}
	var loser string
	for _, tr := range lr.Trace {
		if tr.Condition == domain.TypeLineDisc {
			loser = tr.Reason
		}
	}
	if loser != "excluded: BEST of exclusion group DISC kept step 30" {
		t.Fatalf("the losing member must say why, got %q", loser)
	}
}

func TestProcedure_ExclusionGroupFirstKeepsLowestStep(t *testing.T) {
	res := resolveWith(proc(1, step(10, domain.TypePrice), grouped(20, domain.TypeLineDisc, domain.ExclusionFirst), grouped(30, domain.TypeMultiline, domain.ExclusionFirst)))
	lr := res.Lines[0]
	if !lr.LineNet.Equal(d("92")) || len(lr.Applied) != 2 || lr.Applied[1].RecordID != "line8" {
		t.Fatalf("FIRST: net = %s applied %+v; want 92 via line8 only", lr.LineNet, lr.Applied)
	}
}

func TestProcedure_GroupFallsThroughToTheMemberThatMatched(t *testing.T) {
	recs := discRecords()[:2] // no multiline record
	res := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs,
		Procedure: proc(1, step(10, domain.TypePrice), grouped(20, domain.TypeLineDisc, domain.ExclusionBest), grouped(30, domain.TypeMultiline, domain.ExclusionBest))})
	if got := res.Lines[0].LineNet; !got.Equal(d("92")) {
		t.Fatalf("net = %s; want the only matching member, 92", got)
	}
}

func TestProcedure_StatisticalStepReportsWithoutChangingNet(t *testing.T) {
	stat := step(20, domain.TypeLineDisc)
	stat.Statistical, stat.AccountKey = true, "DISC-STAT"
	res := resolveWith(proc(2, step(10, domain.TypePrice), stat, step(30, domain.TypeMultiline)))
	lr := res.Lines[0]
	if !lr.LineNet.Equal(d("88")) {
		t.Fatalf("net = %s; want 88 (only the 12%% step changes it)", lr.LineNet)
	}
	a := lr.Applied[1]
	if !a.Statistical || a.AccountKey != "DISC-STAT" || a.Amount == nil || !a.Amount.Equal(d("-8")) {
		t.Fatalf("statistical step must report -8.00 on its account key, got %+v", a)
	}
}

func TestProcedure_StepOrderAndOrderLevelSteps(t *testing.T) {
	recs := discRecords()
	total := rec("tot", domain.TypeTotalDisc, domain.CustomerKindAll, "", domain.ProductKindAll, "", "USD", "0", "0")
	total.DiscPct1 = d("10")
	recs = append(recs, total)
	with := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs,
		Procedure: proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc), step(90, domain.TypeTotalDisc))})
	if !with.TotalDiscount.Equal(d("9.20")) || !with.OrderNet.Equal(d("82.80")) {
		t.Fatalf("total discount = %s order net = %s; want 9.20 and 82.80", with.TotalDiscount, with.OrderNet)
	}
	without := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs,
		Procedure: proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc))})
	if !without.TotalDiscount.IsZero() || !without.OrderNet.Equal(d("92")) {
		t.Fatalf("a procedure without TOTAL_DISC must not apply it: %s / %s", without.TotalDiscount, without.OrderNet)
	}
	stat := step(90, domain.TypeTotalDisc)
	stat.Statistical = true
	st := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: recs,
		Procedure: proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc), stat)})
	if !st.TotalDiscount.IsZero() || !st.OrderNet.Equal(d("92")) || len(st.Applied) != 1 || st.Applied[0].Amount == nil || !st.Applied[0].Amount.Equal(d("-9.20")) {
		t.Fatalf("a statistical total discount is reported, not applied: %+v net %s", st.Applied, st.OrderNet)
	}
}

func TestProcedure_VersionIsPartOfTheExplainReference(t *testing.T) {
	steps := []domain.ProcedureStep{step(10, domain.TypePrice), step(20, domain.TypeLineDisc)}
	_, a, _ := domain.ExplainTrace(resolveWith(proc(1, steps...)))
	_, again, _ := domain.ExplainTrace(resolveWith(proc(1, steps...)))
	_, other, _ := domain.ExplainTrace(resolveWith(proc(2, steps...)))
	if a != again || a == other {
		t.Fatalf("same version must share a reference and another version must not: %s %s %s", a, again, other)
	}
}

func TestProcedure_ReferenceOrderIsUnchangedWithoutOne(t *testing.T) {
	plain := domain.Resolve(domain.Input{Header: header("USD"), Lines: []domain.Line{line("1")}, Records: discRecords()})
	if plain.Procedure != nil || !plain.Lines[0].LineNet.Equal(d("80.96")) {
		t.Fatalf("reference order must stack both and name no procedure: %+v", plain)
	}
}

func TestProcedure_PriceStepCarriesItsAccountKey(t *testing.T) {
	price := step(10, domain.TypePrice)
	price.AccountKey = "ERL"
	lr := resolveWith(proc(1, price, step(20, domain.TypeLineDisc))).Lines[0]
	if a := lr.Applied[0]; a.Type != domain.TypePrice || a.StepNo != 10 || a.AccountKey != "ERL" {
		t.Fatalf("the PRICE condition must carry its step and account key, got %+v", a)
	}
}

func TestProcedure_RepeatedTypeAppliesItsRecordOnce(t *testing.T) {
	// A procedure stored before ValidateSteps refused repeated types must not discount twice.
	res := resolveWith(proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc), step(30, domain.TypeLineDisc)))
	lr := res.Lines[0]
	if !lr.LineNet.Equal(d("92")) || len(lr.Applied) != 2 {
		t.Fatalf("net = %s applied %+v; want 92 with LINE_DISC once", lr.LineNet, lr.Applied)
	}
	grouped := resolveWith(proc(1, step(10, domain.TypePrice), step(20, domain.TypeLineDisc),
		grouped(30, domain.TypeLineDisc, domain.ExclusionBest), grouped(40, domain.TypeMultiline, domain.ExclusionBest)))
	if got := grouped.Lines[0]; !got.LineNet.Equal(d("80.96")) || len(got.Applied) != 3 {
		t.Fatalf("net = %s applied %+v; want 8%% once, then the group's 12%%", got.LineNet, got.Applied)
	}
}

package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// orderObs builds one quality order's variable results (one value per sample).
func orderObs(order string, at time.Time, values ...string) []Observation {
	var out []Observation
	for i, v := range values {
		out = append(out, Observation{QualityOrderID: order, SampleNo: i + 1, Value: dec(v), HasValue: true, RecordedAt: at})
	}
	return out
}

func TestVariableSubgroupsModalSize(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var obs []Observation
	// Three subgroups of size 3 and one of size 5 (a different sample plan): the
	// size-5 order is skipped, not mixed into the chart.
	obs = append(obs, orderObs("a", t0, "10", "10.1", "9.9")...)
	obs = append(obs, orderObs("b", t0.Add(time.Hour), "10.2", "9.8", "10")...)
	obs = append(obs, orderObs("c", t0.Add(2*time.Hour), "9.7", "10.3", "10")...)
	obs = append(obs, orderObs("d", t0.Add(3*time.Hour), "10", "10", "10", "10", "10")...)

	_, subs, skipped := variableSubgroups(obs)
	if len(subs) != 3 || skipped != 1 {
		t.Fatalf("subgroups = %d skipped = %d, want 3 and 1", len(subs), skipped)
	}
	for _, s := range subs {
		if len(s) != 3 {
			t.Fatalf("subgroup size %d, want 3", len(s))
		}
	}
}

func TestVariableSubgroupsTieGoesToLargerSize(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var obs []Observation
	obs = append(obs, orderObs("a", t0, "1", "2")...)
	obs = append(obs, orderObs("b", t0, "1", "2", "3")...)
	_, subs, skipped := variableSubgroups(obs)
	if len(subs) != 1 || len(subs[0]) != 3 || skipped != 1 {
		t.Fatalf("got %d subgroups of first size %d, skipped %d", len(subs), len(subs[0]), skipped)
	}
}

func TestComputeSPCVariable(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var obs []Observation
	for i := 0; i < 5; i++ {
		obs = append(obs, orderObs(fmt.Sprintf("o%d", i), t0.Add(time.Duration(i)*time.Hour),
			"10.0", "10.2", "9.8", "10.1")...)
	}
	res, err := ComputeSPC(obs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Chart != ChartXbarR || res.XBarR == nil || res.PChart != nil {
		t.Fatalf("want XBAR_R only, got %+v", res)
	}
	if res.Observations != 20 || res.SkippedSubgroups != 0 {
		t.Fatalf("observations = %d skipped = %d", res.Observations, res.SkippedSubgroups)
	}
}

func TestComputeSPCAttribute(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var obs []Observation
	// Order a: 4 samples, one fails. Order b: 4 samples, none fail.
	for i := 1; i <= 4; i++ {
		obs = append(obs, Observation{QualityOrderID: "a", SampleNo: i, Passed: i != 2, RecordedAt: t0})
		obs = append(obs, Observation{QualityOrderID: "b", SampleNo: i, Passed: true, RecordedAt: t0.Add(time.Hour)})
	}
	res, err := ComputeSPC(obs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Chart != ChartP || res.PChart == nil || res.XBarR != nil {
		t.Fatalf("want P only, got %+v", res)
	}
}

func TestAttributeSamplesCountsDistinctSamples(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	// Sample 1 has a passing and a failing retest-style row: it counts once, as failed.
	obs := []Observation{
		{QualityOrderID: "a", SampleNo: 1, Passed: true, RecordedAt: t0},
		{QualityOrderID: "a", SampleNo: 1, Passed: false, RecordedAt: t0},
		{QualityOrderID: "a", SampleNo: 2, Passed: true, RecordedAt: t0},
	}
	_, s := attributeSamples(obs)
	if len(s) != 1 || s[0].Size != 2 || s[0].Defectives != 1 {
		t.Fatalf("samples = %+v, want size 2 defectives 1", s)
	}
}

func TestComputeSPCNoDataIsAnEmptyChart(t *testing.T) {
	res, err := ComputeSPC(nil)
	if err != nil || res.Chart != ChartNone || res.Observations != 0 || res.XBarR != nil || res.PChart != nil {
		t.Fatalf("no observations = %+v %v, want chart NONE", res, err)
	}
}

// Each chart point names its quality order, oldest first, and an order whose sample
// size differs from the modal size is neither a point nor an order id.
func TestComputeSPCOrderIDsFollowPoints(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	obs := append(orderObs("late", t0.Add(2*time.Hour), "10", "11"), orderObs("early", t0, "10", "12")...)
	obs = append(obs, orderObs("odd", t0.Add(time.Hour), "10", "11", "12")...)
	res, err := ComputeSPC(obs)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.OrderIDs) != "[early late]" || res.SkippedSubgroups != 1 || len(res.XBarR.Xbar) != 2 {
		t.Fatalf("orders %v skipped %d points %d", res.OrderIDs, res.SkippedSubgroups, len(res.XBarR.Xbar))
	}
}

func TestComputeSPCIndividualsRejected(t *testing.T) {
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var obs []Observation
	for i := 0; i < 3; i++ {
		obs = append(obs, orderObs(fmt.Sprintf("o%d", i), t0.Add(time.Duration(i)*time.Hour), "10")...)
	}
	_, err := ComputeSPC(obs)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeSPCUnsupported {
		t.Fatalf("subgroup size 1 is outside the X-bar/R range: %v, want %s", err, CodeSPCUnsupported)
	}
}

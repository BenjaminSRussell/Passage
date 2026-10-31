package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCountVarianceQtyPrefersThePostedVariance(t *testing.T) {
	posted := decimal.NewFromInt(-4)
	if got := CountVarianceQty(decimal.NewFromInt(50), decimal.NewFromInt(46), &posted); !got.Equal(posted) {
		t.Fatalf("posted: %s", got)
	}
	if got := CountVarianceQty(decimal.NewFromInt(10), decimal.NewFromInt(12), nil); !got.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("provisional: %s", got)
	}
}

func TestSummarizeVarianceByCounter(t *testing.T) {
	d := decimal.NewFromInt
	out := SummarizeVarianceByCounter([]string{"bob", "ann", "bob", ""}, []decimal.Decimal{d(-2), d(5), d(3), d(0)}, []decimal.Decimal{d(-10), d(7), d(4), d(0)})
	if len(out) != 3 || out[0].Counter != "" || out[1].Counter != "ann" || out[2].Counter != "bob" {
		t.Fatalf("order %+v", out)
	}
	bob := out[2]
	if bob.Lines != 2 || !bob.GainValue.Equal(d(4)) || !bob.LossValue.Equal(d(10)) || !bob.NetValue.Equal(d(-6)) || !bob.AbsVarianceQt.Equal(d(5)) {
		t.Fatalf("bob %+v", bob)
	}
}

func TestTraceDirectionAndPhysicalStatuses(t *testing.T) {
	for _, d := range []string{TraceBoth, TraceForward, TraceBackward} {
		if !ValidTraceDirection(d) {
			t.Errorf("%s rejected", d)
		}
	}
	if ValidTraceDirection("up") || ValidTraceDirection("") {
		t.Error("unknown direction accepted")
	}
	for _, c := range []struct {
		issue, receipt string
		want           bool
	}{{"SOLD", "", true}, {"DEDUCTED", "", true}, {"PICKED", "", true}, {"", "RECEIVED", true}, {"", "PURCHASED", true}, {"", "REGISTERED", true},
		{"ON_ORDER", "", false}, {"RESERVED_PHYSICAL", "", false}, {"", "ORDERED", false}, {"", "ARRIVED", false}} {
		if got := MovesPhysicalStock(c.issue, c.receipt); got != c.want {
			t.Errorf("%q/%q = %v", c.issue, c.receipt, got)
		}
	}
}

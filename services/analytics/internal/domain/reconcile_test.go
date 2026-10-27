package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCompare_ToleranceBoundary(t *testing.T) {
	tol := DefaultTolerance()
	tests := []struct {
		name, semantic, source, wantDelta, wantStatus string
	}{
		{"equal values", "100.00", "100.00", "0", ReconOK},
		{"delta exactly at tolerance is OK", "100.005", "100.00", "0.005", ReconOK},
		{"delta just above tolerance fails", "100.006", "100.00", "0.006", ReconFailed},
		{"negative delta beyond tolerance fails", "99.99", "100.00", "-0.01", ReconFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			delta, status := Compare(decimal.RequireFromString(tc.semantic), decimal.RequireFromString(tc.source), tol)
			if !delta.Equal(decimal.RequireFromString(tc.wantDelta)) || status != tc.wantStatus {
				t.Fatalf("Compare = (%s, %s), want (%s, %s)", delta, status, tc.wantDelta, tc.wantStatus)
			}
		})
	}
}

func TestCompare_OKIffWithinTolerance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		semantic := decimal.New(rapid.Int64Range(-1e12, 1e12).Draw(t, "semantic"), -2)
		source := decimal.New(rapid.Int64Range(-1e12, 1e12).Draw(t, "source"), -2)
		delta, status := Compare(semantic, source, DefaultTolerance())
		within := delta.Abs().LessThanOrEqual(DefaultTolerance())
		if within != (status == ReconOK) {
			t.Fatalf("delta %s status %s: within tolerance %v", delta, status, within)
		}
	})
}

func TestNextTrust_ResetsOnAnyFailure(t *testing.T) {
	count := 0
	for day := 0; day < TrustThresholdDays; day++ {
		count = NextTrust(count, ReconOK)
	}
	if !IsTrusted(count) {
		t.Fatalf("after %d OK days count = %d, want trusted", TrustThresholdDays, count)
	}
	if NextTrust(count, ReconFailed) != 0 || NextTrust(count, ReconBlocked) != 0 {
		t.Fatal("a FAILED or BLOCKED day must reset the trust counter")
	}
	if IsTrusted(TrustThresholdDays - 1) {
		t.Fatal("29 days must not yet be trusted")
	}
}

func TestFinalizeReconciliation_DerivesDeltaAndStatus(t *testing.T) {
	base := Reconciliation{MetricID: "revenue", LegalEntityID: "11111111-0000-4000-8000-000000000001", Date: "2026-10-26",
		Currency: "USD", Tolerance: DefaultTolerance()}
	tests := []struct {
		name, semantic, source, claimed, wantDelta, wantStatus string
	}{
		{"caller claims OK but delta is over tolerance", "100.01", "100.00", ReconOK, "0.01", ReconFailed},
		{"caller claims FAILED but values agree to the cent", "100.00", "100.00", ReconFailed, "0", ReconOK},
		{"no status given", "99.996", "100.00", "", "-0.004", ReconOK},
		{"BLOCKED survives even inside tolerance", "100.00", "100.00", ReconBlocked, "0", ReconBlocked},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.SemanticValue, in.SourceValue = decimal.RequireFromString(tc.semantic), decimal.RequireFromString(tc.source)
			in.Delta, in.Status = decimal.NewFromInt(999), tc.claimed
			got, err := FinalizeReconciliation(in)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Delta.Equal(decimal.RequireFromString(tc.wantDelta)) || got.Status != tc.wantStatus {
				t.Fatalf("got (%s, %s), want (%s, %s)", got.Delta, got.Status, tc.wantDelta, tc.wantStatus)
			}
		})
	}
}

func TestFinalizeReconciliation_RejectsBadInput(t *testing.T) {
	good := Reconciliation{MetricID: "revenue", LegalEntityID: "11111111-0000-4000-8000-000000000001", Date: "2026-10-26",
		Currency: "USD", Tolerance: DefaultTolerance()}
	tests := []struct {
		field  string
		mutate func(*Reconciliation)
	}{
		{"metricId", func(r *Reconciliation) { r.MetricID = "Revenue" }},
		{"legalEntityId", func(r *Reconciliation) { r.LegalEntityID = "USMF" }},
		{"date", func(r *Reconciliation) { r.Date = "26/10/2026" }},
		{"currency", func(r *Reconciliation) { r.Currency = "usd" }},
		{"tolerance", func(r *Reconciliation) { r.Tolerance = decimal.RequireFromString("-0.001") }},
		{"tolerance", func(r *Reconciliation) { r.Tolerance = decimal.RequireFromString("1.01") }},
		{"status", func(r *Reconciliation) { r.Status = "TRUSTED" }},
	}
	for _, tc := range tests {
		in := good
		tc.mutate(&in)
		_, err := FinalizeReconciliation(in)
		if err == nil || err.Code != CodeValidation || err.Fields[tc.field] == "" {
			t.Errorf("%s: err = %+v, want a %s field error", tc.field, err, tc.field)
		}
	}
}

package domain

import (
	"testing"
	"time"
)

func TestComputeCostMatchesTheCostMeter(t *testing.T) {
	rates, err := ParseCostRates(`{"cpu_core_hours":"0.04","memory_gb_hours":"0.005","storage_gb":"0.10","kafka_gb":"0.02","lakehouse_gb":"0.023","ai_tokens":"0.000002"}`)
	if err != nil {
		t.Fatal(err)
	}
	qty, err := ParseQuantities(map[string]string{"cpu_core_hours": "1000", "memory_gb_hours": "4000", "storage_gb": "500", "kafka_gb": "200", "lakehouse_gb": "300", "ai_tokens": "1000000"})
	if err != nil {
		t.Fatal(err)
	}
	// docs/ops/tools/test_cost_meter.py: subtotal 122.90, USD 137.65, EUR 126.86 at 1.0850 USD per EUR.
	st, err := ComputeCost("t1", "2026-10", qty, rates, "1.0850", "v1")
	if err != nil || st.CostUSD != "137.65" || st.CostEUR != "126.86" {
		t.Fatalf("statement = %+v err=%v", st, err)
	}
	zero := map[string]string{}
	for _, d := range CostDimensions {
		zero[d] = "0"
	}
	zq, _ := ParseQuantities(zero)
	if st, _ := ComputeCost("t1", "2026-01", zq, rates, "1.1", "v1"); st.CostUSD != "0.00" || st.CostEUR != "0.00" {
		t.Fatalf("zero = %+v", st)
	}
	for _, bad := range []struct{ month, fx, model string }{{"2026-13", "1.1", "v"}, {"2026-10", "0", "v"}, {"2026-10", "-1", "v"}, {"2026-10", "1.1", " "}} {
		if _, err := ComputeCost("t1", bad.month, qty, rates, bad.fx, bad.model); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if _, err := ParseQuantities(map[string]string{"cpu_core_hours": "1"}); err == nil {
		t.Fatal("missing dimensions accepted")
	}
	if _, err := ParseCostRates(`{"cpu_core_hours":"0.04","memory_gb_hours":"0.005","storage_gb":"0.10","kafka_gb":"0.02","lakehouse_gb":"0.023","ai_tokens":"0.000002","overhead":"1.5"}`); err == nil {
		t.Fatal("overhead above 1 accepted")
	}
}

func TestValidateProvisionUsesTheResidencyTable(t *testing.T) {
	deployed := []Region{{Name: "eu-west"}, {Name: "us-east"}}
	ok, err := ValidateProvision(ProvisionRequest{TenantID: "t", Name: " Acme ", Residency: "eu", Region: "EU-WEST"}, deployed)
	if err != nil || ok.Residency != "EU" || ok.Region != "eu-west" || ok.Tier != "PROD" || ok.Name != "Acme" {
		t.Fatalf("ok = %+v err=%v", ok, err)
	}
	for _, bad := range []ProvisionRequest{
		{Name: "A", Residency: "EU", Region: "us-east"},
		{Name: "A", Residency: "EU", Region: ""},
		{Name: "A", Residency: "EU", Region: "eu-central"},
		{Name: "A", Residency: "MARS", Region: "eu-west"},
		{Name: "", Residency: "EU", Region: "eu-west"},
		{Name: "A", Residency: "EU", Region: "eu-west", Tier: "STAGING"},
	} {
		if _, err := ValidateProvision(bad, deployed); err == nil {
			t.Fatalf("%+v accepted", bad)
		}
	}
	if _, err := ValidateProvision(ProvisionRequest{Name: "A", Residency: "GLOBAL", Region: "us-east", Tier: "dev"}, deployed); err != nil {
		t.Fatalf("GLOBAL tenant: %v", err)
	}
}

func TestEvaluateSLOTargetsAndOverrides(t *testing.T) {
	p95 := 0.8
	rep := EvaluateSLO("1h", []SLOSample{{Service: "b", Requests: 1000, Failed: 0, P95: &p95}, {Service: "a", Requests: 1000, Failed: 2, P95: &p95}},
		SLOTargets{Availability: 0.999, LatencyP95: 2 * time.Second, LatencyP95By: map[string]time.Duration{"b": 500 * time.Millisecond}}, time.Unix(0, 0))
	if rep.Services[0].Service != "a" || !rep.Services[0].AvailabilityBreach || rep.Services[0].LatencyBreach {
		t.Fatalf("a = %+v", rep.Services[0])
	}
	if !rep.Services[1].LatencyBreach || rep.Services[1].AvailabilityBreach || rep.Breached != 2 {
		t.Fatalf("b = %+v", rep.Services[1])
	}
	if left := *rep.Services[0].ErrorBudgetLeft; left > -0.99 || left < -1.01 {
		t.Fatalf("a burned twice its budget: %v", left)
	}
}

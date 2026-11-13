package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func TestICChargeConsumer_IsSeededAndNeedsItsRevenueAccount(t *testing.T) {
	if !icChargeSeeded(tenant.ServicePrincipalTable()) {
		t.Fatal("service:billing-iccharge must be seeded with billing.iccharge.from-event")
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := icChargeConfig(env(nil)); err == nil {
		t.Fatal("the revenue account is required")
	}
	if _, err := icChargeConfig(env(map[string]string{"BILLING_IC_REVENUE_ACCOUNT": "4900", "BILLING_IC_PAYMENT_TERM_DAYS": "400"})); err == nil {
		t.Fatal("a payment term above 365 days is refused")
	}
	cfg, err := icChargeConfig(env(map[string]string{"BILLING_IC_REVENUE_ACCOUNT": "4900", "BILLING_IC_TAX_CODE": "IC19"}))
	if err != nil || cfg.PaymentTermDays != 30 || cfg.TaxCode != "IC19" {
		t.Fatalf("config %+v %v", cfg, err)
	}
}

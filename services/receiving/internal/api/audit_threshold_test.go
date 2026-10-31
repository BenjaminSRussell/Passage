package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func (rg *rig) putPolicy(t *testing.T, fields string, want int) string {
	t.Helper()
	rec := rg.call(http.MethodPut, auditPolicyPath, fmt.Sprintf(`{"legalEntityId":%q,%s}`, legalEntityID, fields), rg.policyMaker())
	if rec.Code != want {
		t.Fatalf("PUT audit policy %s: %d %s, want %d", fields, rec.Code, rec.Body.String(), want)
	}
	return rec.Body.String()
}

func TestAuditPolicy_ThresholdNeedsCurrencyAndBumpsRuleVersionOnlyOnChange(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.putPolicy(t, `"sampleRatePct":10,"thresholdAmount":500`, http.StatusBadRequest)
	rg.putPolicy(t, `"sampleRatePct":10,"thresholdCurrency":"USD"`, http.StatusBadRequest)
	rg.putPolicy(t, `"sampleRatePct":10,"thresholdAmount":0,"thresholdCurrency":"USD"`, http.StatusBadRequest)

	first := rg.putPolicy(t, `"sampleRatePct":10,"thresholdAmount":500,"thresholdCurrency":"USD"`, http.StatusOK)
	same := rg.putPolicy(t, `"sampleRatePct":10,"thresholdAmount":500,"thresholdCurrency":"USD"`, http.StatusOK)
	changed := rg.putPolicy(t, `"sampleRatePct":10,"thresholdAmount":750,"thresholdCurrency":"USD"`, http.StatusOK)

	for body, version := range map[string]string{first: `"ruleVersion":1`, same: `"ruleVersion":1`, changed: `"ruleVersion":2`} {
		if !strings.Contains(body, version) {
			t.Fatalf("policy body %s lacks %s", body, version)
		}
	}
	if !strings.Contains(first, `"thresholdAmount":"500"`) || !strings.Contains(first, `"thresholdCurrency":"USD"`) {
		t.Fatalf("threshold not echoed: %s", first)
	}
}

func TestAuditBlock_ThresholdScalesTheProbabilityDownForSmallInvoices(t *testing.T) {
	const invoices = 12
	tests := []struct {
		name        string
		policy      string
		wantBlocked bool
	}{
		{"flat rate blocks every invoice at 100 percent", `"sampleRatePct":100`, true},
		{"a threshold far above the invoice makes a draw nearly impossible", `"sampleRatePct":100,"thresholdAmount":1000000,"thresholdCurrency":"USD"`, false},
		{"an invoice at or above the threshold is drawn at the full rate", `"sampleRatePct":100,"thresholdAmount":1,"thresholdCurrency":"USD"`, true},
		{"a threshold in another currency is not applied", `"sampleRatePct":100,"thresholdAmount":1000000,"thresholdCurrency":"EUR"`, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
			rg.putPolicy(t, tc.policy, http.StatusOK)
			for i := range invoices {
				r := rg.posted(t, fmt.Sprintf("PS-%02d", i), 10, 1)
				got := rg.manualInvoice(t, fmt.Sprintf("INV-%02d", i), r.ID, 1, "alice").invoice(t, http.StatusCreated)
				if blocked := got.Status == "AUDIT_BLOCKED"; blocked != tc.wantBlocked {
					t.Fatalf("invoice %d is %s, blocked want %v", i, got.Status, tc.wantBlocked)
				}
			}
		})
	}
}

func TestAuditBlock_InvoiceRecordsTheRuleVersionOfItsDraw(t *testing.T) {
	rg := ersRig("2.60", nil, testSampler("seed-one-0123456789"))
	rg.putPolicy(t, `"sampleRatePct":100`, http.StatusOK)
	rg.putPolicy(t, `"sampleRatePct":100,"thresholdAmount":1,"thresholdCurrency":"USD"`, http.StatusOK)
	r := rg.posted(t, "PS-1", 10, 1)

	res := rg.manualInvoice(t, "INV-1", r.ID, 1, "alice")

	if res.code != http.StatusCreated || !strings.Contains(res.body, `"auditRuleVersion":2`) {
		t.Fatalf("%d %s", res.code, res.body)
	}
}

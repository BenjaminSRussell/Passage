package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

const (
	withholdingPath = "/v1/tax/withholding/calculate"
	withholdingDuty = "Calculate withholding tax"
	withholdingBody = `{"currency":"USD","party_type":"vendor","payment_date":"2026-06-30","code":"NEC","gross_amount":"1000.00","paid_to_date":"0",` +
		`"codes":[{"code":"NEC","jurisdiction":"US","income_type":"nonemployee_compensation","rates":[{"valid_from":"2024-01-01","rate_pct":"24","threshold":"600"}]}]}`
)

func TestWithholding(t *testing.T) {
	tenantID := testkit.TenantID(1)
	tests := []struct {
		name       string
		body       string
		tenant     string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"worked example", withholdingBody, tenantID, withholdingDuty, http.StatusOK, `"withholding_amount":"96"`},
		{"net payable", withholdingBody, tenantID, withholdingDuty, http.StatusOK, `"net_payable":"904"`},
		{"no tenant header", withholdingBody, "", "", http.StatusUnauthorized, "AUTH-0001"},
		{"calculate duty is not enough", withholdingBody, tenantID, duty, http.StatusForbidden, "AUTH-0003"},
		{"malformed JSON", `{`, tenantID, withholdingDuty, http.StatusBadRequest, "COM-0001"},
		{"unknown field", strings.Replace(withholdingBody, `"currency"`, `"x":1,"currency"`, 1), tenantID, withholdingDuty, http.StatusBadRequest, "COM-0001"},
		{"gross as JSON number", strings.Replace(withholdingBody, `"1000.00"`, `1000`, 1), tenantID, withholdingDuty, http.StatusBadRequest, "COM-0001"},
		{"zero gross", strings.Replace(withholdingBody, `"1000.00"`, `"0"`, 1), tenantID, withholdingDuty, http.StatusBadRequest, "COM-0001"},
		{"unknown code", strings.Replace(withholdingBody, `"code":"NEC","gross`, `"code":"X","gross`, 1), tenantID, withholdingDuty, http.StatusUnprocessableEntity, "TAX-0003"},
		{"no rate on date", strings.Replace(withholdingBody, `2026-06-30`, `2020-06-30`, 1), tenantID, withholdingDuty, http.StatusUnprocessableEntity, "TAX-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(fakeDB{}, nil)
			rec := do(r, http.MethodPost, withholdingPath, tc.body, tc.tenant, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

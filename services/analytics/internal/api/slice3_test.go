package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// The scorecard and archive inquiry filters (§9.46.14) are validated before the
// store: these requests never reach the (nil) database.
func TestSlice3_FilterValidationBeforeStore(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		name, path, field string
		headers           map[string]string
	}{
		{"rules unknown rule type", "/v1/catalog/dq-rules?ruleType=ACCURACY", "ruleType", steward},
		{"rules lower-case rule type", "/v1/catalog/dq-rules?ruleType=completeness", "ruleType", steward},
		{"rules owner not a uuid", "/v1/catalog/dq-rules?owner=alice", "owner", steward},
		{"partitions period not a month", "/v1/archive/partitions?period=2024-13", "period", archive},
		{"partitions period a day", "/v1/archive/partitions?period=2024-01-31", "period", archive},
		{"partitions period one-digit month", "/v1/archive/partitions?period=2024-1", "period", archive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, tc.path, "", tc.headers)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body.String())
			}
			p := problemCode(t, rec.Body.String())
			if p.Code != domain.CodeValidation || p.Errors[tc.field] == "" {
				t.Fatalf("want %s naming %s: %s", domain.CodeValidation, tc.field, rec.Body.String())
			}
		})
	}
}

// Rules filter by rule type and owner over HTTP; an upper-case owner id
// matches the stored (lower-case) UUID.
func TestDQRules_FilterByTypeAndOwner(t *testing.T) {
	h := dbServer(t, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	const otherOwner = "0193d6a0-0000-7000-8000-0000000000ab"
	unique := strings.NewReplacer(`"COMPLETENESS"`, `"UNIQUENESS"`, "0193d6a0-0000-7000-8000-000000000001", otherOwner).Replace(ruleBody)
	for path, body := range map[string]string{
		"/v1/catalog/dq-rules/vendor_tax_id":      ruleBody,
		"/v1/catalog/dq-rules/vendor_iban_unique": unique,
	} {
		if rec := do(t, h, http.MethodPut, path, body, steward); rec.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	ids := func(query string) []string {
		t.Helper()
		rec := do(t, h, http.MethodGet, "/v1/catalog/dq-rules"+query, "", steward)
		var list DQRulesResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil {
			t.Fatalf("list %s: %d %s", query, rec.Code, rec.Body.String())
		}
		out := []string{}
		for _, r := range list.Items {
			out = append(out, r.RuleID)
		}
		return out
	}
	if got := ids("?ruleType=UNIQUENESS"); len(got) != 1 || got[0] != "vendor_iban_unique" {
		t.Fatalf("ruleType=UNIQUENESS = %v", got)
	}
	if got := ids("?owner=" + strings.ToUpper(otherOwner)); len(got) != 1 || got[0] != "vendor_iban_unique" {
		t.Fatalf("owner (upper case) = %v", got)
	}
	if got := ids("?ruleType=COMPLETENESS&owner=" + otherOwner); len(got) != 0 {
		t.Fatalf("COMPLETENESS for the other owner = %v, want none", got)
	}
	if got := ids("?asset=core.vendor"); len(got) != 2 {
		t.Fatalf("asset filter = %v, want both rules", got)
	}
}

package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// shortSafetyBody is a run whose on-hand is below the safety stock, with the
// only sale well after today, so the safety-stock policy changes the plan.
const shortSafetyBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":[
 {"itemId":"` + fgID + `","coverage":{"code":"requirement"},"onHand":"3","safetyStock":"4",
  "orderType":"purchase","leadTimeDays":5,"lot":{},
  "demand":[{"ref":"SO-1","refType":"sales-line","date":"2026-11-22","qty":"10"}],
  "receipts":[],"components":[]}]}`

func TestCreateCoverageGroup_FirstIssuePolicyRoundTrips(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, coveragePath, `{"code":"REQ","coverageCode":"requirement","safetyStockPolicy":"first-issue"}`, dutySetup, true, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if g := decodeGroup(t, rec); g.SafetyStockPolicy != "first-issue" {
		t.Fatalf("policy %q, want first-issue", g.SafetyStockPolicy)
	}
	if st.groups[0].Spec.SafetyStockPolicy != "first-issue" {
		t.Fatalf("stored policy %q", st.groups[0].Spec.SafetyStockPolicy)
	}
}

// TestRun_SafetyStockPolicyFirstIssue: the policy sent inline moves the
// safety-stock order from today to the first issue date (BR-22.5), and an
// unknown policy is a 400 naming the field.
func TestRun_SafetyStockPolicyFirstIssue(t *testing.T) {
	_, today := runVersion(t, &fakeStore{}, shortSafetyBody, "k-today")
	first := strings.Replace(shortSafetyBody, `"coverage":{"code":"requirement"}`, `"coverage":{"code":"requirement","safetyStockPolicy":"first-issue"}`, 1)
	_, later := runVersion(t, &fakeStore{}, first, "k-first")
	if o := today.Result.Orders[0]; o.DeliveryDate != 5 || !o.Qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("policy today order %+v; want 1 delivered day 5", o)
	}
	for _, o := range later.Result.Orders {
		if o.DeliveryDate != 20 {
			t.Fatalf("first-issue order %+v; want every order delivered day 20", o)
		}
	}
	bad := strings.Replace(shortSafetyBody, `"coverage":{"code":"requirement"}`, `"coverage":{"code":"requirement","safetyStockPolicy":"weekly"}`, 1)
	rec := do(t, &fakeStore{}, http.MethodPost, "/v1/planning/plans/MAIN:run", bad, "planning.run", true, "k-bad")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "safetyStockPolicy") {
		t.Fatalf("status %d body %s; want 400 naming safetyStockPolicy", rec.Code, rec.Body.String())
	}
}

// TestRun_StoredCoverageGroupSuppliesSafetyPolicy: a bare item takes the
// policy of its stored coverage group.
func TestRun_StoredCoverageGroupSuppliesSafetyPolicy(t *testing.T) {
	c := requirementCoverage()
	c.PlannedOrderType, c.SafetyStockPolicy = "purchase", "first-issue"
	bare := strings.Replace(shortSafetyBody, `"coverage":{"code":"requirement"},`, ``, 1)
	bare = strings.Replace(bare, `"orderType":"purchase","leadTimeDays":5,`, ``, 1)
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: c}}
	_, req := runVersion(t, st, bare, "k-group")
	for _, o := range req.Result.Orders {
		if o.DeliveryDate != 20 {
			t.Fatalf("order %+v; want delivery on the first issue date (day 20)", o)
		}
	}
}

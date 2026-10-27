package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const (
	campaignDuties  = "pricing.view,pricing.maintain"
	procedureDuties = "pricing.view,pricing.admin"
	noCampaign      = "00000000-0000-0000-0011-999999999999"
	otherEntity     = "00000000-0000-0000-0001-000000000002"
	campaignBody    = `{"legalEntityId":"%s","name":"Spring push","validFrom":"2026-03-01","validTo":"2026-03-31",` +
		`"targetCustomerGroup":"%s","budget":"5000.00","currency":"USD"}`
	procedureBody = `{"legalEntityId":"%s","name":"AX_STANDARD","steps":[` +
		`{"stepNo":10,"conditionType":"PRICE"},{"stepNo":20,"conditionType":"LINE_DISC","exclusionGroup":"EG1","exclusionMode":"BEST"},` +
		`{"stepNo":30,"conditionType":"MULTILINE_DISC","exclusionGroup":"EG1","exclusionMode":"BEST"},` +
		`{"stepNo":40,"conditionType":"TOTAL_DISC","requirementCel":"order.net > 0"},{"stepNo":50,"conditionType":"SURCHARGE","accountKey":"ERS"}]}`
	campaignRecordBody = `{"legalEntityId":"%s","type":"PRICE","customerKind":"all","productKind":"product","productKey":"P1",
	"currency":"USD","unit":"EA","amount":"%s","validFrom":"2026-01-01","validTo":"2026-12-31","campaignId":"%s"}`
	groupResolveBody = `{"legalEntityId":"%s","customerId":"C1","customerGroupId":"%s","currency":"USD","pricingDate":"%s",
	"lines":[{"productId":"P1","unit":"EA","quantity":"1"}]}`
)

func createCampaign(t *testing.T, h http.Handler, group string) map[string]any {
	t.Helper()
	code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns", campaignDuties, fmt.Sprintf(campaignBody, legalEntity, group), "Idempotency-Key", "cmp-"+group)
	if code != http.StatusCreated {
		t.Fatalf("create campaign = %d %v", code, out)
	}
	return out
}

// activeP1Record creates and activates a PRICE record for P1, optionally in a campaign.
func activeP1Record(t *testing.T, h http.Handler, amount, campaignID string) {
	t.Helper()
	code, rec := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(campaignRecordBody, legalEntity, amount, campaignID))
	if code != http.StatusCreated || rec["campaignId"] != nilIfEmpty(campaignID) {
		t.Fatalf("create record = %d %v", code, rec)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+rec["id"].(string)+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("activate = %d %v", code, out)
	}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func resolvedPrice(t *testing.T, h http.Handler, group, day string) string {
	t.Helper()
	code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, fmt.Sprintf(groupResolveBody, legalEntity, group, day))
	if code != http.StatusOK {
		t.Fatalf("resolve = %d %v", code, out)
	}
	return out["lines"].([]any)[0].(map[string]any)["unitPrice"].(string)
}

// TestCampaign_KillSwitchGatesResolve covers FR-26.10 end to end: a campaign record wins
// over the plain price while the campaign is live, for its target group, inside its
// validity; the kill switch drops it from the next resolve and restore brings it back.
func TestCampaign_KillSwitchGatesResolve(t *testing.T) {
	h := router(newFake())
	c := createCampaign(t, h, "RETAIL")
	id := c["id"].(string)
	if c["budget"] != "5000" || c["killed"] != false || c["version"] != float64(1) || c["targetCustomerGroup"] != "RETAIL" {
		t.Fatalf("created campaign = %v", c)
	}
	activeP1Record(t, h, "9.00", "")
	activeP1Record(t, h, "8.00", id)

	if got := resolvedPrice(t, h, "RETAIL", "2026-03-15"); got != "8" {
		t.Fatalf("live campaign for its group: price %s, want 8", got)
	}
	if got := resolvedPrice(t, h, "WHOLESALE", "2026-03-15"); got != "9" {
		t.Fatalf("another customer group: price %s, want the plain 9", got)
	}
	if got := resolvedPrice(t, h, "RETAIL", "2026-04-01"); got != "9" {
		t.Fatalf("after the campaign: price %s, want the plain 9", got)
	}

	code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":kill", campaignDuties, `{"version":1}`, "Idempotency-Key", "kill-1")
	if code != http.StatusOK || out["killed"] != true || out["version"] != float64(2) {
		t.Fatalf("kill = %d %v", code, out)
	}
	if code, again := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":kill", campaignDuties, `{"version":1}`, "Idempotency-Key", "kill-1"); code != http.StatusOK || again["version"] != float64(2) {
		t.Fatalf("replayed kill = %d %v; want the first response", code, again)
	}
	if got := resolvedPrice(t, h, "RETAIL", "2026-03-15"); got != "9" {
		t.Fatalf("killed campaign: price %s, want the plain 9 on the next resolve", got)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":kill", campaignDuties, `{"version":2}`); code != http.StatusConflict || out["code"] != "PRC-0031" {
		t.Fatalf("kill a killed campaign = %d %v; want 409 PRC-0031", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":restore", campaignDuties, `{"version":1}`); code != http.StatusPreconditionFailed {
		t.Fatalf("stale restore = %d %v; want 412", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":restore", campaignDuties, `{"version":2}`); code != http.StatusOK || out["killed"] != false {
		t.Fatalf("restore = %d %v", code, out)
	}
	if got := resolvedPrice(t, h, "RETAIL", "2026-03-15"); got != "8" {
		t.Fatalf("restored campaign: price %s, want 8", got)
	}
}

func TestCampaign_ReadListAndValidation(t *testing.T) {
	h := router(newFake())
	c := createCampaign(t, h, "")
	id := c["id"].(string)
	if replay := createCampaign(t, h, ""); replay["id"] != id {
		t.Fatalf("replayed create = %v; want campaign %s", replay, id)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/campaigns/"+id, "pricing.view", ""); code != http.StatusOK || out["name"] != "Spring push" || out["validTo"] != "2026-03-31" {
		t.Fatalf("get = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/campaigns?legalEntityId="+legalEntity, "pricing.view", ""); code != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/campaigns?legalEntityId=nope", "pricing.view", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("list with a bad entity = %d %v; want 422", code, out)
	}

	bad := fmt.Sprintf(`{"legalEntityId":"%s","name":"","validFrom":"2026-03-31","validTo":"2026-03-01","budget":"-1","currency":"usd"}`, legalEntity)
	code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns", campaignDuties, bad)
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["name"] == nil || errs["validity"] == nil || errs["budget"] == nil || errs["currency"] == nil {
		t.Fatalf("invalid campaign = %d %v; want 422 naming name, validity, budget and currency", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns", campaignDuties,
		fmt.Sprintf(`{"legalEntityId":"%s","name":"X","validFrom":"2026-03-01","validTo":"2026-03-31","budget":"","currency":"USD"}`, legalEntity)); code != http.StatusUnprocessableEntity {
		t.Fatalf("missing budget = %d %v; want 422", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/campaigns/"+noCampaign, campaignDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0041" {
		t.Fatalf("unknown campaign = %d %v; want 404 PRC-0041", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":kill", campaignDuties, `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("kill without version = %d %v; want 422", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns", "pricing.view", fmt.Sprintf(campaignBody, legalEntity, "")); code != http.StatusForbidden {
		t.Fatalf("view-only create = %d %v; want 403", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/campaigns/"+id+":kill", "pricing.view", `{"version":1}`); code != http.StatusForbidden {
		t.Fatalf("view-only kill = %d %v; want 403", code, out)
	}
}

func TestCampaign_RecordMustNameAKnownCampaignOfItsEntity(t *testing.T) {
	h := router(newFake())
	id := createCampaign(t, h, "")["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(campaignRecordBody, legalEntity, "8", noCampaign)); code != http.StatusNotFound || out["code"] != "PRC-0041" {
		t.Fatalf("record in an unknown campaign = %d %v; want 404 PRC-0041", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(campaignRecordBody, otherEntity, "8", id)); code != http.StatusUnprocessableEntity {
		t.Fatalf("record in another entity's campaign = %d %v; want 422", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(campaignRecordBody, legalEntity, "8", "not-a-uuid")); code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed campaign id = %d %v; want 422", code, out)
	}
}

func TestProcedure_VersionsReviseAndActivation(t *testing.T) {
	h := router(newFake())
	code, v1 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(procedureBody, legalEntity), "Idempotency-Key", "proc-1")
	if code != http.StatusCreated || v1["version"] != float64(1) || v1["status"] != "DRAFT" || v1["revision"] != float64(1) || len(v1["steps"].([]any)) != 5 {
		t.Fatalf("create v1 = %d %v", code, v1)
	}
	step := v1["steps"].([]any)[1].(map[string]any)
	if step["exclusionGroup"] != "EG1" || step["exclusionMode"] != "BEST" {
		t.Fatalf("step 20 = %v; want exclusion group EG1 BEST", step)
	}
	if code, again := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(procedureBody, legalEntity), "Idempotency-Key", "proc-1"); code != http.StatusCreated || again["id"] != v1["id"] {
		t.Fatalf("replayed create = %d %v", code, again)
	}
	code, v2 := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, fmt.Sprintf(procedureBody, legalEntity))
	if code != http.StatusCreated || v2["version"] != float64(2) {
		t.Fatalf("create v2 = %d %v", code, v2)
	}
	id1, id2 := v1["id"].(string), v2["id"].(string)

	revise := `{"revision":%d,"steps":[{"stepNo":10,"conditionType":"PRICE"},{"stepNo":20,"conditionType":"LINE_DISC"}]}`
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id2+":revise", procedureDuties, fmt.Sprintf(revise, 1)); code != http.StatusOK ||
		out["revision"] != float64(2) || len(out["steps"].([]any)) != 2 {
		t.Fatalf("revise draft = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id2+":revise", procedureDuties, fmt.Sprintf(revise, 1)); code != http.StatusPreconditionFailed {
		t.Fatalf("stale revise = %d %v; want 412", code, out)
	}

	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id1+":activate", procedureDuties, ``, "Idempotency-Key", "act-1"); code != http.StatusOK || out["status"] != "ACTIVE" {
		t.Fatalf("activate v1 = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id1+":activate", procedureDuties, ``, "Idempotency-Key", "act-1"); code != http.StatusOK || out["status"] != "ACTIVE" {
		t.Fatalf("replayed activate = %d %v; want the first response", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id2+":activate", procedureDuties, ``); code != http.StatusOK || out["status"] != "ACTIVE" {
		t.Fatalf("activate v2 = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/procedures/"+id1, "pricing.view", ""); code != http.StatusOK || out["status"] != "SUPERSEDED" {
		t.Fatalf("v1 after v2 activation = %d %v; want SUPERSEDED", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id1+":activate", procedureDuties, ``); code != http.StatusConflict || out["code"] != "PRC-0008" {
		t.Fatalf("re-activate superseded = %d %v; want 409 PRC-0008", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/"+id2+":revise", procedureDuties, fmt.Sprintf(revise, 2)); code != http.StatusConflict || out["code"] != "PRC-0008" {
		t.Fatalf("revise an active version = %d %v; want 409 PRC-0008", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/procedures", "pricing.view", ""); code != http.StatusOK || len(out["items"].([]any)) != 2 {
		t.Fatalf("list = %d %v", code, out)
	}
}

func TestProcedure_ValidationNotFoundAndDuties(t *testing.T) {
	h := router(newFake())
	bad := fmt.Sprintf(`{"legalEntityId":"%s","name":"ax standard","steps":[{"stepNo":20,"conditionType":"PRICE"},{"stepNo":10,"conditionType":"NOPE"}]}`, legalEntity)
	code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, bad)
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || errs["name"] == nil || errs["steps[1].stepNo"] == nil || errs["steps[1].conditionType"] == nil {
		t.Fatalf("invalid procedure = %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures", procedureDuties, `{"legalEntityId":"x","name":"P","steps":[]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("no steps and bad entity = %d %v; want 422", code, out)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/procedures/"+noCampaign, procedureDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0042" {
		t.Fatalf("unknown procedure = %d %v; want 404 PRC-0042", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures/not-an-id:activate", procedureDuties, ``); code != http.StatusNotFound || out["code"] != "PRC-0042" {
		t.Fatalf("malformed id = %d %v; want 404 PRC-0042", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/procedures", campaignDuties, fmt.Sprintf(procedureBody, legalEntity)); code != http.StatusForbidden {
		t.Fatalf("PricingMaintain writing a procedure = %d %v; want 403 (PricingAdmin only)", code, out)
	}
}

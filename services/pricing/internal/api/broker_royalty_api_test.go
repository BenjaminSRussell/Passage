package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

const (
	brokerVendorID   = "00000000-0000-0000-0008-0000000000b1"
	licensorVendorID = "00000000-0000-0000-0008-0000000000c1"
)

func brokerBody(extra string) string {
	return `{"legalEntityId":"` + legalEntity + `","name":"Agent Smith","brokerVendorId":"` + brokerVendorID + `","productGroupId":"BRANDED",
		"rateKind":"PERCENT_NET","rate":"2.5","currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"` + extra + `}`
}

func royaltyBody(minimum string) string {
	return `{"legalEntityId":"` + legalEntity + `","name":"Brand licence","licensorVendorId":"` + licensorVendorID + `","productGroupId":"BRANDED",
		"basis":"NET_SALES","period":"MONTH","tiers":[{"from":"0","ratePct":"4"},{"from":"50000","ratePct":"5"}],"minimumRoyalty":"` + minimum + `",
		"currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`
}

func contractLineBody(line, date, net, qty, reverses string) string {
	r := ""
	if reverses != "" {
		r = `,"reversesInvoiceLineId":"` + reverses + `"`
	}
	return fmt.Sprintf(`{"invoiceLineId":"%s","invoiceDate":"%s","productGroupId":"BRANDED","currency":"USD","netAmount":"%s","quantity":"%s"%s}`, line, date, net, qty, r)
}

func create(t *testing.T, h http.Handler, path, body string) string {
	t.Helper()
	code, out := call(t, h, http.MethodPost, path, allDuties, body, inEntity...)
	if code != http.StatusCreated {
		t.Fatalf("create %s = %d %v", path, code, out)
	}
	return out["id"].(string)
}

// FR-26.16 over HTTP: a broker contract accrues 2.5% of net per posted invoice line, the
// credit note reverses in proportion, and the period is claimed as BC-n and approved by a
// second user for w19 AP.
func TestBroker_AccrualCreditAndClaim(t *testing.T) {
	h := router(newFake())
	id := create(t, h, "/v1/pricing/broker-contracts", brokerBody(""))
	_, got := call(t, h, http.MethodGet, "/v1/pricing/broker-contracts/"+id, "pricing.rebate.view", "")
	if got["rateKind"] != "PERCENT_NET" || got["rate"] != "2.5" || got["payableAccount"] != "2175" || got["expenseAccount"] != "6210" || got["status"] != "ACTIVE" {
		t.Fatalf("contract = %v; want 2.5%% of net on 6210 / 2175", got)
	}
	accrue := func(body string) (int, map[string]any) {
		return call(t, h, http.MethodPost, "/v1/pricing/broker-contracts/"+id+"/accruals", allDuties, body)
	}
	code, acc := accrue(contractLineBody("INV-1/1", "2027-03-10", "10000.00", "5", ""))
	if code != http.StatusCreated || acc["amount"] != "250" || acc["periodId"] != "2027-03" || acc["kind"] != "ACCRUAL" {
		t.Fatalf("accrual = %d %v; want 250 ACCRUAL in 2027-03", code, acc)
	}
	if code, again := accrue(contractLineBody("INV-1/1", "2027-03-10", "10000.00", "5", "")); code != http.StatusOK || again["id"] != acc["id"] {
		t.Fatalf("redelivery = %d %v; want 200 with the first accrual", code, again)
	}
	code, credit := accrue(contractLineBody("CN-1/1", "2027-03-20", "4000.00", "2", "INV-1/1"))
	if code != http.StatusCreated || credit["amount"] != "-100" || credit["kind"] != "CREDIT" || credit["reversesLineKey"] != "INV-1/1" {
		t.Fatalf("credit = %d %v; want -100 CREDIT", code, credit)
	}
	if code, out := accrue(contractLineBody("CN-2/1", "2027-03-21", "6000.01", "3", "INV-1/1")); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0059" {
		t.Fatalf("over-credit = %d %v; want 422 PRC-0059", code, out)
	}
	// The accrual route stands in for the invoice.posted consumer and posts to the GL, so it
	// takes the service-only duty: a person holding every rebate duty cannot feed a line.
	humanDuties := "pricing.rebate.maintain,pricing.rebate.view,pricing.rebate.claim.approve"
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/broker-contracts/"+id+"/accruals", humanDuties,
		contractLineBody("INV-7/1", "2027-03-10", "100", "1", "")); code != http.StatusForbidden {
		t.Fatalf("accrual with the rebate duties only = %d; want 403", code)
	}
	if code, out := accrue(contractLineBody("INV-9/1", "2027-03-10", "-5", "1", "")); code != http.StatusUnprocessableEntity {
		t.Fatalf("negative net = %d %v; want 422", code, out)
	}
	bad := `{"invoiceLineId":"INV-8/1","invoiceDate":"2027-03-10","productGroupId":"OTHER","currency":"USD","netAmount":"100","quantity":"1"}`
	if code, out := accrue(bad); code != http.StatusUnprocessableEntity {
		t.Fatalf("line outside the scope = %d %v; want 422", code, out)
	}

	_, preview := call(t, h, http.MethodGet, "/v1/pricing/broker-contracts/"+id+"/claim-preview?period=2027-03", "pricing.rebate.view", "")
	if preview["amount"] != "150" || preview["accrued"] != "150" || preview["shortfall"] != "0" || preview["vendorId"] != brokerVendorID {
		t.Fatalf("preview = %v; want 150", preview)
	}
	claimBody := fmt.Sprintf(`{"contractKind":"BROKER","contractId":"%s","period":"2027-03"}`, id)
	code, claim := call(t, h, http.MethodPost, "/v1/pricing/contract-claims", allDuties, claimBody, "X-Passage-User", "u1")
	if code != http.StatusCreated || claim["number"] != "BC-000001" || claim["amount"] != "150" || claim["status"] != "OPEN" || claim["vendorId"] != brokerVendorID {
		t.Fatalf("claim = %d %v; want BC-000001 for 150", code, claim)
	}
	cid := claim["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/contract-claims", allDuties, claimBody, "X-Passage-User", "u1"); code != http.StatusConflict || out["code"] != "PRC-0015" {
		t.Fatalf("second claim = %d %v; want 409 PRC-0015", code, out)
	}
	approve := func(user, duties string) (int, map[string]any) {
		return call(t, h, http.MethodPost, "/v1/pricing/contract-claims/"+cid+":approve", duties, `{"version":1}`, "X-Passage-User", user)
	}
	if code, out := approve("u1", allDuties); code != http.StatusForbidden || out["code"] != "PRC-0046" {
		t.Fatalf("approval by the creator = %d %v; want 403 PRC-0046", code, out)
	}
	if code, _ := approve("u2", "pricing.rebate.maintain,pricing.rebate.view"); code != http.StatusForbidden {
		t.Fatalf("approval without the duty = %d; want 403", code)
	}
	if code, out := approve("u2", allDuties); code != http.StatusOK || out["status"] != "APPROVED" || out["version"] != float64(2) {
		t.Fatalf("approval = %d %v", code, out)
	}
	_, list := call(t, h, http.MethodGet, "/v1/pricing/contract-claims?contractKind=BROKER&status=APPROVED", "pricing.rebate.view", "")
	if items := list["items"].([]any); len(items) != 1 {
		t.Fatalf("approved broker claims = %v; want 1", list)
	}
	_, accs := call(t, h, http.MethodGet, "/v1/pricing/broker-contracts/"+id+"/accruals?period=2027-03", "pricing.rebate.view", "")
	if items := accs["items"].([]any); len(items) != 2 {
		t.Fatalf("accrual rows = %v; want 2", accs)
	}

	code, closed := call(t, h, http.MethodPost, "/v1/pricing/broker-contracts/"+id+":close", allDuties, `{"version":1}`)
	if code != http.StatusOK || closed["status"] != "CLOSED" {
		t.Fatalf("close = %d %v", code, closed)
	}
	if code, out := accrue(contractLineBody("INV-3/1", "2027-04-01", "100", "1", "")); code != http.StatusConflict || out["code"] != "PRC-0061" {
		t.Fatalf("accrual on a closed contract = %d %v; want 409 PRC-0061", code, out)
	}
}

func TestBroker_ValidationAndDuties(t *testing.T) {
	h := router(newFake())
	cases := map[string]struct{ body, field string }{
		"percent above 100":    {brokerBody(`,"rate":"101"`), "rate"},
		"no vendor":            {`{"legalEntityId":"` + legalEntity + `","name":"x","rateKind":"PER_UNIT","rate":"1","currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`, "brokerVendorId"},
		"both customer scopes": {brokerBody(`,"customerId":"00000000-0000-0000-0005-000000000001","customerGroupId":"00000000-0000-0000-0005-000000000002"`), "customer"},
		"bad rate kind":        {brokerBody(`,"rateKind":"FLAT"`), "rateKind"},
		"reversed dates":       {brokerBody(`,"validFrom":"2028-01-01"`), "validity"},
		"bad account":          {brokerBody(`,"expenseAccount":"X"`), "accounts"},
	}
	for name, c := range cases {
		code, out := call(t, h, http.MethodPost, "/v1/pricing/broker-contracts", allDuties, c.body)
		errs, _ := out["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs[c.field] == nil {
			t.Errorf("%s = %d %v; want 422 naming %s", name, code, out, c.field)
		}
	}
	otherEntity := []string{"X-Passage-Legal-Entity", "00000000-0000-0000-0000-0000000000e2"}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/broker-contracts", allDuties, brokerBody(""), otherEntity...); code != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("create for a legal entity outside the scope = %d %v; want 403 AUTH-0003", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements", allDuties, royaltyBody("5000"), otherEntity...); code != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("royalty create outside the scope = %d %v; want 403 AUTH-0003", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/broker-contracts", "pricing.rebate.view", brokerBody("")); code != http.StatusForbidden {
		t.Fatalf("create with the view duty = %d; want 403", code)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/broker-contracts", "pricing.maintain", ""); code != http.StatusForbidden {
		t.Fatalf("list without the rebate view duty = %d; want 403", code)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/broker-contracts/not-a-uuid", allDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0058" {
		t.Fatalf("malformed id = %d %v; want 404 PRC-0058", code, out)
	}
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/broker-contracts?status=OPEN", allDuties, ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status filter = %d; want 422", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/contract-claims", allDuties, `{"contractKind":"X","contractId":"1","period":"March"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad claim request = %d; want 422", code)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/broker-contracts/"+create(t, h, "/v1/pricing/broker-contracts", brokerBody(""))+":close", allDuties, `{}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("close without a version = %d; want 422", code)
	}
}

// FR-26.17 over HTTP: tiers recompute the period, the minimum royalty shortfall posts when the
// period closes (PRC-0017 notice), the period is then claimed as RY-n.
func TestRoyalty_TiersMinimumAndClaim(t *testing.T) {
	h := router(newFake())
	id := create(t, h, "/v1/pricing/royalty-agreements", royaltyBody("5000"))
	_, got := call(t, h, http.MethodGet, "/v1/pricing/royalty-agreements/"+id, "pricing.rebate.view", "")
	if got["minimumRoyalty"] != "5000" || got["payableAccount"] != "2176" || got["expenseAccount"] != "6220" || len(got["tiers"].([]any)) != 2 {
		t.Fatalf("agreement = %v", got)
	}
	accrue := func(body string) (int, map[string]any) {
		return call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements/"+id+"/accruals", allDuties, body)
	}
	if code, acc := accrue(contractLineBody("INV-1/1", "2027-03-10", "40000", "1", "")); code != http.StatusCreated || acc["amount"] != "1600" {
		t.Fatalf("first accrual = %d %v; want 1600", code, acc)
	}
	if code, acc := accrue(contractLineBody("INV-2/1", "2027-03-11", "20000", "1", "")); code != http.StatusCreated || acc["amount"] != "1400" {
		t.Fatalf("tier crossing = %d %v; want 1400", code, acc)
	}
	claimBody := fmt.Sprintf(`{"contractKind":"ROYALTY","contractId":"%s","period":"2027-03"}`, id)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/contract-claims", allDuties, claimBody); code != http.StatusConflict || out["code"] != "PRC-0062" {
		t.Fatalf("claim of an open royalty period = %d %v; want 409 PRC-0062", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements/"+id+":close-period", allDuties, `{"period":"2027-Q1"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("a quarter on a monthly agreement = %d; want 422", code)
	}
	code, closed := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements/"+id+":close-period", allDuties, `{"period":"2027-03"}`)
	if code != http.StatusOK || closed["accrued"] != "3000" || closed["shortfall"] != "2000" || closed["alreadyClosed"] != false ||
		closed["notice"] != "PRC-0017: minimum royalty shortfall 2000 USD posted at period close" {
		t.Fatalf("close = %d %v; want 3000 accrued and a 2000 shortfall with PRC-0017", code, closed)
	}
	if accrual := closed["accrual"].(map[string]any); accrual["kind"] != "SHORTFALL" || accrual["lineKey"] != "MIN:2027-03" || accrual["amount"] != "2000" {
		t.Fatalf("shortfall accrual = %v", accrual)
	}
	if _, again := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements/"+id+":close-period", allDuties, `{"period":"2027-03"}`); again["alreadyClosed"] != true || again["accrual"] != nil {
		t.Fatalf("second close = %v; want alreadyClosed with nothing new", again)
	}
	if code, out := accrue(contractLineBody("INV-3/1", "2027-03-28", "100", "1", "")); code != http.StatusConflict || out["code"] != "PRC-0060" {
		t.Fatalf("accrual into a closed period = %d %v; want 409 PRC-0060", code, out)
	}
	code, claim := call(t, h, http.MethodPost, "/v1/pricing/contract-claims", allDuties, claimBody, "X-Passage-User", "u1")
	if code != http.StatusCreated || claim["number"] != "RY-000001" || claim["amount"] != "5000" || claim["vendorId"] != licensorVendorID {
		t.Fatalf("claim = %d %v; want RY-000001 for the 5000 minimum", code, claim)
	}
	_, preview := call(t, h, http.MethodGet, "/v1/pricing/royalty-agreements/"+id+"/claim-preview?period=2027-03", allDuties, "")
	if preview["amount"] != "0" || preview["closed"] != true || preview["claims"] != float64(1) {
		t.Fatalf("preview after the claim = %v", preview)
	}
	code, closedAgreement := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements/"+id+":close", allDuties, `{"version":1}`)
	if code != http.StatusOK || closedAgreement["status"] != "CLOSED" {
		t.Fatalf("close agreement = %d %v", code, closedAgreement)
	}
	_, list := call(t, h, http.MethodGet, "/v1/pricing/royalty-agreements?licensorVendorId="+licensorVendorID+"&status=CLOSED", allDuties, "")
	if items := list["items"].([]any); len(items) != 1 {
		t.Fatalf("closed agreements of the licensor = %v", list)
	}
}

func TestRoyalty_Validation(t *testing.T) {
	h := router(newFake())
	cases := map[string]struct{ body, field string }{
		"no product":       {`{"legalEntityId":"` + legalEntity + `","name":"x","licensorVendorId":"` + licensorVendorID + `","basis":"NET_SALES","period":"MONTH","tiers":[{"from":"0","ratePct":"4"}],"currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`, "product"},
		"no tiers":         {`{"legalEntityId":"` + legalEntity + `","name":"x","licensorVendorId":"` + licensorVendorID + `","productId":"p1","basis":"NET_SALES","period":"MONTH","tiers":[],"currency":"USD","validFrom":"2027-01-01","validTo":"2027-12-31"}`, "tiers"},
		"negative minimum": {royaltyBody("-1"), "minimumRoyalty"},
		"not a decimal":    {royaltyBody("abc"), "minimumRoyalty"},
		"sub-cent minimum": {royaltyBody("5000.005"), "minimumRoyalty"},
	}
	for name, c := range cases {
		code, out := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements", allDuties, c.body)
		errs, _ := out["errors"].(map[string]any)
		if code != http.StatusUnprocessableEntity || errs[c.field] == nil {
			t.Errorf("%s = %d %v; want 422 naming %s", name, code, out, c.field)
		}
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/royalty-agreements/00000000-0000-4000-8000-000000000099:close-period", allDuties, `{"period":"March"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("bad period = %d; want 422", code)
	}
	if code, out := call(t, h, http.MethodGet, "/v1/pricing/royalty-agreements/00000000-0000-4000-8000-000000000099", allDuties, ""); code != http.StatusNotFound || out["code"] != "PRC-0058" {
		t.Fatalf("unknown agreement = %d %v; want 404 PRC-0058", code, out)
	}
}

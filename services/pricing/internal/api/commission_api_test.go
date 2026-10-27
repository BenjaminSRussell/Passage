package api_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

const (
	salesGroupsURL  = "/v1/pricing/commission/sales-groups"
	rulesURL        = "/v1/pricing/commission/rules"
	postingsURL     = "/v1/pricing/commission/postings"
	transactionsURL = "/v1/pricing/commission/transactions"
	salesGroupBody  = `{"legalEntityId":"%s","name":"SG-E","reps":[{"repId":"R1","sharePct":"60"},{"repId":"R2","sharePct":"40"}]}`
)

// seedCommission creates sales group SG-E (R1 60% / R2 40%) and a 5% revenue rule for all customers and items.
func seedCommission(t *testing.T, h http.Handler) string {
	t.Helper()
	code, sg := call(t, h, http.MethodPost, salesGroupsURL, allDuties, fmt.Sprintf(salesGroupBody, legalEntity))
	if code != http.StatusCreated {
		t.Fatalf("sales group = %d %v", code, sg)
	}
	id := sg["id"].(string)
	if code, out := call(t, h, http.MethodPost, rulesURL, allDuties, `{"salesGroupId":"`+id+`","basis":"REVENUE","ratePct":"5"}`); code != http.StatusCreated {
		t.Fatalf("rule = %d %v", code, out)
	}
	return id
}

func posting(sg, line, reverses, net string) string {
	rev := ""
	if reverses != "" {
		rev = `,"reversesInvoiceLineId":"` + reverses + `"`
	}
	return `{"legalEntityId":"` + legalEntity + `","salesGroupId":"` + sg + `","invoiceLineId":"` + line + `"` + rev +
		`,"invoiceDate":"2027-03-10","customerId":"C1","currency":"USD","lineNet":"` + net + `"}`
}

func repAmounts(t *testing.T, out map[string]any) map[string]string {
	t.Helper()
	items, _ := out["items"].([]any)
	got := map[string]string{}
	for _, it := range items {
		m := it.(map[string]any)
		got[m["repId"].(string)] = decimal.RequireFromString(m["amount"].(string)).StringFixed(2)
	}
	return got
}

func TestCommission_AC26_12OverHTTP(t *testing.T) {
	// AC-26.12: a 10,000.00 USD invoice at 5% splits 300.00 / 200.00; its credit note posts -300.00 / -200.00.
	h := router(newFake())
	sg := seedCommission(t, h)
	code, out := call(t, h, http.MethodPost, postingsURL, allDuties, posting(sg, "INV-1", "", "10000.00"))
	if got := repAmounts(t, out); code != http.StatusCreated || got["R1"] != "300.00" || got["R2"] != "200.00" {
		t.Fatalf("invoice posting = %d %v", code, out)
	}
	first := out["items"].([]any)[0].(map[string]any)
	if first["debitAccount"] != "6200" || first["creditAccount"] != "2170" || first["periodId"] != "2027-03" || first["basis"] != "REVENUE" {
		t.Fatalf("transaction view = %v", first)
	}

	// The invoice feed may redeliver: the stored rows come back with 200 and nothing doubles.
	if code, again := call(t, h, http.MethodPost, postingsURL, allDuties, posting(sg, "INV-1", "", "10000.00")); code != http.StatusOK || len(again["items"].([]any)) != 2 {
		t.Fatalf("redelivery = %d %v", code, again)
	}
	if code, diff := call(t, h, http.MethodPost, postingsURL, allDuties, posting(sg, "INV-1", "", "9000.00")); code != http.StatusUnprocessableEntity {
		t.Fatalf("redelivery with another net must be refused, got %d %v", code, diff)
	}

	code, out = call(t, h, http.MethodPost, postingsURL, allDuties, posting(sg, "CN-1", "INV-1", "-10000.00"))
	if got := repAmounts(t, out); code != http.StatusCreated || got["R1"] != "-300.00" || got["R2"] != "-200.00" {
		t.Fatalf("credit posting = %d %v", code, out)
	}
	if out["items"].([]any)[0].(map[string]any)["reversesInvoiceLineId"] != "INV-1" {
		t.Fatalf("the credit must name the original line: %v", out)
	}
	// The inquiry by invoice line shows the invoice and its credit, and nets to zero.
	_, list := call(t, h, http.MethodGet, transactionsURL+"?invoiceLineId=INV-1", allDuties, "")
	sum := decimal.Zero
	for _, it := range list["items"].([]any) {
		sum = sum.Add(decimal.RequireFromString(it.(map[string]any)["amount"].(string)))
	}
	if len(list["items"].([]any)) != 4 || !sum.IsZero() {
		t.Fatalf("inquiry = %v; want four rows netting to zero", list)
	}
	// Nothing is left to credit.
	if code, over := call(t, h, http.MethodPost, postingsURL, allDuties, posting(sg, "CN-2", "INV-1", "-1.00")); code != http.StatusUnprocessableEntity || over["code"] != domain.CodeCommissionCredit {
		t.Fatalf("over-credit must be 422 PRC-0037, got %d %v", code, over)
	}
}

func TestCommission_ErrorPaths(t *testing.T) {
	h := router(newFake())
	code, sg := call(t, h, http.MethodPost, salesGroupsURL, allDuties, fmt.Sprintf(salesGroupBody, legalEntity))
	if code != http.StatusCreated {
		t.Fatalf("sales group = %d", code)
	}
	id := sg["id"].(string)
	cases := []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"no rule at any level is PRC-0013", http.MethodPost, postingsURL, posting(id, "INV-9", "", "100"), http.StatusUnprocessableEntity, domain.CodeCommissionRuleMissing},
		{"a credit with nothing to reverse is PRC-0037", http.MethodPost, postingsURL, posting(id, "CN-9", "INV-404", "-5"), http.StatusUnprocessableEntity, domain.CodeCommissionCredit},
		{"a negative net without a reversed line is refused", http.MethodPost, postingsURL, posting(id, "INV-8", "", "-5"), http.StatusUnprocessableEntity, "COM-0001"},
		{"an unknown sales group is PRC-0036", http.MethodPost, postingsURL, posting("00000000-0000-0000-0007-000000000099", "INV-7", "", "5"), http.StatusNotFound, domain.CodeSalesGroupNotFound},
		{"shares must sum to 100", http.MethodPost, salesGroupsURL, `{"legalEntityId":"` + legalEntity + `","name":"X","reps":[{"repId":"R1","sharePct":"60"},{"repId":"R2","sharePct":"30"}]}`, http.StatusUnprocessableEntity, "COM-0001"},
		{"a rule needs a known basis", http.MethodPost, rulesURL, `{"salesGroupId":"` + id + `","basis":"PROFIT","ratePct":"5"}`, http.StatusUnprocessableEntity, "COM-0001"},
		{"a rule for an unknown group is PRC-0036", http.MethodPost, rulesURL, `{"salesGroupId":"00000000-0000-0000-0007-000000000099","basis":"REVENUE","ratePct":"5"}`, http.StatusNotFound, domain.CodeSalesGroupNotFound},
		{"the inquiry period is a month", http.MethodGet, transactionsURL + "?period=2027-Q1", "", http.StatusUnprocessableEntity, "COM-0001"},
		{"an unknown sales group id reads 404", http.MethodGet, salesGroupsURL + "/not-a-uuid", "", http.StatusNotFound, domain.CodeSalesGroupNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out := call(t, h, c.method, c.path, allDuties, c.body)
			if code != c.status || out["code"] != c.code {
				t.Fatalf("got %d %v; want %d %s", code, out, c.status, c.code)
			}
		})
	}
	// One rule per (sales group, customer group, item group).
	rule := `{"salesGroupId":"` + id + `","customerGroup":"CG-1","basis":"MARGIN","ratePct":"4"}`
	if code, _ := call(t, h, http.MethodPost, rulesURL, allDuties, rule); code != http.StatusCreated {
		t.Fatalf("first rule = %d", code)
	}
	if code, out := call(t, h, http.MethodPost, rulesURL, allDuties, rule); code != http.StatusConflict || out["code"] != domain.CodeCommissionRuleExists {
		t.Fatalf("duplicate rule = %d %v; want 409 PRC-0038", code, out)
	}
	// The MARGIN rule for CG-1 needs a cost snapshot.
	body := `{"legalEntityId":"` + legalEntity + `","salesGroupId":"` + id + `","invoiceLineId":"INV-M","invoiceDate":"2027-03-10","customerGroup":"CG-1","currency":"USD","lineNet":"1000"}`
	if code, out := call(t, h, http.MethodPost, postingsURL, allDuties, body); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["costSnapshot"] == nil {
		t.Fatalf("MARGIN without cost = %d %v", code, out)
	}
}

func TestCommission_SalesGroupReadAndRuleList(t *testing.T) {
	h := router(newFake())
	id := seedCommission(t, h)
	code, g := call(t, h, http.MethodGet, salesGroupsURL+"/"+id, allDuties, "")
	reps, _ := g["reps"].([]any)
	if code != http.StatusOK || g["name"] != "SG-E" || len(reps) != 2 || reps[0].(map[string]any)["repId"] != "R1" {
		t.Fatalf("read sales group = %d %v", code, g)
	}
	code, rules := call(t, h, http.MethodGet, rulesURL+"?salesGroupId="+id, allDuties, "")
	items, _ := rules["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["legalEntityId"] != legalEntity {
		t.Fatalf("rules = %d %v; the rule takes the group's legal entity", code, rules)
	}
}

func TestCommission_DutiesGateMaintainAndView(t *testing.T) {
	h := router(newFake())
	if code, _ := call(t, h, http.MethodPost, salesGroupsURL, "pricing.commission.view", fmt.Sprintf(salesGroupBody, legalEntity)); code != http.StatusForbidden {
		t.Fatalf("a viewer must not create sales groups, got %d", code)
	}
	if code, _ := call(t, h, http.MethodPost, postingsURL, "pricing.commission.view", posting(legalEntity, "INV-1", "", "1")); code != http.StatusForbidden {
		t.Fatalf("a viewer must not post commission, got %d", code)
	}
	if code, _ := call(t, h, http.MethodGet, transactionsURL, "pricing.view", ""); code != http.StatusForbidden {
		t.Fatalf("the inquiry needs pricing.commission.view, got %d", code)
	}
}

// retroAgreement creates a 2% monthly agreement from 2027-03-01 and marks it activated on 2027-03-20.
func retroAgreement(t *testing.T, f *fakeStore, h http.Handler) string {
	t.Helper()
	body := `{"legalEntityId":"` + legalEntity + `","name":"Retro 2%","customerId":"00000000-0000-0000-0005-000000000001",
		"basis":"NET_AMOUNT","period":"MONTH","tiers":[{"from":"0","ratePct":"2"}],"currency":"USD","validFrom":"2027-03-01","validTo":"2027-12-31"}`
	code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements", allDuties, body)
	if code != http.StatusCreated {
		t.Fatalf("agreement = %d %v", code, out)
	}
	id := out["id"].(string)
	f.commission().activated[id] = time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	return id
}

func TestRetro_AC26_14OverHTTP(t *testing.T) {
	// AC-26.14: 50,000.00 USD invoiced between valid_from and activation posts a 1,000.00 delta.
	f := newFake()
	h := router(f)
	id := retroAgreement(t, f, h)
	path := "/v1/pricing/rebates/agreements/" + id + ":retro-recompute"
	body := `{"lines":[{"invoiceLineId":"INV-A","invoiceDate":"2027-03-05","value":"20000","currency":"USD"},
		{"invoiceLineId":"INV-B","invoiceDate":"2027-03-18","value":"30000","currency":"USD"}]}`
	code, out := call(t, h, http.MethodPost, path, allDuties, body, "Idempotency-Key", "run-1")
	deltas, _ := out["deltas"].([]any)
	if code != http.StatusOK || len(deltas) != 1 || out["reaccruedLines"] != float64(2) || out["activatedOn"] != "2027-03-20" {
		t.Fatalf("recompute = %d %v", code, out)
	}
	d := deltas[0].(map[string]any)
	if d["periodId"] != "2027-03" || decimal.RequireFromString(d["delta"].(string)).StringFixed(2) != "1000.00" {
		t.Fatalf("delta = %v; want 1000.00 in 2027-03", d)
	}
	if notice, _ := out["notice"].(map[string]any); notice["code"] != domain.CodeRetroactive {
		t.Fatalf("the response must carry the PRC-0006 notice, got %v", out["notice"])
	}
	if f.comm.lastRetro.RunKey != "run-1" || len(f.comm.lastRetro.Lines) != 2 {
		t.Fatalf("store input = %+v", f.comm.lastRetro)
	}
	// The accrual inquiry flags the replayed lines and the delta as retroactive.
	_, acc := call(t, h, http.MethodGet, "/v1/pricing/rebates/accruals?agreementId="+id, allDuties, "")
	retro := 0
	for _, it := range acc["items"].([]any) {
		if it.(map[string]any)["retroactive"] == true {
			retro++
		}
	}
	if retro != 3 {
		t.Fatalf("accruals = %v; want two replayed lines and one delta flagged retroactive", acc)
	}
	// The same run key returns the stored result; a new run over the same invoices posts nothing.
	if code, again := call(t, h, http.MethodPost, path, allDuties, body, "Idempotency-Key", "run-1"); code != http.StatusOK || again["replayed"] != true {
		t.Fatalf("replay = %d %v", code, again)
	}
	if code, second := call(t, h, http.MethodPost, path, allDuties, body, "Idempotency-Key", "run-2"); code != http.StatusOK || len(second["deltas"].([]any)) != 0 || second["reaccruedLines"] != float64(0) {
		t.Fatalf("second run = %d %v; want no delta", code, second)
	}
}

func TestRetro_Validation(t *testing.T) {
	f := newFake()
	h := router(f)
	id := retroAgreement(t, f, h)
	path := "/v1/pricing/rebates/agreements/" + id + ":retro-recompute"
	line := `{"lines":[{"invoiceLineId":"INV-A","invoiceDate":"2027-03-05","value":"100","currency":"USD"}]}`
	if code, out := call(t, h, http.MethodPost, path, allDuties, line); code != http.StatusUnprocessableEntity || out["errors"].(map[string]any)["Idempotency-Key"] == nil {
		t.Fatalf("a missing Idempotency-Key must be 422, got %d %v", code, out)
	}
	bad := `{"lines":[{"invoiceLineId":"INV-A","invoiceDate":"05/03/2027","value":"x","currency":"usd"}]}`
	if code, out := call(t, h, http.MethodPost, path, allDuties, bad, "Idempotency-Key", "k"); code != http.StatusUnprocessableEntity || len(out["errors"].(map[string]any)) != 3 {
		t.Fatalf("malformed lines = %d %v", code, out)
	}
	late := `{"lines":[{"invoiceLineId":"INV-L","invoiceDate":"2027-04-02","value":"100","currency":"USD"}]}`
	if code, out := call(t, h, http.MethodPost, path, allDuties, late, "Idempotency-Key", "k2"); code != http.StatusUnprocessableEntity {
		t.Fatalf("an invoice after the activation must go through /rebates/accruals, got %d %v", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, path, "pricing.rebate.view", line, "Idempotency-Key", "k3"); code != http.StatusForbidden {
		t.Fatalf("the recompute needs pricing.rebate.maintain, got %d", code)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/rebates/agreements/nope:retro-recompute", allDuties, line, "Idempotency-Key", "k4"); code != http.StatusNotFound {
		t.Fatalf("a malformed id must be 404, got %d %v", code, out)
	}
}

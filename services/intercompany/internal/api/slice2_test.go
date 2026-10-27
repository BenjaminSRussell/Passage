package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/intercompany/internal/api"
)

const (
	chargePath   = "/v1/interco/charges:preview"
	paymentPath  = "/v1/interco/centralized-payments:preview"
	planningPath = "/v1/interco/planning-groups:sequence"
)

type problemBody struct {
	Code   string            `json:"code"`
	Errors map[string]string `json:"errors"`
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) problemBody {
	t.Helper()
	var p problemBody
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	return p
}

// chargeBody is AC-40.11: 8 hours by a lending-entity consultant at cost 60.00
// with cost-plus 10 %, and a newer table price that is not yet effective.
const chargeBody = `{
  "lendingEntity":"P","borrowingEntity":"S","category":"Hour","resourceRole":"Consultant",
  "transactionDate":"2026-11-14","quantity":"8","unitCost":"60.00","currency":"EUR",
  "transferPrices":[
    {"category":"Hour","resourceRole":"Consultant","method":"CostPlus","markupPct":"10","effectiveFrom":"2026-01-01"},
    {"category":"Hour","resourceRole":"Consultant","method":"Table","unitPrice":"70.00","effectiveFrom":"2026-12-01"}
  ],
  "costAccount":"5100-PROJECT-COST","recoveryAccount":"4800-COST-RECOVERY","marginAccount":"4810-IC-MARGIN",
  "matrix":[{"entityA":"P","entityB":"S","dueFrom":"1510-DUE-FROM-S","dueTo":"2510-DUE-TO-P"}]
}`

func TestPreviewCharge_AC4011(t *testing.T) {
	a := api.New(nil)
	rec := do(router(a), chargePath, tenantID, api.DutyPost, chargeBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		PairID        string `json:"pairId"`
		TransferPrice struct {
			Method, MarkupPct, EffectiveFrom string
		} `json:"transferPrice"`
		Cost, Price, Margin string
		Balanced            bool
		Legs                []legBody
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TransferPrice.Method != "CostPlus" || got.TransferPrice.MarkupPct != "10" || got.TransferPrice.EffectiveFrom != "2026-01-01" {
		t.Fatalf("applied price = %+v; want the cost-plus row effective on the transaction date", got.TransferPrice)
	}
	if got.Cost != "480.00" || got.Price != "528.00" || got.Margin != "48.00" || !got.Balanced || len(got.PairID) != 36 {
		t.Fatalf("pricing = %+v", got)
	}
	want := []legBody{
		{"S", "5100-PROJECT-COST", "debit", "528.00", "EUR"},
		{"S", "2510-DUE-TO-P", "credit", "528.00", "EUR"},
		{"P", "1510-DUE-FROM-S", "debit", "528.00", "EUR"},
		{"P", "4800-COST-RECOVERY", "credit", "480.00", "EUR"},
		{"P", "4810-IC-MARGIN", "credit", "48.00", "EUR"},
	}
	if len(got.Legs) != len(want) {
		t.Fatalf("legs = %+v", got.Legs)
	}
	for i := range want {
		if got.Legs[i] != want[i] {
			t.Fatalf("leg %d = %+v; want %+v", i, got.Legs[i], want[i])
		}
	}
	mrec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(mrec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(mrec.Body.String(), "intercompany_charge_previews_total 1") {
		t.Fatalf("metrics = %s", mrec.Body.String())
	}
}

func TestPreviewCharge_Errors(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		code, field        string
	}{
		{"no price effective on the date", strings.Replace(chargeBody, `"2026-11-14"`, `"2025-12-31"`, 1), api.DutyPost, http.StatusUnprocessableEntity, "IC-0821", ""},
		{"no price for the role", strings.Replace(chargeBody, "\"resourceRole\":\"Consultant\",\n", "\"resourceRole\":\"Architect\",\n", 1), api.DutyPost, http.StatusUnprocessableEntity, "IC-0821", ""},
		{"missing matrix row", strings.Replace(chargeBody, `"entityB":"S"`, `"entityB":"X"`, 1), api.DutyPost, http.StatusUnprocessableEntity, "IC-0805", ""},
		{"unknown category", strings.Replace(chargeBody, `"borrowingEntity":"S","category":"Hour"`, `"borrowingEntity":"S","category":"Travel"`, 1), api.DutyPost, http.StatusBadRequest, "COM-0001", "category"},
		{"same lender and borrower", strings.Replace(chargeBody, `"borrowingEntity":"S"`, `"borrowingEntity":"P"`, 1), api.DutyPost, http.StatusBadRequest, "COM-0001", "borrowingEntity"},
		{"zero quantity", strings.Replace(chargeBody, `"quantity":"8"`, `"quantity":"0"`, 1), api.DutyPost, http.StatusBadRequest, "COM-0001", "quantity"},
		{"bad date", strings.Replace(chargeBody, `"2026-11-14"`, `"14.11.2026"`, 1), api.DutyPost, http.StatusBadRequest, "COM-0001", "transactionDate"},
		{"cost-plus without markup", strings.Replace(chargeBody, `"markupPct":"10",`, ``, 1), api.DutyPost, http.StatusBadRequest, "COM-0001", "transferPrices[0].markupPct"},
		{"unknown method", strings.Replace(chargeBody, `"method":"Table"`, `"method":"Market"`, 1), api.DutyPost, http.StatusBadRequest, "COM-0001", "transferPrices[1].method"},
		{"no transfer prices", `{"lendingEntity":"P","borrowingEntity":"S","category":"Hour","transactionDate":"2026-11-14","quantity":"1","unitCost":"1","currency":"EUR","costAccount":"a","recoveryAccount":"b","marginAccount":"c","transferPrices":[],"matrix":[]}`, api.DutyPost, http.StatusBadRequest, "COM-0001", "transferPrices"},
		{"view duty may not preview", chargeBody, api.DutyView, http.StatusForbidden, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New(nil)), chargePath, tenantID, c.duties, c.body)
			p := problem(t, rec)
			if rec.Code != c.status || (c.code != "" && p.Code != c.code) || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
}

// paymentBody is AC-40.10: P pays S's vendor invoice of EUR 10,000.00.
const paymentBody = `{
  "payingEntity":"P","owningEntity":"S","paymentMethod":"SEPA","direction":"Pay",
  "amount":"10000.00","currency":"EUR","bankAccount":"1100-BANK","owningAccount":"2000-VENDOR",
  "matrix":[{"entityA":"P","entityB":"S","dueFrom":"1510-DUE-FROM-S","dueTo":"2510-DUE-TO-P"},
            {"entityA":"S","entityB":"P","dueFrom":"1520-DUE-FROM-P","dueTo":"2520-DUE-TO-S"}]
}`

func TestPreviewCentralPayment_AC4010(t *testing.T) {
	rec := do(router(api.New(nil)), paymentPath, tenantID, api.DutyConfigure, paymentBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got journalBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := []legBody{
		{"P", "1510-DUE-FROM-S", "debit", "10000.00", "EUR"},
		{"P", "1100-BANK", "credit", "10000.00", "EUR"},
		{"S", "2000-VENDOR", "debit", "10000.00", "EUR"},
		{"S", "2510-DUE-TO-P", "credit", "10000.00", "EUR"},
	}
	if !got.Balanced || len(got.PairID) != 36 || len(got.Legs) != len(want) {
		t.Fatalf("response = %+v", got)
	}
	for i := range want {
		if got.Legs[i] != want[i] {
			t.Fatalf("leg %d = %+v; want %+v", i, got.Legs[i], want[i])
		}
	}

	recv := strings.NewReplacer(`"Pay"`, `"Receive"`, `"2000-VENDOR"`, `"1200-CUSTOMER"`).Replace(paymentBody)
	rec = do(router(api.New(nil)), paymentPath, tenantID, api.DutyConfigure, recv)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("receive status %d body %s", rec.Code, rec.Body.String())
	}
	if got.Legs[0] != (legBody{"P", "1100-BANK", "debit", "10000.00", "EUR"}) || got.Legs[1].Account != "2520-DUE-TO-S" ||
		got.Legs[2].Account != "1520-DUE-FROM-P" || got.Legs[3] != (legBody{"S", "1200-CUSTOMER", "credit", "10000.00", "EUR"}) {
		t.Fatalf("receive legs = %+v", got.Legs)
	}
}

func TestPreviewCentralPayment_Errors(t *testing.T) {
	cases := []struct {
		name, body, duties string
		status             int
		code, field        string
	}{
		{"bad direction", strings.Replace(paymentBody, `"Pay"`, `"Both"`, 1), api.DutyConfigure, http.StatusBadRequest, "COM-0001", "direction"},
		{"same entities", strings.Replace(paymentBody, `"owningEntity":"S"`, `"owningEntity":"P"`, 1), api.DutyConfigure, http.StatusBadRequest, "COM-0001", "owningEntity"},
		{"no payment method", strings.Replace(paymentBody, `"SEPA"`, `""`, 1), api.DutyConfigure, http.StatusBadRequest, "COM-0001", "paymentMethod"},
		{"zero amount", strings.Replace(paymentBody, `"10000.00"`, `"0"`, 1), api.DutyConfigure, http.StatusBadRequest, "COM-0001", "amount"},
		{"amount rounding to zero", strings.Replace(paymentBody, `"10000.00"`, `"0.004"`, 1), api.DutyConfigure, http.StatusBadRequest, "COM-0001", ""},
		{"missing matrix row", strings.Replace(paymentBody, `"owningEntity":"S"`, `"owningEntity":"T"`, 1), api.DutyConfigure, http.StatusUnprocessableEntity, "IC-0805", ""},
		{"post duty is not configure", paymentBody, api.DutyPost, http.StatusForbidden, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New(nil)), paymentPath, tenantID, c.duties, c.body)
			p := problem(t, rec)
			if rec.Code != c.status || (c.code != "" && p.Code != c.code) || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
}

// planningBody is AC-40.16: buyer A has a planned IC PO of 100 units for
// 2026-12-10 from supplier B with a 3-day transport lead time; B buys from C.
const planningBody = `{
  "members":[{"entityId":"C"},{"entityId":"B"},{"entityId":"A"}],
  "edges":[{"buyer":"A","supplier":"B","leadTimeDays":3},{"buyer":"B","supplier":"C","leadTimeDays":1}],
  "plannedOrders":[{"buyer":"A","supplier":"B","item":"PUMP-100","quantity":"100","requestedDate":"2026-12-10"}]
}`

type planningBodyOut struct {
	Order          []string `json:"order"`
	Demand         []struct{ Buyer, Supplier, Item, Quantity, DueDate string }
	StaleSuppliers []string `json:"staleSuppliers"`
	Warnings       []struct{ Code, Message string }
}

func TestSequencePlanning_AC4016(t *testing.T) {
	rec := do(router(api.New(nil)), planningPath, tenantID, api.DutyPlan, planningBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got planningBodyOut
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Order, ",") != "A,B,C" {
		t.Fatalf("order = %v; want buyer A before supplier B before C", got.Order)
	}
	if len(got.Demand) != 1 || got.Demand[0].Supplier != "B" || got.Demand[0].Quantity != "100" || got.Demand[0].DueDate != "2026-12-07" {
		t.Fatalf("demand = %+v; want 100 units at B on 2026-12-07", got.Demand)
	}
	if len(got.StaleSuppliers) != 0 || len(got.Warnings) != 0 {
		t.Fatalf("no replanning, yet stale = %v warnings = %v", got.StaleSuppliers, got.Warnings)
	}

	replan := strings.Replace(planningBody, `"plannedOrders"`, `"replannedBuyer":"A","plannedOrders"`, 1)
	rec = do(router(api.New(nil)), planningPath, tenantID, api.DutyPlan, replan)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("replan status %d body %s", rec.Code, rec.Body.String())
	}
	if strings.Join(got.StaleSuppliers, ",") != "B" || len(got.Warnings) != 1 || got.Warnings[0].Code != "IC-0815" {
		t.Fatalf("replanning A: stale = %v warnings = %+v; want B with IC-0815", got.StaleSuppliers, got.Warnings)
	}
}

func TestSequencePlanning_Errors(t *testing.T) {
	cycle := `{"members":[{"entityId":"A"},{"entityId":"B"}],"edges":[{"buyer":"A","supplier":"B"},{"buyer":"B","supplier":"A"}]}`
	cases := []struct {
		name, body, duties string
		status             int
		code, field        string
	}{
		{"cycle without sequence", cycle, api.DutyPlan, http.StatusUnprocessableEntity, "IC-0816", ""},
		{"no members", `{"members":[]}`, api.DutyPlan, http.StatusBadRequest, "COM-0001", "members"},
		{"duplicate member", `{"members":[{"entityId":"A"},{"entityId":"A"}]}`, api.DutyPlan, http.StatusBadRequest, "COM-0001", "members[1].entityId"},
		{"edge to unknown entity", strings.Replace(planningBody, `"supplier":"C"`, `"supplier":"X"`, 1), api.DutyPlan, http.StatusBadRequest, "COM-0001", "edges[1].supplier"},
		{"negative lead time", strings.Replace(planningBody, `"leadTimeDays":3`, `"leadTimeDays":-3`, 1), api.DutyPlan, http.StatusBadRequest, "COM-0001", "edges[0].leadTimeDays"},
		{"order without an edge", strings.Replace(planningBody, `"buyer":"A","supplier":"B","item"`, `"buyer":"A","supplier":"C","item"`, 1), api.DutyPlan, http.StatusBadRequest, "COM-0001", "plannedOrders[0].supplier"},
		{"bad requested date", strings.Replace(planningBody, `"2026-12-10"`, `"10/12/2026"`, 1), api.DutyPlan, http.StatusBadRequest, "COM-0001", "plannedOrders[0].requestedDate"},
		{"zero quantity", strings.Replace(planningBody, `"quantity":"100"`, `"quantity":"0"`, 1), api.DutyPlan, http.StatusBadRequest, "COM-0001", "plannedOrders[0].quantity"},
		{"replanned buyer outside the group", strings.Replace(planningBody, `"plannedOrders"`, `"replannedBuyer":"Z","plannedOrders"`, 1), api.DutyPlan, http.StatusBadRequest, "COM-0001", "replannedBuyer"},
		{"view duty may not plan", planningBody, api.DutyView, http.StatusForbidden, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(router(api.New(nil)), planningPath, tenantID, c.duties, c.body)
			p := problem(t, rec)
			if rec.Code != c.status || (c.code != "" && p.Code != c.code) || (c.field != "" && p.Errors[c.field] == "") {
				t.Fatalf("status %d body %s; want %d %s on %q", rec.Code, rec.Body.String(), c.status, c.code, c.field)
			}
		})
	}
	if rec := do(router(api.New(nil)), planningPath, "", api.DutyPlan, planningBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without tenant status = %d; want 401", rec.Code)
	}
}

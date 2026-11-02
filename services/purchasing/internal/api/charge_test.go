package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// chargeLog is what the charge methods of the fake hold and received.
type chargeLog struct {
	codes   map[string]domain.ChargeCode
	autos   []domain.AutoCharge
	created *domain.ChargeCode
	updated *domain.ChargeCode
	auto    *domain.AutoCharge
	err     error
}

func (f *fakeStore) CreateChargeCode(_ context.Context, in domain.ChargeCode, _ store.Idem) (domain.ChargeCode, error) {
	in.ID, in.Version = ids.New(), 1
	f.charges.created = &in
	return in, f.charges.err
}

func (f *fakeStore) GetChargeCode(_ context.Context, id string) (domain.ChargeCode, error) {
	if c, ok := f.charges.codes[id]; ok {
		return c, nil
	}
	return domain.ChargeCode{}, httpx.NewProblem(http.StatusNotFound, store.CodeChargeCodeNotFound, "not found")
}

func (f *fakeStore) ListChargeCodes(context.Context, store.ChargeCodeFilter, string, int) (store.Page[domain.ChargeCode], error) {
	out := []domain.ChargeCode{}
	for _, c := range f.charges.codes {
		out = append(out, c)
	}
	return store.Page[domain.ChargeCode]{Items: out}, f.charges.err
}

func (f *fakeStore) UpdateChargeCode(_ context.Context, id string, _ int, in domain.ChargeCode) (domain.ChargeCode, error) {
	in.ID, in.Version = id, 2
	f.charges.updated = &in
	return in, f.charges.err
}

func (f *fakeStore) CreateAutoCharge(_ context.Context, in domain.AutoCharge, _ store.Idem) (domain.AutoCharge, error) {
	in.ID, in.Version = ids.New(), 1
	f.charges.auto = &in
	return in, f.charges.err
}

func (f *fakeStore) GetAutoCharge(_ context.Context, id string) (domain.AutoCharge, error) {
	for _, a := range f.charges.autos {
		if a.ID == id {
			return a, nil
		}
	}
	return domain.AutoCharge{}, httpx.NewProblem(http.StatusNotFound, store.CodeAutoChargeNotFound, "not found")
}

func (f *fakeStore) ListAutoCharges(context.Context, store.AutoChargeFilter, string, int) (store.Page[domain.AutoCharge], error) {
	return store.Page[domain.AutoCharge]{Items: append([]domain.AutoCharge{}, f.charges.autos...)}, f.charges.err
}

func (f *fakeStore) UpdateAutoCharge(_ context.Context, id string, _ int, in domain.AutoCharge) (domain.AutoCharge, error) {
	in.ID, in.Version = id, 2
	f.charges.auto = &in
	return in, f.charges.err
}

func (f *fakeStore) ActiveAutoCharges(context.Context, string, string, []string, domain.Day) ([]domain.AutoCharge, error) {
	return append([]domain.AutoCharge{}, f.charges.autos...), f.charges.err
}

const (
	chargeCodesPath = "/v1/procurement/charge-codes"
	autoChargesPath = "/v1/procurement/auto-charges"
	ordersPath      = "/v1/procurement/purchase-orders"
)

func (h *harness) seedCode(c domain.ChargeCode) domain.ChargeCode {
	if h.fake.charges.codes == nil {
		h.fake.charges.codes = map[string]domain.ChargeCode{}
	}
	if c.ID == "" {
		c.ID = ids.New()
	}
	if c.LegalEntityID == "" {
		c.LegalEntityID = leA
	}
	if c.Status == "" {
		c.Status = domain.ChargeActive
	}
	h.fake.charges.codes[c.ID] = c
	return c
}

func freight() domain.ChargeCode {
	return domain.ChargeCode{Code: "FREIGHT", Name: "Freight", Calc: domain.CalcFixed, DefaultValue: decimal.NewFromInt(40),
		DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideLedger, CreditAccountID: ids.New(), AllocationRule: domain.AllocateQty}
}

func TestChargeCode_CreateValidatesAndNeedsTheDuty(t *testing.T) {
	h := newHarness(t)
	good := `{"legalEntityId":"` + leA + `","code":" freight ","name":" Freight ","calc":"FIXED","defaultValue":"40"}`
	maintain := []string{dutyPolicyMaintain}
	for _, tc := range []struct {
		name   string
		c      call
		status int
		code   string
	}{
		{"inquire duty cannot write", call{method: "POST", path: chargeCodesPath, user: userA, duties: []string{dutyPurchaseOrderInquire}, body: good, headers: idem("k")}, 403, "AUTH-0003"},
		{"no Idempotency-Key", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, body: good}, 400, codeIdemRequired},
		{"bad calc", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, "FIXED", "WEIGHT", 1)}, 422, domain.CodeValidation},
		{"bad code characters", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, " freight ", "fr eight", 1)}, 422, domain.CodeValidation},
		{"percent over 100", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(strings.Replace(good, "FIXED", "PERCENT", 1), `"40"`, `"101"`, 1)}, 422, domain.CodeValidation},
		{"ledger credit needs an account", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, `"calc"`, `"creditType":"LEDGER","calc"`, 1)}, 422, domain.CodeValidation},
		{"vendor debit needs a vendor", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, `"calc"`, `"debitType":"VENDOR","calc"`, 1)}, 422, domain.CodeValidation},
		{"legal entity outside scope", call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, headers: idem("k"), body: strings.Replace(good, leA, "00000000-0000-7000-8000-0000000001e2", 1)}, 403, codeScope},
	} {
		rec := h.do(tc.c)
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	rec := h.do(call{method: "POST", path: chargeCodesPath, user: userA, duties: maintain, body: good, headers: idem("ok")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.charges.created
	if got == nil || got.Code != "FREIGHT" || got.Name != "Freight" || got.DebitType != domain.ChargeSideItem ||
		got.CreditType != domain.ChargeSideVendor || got.AllocationRule != domain.AllocateAmount || got.Status != domain.ChargeActive {
		t.Errorf("store got %+v; want the normalized code and the PRD default sides", got)
	}
}

func TestChargeCode_ChangeKeepsTheCodeAndNeedsIfMatch(t *testing.T) {
	h := newHarness(t)
	c := h.seedCode(freight())
	path := chargeCodesPath + "/" + c.ID
	body := `{"name":"Inbound freight","calc":"PER_UNIT","defaultValue":"2.5","code":"HIJACK","legalEntityId":"` + ids.New() + `","creditType":"LEDGER","creditAccountId":"` + ids.New() + `","status":"INACTIVE"}`
	duties := []string{dutyPolicyMaintain}
	if rec := h.do(call{method: "PUT", path: path, user: userA, duties: duties, body: body}); rec.Code != 428 {
		t.Errorf("no If-Match: status %d, want 428: %s", rec.Code, rec.Body)
	}
	rec := h.do(call{method: "PUT", path: path, user: userA, duties: duties, body: body, headers: map[string]string{"If-Match": `"1"`}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("change: status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	u := h.fake.charges.updated
	if u == nil || u.Code != "FREIGHT" || u.LegalEntityID != leA || u.Calc != domain.CalcPerUnit || u.Status != domain.ChargeInactive {
		t.Errorf("store got %+v; want the code and legal entity of the stored row and the new fields", u)
	}
}

func TestChargeCode_InquireMayRead(t *testing.T) {
	h := newHarness(t)
	c := h.seedCode(freight())
	for _, path := range []string{chargeCodesPath, chargeCodesPath + "/" + c.ID, autoChargesPath} {
		rec := h.do(call{method: "GET", path: path, user: userA, duties: []string{dutyPurchaseOrderInquire}})
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d: %s", path, rec.Code, rec.Body)
		}
	}
	if rec := h.do(call{method: "GET", path: chargeCodesPath + "?status=GONE", user: userA, duties: []string{dutyPurchaseOrderInquire}}); rec.Code != 422 {
		t.Errorf("bad status filter: status %d, want 422", rec.Code)
	}
	if rec := h.do(call{method: "GET", path: chargeCodesPath, user: userA, duties: []string{dutyRFQMaintain}}); rec.Code != 403 {
		t.Errorf("unrelated duty: status %d, want 403", rec.Code)
	}
}

func TestAutoCharge_CreateChecksTheCodeAndTheMoneyCurrency(t *testing.T) {
	h := newHarness(t)
	c := h.seedCode(freight())
	other := h.seedCode(domain.ChargeCode{Code: "OTHER", Name: "Other", Calc: domain.CalcFixed, LegalEntityID: "00000000-0000-7000-8000-0000000001e2",
		DebitType: "ITEM", CreditType: "VENDOR", AllocationRule: "AMOUNT"})
	off := h.seedCode(domain.ChargeCode{Code: "OFF", Name: "Off", Calc: domain.CalcPercent, Status: domain.ChargeInactive,
		DebitType: "ITEM", CreditType: "VENDOR", AllocationRule: "AMOUNT"})
	vendor := ids.New()
	good := `{"legalEntityId":"` + leA + `","chargeCodeId":"` + c.ID + `","accountCode":"VENDOR","accountId":"` + vendor + `","currency":"USD","minOrderAmount":"500"}`
	maintain := []string{dutyPolicyMaintain}
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"unknown code", strings.Replace(good, c.ID, ids.New(), 1), 404, store.CodeChargeCodeNotFound},
		{"code of another legal entity", strings.Replace(good, c.ID, other.ID, 1), 422, domain.CodeChargeInactive},
		{"inactive code", strings.Replace(good, c.ID, off.ID, 1), 422, domain.CodeChargeInactive},
		{"vendor key without a vendor", strings.Replace(good, `"accountId":"`+vendor+`",`, "", 1), 422, domain.CodeValidation},
		{"ALL key with a vendor", strings.Replace(good, `"VENDOR"`, `"ALL"`, 1), 422, domain.CodeValidation},
		{"money charge without currency", strings.Replace(good, `"currency":"USD",`, "", 1), 422, domain.CodeValidation},
		{"window ends before it starts", strings.Replace(good, `"minOrderAmount"`, `"validFrom":"2026-06-01","validTo":"2026-05-31","minOrderAmount"`, 1), 422, domain.CodeValidation},
	} {
		rec := h.do(call{method: "POST", path: autoChargesPath, user: userA, duties: maintain, headers: idem("k"), body: tc.body})
		if rec.Code != tc.status || problemCode(t, rec) != tc.code {
			t.Errorf("%s: status %d code %s: %s", tc.name, rec.Code, problemCode(t, rec), rec.Body)
		}
	}
	rec := h.do(call{method: "POST", path: autoChargesPath, user: userA, duties: maintain, headers: idem("ok"), body: good})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status %d: %s", rec.Code, rec.Body)
	}
	got := h.fake.charges.auto
	if got == nil || got.ValidFrom.String() != "2026-03-01" || got.MinOrderAmount == nil || !got.MinOrderAmount.Equal(decimal.NewFromInt(500)) || got.Status != domain.ChargeActive {
		t.Errorf("store got %+v; want validFrom today, the minimum and ACTIVE", got)
	}
}

func orderWith(vendor, charges string, apply bool) string {
	auto := ""
	if apply {
		auto = `,"applyAutoCharges":true`
	}
	return `{"legalEntityId":"` + leA + `","vendorId":"` + vendor + `","currency":"USD"` + auto + `,
	 "lines":[{"description":"Steel","qty":"100","unit":"EA","unitPrice":"10","deliveryDate":"2026-04-01"}]` + charges + `}`
}

func createOrder(h *harness, body string) (store.PurchaseOrder, int, string) {
	rec := h.do(call{method: "POST", path: ordersPath, duties: []string{dutyPurchaseOrderMaintain}, body: body, headers: idem("po")})
	var po store.PurchaseOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &po)
	code := ""
	if rec.Code >= 400 {
		code = problemCode(h.t, rec)
	}
	return po, rec.Code, code
}

func TestCreateOrder_ChargeCodeFillsTheChargeAndCopiesItsSides(t *testing.T) {
	h := newHarness(t)
	c := h.seedCode(freight())
	vendor := ids.New()
	// The code supplies calc and value (40); an explicit value overrides the default.
	po, status, _ := createOrder(h, orderWith(vendor, `,"charges":[{"chargeCodeId":"`+c.ID+`"},{"chargeCodeId":"`+c.ID+`","value":"15"}]`, false))
	if status != http.StatusCreated || len(po.Charges) != 2 {
		t.Fatalf("status %d charges %+v", status, po.Charges)
	}
	first, second := po.Charges[0], po.Charges[1]
	if !first.Amount.Equal(decimal.NewFromInt(40)) || !second.Amount.Equal(decimal.NewFromInt(15)) || first.ChargeCode != "FREIGHT" ||
		first.CreditType != domain.ChargeSideLedger || first.CreditAccountID != c.CreditAccountID || first.AllocationRule != domain.AllocateQty || first.Auto {
		t.Errorf("charges %+v; want the code's calc, value and sides, the override 15 on the second", po.Charges)
	}
	if !po.ChargesTotal.Equal(decimal.NewFromInt(55)) || !po.Total.Equal(decimal.NewFromInt(1055)) {
		t.Errorf("chargesTotal %s total %s; want 55 and 1055", po.ChargesTotal, po.Total)
	}
}

func TestCreateOrder_ChargeCodeRefusals(t *testing.T) {
	h := newHarness(t)
	c := h.seedCode(freight())
	off := h.seedCode(domain.ChargeCode{Code: "OFF", Name: "Off", Calc: domain.CalcFixed, DefaultValue: decimal.NewFromInt(1), Status: domain.ChargeInactive,
		DebitType: "ITEM", CreditType: "VENDOR", AllocationRule: "AMOUNT"})
	foreign := h.seedCode(domain.ChargeCode{Code: "FOR", Name: "Foreign", Calc: domain.CalcFixed, LegalEntityID: "00000000-0000-7000-8000-0000000001e2",
		DebitType: "ITEM", CreditType: "VENDOR", AllocationRule: "AMOUNT"})
	vendor := ids.New()
	for _, tc := range []struct {
		name, charges string
		status        int
		code          string
	}{
		{"inactive code", `[{"chargeCodeId":"` + off.ID + `"}]`, 422, domain.CodeChargeInactive},
		{"code of another legal entity", `[{"chargeCodeId":"` + foreign.ID + `"}]`, 422, domain.CodeChargeInactive},
		{"calc differs from the code", `[{"chargeCodeId":"` + c.ID + `","calc":"PERCENT"}]`, 422, domain.CodeChargeCalcMismatch},
		{"unknown code", `[{"chargeCodeId":"` + ids.New() + `"}]`, 404, store.CodeChargeCodeNotFound},
		{"code is not a UUID", `[{"chargeCodeId":"nope"}]`, 422, codeInvalid},
		{"negative override", `[{"chargeCodeId":"` + c.ID + `","value":"-1"}]`, 422, codeInvalid},
	} {
		_, status, code := createOrder(h, orderWith(vendor, `,"charges":`+tc.charges, false))
		if status != tc.status || code != tc.code {
			t.Errorf("%s: status %d code %s", tc.name, status, code)
		}
	}
}

func TestCreateOrder_AutoChargesApplyByVendorCurrencyAndOrderTotal(t *testing.T) {
	h := newHarness(t)
	vendor, group := ids.New(), ids.New()
	h.api.WithMemberships(groups{vendor: []string{group}})
	fr := h.seedCode(freight())
	pct := h.seedCode(domain.ChargeCode{Code: "HANDLING", Name: "Handling", Calc: domain.CalcPercent, DefaultValue: decimal.NewFromInt(2),
		DebitType: domain.ChargeSideLedger, DebitAccountID: ids.New(), CreditType: domain.ChargeSideVendor, AllocationRule: domain.AllocateAmount})
	day := func(s string) domain.Day { return mustDay(t, s) }
	dec := func(s string) *decimal.Decimal { d := decimal.RequireFromString(s); return &d }
	h.fake.charges.autos = []domain.AutoCharge{
		// Applies: the vendor, USD, order total 1000 >= 500.
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeVendor, AccountID: vendor, Currency: "USD", MinOrderAmount: dec("500"),
			ValidFrom: day("2026-01-01"), Status: domain.ChargeActive},
		// Applies: the vendor's group, PERCENT in any currency.
		{ID: ids.New(), ChargeCodeID: pct.ID, AccountCode: domain.AutoChargeGroup, AccountID: group, ValidFrom: day("2026-01-01"), Status: domain.ChargeActive},
		// Does not apply: another vendor.
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeVendor, AccountID: ids.New(), Currency: "USD", ValidFrom: day("2026-01-01"), Status: domain.ChargeActive},
		// Does not apply: minimum above the order.
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, Currency: "USD", MinOrderAmount: dec("5000"), ValidFrom: day("2026-01-01"), Status: domain.ChargeActive},
		// Does not apply: window ended.
		{ID: ids.New(), ChargeCodeID: pct.ID, AccountCode: domain.AutoChargeAll, ValidFrom: day("2025-01-01"), ValidTo: func() *domain.Day { d := day("2025-12-31"); return &d }(), Status: domain.ChargeActive},
	}
	po, status, code := createOrder(h, orderWith(vendor, "", true))
	if status != http.StatusCreated {
		t.Fatalf("status %d %s", status, code)
	}
	if len(po.Charges) != 2 || po.Charges[0].ChargeCode != "FREIGHT" || po.Charges[1].ChargeCode != "HANDLING" || !po.Charges[0].Auto || !po.Charges[1].Auto {
		t.Fatalf("charges %+v; want FREIGHT then HANDLING, both auto", po.Charges)
	}
	// FREIGHT 40 fixed; HANDLING 2% of 1000 = 20.
	if !po.ChargesTotal.Equal(decimal.NewFromInt(60)) || po.Charges[1].DebitType != domain.ChargeSideLedger {
		t.Errorf("chargesTotal %s, second %+v; want 60 and a LEDGER debit", po.ChargesTotal, po.Charges[1])
	}
	// A code entered by hand is not added a second time, and nothing is added without the flag.
	po, _, _ = createOrder(h, orderWith(vendor, `,"charges":[{"chargeCodeId":"`+fr.ID+`","value":"7"}]`, true))
	if len(po.Charges) != 2 || !po.Charges[0].Amount.Equal(decimal.NewFromInt(7)) || po.Charges[0].Auto {
		t.Errorf("charges %+v; want the entered FREIGHT 7 kept (not auto) and only HANDLING added", po.Charges)
	}
	if po, _, _ = createOrder(h, orderWith(vendor, "", false)); len(po.Charges) != 0 {
		t.Errorf("charges %+v; want none without applyAutoCharges", po.Charges)
	}
	// Another currency drops the money charge and keeps the PERCENT one.
	eur := strings.Replace(orderWith(vendor, "", true), `"USD"`, `"EUR"`, 1)
	if po, _, _ = createOrder(h, eur); len(po.Charges) != 1 || po.Charges[0].ChargeCode != "HANDLING" {
		t.Errorf("charges %+v; want only HANDLING in EUR", po.Charges)
	}
}

// editWith is a PATCH body for storedPO that keeps line 3 and carries charges.
func editWith(charges string, apply bool) string {
	auto := ""
	if apply {
		auto = `,"applyAutoCharges":true`
	}
	return `{"legalEntityId":"` + leA + `","vendorId":"` + vendorA + `","currency":"USD"` + auto + `,
	 "lines":[{"lineNo":3,"description":"Freight box","qty":"2","unit":"EA","unitPrice":"4","deliveryDate":"2026-04-01"}]` + charges + `}`
}

func TestEditOrder_AutoChargesAreAddedOncePerCodeAndStayRemoved(t *testing.T) {
	h := newHarness(t)
	fr := h.seedCode(freight())
	pct := h.seedCode(domain.ChargeCode{Code: "HANDLING", Name: "Handling", Calc: domain.CalcPercent, DefaultValue: decimal.NewFromInt(10),
		DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideVendor, AllocationRule: domain.AllocateAmount})
	h.fake.charges.autos = []domain.AutoCharge{
		{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, Currency: "USD", ValidFrom: mustDay(t, "2026-01-01"), Status: domain.ChargeActive},
		{ID: ids.New(), ChargeCodeID: pct.ID, AccountCode: domain.AutoChargeVendor, AccountID: vendorA, ValidFrom: mustDay(t, "2026-01-01"), Status: domain.ChargeActive},
	}
	po := storedPO(h, domain.POStatusDraft)
	// FREIGHT was added by an auto charge before and the buyer removed it; HANDLING is new.
	po.AutoChargeCodes = []string{fr.ID}
	h.fake.poByID[po.ID] = po
	edit := func(body string) store.PurchaseOrder {
		t.Helper()
		rec := h.do(call{method: "PATCH", path: poPath(po.ID), duties: []string{dutyPurchaseOrderMaintain}, body: body,
			headers: map[string]string{"If-Match": `"5"`}})
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		return h.fake.lastEdit.next
	}
	next := edit(editWith("", true))
	if len(next.Charges) != 1 || next.Charges[0].ChargeCode != "HANDLING" || !next.Charges[0].Auto ||
		len(next.AutoChargeCodes) != 1 || next.AutoChargeCodes[0] != pct.ID {
		t.Fatalf("charges %+v codes %v; want only HANDLING added (FREIGHT was removed by the buyer)", next.Charges, next.AutoChargeCodes)
	}
	// 10% of the 8.00 line; the total the signing limit is checked against carries the charge.
	if !next.ChargesTotal.Equal(decimal.RequireFromString("0.8")) || !next.Total.Equal(decimal.RequireFromString("8.8")) {
		t.Errorf("chargesTotal %s total %s; want 0.8 and 8.8", next.ChargesTotal, next.Total)
	}

	// The PO now carries HANDLING (auto) and the code has since been deactivated and changed: an edit
	// that keeps it keeps its calculation, value and sides, and is not refused; nothing is added twice.
	po.AutoChargeCodes = []string{fr.ID, pct.ID}
	po.Charges = []store.PurchaseOrderCharge{{Seq: 1, Calc: domain.CalcPercent, Value: decimal.NewFromInt(10), Amount: decimal.RequireFromString("0.8"),
		ChargeCodeID: pct.ID, ChargeCode: "HANDLING", DebitType: domain.ChargeSideItem, CreditType: domain.ChargeSideVendor,
		AllocationRule: domain.AllocateAmount, Auto: true}}
	h.fake.poByID[po.ID] = po
	changed := pct
	changed.Status, changed.Calc, changed.DefaultValue, changed.DebitType = domain.ChargeInactive, domain.CalcFixed, decimal.NewFromInt(99), domain.ChargeSideLedger
	h.fake.charges.codes[pct.ID] = changed
	next = edit(editWith(`,"charges":[{"chargeCodeId":"`+pct.ID+`"}]`, true))
	if len(next.Charges) != 1 || next.Charges[0].Calc != domain.CalcPercent || !next.Charges[0].Value.Equal(decimal.NewFromInt(10)) ||
		next.Charges[0].DebitType != domain.ChargeSideItem || !next.Charges[0].Auto || len(next.AutoChargeCodes) != 0 {
		t.Errorf("charges %+v codes %v; want the kept HANDLING 10%% ITEM charge and no new code", next.Charges, next.AutoChargeCodes)
	}
	// Removing it again keeps it removed.
	if next = edit(editWith("", true)); len(next.Charges) != 0 {
		t.Errorf("charges %+v; want none: both codes were added before", next.Charges)
	}
}

func TestCreateOrder_AutoChargeMinimumIsTheLinesAfterDiscountBeforeCharges(t *testing.T) {
	h := newHarness(t)
	fr := h.seedCode(freight())
	vendor := ids.New()
	min := decimal.NewFromInt(1000)
	h.fake.charges.autos = []domain.AutoCharge{{ID: ids.New(), ChargeCodeID: fr.ID, AccountCode: domain.AutoChargeAll, Currency: "USD",
		MinOrderAmount: &min, ValidFrom: mustDay(t, "2026-01-01"), Status: domain.ChargeActive}}
	// The lines total exactly 1000: the minimum is met.
	if po, status, code := createOrder(h, orderWith(vendor, "", true)); status != http.StatusCreated || len(po.Charges) != 1 {
		t.Fatalf("at the minimum: status %d %s charges %+v", status, code, po.Charges)
	}
	// A 1% line discount takes the lines to 990; a manual charge of 50 does not count towards the minimum.
	body := strings.Replace(orderWith(vendor, `,"charges":[{"calc":"FIXED","value":"50"}]`, true), `"unitPrice":"10"`, `"unitPrice":"10","discountPct":"1"`, 1)
	if po, status, code := createOrder(h, body); status != http.StatusCreated || len(po.Charges) != 1 || po.Charges[0].Auto {
		t.Errorf("below the minimum after discount: status %d %s charges %+v; want only the manual charge", status, code, po.Charges)
	}
}

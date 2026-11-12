package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/store"
	"github.com/BenjaminSRussell/passage/services/warehouse/migrations"
)

func (f *fakeStore) GetGS1Company(context.Context) (store.GS1Company, error) {
	return store.GS1Company{CompanyPrefix: "0614141", NextSerial: 1}, f.err
}
func (f *fakeStore) GetPalletLabel(context.Context, string) (store.PalletLabel, error) {
	return store.PalletLabel{}, f.err
}
func (f *fakeStore) PutGS1Company(_ context.Context, prefix string, ext int, by string) (store.GS1Company, error) {
	return store.GS1Company{CompanyPrefix: prefix, Extension: ext, NextSerial: 1, UpdatedBy: by}, f.err
}
func (f *fakeStore) IngestRFID(context.Context, []store.RFIDRead) (store.RFIDIngest, error) {
	return store.RFIDIngest{}, f.err
}
func (f *fakeStore) ListRFIDReads(context.Context, store.RFIDFilter) ([]store.RFIDReadRow, error) {
	return nil, f.err
}
func (f *fakeStore) CreateAutomationOrder(context.Context, store.OrderInput) (store.AutomationOrder, error) {
	return store.AutomationOrder{}, f.err
}
func (f *fakeStore) ListAutomationOrders(context.Context, store.OrderFilter) ([]store.AutomationOrder, error) {
	return nil, f.err
}
func (f *fakeStore) ReportAutomationOrder(context.Context, string, string, string, time.Time) (store.AutomationOrder, error) {
	return store.AutomationOrder{}, f.err
}
func (f *fakeStore) IngestAutomationEvents(context.Context, string, []store.AutomationEvent, time.Time) (store.AutomationIngest, error) {
	return store.AutomationIngest{}, f.err
}

// Duties and validation of the RFID and automation routes, before any store is reached.
func TestAutomationRoutes_DutiesAndValidation(t *testing.T) {
	h := persistedRouter(&fakeStore{})
	wh := ids.New()
	read := `{"readerId":"portal-1","antenna":1,"epc":"3074257BF7194E4000001A85","rssi":-52,"readAt":"2026-10-27T09:00:00Z"}`
	for name, tc := range map[string]struct {
		q      req
		status int
		field  string
	}{
		"rfid ingest needs its duty":      {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyInquiry, body: `{"reads":[` + read + `]}`}, 403, ""},
		"rfid ingest accepts a read":      {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyRFIDIngest, body: `{"reads":[` + read + `]}`}, 200, ""},
		"rfid reads must not be empty":    {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyRFIDIngest, body: `{"reads":[]}`}, 400, "reads"},
		"rfid reader is required":         {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyRFIDIngest, body: `{"reads":[{"epc":"x","readAt":"2026-10-27T09:00:00Z"}]}`}, 400, "reads[0].readerId"},
		"rfid time must parse":            {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyRFIDIngest, body: `{"reads":[{"readerId":"r","epc":"x","readAt":"yesterday"}]}`}, 400, "reads[0].readAt"},
		"rfid read from the future":       {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyRFIDIngest, body: `{"reads":[{"readerId":"r","epc":"x","readAt":"2999-01-01T00:00:00Z"}]}`}, 400, "reads[0].readAt"},
		"rfid rssi range":                 {req{method: "POST", path: "/v1/warehouse/rfid-reads:ingest", duties: dutyRFIDIngest, body: `{"reads":[{"readerId":"r","epc":"x","rssi":5,"readAt":"2026-10-27T09:00:00Z"}]}`}, 400, "reads[0].rssi"},
		"rfid listing needs inquiry":      {req{method: "GET", path: "/v1/warehouse/rfid-reads", duties: dutyRFIDIngest}, 403, ""},
		"rfid listing filters validate":   {req{method: "GET", path: "/v1/warehouse/rfid-reads?from=yesterday", duties: dutyInquiry}, 400, "from"},
		"order needs the supervise duty":  {req{method: "POST", path: "/v1/warehouse/automation/transport-orders", duties: dutyAutomation, body: `{"workId":"` + wh + `","lineNo":1,"station":"PLC"}`}, 403, ""},
		"order station is validated":      {req{method: "POST", path: "/v1/warehouse/automation/transport-orders", duties: dutyWorkSupervise, body: `{"workId":"` + wh + `","lineNo":1,"station":"TOO-LONG-NAME"}`}, 400, "station"},
		"order load unit check digit":     {req{method: "POST", path: "/v1/warehouse/automation/transport-orders", duties: dutyWorkSupervise, body: `{"workId":"` + wh + `","lineNo":1,"station":"PLC","loadUnit":"306141410000000012"}`}, 400, "loadUnit"},
		"orders listing: status checked":  {req{method: "GET", path: "/v1/warehouse/automation/transport-orders?status=DONE", duties: dutyAutomation}, 400, "status"},
		"orders listing: connector duty":  {req{method: "GET", path: "/v1/warehouse/automation/transport-orders?status=PENDING&station=PLC", duties: dutyAutomation}, 200, ""},
		"orders listing: person duty":     {req{method: "GET", path: "/v1/warehouse/automation/transport-orders", duties: dutyInquiry}, 200, ""},
		"orders listing: no duty":         {req{method: "GET", path: "/v1/warehouse/automation/transport-orders", duties: dutyReceive}, 403, ""},
		"report status is checked":        {req{method: "POST", path: "/v1/warehouse/automation/transport-orders/" + wh + ":report", duties: dutyAutomation, body: `{"status":"ARRIVED"}`}, 400, "status"},
		"report needs the connector duty": {req{method: "POST", path: "/v1/warehouse/automation/transport-orders/" + wh + ":report", duties: dutyWorkSupervise, body: `{"status":"SENT"}`}, 403, ""},
		"events kind is checked":          {req{method: "POST", path: "/v1/warehouse/automation/events", duties: dutyAutomation, body: `{"station":"PLC","events":[{"kind":"melted","session":"s1","seq":1,"at":"2026-10-27T09:00:00Z"}]}`}, 400, "events[0].kind"},
		"events station is checked":       {req{method: "POST", path: "/v1/warehouse/automation/events", duties: dutyAutomation, body: `{"station":"","events":[{"kind":"fault","session":"s1","seq":1,"at":"2026-10-27T09:00:00Z"}]}`}, 400, "station"},
		"events session is required":      {req{method: "POST", path: "/v1/warehouse/automation/events", duties: dutyAutomation, body: `{"station":"PLC","events":[{"kind":"fault","seq":1,"code":"0042","at":"2026-10-27T09:00:00Z"}]}`}, 400, "events[0].session"},
		"events are accepted":             {req{method: "POST", path: "/v1/warehouse/automation/events", duties: dutyAutomation, body: `{"station":"PLC","events":[{"kind":"fault","session":"s1","seq":1,"code":"0042","at":"2026-10-27T09:00:00Z"}]}`}, 200, ""},
		"pallet label needs inquiry":      {req{method: "GET", path: "/v1/warehouse/pallets/306141410000000013/label", duties: dutyReceive}, 403, ""},
		"gs1 settings need setup":         {req{method: "PUT", path: "/v1/warehouse/gs1-settings", duties: dutySetupView, body: `{"companyPrefix":"0614141"}`}, 403, ""},
		"gs1 prefix is validated":         {req{method: "PUT", path: "/v1/warehouse/gs1-settings", duties: dutySetupMaintain, body: `{"companyPrefix":"12"}`}, 400, "companyPrefix"},
		"gs1 extension is validated":      {req{method: "PUT", path: "/v1/warehouse/gs1-settings", duties: dutySetupMaintain, body: `{"companyPrefix":"0614141","extension":12}`}, 400, "extension"},
		"gs1 settings are set":            {req{method: "PUT", path: "/v1/warehouse/gs1-settings", duties: dutySetupMaintain, body: `{"companyPrefix":"0614141","extension":3}`}, 200, ""},
	} {
		rec := call(h, tc.q)
		if rec.Code != tc.status || (tc.field != "" && !strings.Contains(rec.Body.String(), tc.field)) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// Receive, label, scan, move: the warehouse generates the pallet's SSCC when it is received and the work carries it;
// an RFID portal reading the pallet's tag names its receipt; a transport order sends the move to a PLC; the PLC's
// arrival (repeated, as a PLC repeats a telegram) confirms the put-away exactly once and stock lands.
func TestPalletFromReceiptToPLCArrival(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := persistedRouter(store.New(pool))
	wh := ids.New()
	all := strings.Join([]string{dutySetupMaintain, dutySetupView, dutyReceive, dutyInquiry, dutyWorkSupervise, dutyRFIDIngest, dutyAutomation}, ",")
	post := func(path, key, body, user string) *httptest.ResponseRecorder {
		return call(h, req{method: "POST", path: path, duties: all, key: key, body: body, user: user})
	}
	if rec := post("/v1/warehouse/locations:bulk", "l1", `{"warehouseId":"`+wh+`","locations":[{"name":"INBOUND","zone":"I","capacity":{"allowMixedItems":true}},{"name":"ASRS-01","zone":"A","capacity":{"allowMixedItems":true}}]}`, ""); rec.Code != 201 {
		t.Fatalf("locations: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/v1/warehouse/location-directives", "d1", `{"warehouseId":"`+wh+`","sequence":10,"workType":"PURCHASE_PUTAWAY","direction":"PUT","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"consolidate","zone":"A"}]}]}`, ""); rec.Code != 201 {
		t.Fatalf("directive: %d %s", rec.Code, rec.Body)
	}
	// The ASN names no license plate: the warehouse needs its GS1 prefix first.
	if rec := post("/v1/warehouse/receipts:from-asn", "asn", `{"warehouseId":"`+wh+`","asnRef":"ASN-9","lines":[{"lineNo":1,"itemId":"BOLT","expectedQty":"100","lot":"L-9","expiryDate":"2027-03-31"}]}`, ""); rec.Code != 201 {
		t.Fatalf("ASN without a license plate: %d %s", rec.Code, rec.Body)
	}
	var rcp domain.Receipt
	rec := call(h, req{method: "GET", path: "/v1/warehouse/receipts?warehouseId=" + wh, duties: all})
	var list struct{ Items []domain.ReceiptSummary }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	rec = call(h, req{method: "GET", path: "/v1/warehouse/receipts/" + list.Items[0].ID, duties: all})
	_ = json.Unmarshal(rec.Body.Bytes(), &rcp)
	receive := "/v1/warehouse/receipts/" + rcp.ID + "/lines/1:receive"
	if rec := post(receive, "r-early", `{"qty":"100"}`, "clerk"); rec.Code != 422 || problemOf(t, rec).Code != domain.CodeGS1NotSet {
		t.Fatalf("receive before the GS1 prefix is set: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, req{method: "GET", path: "/v1/warehouse/gs1-settings", duties: all}); rec.Code != 404 {
		t.Fatalf("settings before they are set: %d", rec.Code)
	}
	if rec := call(h, req{method: "PUT", path: "/v1/warehouse/gs1-settings", duties: all, body: `{"companyPrefix":"0614141","extension":3}`}); rec.Code != 200 {
		t.Fatalf("set the prefix: %d %s", rec.Code, rec.Body)
	}
	rec = post(receive, "r1", `{"qty":"100"}`, "clerk")
	var got store.Received
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body)
	}
	sscc := got.Receipt.Lines[0].SSCC
	attrs := got.Work.Lines[0].Attributes
	if sscc != "306141410000000013" || attrs["sscc"] != sscc || attrs["lpId"] != sscc || attrs["lot"] != "L-9" || attrs["expiry"] != "2027-03-31" {
		t.Fatalf("SSCC %q, work line attributes %v", sscc, attrs)
	}
	// The same work, read through the work API a device uses, carries what the label needs.
	rec = call(h, req{method: "GET", path: "/v1/warehouse/work/" + got.Work.ID, duties: all})
	if !strings.Contains(rec.Body.String(), `"sscc":"`+sscc+`"`) {
		t.Fatalf("work API: %s", rec.Body)
	}

	// The device asks for the label data by the SSCC it scanned and prints from it.
	rec = call(h, req{method: "GET", path: "/v1/warehouse/pallets/" + sscc + "/label", duties: dutyInquiry})
	var label store.PalletLabel
	_ = json.Unmarshal(rec.Body.Bytes(), &label)
	if rec.Code != 200 || label.ItemID != "BOLT" || label.Lot != "L-9" || label.ExpiryDate != "2027-03-31" || label.LPID != sscc || label.WorkID != got.Work.ID || !label.Received {
		t.Fatalf("pallet label: %d %s", rec.Code, rec.Body)
	}
	if rec := call(h, req{method: "GET", path: "/v1/warehouse/pallets/306141410000000020/label", duties: dutyInquiry}); rec.Code != 404 {
		t.Fatalf("a pallet that does not exist: %d", rec.Code)
	}
	if rec := call(h, req{method: "GET", path: "/v1/warehouse/pallets/123/label", duties: dutyInquiry}); rec.Code != 400 {
		t.Fatalf("a bad SSCC: %d", rec.Code)
	}

	// A portal reads the pallet's tag: the SSCC-96 of the label.
	epc, err := domain.EncodeEPC96(0x31, 3, "0614141", "3000000001", nil)
	if err != nil {
		t.Fatal(err)
	}
	read := func(at string) string {
		return `{"reads":[{"readerId":"portal-1","antenna":2,"locationCode":"INBOUND","epc":"` + epc + `","rssi":-48,"readAt":"` + at + `"},{"readerId":"portal-1","epc":"FF74257BF7194E4000001A85","readAt":"` + at + `"}]}`
	}
	rec = post("/v1/warehouse/rfid-reads:ingest", "", read("2026-10-27T09:00:00Z"), "")
	var ing store.RFIDIngest
	_ = json.Unmarshal(rec.Body.Bytes(), &ing)
	if rec.Code != 200 || ing.Accepted != 1 || len(ing.Rejected) != 1 || ing.Rejected[0].Index != 1 || ing.Rejected[0].Code != domain.CodeEPCInvalid {
		t.Fatalf("rfid ingest: %d %s", rec.Code, rec.Body)
	}
	if ing.Reads[0].EPC.Key != sscc || ing.Reads[0].ReceiptID != rcp.ID || ing.Reads[0].LineNo != 1 {
		t.Fatalf("the read names %+v, want the SSCC %s on receipt %s", ing.Reads[0], sscc, rcp.ID)
	}
	rec = post("/v1/warehouse/rfid-reads:ingest", "", read("2026-10-27T09:00:02Z"), "")
	_ = json.Unmarshal(rec.Body.Bytes(), &ing)
	if ing.Accepted != 0 || ing.Duplicates != 1 {
		t.Fatalf("the same tag two seconds later: %+v", ing)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/rfid-reads?key=" + sscc, duties: all})
	if !strings.Contains(rec.Body.String(), `"locationCode":"INBOUND"`) || strings.Count(rec.Body.String(), `"readerId"`) != 1 {
		t.Fatalf("stored reads: %s", rec.Body)
	}

	// The put-away is moved by a PLC: the line has the shelf it goes to and no source, so the order names the
	// infeed point. The work is assigned to the station's identity and the order waits for the connector.
	orderPath := "/v1/warehouse/automation/transport-orders"
	if rec := post(orderPath, "", fmt.Sprintf(`{"workId":%q,"lineNo":1,"station":"PLC"}`, got.Work.ID), "super"); rec.Code != 422 || problemOf(t, rec).Code != store.CodeAutoLocation {
		t.Fatalf("an order for a line with no from location: %d %s", rec.Code, rec.Body)
	}
	rec = post(orderPath, "", fmt.Sprintf(`{"workId":%q,"lineNo":1,"station":"PLC","fromLocation":"INFEED-1"}`, got.Work.ID), "super")
	var ord store.AutomationOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &ord)
	if rec.Code != 201 || ord.Status != store.OrderPending || ord.LoadUnit != sscc || ord.FromLocation != "INFEED-1" || ord.ToLocation == "" || ord.Priority != 5 {
		t.Fatalf("order: %d %s", rec.Code, rec.Body)
	}
	if rec := post(orderPath, "", fmt.Sprintf(`{"workId":%q,"lineNo":1,"station":"PLC","fromLocation":"INFEED-1"}`, got.Work.ID), "super"); rec.Code != 409 || problemOf(t, rec).Code != store.CodeAutoOrderExists {
		t.Fatalf("a second order for the line: %d %s", rec.Code, rec.Body)
	}
	pending := func() []store.AutomationOrder {
		rec := call(h, req{method: "GET", path: orderPath + "?status=PENDING&station=PLC", duties: dutyAutomation})
		var l struct{ Items []store.AutomationOrder }
		_ = json.Unmarshal(rec.Body.Bytes(), &l)
		return l.Items
	}
	if p := pending(); len(p) != 1 || p[0].ID != ord.ID {
		t.Fatalf("pending orders %+v", p)
	}
	report := func(status, reason string) *httptest.ResponseRecorder {
		return call(h, req{method: "POST", path: orderPath + "/" + ord.ID + ":report", duties: dutyAutomation, body: fmt.Sprintf(`{"status":%q,"reason":%q}`, status, reason)})
	}
	if rec := report("SENT", ""); rec.Code != 200 {
		t.Fatalf("report sent: %d %s", rec.Code, rec.Body)
	}
	if rec := report("SENT", ""); rec.Code != 200 { // a connector that lost the answer repeats it
		t.Fatalf("report sent twice: %d %s", rec.Code, rec.Body)
	}
	if rec := report("REFUSED", "no"); rec.Code != 409 || problemOf(t, rec).Code != store.CodeAutoOrderState {
		t.Fatalf("refusing a sent order: %d %s", rec.Code, rec.Body)
	}
	if len(pending()) != 0 {
		t.Fatal("a sent order is still pending")
	}

	// The PLC reports the arrival, and repeats the telegram; stock lands once.
	events := func(seq int) string {
		return fmt.Sprintf(`{"station":"PLC","events":[{"kind":"arrived","session":"link-1","seq":%d,"loadUnit":%q,"location":%q,"at":"2026-10-27T09:05:00Z"},{"kind":"arrived","session":"link-1","seq":%d,"loadUnit":"000000000000000000","at":"2026-10-27T09:05:01Z"}]}`, seq, sscc, ord.ToLocation, seq+1)
	}
	rec = call(h, req{method: "POST", path: "/v1/warehouse/automation/events", duties: dutyAutomation, body: events(7)})
	var res store.AutomationIngest
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res.Accepted != 2 || res.Confirmed != 1 || res.Duplicates != 0 {
		t.Fatalf("arrival: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, req{method: "POST", path: "/v1/warehouse/automation/events", duties: dutyAutomation, body: events(7)})
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res.Accepted != 0 || res.Duplicates != 2 || res.Confirmed != 0 {
		t.Fatalf("the repeated telegram: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/work/" + got.Work.ID, duties: all})
	var w domain.Work
	_ = json.Unmarshal(rec.Body.Bytes(), &w)
	if w.Status != domain.WorkCompleted || w.Lines[0].Status != domain.LineCompleted || w.AssignedUser != "automation:PLC" {
		t.Fatalf("work after the arrival: %s %+v", w.Status, w.Lines[0])
	}
	rec = call(h, req{method: "GET", path: orderPath + "?station=PLC&status=ARRIVED", duties: dutyInquiry})
	if !strings.Contains(rec.Body.String(), ord.ID) || strings.Contains(rec.Body.String(), `"confirmError"`) {
		t.Fatalf("orders after the arrival: %s", rec.Body)
	}
}
func (f *fakeStore) CancelAutomationOrder(context.Context, store.CancelInput) (store.AutomationOrder, error) {
	return store.AutomationOrder{}, f.err
}
func (f *fakeStore) CreateRFIDRule(_ context.Context, r store.RFIDRule) (store.RFIDRule, error) {
	return r, f.err
}
func (f *fakeStore) ListRFIDRules(context.Context, string) ([]store.RFIDRule, error) {
	return nil, f.err
}
func (f *fakeStore) DeleteRFIDRule(context.Context, string) error { return f.err }

// Duties and validation of the RFID rule and order cancel routes.
func TestRFIDRuleAndCancelRoutes_DutiesAndValidation(t *testing.T) {
	h := persistedRouter(&fakeStore{})
	id := ids.New()
	for name, tc := range map[string]struct {
		q      req
		status int
		field  string
	}{
		"rules listing needs setup view":  {req{method: "GET", path: "/v1/warehouse/rfid-rules", duties: dutyInquiry}, 403, ""},
		"rules are listed":                {req{method: "GET", path: "/v1/warehouse/rfid-rules?readerId=dock-1", duties: dutySetupView}, 200, ""},
		"a rule needs setup maintain":     {req{method: "POST", path: "/v1/warehouse/rfid-rules", duties: dutySetupView, body: `{"readerId":"dock-1","action":"RECEIVE_PALLET"}`}, 403, ""},
		"a rule's action is checked":      {req{method: "POST", path: "/v1/warehouse/rfid-rules", duties: dutySetupMaintain, body: `{"readerId":"dock-1","action":"SHIP"}`}, 400, "action"},
		"a rule's reader is required":     {req{method: "POST", path: "/v1/warehouse/rfid-rules", duties: dutySetupMaintain, body: `{"action":"RECEIVE_PALLET"}`}, 400, "readerId"},
		"a rule's antenna range":          {req{method: "POST", path: "/v1/warehouse/rfid-rules", duties: dutySetupMaintain, body: `{"readerId":"dock-1","antenna":300,"action":"RECEIVE_PALLET"}`}, 400, "antenna"},
		"a rule's warehouse is a uuid":    {req{method: "POST", path: "/v1/warehouse/rfid-rules", duties: dutySetupMaintain, body: `{"readerId":"dock-1","warehouseId":"WH1","action":"RECEIVE_PALLET"}`}, 400, "warehouseId"},
		"a rule is created":               {req{method: "POST", path: "/v1/warehouse/rfid-rules", duties: dutySetupMaintain, body: `{"readerId":"dock-1","antenna":2,"action":"CONFIRM_PUTAWAY","locationCode":"A-01"}`}, 201, ""},
		"a rule is deleted":               {req{method: "DELETE", path: "/v1/warehouse/rfid-rules/" + id, duties: dutySetupMaintain}, 204, ""},
		"deleting needs setup maintain":   {req{method: "DELETE", path: "/v1/warehouse/rfid-rules/" + id, duties: dutySetupView}, 403, ""},
		"a bad rule id is not found":      {req{method: "DELETE", path: "/v1/warehouse/rfid-rules/x", duties: dutySetupMaintain}, 404, ""},
		"cancel needs the supervise duty": {req{method: "POST", path: "/v1/warehouse/automation/transport-orders/" + id + ":cancel", duties: dutyAutomation, body: `{"reason":"jam"}`}, 403, ""},
		"cancel needs a reason":           {req{method: "POST", path: "/v1/warehouse/automation/transport-orders/" + id + ":cancel", duties: dutyWorkSupervise, body: `{}`}, 400, "reason"},
		"cancel is accepted":              {req{method: "POST", path: "/v1/warehouse/automation/transport-orders/" + id + ":cancel", duties: dutyWorkSupervise, body: `{"reason":"jam","plcCleared":true}`}, 200, ""},
		"cancel of a bad id is not found": {req{method: "POST", path: "/v1/warehouse/automation/transport-orders/x:cancel", duties: dutyWorkSupervise, body: `{"reason":"jam"}`}, 404, ""},
	} {
		rec := call(h, tc.q)
		if rec.Code != tc.status || (tc.field != "" && !strings.Contains(rec.Body.String(), tc.field)) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

// RFID rules act on reads: a dock portal that reads the vendor's SSCC receives the ASN pallet at the announced
// quantity, and a reader at the shelf confirms the put-away of that pallet once it arrives there; a repeat read or a
// tag that is not a pallet does nothing.
func TestRFIDRulesReceiveAndConfirmPutaway(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := persistedRouter(store.New(pool))
	wh := ids.New()
	all := strings.Join([]string{dutySetupMaintain, dutySetupView, dutyReceive, dutyInquiry, dutyWorkSupervise, dutyRFIDIngest, dutyAutomation}, ",")
	post := func(path, key, body string) *httptest.ResponseRecorder {
		return call(h, req{method: "POST", path: path, duties: all, key: key, body: body})
	}
	if rec := post("/v1/warehouse/locations:bulk", "l1", `{"warehouseId":"`+wh+`","locations":[{"name":"DOCK","zone":"I","capacity":{"allowMixedItems":true}},{"name":"A-01","zone":"A","capacity":{"allowMixedItems":true}}]}`); rec.Code != 201 {
		t.Fatalf("locations: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/v1/warehouse/location-directives", "d1", `{"warehouseId":"`+wh+`","sequence":10,"workType":"PURCHASE_PUTAWAY","direction":"PUT","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"consolidate","zone":"A"}]}]}`); rec.Code != 201 {
		t.Fatalf("directive: %d %s", rec.Code, rec.Body)
	}
	// The vendor labelled the pallet with its own SSCC, which the ASN names as the license plate.
	tag, _ := domain.EncodeEPC96(0x31, 3, "0614141", "1000000077", nil)
	vendor, _ := domain.DecodeEPC(tag)
	if rec := post("/v1/warehouse/receipts:from-asn", "asn", `{"warehouseId":"`+wh+`","asnRef":"ASN-RFID","lines":[{"lineNo":1,"itemId":"VALVE","lpId":"`+vendor.Key+`","expectedQty":"40"}]}`); rec.Code != 201 {
		t.Fatalf("ASN: %d %s", rec.Code, rec.Body)
	}
	for _, rule := range []string{
		`{"readerId":"dock-1","action":"RECEIVE_PALLET","warehouseId":"` + wh + `"}`,
		`{"readerId":"rack-A","antenna":1,"action":"CONFIRM_PUTAWAY","locationCode":"A-01"}`,
	} {
		if rec := post("/v1/warehouse/rfid-rules", "", rule); rec.Code != 201 {
			t.Fatalf("rule %s: %d %s", rule, rec.Code, rec.Body)
		}
	}
	if rec := post("/v1/warehouse/rfid-rules", "", `{"readerId":"dock-1","action":"RECEIVE_PALLET"}`); rec.Code != 409 || problemOf(t, rec).Code != store.CodeRFIDRuleExists {
		t.Fatalf("a second receive rule on the reader: %d %s", rec.Code, rec.Body)
	}
	ingest := func(reader string, antenna int, epc, at string) store.RFIDIngest {
		t.Helper()
		rec := post("/v1/warehouse/rfid-reads:ingest", "", fmt.Sprintf(`{"reads":[{"readerId":%q,"antenna":%d,"epc":%q,"readAt":%q}]}`, reader, antenna, epc, at))
		var out store.RFIDIngest
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != 200 {
			t.Fatalf("ingest: %d %s", rec.Code, rec.Body)
		}
		return out
	}

	// The dock portal reads the pallet: the receipt is received at the 40 the ASN announced, with put-away work.
	got := ingest("dock-1", 0, tag, "2026-10-27T09:00:00Z")
	if len(got.Reads) != 1 || len(got.Reads[0].Actions) != 1 || got.Reads[0].Actions[0].Outcome != store.RFIDDone || got.Reads[0].Actions[0].WorkID == "" {
		t.Fatalf("dock read: %+v", got.Reads)
	}
	workID := got.Reads[0].Actions[0].WorkID
	var rcp domain.Receipt
	rec := call(h, req{method: "GET", path: "/v1/warehouse/receipts/" + got.Reads[0].Actions[0].ReceiptID, duties: all})
	_ = json.Unmarshal(rec.Body.Bytes(), &rcp)
	if rcp.Lines[0].Status != domain.PalletReceived || rcp.Lines[0].ReceivedQty.String() != "40" || rcp.Lines[0].ReceivedBy != "rfid:dock-1" || rcp.Lines[0].SSCC != vendor.Key {
		t.Fatalf("receipt after the dock read: %+v", rcp.Lines[0])
	}
	// The same portal reading the pallet a minute later (it was received) does nothing.
	if again := ingest("dock-1", 0, tag, "2026-10-27T09:01:00Z"); again.Reads[0].Actions[0].Outcome != store.RFIDSkipped {
		t.Fatalf("a second dock read: %+v", again.Reads[0].Actions)
	}
	// A reader at the shelf, on another antenna than the rule's, does nothing either.
	if other := ingest("rack-A", 2, tag, "2026-10-27T09:05:00Z"); len(other.Reads[0].Actions) != 0 {
		t.Fatalf("a read on another antenna: %+v", other.Reads[0].Actions)
	}
	// The reader at A-01 sees the pallet arrive: the put-away is confirmed and the stock lands.
	put := ingest("rack-A", 1, tag, "2026-10-27T09:06:00Z")
	if a := put.Reads[0].Actions; len(a) != 1 || a[0].Outcome != store.RFIDDone || a[0].WorkID != workID {
		t.Fatalf("shelf read: %+v", a)
	}
	var w domain.Work
	rec = call(h, req{method: "GET", path: "/v1/warehouse/work/" + workID, duties: all})
	_ = json.Unmarshal(rec.Body.Bytes(), &w)
	if w.Status != domain.WorkCompleted || w.AssignedUser != "rfid:rack-A" {
		t.Fatalf("work after the shelf read: %s %s", w.Status, w.AssignedUser)
	}
	if late := ingest("rack-A", 1, tag, "2026-10-27T09:20:00Z"); late.Reads[0].Actions[0].Outcome != store.RFIDSkipped {
		t.Fatalf("a read after the put-away: %+v", late.Reads[0].Actions)
	}
	// A trade item's SGTIN-198 is decoded and stored; it is not a pallet, so the dock rule skips it.
	item, _ := domain.EncodeEPCString(0x36, 1, "0614141", "712345", "SN-0042")
	sg := ingest("dock-1", 0, item, "2026-10-27T09:30:00Z")
	if sg.Accepted != 1 || sg.Reads[0].EPC.Scheme != "SGTIN-198" || sg.Reads[0].Actions[0].Outcome != store.RFIDSkipped {
		t.Fatalf("SGTIN-198 read: %+v", sg)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/rfid-reads?readerId=rack-A", duties: all})
	if !strings.Contains(rec.Body.String(), `"action":"CONFIRM_PUTAWAY"`) || !strings.Contains(rec.Body.String(), `"actionOutcome":"DONE"`) {
		t.Fatalf("stored reads keep what was done: %s", rec.Body)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/rfid-rules", duties: all})
	var rules struct{ Items []store.RFIDRule }
	_ = json.Unmarshal(rec.Body.Bytes(), &rules)
	if len(rules.Items) != 2 {
		t.Fatalf("rules: %s", rec.Body)
	}
	if rec := call(h, req{method: "DELETE", path: "/v1/warehouse/rfid-rules/" + rules.Items[0].ID, duties: all}); rec.Code != 204 {
		t.Fatalf("delete rule: %d", rec.Code)
	}
	if rec := call(h, req{method: "DELETE", path: "/v1/warehouse/rfid-rules/" + rules.Items[0].ID, duties: all}); rec.Code != 404 {
		t.Fatalf("delete it again: %d", rec.Code)
	}
}

// A PLC fault or divert gives the work back to people with the reason on its line; the line can be given to the PLC
// again; an order can be cancelled before it arrives (a SENT one only once the PLC is cleared), and the work goes back.
func TestAutomationFaultDivertAndCancelChangeTheWork(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := persistedRouter(store.New(pool))
	wh := ids.New()
	all := strings.Join([]string{dutySetupMaintain, dutySetupView, dutyReceive, dutyInquiry, dutyWorkSupervise, dutyRFIDIngest, dutyAutomation}, ",")
	post := func(path, key, body string) *httptest.ResponseRecorder {
		return call(h, req{method: "POST", path: path, duties: all, key: key, body: body, user: "super"})
	}
	if rec := post("/v1/warehouse/locations:bulk", "l1", `{"warehouseId":"`+wh+`","locations":[{"name":"ASRS-01","zone":"A","capacity":{"allowMixedItems":true}}]}`); rec.Code != 201 {
		t.Fatalf("locations: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/v1/warehouse/location-directives", "d1", `{"warehouseId":"`+wh+`","sequence":10,"workType":"PURCHASE_PUTAWAY","direction":"PUT","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"consolidate","zone":"A"}]}]}`); rec.Code != 201 {
		t.Fatalf("directive: %d %s", rec.Code, rec.Body)
	}
	tag, _ := domain.EncodeEPC96(0x31, 3, "0614141", "1000000088", nil)
	pallet, _ := domain.DecodeEPC(tag)
	if rec := post("/v1/warehouse/receipts:from-asn", "asn", `{"warehouseId":"`+wh+`","asnRef":"ASN-PLC","lines":[{"lineNo":1,"itemId":"PUMP","lpId":"`+pallet.Key+`","expectedQty":"1"}]}`); rec.Code != 201 {
		t.Fatalf("ASN: %d %s", rec.Code, rec.Body)
	}
	var list struct{ Items []domain.ReceiptSummary }
	rec := call(h, req{method: "GET", path: "/v1/warehouse/receipts?warehouseId=" + wh, duties: all})
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	rec = post("/v1/warehouse/receipts/"+list.Items[0].ID+"/lines/1:receive", "r1", `{"qty":"1"}`)
	var got store.Received
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body)
	}
	workID := got.Work.ID
	orderPath := "/v1/warehouse/automation/transport-orders"
	newOrder := func() store.AutomationOrder {
		t.Helper()
		rec := post(orderPath, "", fmt.Sprintf(`{"workId":%q,"lineNo":1,"station":"PLC","fromLocation":"INFEED-1"}`, workID))
		var o store.AutomationOrder
		if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil || rec.Code != 201 {
			t.Fatalf("order: %d %s", rec.Code, rec.Body)
		}
		return o
	}
	send := func(o store.AutomationOrder) {
		t.Helper()
		if rec := call(h, req{method: "POST", path: orderPath + "/" + o.ID + ":report", duties: all, body: `{"status":"SENT"}`}); rec.Code != 200 {
			t.Fatalf("report: %d %s", rec.Code, rec.Body)
		}
	}
	work := func() domain.Work {
		t.Helper()
		var w domain.Work
		rec := call(h, req{method: "GET", path: "/v1/warehouse/work/" + workID, duties: all})
		_ = json.Unmarshal(rec.Body.Bytes(), &w)
		return w
	}
	event := func(seq int, kind, extra string) {
		t.Helper()
		body := fmt.Sprintf(`{"station":"PLC","events":[{"kind":%q,"session":"link-9","seq":%d,"loadUnit":%q,%s"at":"2026-10-27T10:00:00Z"}]}`, kind, seq, pallet.Key, extra)
		if rec := call(h, req{method: "POST", path: "/v1/warehouse/automation/events", duties: all, body: body}); rec.Code != 200 {
			t.Fatalf("event: %d %s", rec.Code, rec.Body)
		}
	}

	// A fault: the order is FAULT and the work is OPEN again for people, its line saying why.
	first := newOrder()
	if w := work(); w.Status != domain.WorkAssigned || w.AssignedUser != "automation:PLC" {
		t.Fatalf("work given to the PLC: %s %s", w.Status, w.AssignedUser)
	}
	send(first)
	event(1, "fault", `"code":"E12","text":"jam at infeed",`)
	if w := work(); w.Status != domain.WorkOpen || w.AssignedUser != "" || !strings.Contains(w.Lines[0].Reason, "PLC fault: E12 jam at infeed") {
		t.Fatalf("work after the fault: %s %q %q", w.Status, w.AssignedUser, w.Lines[0].Reason)
	}
	// The line can be given to the PLC again; a divert gives it back once more, naming the lane.
	second := newOrder()
	send(second)
	event(2, "diverted", `"lane":"3","location":"REJECT","text":"height",`)
	if w := work(); w.Status != domain.WorkOpen || w.Lines[0].Reason != "diverted by the PLC to lane 3 at REJECT: height" {
		t.Fatalf("work after the divert: %s %q", w.Status, w.Lines[0].Reason)
	}
	// A pending order is cancelled; the work goes back to OPEN.
	third := newOrder()
	cancel := func(o store.AutomationOrder, body string) *httptest.ResponseRecorder {
		return post(orderPath+"/"+o.ID+":cancel", "", body)
	}
	rec = cancel(third, `{"reason":"moving it by forklift"}`)
	var c store.AutomationOrder
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if rec.Code != 200 || c.Status != store.OrderCancelled || c.Reason != "moving it by forklift" {
		t.Fatalf("cancel pending: %d %s", rec.Code, rec.Body)
	}
	if w := work(); w.Status != domain.WorkOpen || w.Lines[0].Reason != "automation order cancelled: moving it by forklift" {
		t.Fatalf("work after the cancel: %s %q", w.Status, w.Lines[0].Reason)
	}
	if rec := cancel(third, `{"reason":"again"}`); rec.Code != 200 {
		t.Fatalf("cancelling a cancelled order: %d %s", rec.Code, rec.Body)
	}
	// A sent order needs the PLC cleared first.
	fourth := newOrder()
	send(fourth)
	if rec := cancel(fourth, `{"reason":"wrong pallet"}`); rec.Code != 409 || problemOf(t, rec).Code != store.CodeAutoOrderState {
		t.Fatalf("cancel a sent order: %d %s", rec.Code, rec.Body)
	}
	if w := work(); w.Status != domain.WorkAssigned {
		t.Fatalf("a refused cancel changed the work: %s", w.Status)
	}
	if rec := cancel(fourth, `{"reason":"wrong pallet","plcCleared":true}`); rec.Code != 200 {
		t.Fatalf("cancel a cleared sent order: %d %s", rec.Code, rec.Body)
	}
	// An arrived order is done.
	fifth := newOrder()
	send(fifth)
	event(3, "arrived", fmt.Sprintf(`"location":%q,`, fifth.ToLocation))
	if w := work(); w.Status != domain.WorkCompleted {
		t.Fatalf("work after the arrival: %s", w.Status)
	}
	if rec := cancel(fifth, `{"reason":"late"}`); rec.Code != 409 {
		t.Fatalf("cancel an arrived order: %d %s", rec.Code, rec.Body)
	}
}

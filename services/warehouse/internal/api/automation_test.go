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

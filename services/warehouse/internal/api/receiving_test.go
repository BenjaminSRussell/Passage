package api

import (
	"context"
	"encoding/json"
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

func TestReceivingRoutes_Refusals(t *testing.T) {
	wh, doorID, rid := ids.New(), ids.New(), ids.New()
	appt := func(fields string) string {
		return `{"warehouseId":"` + wh + `","doorId":"` + doorID + `","reference":"ASN-1",` + fields + `}`
	}
	window := `"start":"2026-10-27T09:00:00Z","end":"2026-10-27T09:45:00Z"`
	asn := func(lines string) string {
		return `{"warehouseId":"` + wh + `","asnRef":"ASN-1","lines":[` + lines + `]}`
	}
	pallet := `{"lineNo":1,"itemId":"BOLT","lpId":"LP-1","expectedQty":"100"}`
	receive := "/v1/warehouse/receipts/" + rid + "/lines/2:receive"
	cases := []struct {
		name   string
		req    req
		status int
		field  string
	}{
		{"door needs the setup duty", req{method: "POST", path: "/v1/warehouse/dock-doors", duties: dutyReceive, key: "k", body: `{"warehouseId":"` + wh + `","code":"D1"}`}, 403, ""},
		{"door needs a code", req{method: "POST", path: "/v1/warehouse/dock-doors", duties: dutySetupMaintain, key: "k", body: `{"warehouseId":"` + wh + `","code":" "}`}, 400, "code"},
		{"door code at most 20", req{method: "POST", path: "/v1/warehouse/dock-doors", duties: dutySetupMaintain, key: "k", body: `{"warehouseId":"` + wh + `","code":"` + strings.Repeat("D", 21) + `"}`}, 400, "code"},
		{"door needs the idempotency key", req{method: "POST", path: "/v1/warehouse/dock-doors", duties: dutySetupMaintain, body: `{"warehouseId":"` + wh + `","code":"D1"}`}, 400, headerKey},
		{"doors list needs a warehouse", req{method: "GET", path: "/v1/warehouse/dock-doors", duties: dutyInquiry}, 400, "warehouseId"},
		{"appointment needs a receive or ship duty", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyInquiry, key: "k", body: appt(`"direction":"INBOUND",` + window)}, 403, ""},
		{"inbound needs the receive duty", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyShipConfirm, key: "k", body: appt(`"direction":"INBOUND",` + window)}, 403, ""},
		{"outbound needs the ship-confirm duty", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k", body: appt(`"direction":"OUTBOUND",` + window)}, 403, ""},
		{"appointment direction must be known", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k", body: appt(`"direction":"SIDEWAYS",` + window)}, 400, "direction"},
		{"appointment start on the grid", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k",
			body: appt(`"direction":"INBOUND","start":"2026-10-27T09:10:00Z","end":"2026-10-27T09:45:00Z"`)}, 400, "start"},
		{"appointment end after start", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k",
			body: appt(`"direction":"INBOUND","start":"2026-10-27T09:45:00Z","end":"2026-10-27T09:00:00Z"`)}, 400, "end"},
		{"appointment at most a day", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k",
			body: appt(`"direction":"INBOUND","start":"2026-10-27T09:00:00Z","end":"2026-10-28T09:15:00Z"`)}, 400, "end"},
		{"appointment needs a start", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k", body: appt(`"direction":"INBOUND","end":"2026-10-27T09:45:00Z"`)}, 400, "start"},
		{"appointment needs a door uuid", req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "k",
			body: `{"warehouseId":"` + wh + `","doorId":"D1","direction":"INBOUND","reference":"A",` + window + `}`}, 400, "doorId"},
		{"appointments list rejects a bad time", req{method: "GET", path: "/v1/warehouse/dock-appointments?warehouseId=" + wh + "&from=today", duties: dutyInquiry}, 400, "from"},
		{"appointments list to after from", req{method: "GET", path: "/v1/warehouse/dock-appointments?warehouseId=" + wh + "&from=2026-10-27T10:00:00Z&to=2026-10-27T09:00:00Z", duties: dutyInquiry}, 400, "to"},
		{"asn needs the receive duty", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyWorkExecute, key: "k", body: asn(pallet)}, 403, ""},
		{"asn needs pallets", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k", body: asn("")}, 400, "lines"},
		{"asn needs a reference", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: `{"warehouseId":"` + wh + `","asnRef":"","lines":[` + pallet + `]}`}, 400, "asnRef"},
		{"asn tolerance at most 20", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: `{"warehouseId":"` + wh + `","asnRef":"A","tolerancePct":"25","lines":[` + pallet + `]}`}, 400, "tolerancePct"},
		{"asn tolerance two decimals", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: `{"warehouseId":"` + wh + `","asnRef":"A","tolerancePct":"2.125","lines":[` + pallet + `]}`}, 400, "tolerancePct"},
		{"asn pallets need an expected quantity", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: asn(`{"lineNo":1,"itemId":"BOLT","lpId":"LP-1","expectedQty":"0"}`)}, 400, "lines[0].expectedQty"},
		{"asn quantities at most 6 decimals", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: asn(`{"lineNo":1,"itemId":"BOLT","lpId":"LP-1","expectedQty":"1.0000001"}`)}, 400, "lines[0].expectedQty"},
		{"asn license plates are valid ids", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: asn(`{"lineNo":1,"itemId":"BOLT","lpId":"LP 1","expectedQty":"1"}`)}, 400, "lines[0].lpId"},
		{"asn license plates unique", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: asn(pallet + `,{"lineNo":2,"itemId":"BOLT","lpId":"LP-1","expectedQty":"1"}`)}, 400, "lines[1].lpId"},
		{"asn line numbers unique", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: asn(pallet + `,{"lineNo":1,"itemId":"BOLT","lpId":"LP-2","expectedQty":"1"}`)}, 400, "lines[1].lineNo"},
		{"asn weight not negative", req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "k",
			body: asn(`{"lineNo":1,"itemId":"BOLT","lpId":"LP-1","expectedQty":"1","weight":"-1"}`)}, 400, "lines[0].weight"},
		{"receipts list rejects an unknown status", req{method: "GET", path: "/v1/warehouse/receipts?warehouseId=" + wh + "&status=DONE", duties: dutyInquiry}, 400, "status"},
		{"receipts list needs the inquiry duty", req{method: "GET", path: "/v1/warehouse/receipts?warehouseId=" + wh, duties: dutyReceive}, 403, ""},
		{"receipt id must be a uuid", req{method: "GET", path: "/v1/warehouse/receipts/ASN-1", duties: dutyInquiry}, 404, ""},
		{"receive needs a positive quantity", req{method: "POST", path: receive, duties: dutyReceive, key: "k", body: `{"qty":"0"}`}, 400, "qty"},
		{"receive needs a user", req{method: "POST", path: receive, duties: dutyReceive, key: "k", user: "-", body: `{"qty":"95"}`}, 400, "X-Passage-User"},
		{"receive needs a line number", req{method: "POST", path: "/v1/warehouse/receipts/" + rid + "/lines/0:receive", duties: dutyReceive, key: "k", body: `{"qty":"95"}`}, 400, "n"},
		{"receive If-Match is a version", req{method: "POST", path: receive, duties: dutyReceive, key: "k", ifMatch: "x", body: `{"qty":"95"}`}, 400, "If-Match"},
		{"receive needs the receive duty", req{method: "POST", path: receive, duties: dutyWorkExecute, key: "k", body: `{"qty":"95"}`}, 403, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStore{}
			rec := call(persistedRouter(fake), tc.req)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d; body %s", rec.Code, tc.status, rec.Body.String())
			}
			if fake.calls != 0 {
				t.Fatalf("a refused request reached the store")
			}
			if tc.field != "" {
				if p := problemOf(t, rec); p.Errors[tc.field] == "" {
					t.Fatalf("no error on field %q: %+v", tc.field, p.Errors)
				}
			}
		})
	}
}

// The new catalog codes get the status the README lists.
func TestReceivingRoutes_ErrorMapping(t *testing.T) {
	cases := []struct {
		path   string
		body   string
		duties string
		err    error
		status int
		code   string
	}{
		{"/v1/warehouse/dock-appointments", `{"warehouseId":"` + ids.New() + `","doorId":"` + ids.New() + `","direction":"OUTBOUND","reference":"LOAD-1","start":"2026-10-27T09:30:00Z","end":"2026-10-27T10:00:00Z"}`,
			dutyShipConfirm, &domain.Error{Code: domain.CodeDoorOverlap, Detail: "x"}, 409, "WMS-0108"},
		{"/v1/warehouse/dock-appointments", `{"warehouseId":"` + ids.New() + `","doorId":"` + ids.New() + `","direction":"INBOUND","reference":"ASN-1","start":"2026-10-27T09:30:00Z","end":"2026-10-27T10:00:00Z"}`,
			dutyReceive, store.ErrNotFound, 404, codeDoorNotFound},
		{"/v1/warehouse/receipts/" + ids.New() + "/lines/1:receive", `{"qty":"1"}`, dutyReceive, &domain.Error{Code: domain.CodeLineReceived, Detail: "x"}, 409, domain.CodeLineReceived},
		{"/v1/warehouse/receipts/" + ids.New() + "/lines/1:receive", `{"qty":"1"}`, dutyReceive, domain.ErrPalletNotFound, 404, codeLineNotFound},
		{"/v1/warehouse/receipts/" + ids.New() + "/lines/1:receive", `{"qty":"1"}`, dutyReceive, store.ErrNotFound, 404, codeReceiptNotFound},
		{"/v1/warehouse/receipts:from-asn", `{"warehouseId":"` + ids.New() + `","asnRef":"ASN-1","lines":[{"lineNo":1,"itemId":"B","lpId":"LP-1","expectedQty":"1"}]}`,
			dutyReceive, store.ErrConflict, 409, codeDuplicate},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			rec := call(persistedRouter(&fakeStore{err: tc.err}), req{method: "POST", path: tc.path, duties: tc.duties, key: "k", body: tc.body})
			if rec.Code != tc.status || problemOf(t, rec).Code != tc.code {
				t.Fatalf("status %d body %s, want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}
}

func TestReceivingRoutes_PassTheCommand(t *testing.T) {
	wh, doorID, rid := ids.New(), ids.New(), ids.New()
	fake := &fakeStore{}
	h := persistedRouter(fake)

	rec := call(h, req{method: "POST", path: "/v1/warehouse/dock-appointments", duties: dutyReceive, key: "a1",
		body: `{"warehouseId":"` + wh + `","doorId":"` + doorID + `","direction":"INBOUND","reference":" ASN-1 ","start":"2026-10-27T05:00:00-04:00","end":"2026-10-27T05:45:00-04:00"}`})
	if rec.Code != 201 || fake.appt.Reference != "ASN-1" || !fake.appt.Start.Equal(time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)) || fake.appt.Start.Location() != time.UTC || fake.appt.ID == "" {
		t.Fatalf("appointment: %d %+v", rec.Code, fake.appt)
	}

	rec = call(h, req{method: "POST", path: "/v1/warehouse/receipts:from-asn", duties: dutyReceive, key: "ASN-1",
		body: `{"warehouseId":"` + strings.ToUpper(wh) + `","asnRef":"ASN-1","lines":[{"lineNo":2,"itemId":" BOLT ","lpId":"LP-2","poLineRef":"PO-7/1","expectedQty":"100","weight":"40"}]}`})
	if rec.Code != 201 || fake.receipt.WarehouseID != wh || !fake.receipt.TolerancePct.Equal(domain.DefaultASNTolerance()) ||
		fake.receipt.Lines[0].ItemID != "BOLT" || fake.receipt.Lines[0].POLineRef != "PO-7/1" || fake.idem.Key != "ASN-1" {
		t.Fatalf("register ASN: %d %+v", rec.Code, fake.receipt)
	}

	fake.replayed = true
	fake.received = store.Received{Work: domain.Work{ID: ids.New()}, Warnings: []store.Warning{{Code: domain.CodeVariance, Detail: "x"}}}
	rec = call(h, req{method: "POST", path: "/v1/warehouse/receipts/" + rid + "/lines/2:receive", duties: dutyReceive, key: "r1", ifMatch: `"3"`, user: "clerk", body: `{"qty":"95"}`})
	if rec.Code != 200 || rec.Header().Get(headerReplayed) != "true" || fake.receive.ReceiptID != rid || fake.receive.LineNo != 2 ||
		!fake.receive.Qty.Equal(dec("95")) || fake.receive.IfMatch != 3 || fake.receive.User != "clerk" {
		t.Fatalf("receive: %d %+v", rec.Code, fake.receive)
	}
	if !strings.Contains(rec.Body.String(), `"code":"WMS-0107"`) {
		t.Fatalf("receive body has no warning: %s", rec.Body)
	}

	rec = call(h, req{method: "GET", path: "/v1/warehouse/receipts?warehouseId=" + wh + "&status=PARTIALLY_RECEIVED", duties: dutyInquiry})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) || fake.rfilter.Status != "PARTIALLY_RECEIVED" || fake.rfilter.Limit != defaultPageSize {
		t.Fatalf("list receipts: %d %s %+v", rec.Code, rec.Body, fake.rfilter)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/dock-appointments?warehouseId=" + wh + "&doorId=" + doorID + "&from=2026-10-27T00:00:00Z", duties: dutyInquiry})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) || fake.afilter.DoorID != doorID || fake.afilter.From.IsZero() || !fake.afilter.To.IsZero() {
		t.Fatalf("list appointments: %d %s %+v", rec.Code, rec.Body, fake.afilter)
	}
}

// End to end over HTTP and Postgres: AC-31.8 (door overlap) and AC-31.7 (an
// ASN of 3 pallets, pallet 2 received at 95 of 100).
func TestReceivingRoutes_DockAndASNFlow(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := persistedRouter(store.New(pool))
	wh := ids.New()
	all := strings.Join([]string{dutySetupMaintain, dutySetupView, dutyReceive, dutyShipConfirm, dutyInquiry}, ",")
	post := func(path, key, body string) *httptest.ResponseRecorder {
		return call(h, req{method: "POST", path: path, duties: all, key: key, body: body})
	}

	rec := post("/v1/warehouse/dock-doors", "door", `{"warehouseId":"`+wh+`","code":"D1"}`)
	var d domain.DockDoor
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != 201 {
		t.Fatalf("door: %d %s", rec.Code, rec.Body)
	}
	booking := func(key, start, end string) *httptest.ResponseRecorder {
		return post("/v1/warehouse/dock-appointments", key, `{"warehouseId":"`+wh+`","doorId":"`+d.ID+`","direction":"INBOUND","reference":"ASN-3",
			"start":"2026-10-27T`+start+`:00Z","end":"2026-10-27T`+end+`:00Z"}`)
	}
	if rec := booking("b1", "09:00", "09:45"); rec.Code != 201 {
		t.Fatalf("09:00–09:45: %d %s", rec.Code, rec.Body)
	}
	if rec := booking("b2", "09:30", "10:00"); rec.Code != 409 || problemOf(t, rec).Code != domain.CodeDoorOverlap {
		t.Fatalf("09:30–10:00: %d %s", rec.Code, rec.Body)
	}

	if rec := post("/v1/warehouse/locations:bulk", "l1", `{"warehouseId":"`+wh+`","locations":[{"name":"A-01","zone":"A","capacity":{"allowMixedItems":true}}]}`); rec.Code != 201 {
		t.Fatalf("locations: %d %s", rec.Code, rec.Body)
	}
	if rec := post("/v1/warehouse/location-directives", "d1", `{"warehouseId":"`+wh+`","sequence":10,"workType":"PURCHASE_PUTAWAY","direction":"PUT","lines":[{"seq":1,"fromQty":"0","actions":[{"seq":1,"strategy":"consolidate","zone":"A"}]}]}`); rec.Code != 201 {
		t.Fatalf("directive: %d %s", rec.Code, rec.Body)
	}
	rec = post("/v1/warehouse/receipts:from-asn", "ASN-3", `{"warehouseId":"`+wh+`","asnRef":"ASN-3","lines":[
		{"lineNo":1,"itemId":"BOLT","lpId":"LP-1","expectedQty":"100"},
		{"lineNo":2,"itemId":"BOLT","lpId":"LP-2","expectedQty":"100"},
		{"lineNo":3,"itemId":"BOLT","lpId":"LP-3","expectedQty":"100"}]}`)
	var r domain.Receipt
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || rec.Code != 201 || r.Status != domain.ReceiptOpen {
		t.Fatalf("ASN: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, req{method: "POST", path: "/v1/warehouse/receipts/" + r.ID + "/lines/2:receive", duties: all, key: "p2", user: "clerk", body: `{"qty":"95"}`})
	var got store.Received
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("receive pallet 2: %d %s", rec.Code, rec.Body)
	}
	p2 := got.Receipt.Lines[1]
	if !p2.VarianceQty.Equal(dec("-5")) || !p2.VariancePct.Equal(dec("-5")) || len(got.Warnings) != 1 || got.Work.Lines[0].Attributes["lpId"] != "LP-2" {
		t.Fatalf("pallet 2: %+v warnings %+v", p2, got.Warnings)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/receipts?warehouseId=" + wh, duties: all})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"received":1`) || !strings.Contains(rec.Body.String(), `"discrepancies":1`) {
		t.Fatalf("receipts list: %d %s", rec.Code, rec.Body)
	}
	rec = call(h, req{method: "GET", path: "/v1/warehouse/receipts/" + r.ID, duties: all})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"PARTIALLY_RECEIVED"`) {
		t.Fatalf("receipt: %d %s", rec.Code, rec.Body)
	}
}

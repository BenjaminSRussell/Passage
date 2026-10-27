package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// saLog records what the scheduling agreement store methods received; err,
// when set, is what they answer.
type saLog struct {
	created     *domain.SchedulingAgreement
	createIdem  store.Idem
	filter      store.SchedulingAgreementFilter
	replaceID   string
	ifMatch     int
	asOf        domain.Day
	changes     []domain.ScheduleChange
	warnings    []string
	releaseKind string
	runDate     domain.Day
	reset       bool
	releaseIdem store.Idem
	existing    bool // RunSchedulingRelease answers an existing release
	releaseNo   int
	err         error
}

const saID = "00000000-0000-7000-8000-00000000d001"

func saDetail() store.SchedulingAgreementDetail {
	d := func(s string) domain.Day { v, _ := domain.ParseDay(s); return v }
	return store.SchedulingAgreementDetail{Agreement: domain.SchedulingAgreement{
		ID: saID, Number: "SA-00000001", LegalEntityID: leA, VendorID: userA, ProductID: userB, SiteID: userA,
		Currency: "USD", Unit: "EA", UnitPrice: decimal.RequireFromString("2.5"), TargetQty: decimal.NewFromInt(300),
		ValidFrom: d("2026-01-01"), Profile: domain.DefaultReleaseProfile(), Version: 3,
		Lines: []domain.ScheduleLine{
			{LineNo: 1, DeliveryDate: d("2026-03-08"), Qty: decimal.NewFromInt(100), ReceivedQty: decimal.NewFromInt(40)},
			{LineNo: 2, DeliveryDate: d("2026-03-22"), Qty: decimal.NewFromInt(100), ReceivedQty: decimal.Zero},
			{LineNo: 3, DeliveryDate: d("2026-05-01"), Qty: decimal.NewFromInt(100), ReceivedQty: decimal.Zero},
		},
	}}
}

func (f *fakeStore) CreateSchedulingAgreement(_ context.Context, in domain.SchedulingAgreement, idem store.Idem) (store.SchedulingAgreementDetail, error) {
	f.sa.created, f.sa.createIdem = &in, idem
	in.ID, in.Number, in.Version = saID, "SA-00000001", 1
	return store.SchedulingAgreementDetail{Agreement: in}, f.sa.err
}

func (f *fakeStore) GetSchedulingAgreement(_ context.Context, id string) (store.SchedulingAgreementDetail, error) {
	if f.sa.err != nil || id != saID {
		return store.SchedulingAgreementDetail{}, httpx.NewProblem(http.StatusNotFound, store.CodeSchedulingAgreementNotFound, "not found")
	}
	return saDetail(), nil
}

func (f *fakeStore) ListSchedulingAgreements(_ context.Context, sf store.SchedulingAgreementFilter, after string, limit int) (store.Page[store.SAMonitorRow], error) {
	f.sa.filter, f.lastAfter, f.lastLimit = sf, after, limit
	due, _ := domain.ParseDay("2026-03-08")
	return store.Page[store.SAMonitorRow]{Items: []store.SAMonitorRow{{ID: saID, Number: "SA-00000001", Scheduled: decimal.NewFromInt(300),
		Received: decimal.NewFromInt(40), Open: decimal.NewFromInt(260), ReleaseCount: 1, LastReleaseNumber: "REL-SA-00000001-1",
		LastReleaseKind: domain.ReleaseJIT, NextReleaseDue: &due, ReleaseFrequency: domain.FrequencyWeekly}}, Next: saID}, f.sa.err
}

func (f *fakeStore) ReplaceSchedule(_ context.Context, id string, ifMatch int, asOf domain.Day, changes []domain.ScheduleChange) (store.SchedulingAgreementDetail, []string, error) {
	f.sa.replaceID, f.sa.ifMatch, f.sa.asOf, f.sa.changes = id, ifMatch, asOf, changes
	if f.sa.err != nil {
		return store.SchedulingAgreementDetail{}, nil, f.sa.err
	}
	out := saDetail()
	out.Agreement.Version = ifMatch + 1
	return out, f.sa.warnings, nil
}

func (f *fakeStore) RunSchedulingRelease(_ context.Context, id, kind string, runDate domain.Day, reset bool, idem store.Idem) (domain.SARelease, bool, error) {
	f.sa.releaseKind, f.sa.runDate, f.sa.reset, f.sa.releaseIdem = kind, runDate, reset, idem
	if f.sa.err != nil {
		return domain.SARelease{}, false, f.sa.err
	}
	return domain.SARelease{ID: saID, AgreementID: id, ReleaseNo: 2, Number: "REL-SA-00000001-2", Kind: kind, RunDate: runDate,
		CumulativeReleased: decimal.NewFromInt(300), CumulativeReceived: decimal.NewFromInt(40),
		Lines: []domain.SAReleaseLine{{LineNo: 1, ScheduleLineNo: 1, Zone: domain.ZoneFirm, InHorizon: true, Qty: decimal.NewFromInt(100),
			CumulativeReleased: decimal.NewFromInt(100), CumulativeReceived: decimal.NewFromInt(40)}}}, !f.sa.existing, nil
}

func (f *fakeStore) ListSchedulingReleases(_ context.Context, agreementID string) ([]domain.SARelease, error) {
	f.sa.replaceID = agreementID
	return []domain.SARelease{{ID: saID, AgreementID: agreementID, ReleaseNo: 1, Number: "REL-SA-00000001-1", Kind: domain.ReleaseJIT,
		Lines: []domain.SAReleaseLine{{LineNo: 1}}}}, f.sa.err
}

func (f *fakeStore) GetSchedulingRelease(_ context.Context, agreementID string, releaseNo int) (domain.SARelease, error) {
	f.sa.releaseNo = releaseNo
	return domain.SARelease{ID: saID, AgreementID: agreementID, ReleaseNo: releaseNo, Lines: []domain.SAReleaseLine{{LineNo: 1}}}, f.sa.err
}

var (
	saMaintain = []string{dutyAgreementMaintain}
	saInquire  = []string{dutyPurchaseOrderInquire}
)

const saPath = "/v1/procurement/scheduling-agreements"

func saCreateJSON(mutate func(m map[string]any)) string {
	m := map[string]any{
		"legalEntityId": leA, "vendorId": userA, "productId": userB, "siteId": userA, "currency": "USD", "unit": "EA",
		"unitPrice": "2.50", "targetQty": "1200", "validFrom": "2026-03-01",
		"lines": []map[string]any{{"deliveryDate": "2026-03-02", "qty": "100"}, {"deliveryDate": "2026-03-09", "qty": "100"}},
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func TestCreateSchedulingAgreement_DefaultsProfileAndNumbersLines(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "POST", path: saPath, duties: saMaintain, body: saCreateJSON(nil), headers: idem("sa-1")})
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.sa.created
	if got.Profile != domain.DefaultReleaseProfile() {
		t.Errorf("profile = %+v, want the PRD default", got.Profile)
	}
	if len(got.Lines) != 2 || got.Lines[1].LineNo != 2 || got.Lines[1].DeliveryDate.String() != "2026-03-09" ||
		!got.UnitPrice.Equal(decimal.RequireFromString("2.5")) || !got.TargetQty.Equal(decimal.NewFromInt(1200)) || got.Unit != "EA" {
		t.Errorf("agreement = %+v", got)
	}
	if h.fake.sa.createIdem.Key != "sa-1" || h.fake.sa.createIdem.Hash == "" {
		t.Errorf("idempotency = %+v", h.fake.sa.createIdem)
	}
	var dto saDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	// Harness today is 2026-03-01: line 1 (1 day out) and line 2 (8 days) are FIRM.
	if dto.Number != "SA-00000001" || dto.ScheduledQty != "200" || dto.Lines[0].Zone != domain.ZoneFirm || dto.ZonesAsOf.String() != "2026-03-01" {
		t.Errorf("dto = %+v", dto)
	}

	custom := saCreateJSON(func(m map[string]any) {
		m["profile"] = map[string]any{"firmZoneDays": 7, "tradeoffZoneDays": 21, "releaseFrequency": "DAILY"}
	})
	if rec := h.do(call{method: "POST", path: saPath, duties: saMaintain, body: custom, headers: idem("sa-2")}); rec.Code != http.StatusCreated {
		t.Fatalf("custom profile: %d %s", rec.Code, rec.Body)
	}
	if p := h.fake.sa.created.Profile; p.FirmDays != 7 || p.TradeoffDays != 21 || p.ReleaseFrequency != domain.FrequencyDaily || p.FRCHorizonDays != 90 {
		t.Errorf("custom profile = %+v", p)
	}
}

func TestCreateSchedulingAgreement_Rejections(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		field  string
		duties []string
		scope  string
		hdr    map[string]string
		status int
		code   string
	}{
		{"no unit", saCreateJSON(func(m map[string]any) { m["unit"] = " " }), "unit", saMaintain, "", idem("k"), 422, codeInvalid},
		{"no price", saCreateJSON(func(m map[string]any) { delete(m, "unitPrice") }), "unitPrice", saMaintain, "", idem("k"), 422, codeInvalid},
		{"negative price", saCreateJSON(func(m map[string]any) { m["unitPrice"] = "-1" }), "unitPrice", saMaintain, "", idem("k"), 422, codeInvalid},
		{"no site", saCreateJSON(func(m map[string]any) { delete(m, "siteId") }), "siteId", saMaintain, "", idem("k"), 422, codeInvalid},
		{"valid to before from", saCreateJSON(func(m map[string]any) { m["validTo"] = "2026-02-01" }), "validTo", saMaintain, "", idem("k"), 422, codeInvalid},
		{"no lines", saCreateJSON(func(m map[string]any) { m["lines"] = []any{} }), "lines", saMaintain, "", idem("k"), 422, codeInvalid},
		{"zero qty", saCreateJSON(func(m map[string]any) { m["lines"] = []map[string]any{{"deliveryDate": "2026-03-02", "qty": "0"}} }), "lines[0].qty", saMaintain, "", idem("k"), 422, codeInvalid},
		{"bad date", saCreateJSON(func(m map[string]any) { m["lines"] = []map[string]any{{"deliveryDate": "03/02/2026", "qty": "1"}} }), "lines[0].deliveryDate", saMaintain, "", idem("k"), 422, codeInvalid},
		{"trade-off shorter than firm", saCreateJSON(func(m map[string]any) { m["profile"] = map[string]any{"firmZoneDays": 30} }), "profile.tradeoffZoneDays", saMaintain, "", idem("k"), 422, codeInvalid},
		{"unknown frequency", saCreateJSON(func(m map[string]any) { m["profile"] = map[string]any{"releaseFrequency": "HOURLY"} }), "profile.releaseFrequency", saMaintain, "", idem("k"), 422, codeInvalid},
		{"no idempotency key", saCreateJSON(nil), "", saMaintain, "", nil, 400, codeIdemRequired},
		{"inquire duty cannot create", saCreateJSON(nil), "", saInquire, "", idem("k"), 403, "AUTH-0003"},
		{"legal entity outside scope", saCreateJSON(nil), "", saMaintain, "00000000-0000-7000-8000-0000000001e2", idem("k"), 403, codeScope},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(call{method: "POST", path: saPath, duties: tc.duties, scope: tc.scope, body: tc.body, headers: tc.hdr})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s: %s", rec.Code, problemCode(t, rec), tc.status, tc.code, rec.Body)
			}
			if tc.field != "" {
				var p httpx.Problem
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if _, ok := p.Errors[tc.field]; !ok {
					t.Errorf("errors %v do not name %s", p.Errors, tc.field)
				}
			}
			if h.fake.sa.created != nil {
				t.Error("a rejected request reached the store")
			}
		})
	}
}

func TestGetSchedulingAgreement_ShowsZonesAndOpenQuantity(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "GET", path: saPath + "/" + saID, duties: saInquire})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	var dto saDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	// 2026-03-01: line 1 is 7 days out (FIRM), line 2 21 days (TRADEOFF), line 3 61 days (FORECAST).
	zones := []string{dto.Lines[0].Zone, dto.Lines[1].Zone, dto.Lines[2].Zone}
	if strings.Join(zones, ",") != "FIRM,TRADEOFF,FORECAST" {
		t.Errorf("zones = %v", zones)
	}
	if dto.Lines[0].OpenQty != "60" || dto.ScheduledQty != "300" || dto.ReceivedQty != "40" || dto.Profile.ReleaseFrequency != "WEEKLY" {
		t.Errorf("dto = %+v", dto)
	}
	if rec := h.do(call{method: "GET", path: saPath + "/" + userA, duties: saMaintain}); rec.Code != 404 || problemCode(t, rec) != store.CodeSchedulingAgreementNotFound {
		t.Errorf("missing agreement = %d %s", rec.Code, rec.Body)
	}
	if rec := h.do(call{method: "GET", path: saPath + "/" + saID, duties: []string{dutyRFQMaintain}}); rec.Code != http.StatusForbidden {
		t.Errorf("RFQ duty may not read: %d", rec.Code)
	}
}

func TestListSchedulingAgreements_Monitor(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "GET", path: saPath + "?vendorId=" + userA + "&limit=10", duties: saInquire})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var page pageBody[saMonitorDTO]
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if h.fake.sa.filter.VendorID != userA || h.fake.lastLimit != 10 || page.NextCursor != saID || len(page.Items) != 1 {
		t.Fatalf("filter %+v limit %d page %+v", h.fake.sa.filter, h.fake.lastLimit, page)
	}
	if m := page.Items[0]; m.OpenQty != "260" || m.NextReleaseDue.String() != "2026-03-08" || m.LastReleaseKind != "JIT" {
		t.Errorf("row = %+v", m)
	}
	if rec := h.do(call{method: "GET", path: saPath + "?productId=x", duties: saInquire}); rec.Code != 422 {
		t.Errorf("bad productId = %d", rec.Code)
	}
}

func TestReplaceSchedule_PassesTodayVersionAndWarnings(t *testing.T) {
	h := newHarness(t)
	h.fake.sa.warnings = []string{"schedule line 2 (2026-03-22) was reduced inside the trade-off zone"}
	body := `{"lines":[{"lineNo":1,"deliveryDate":"2026-03-08","qty":"50","reason":" VENDOR_CAPACITY "},{"deliveryDate":"2026-06-01","qty":"20"},{"lineNo":2,"deliveryDate":"2026-03-22","qty":"0","reason":"DROP"}]}`
	rec := h.do(call{method: "PUT", path: saPath + "/" + saID + "/schedule", duties: saMaintain, body: body, headers: map[string]string{"If-Match": `"3"`}})
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("status %d ETag %q: %s", rec.Code, rec.Header().Get("ETag"), rec.Body)
	}
	got := h.fake.sa
	if got.replaceID != saID || got.ifMatch != 3 || got.asOf.String() != "2026-03-01" || len(got.changes) != 3 || !got.changes[2].Qty.IsZero() ||
		got.changes[0].Reason != "VENDOR_CAPACITY" || got.changes[1].LineNo != 0 || !got.changes[0].Qty.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("store got %+v", got)
	}
	var dto saDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if len(dto.Warnings) != 1 {
		t.Errorf("warnings = %v", dto.Warnings)
	}

	for _, tc := range []struct {
		name   string
		hdr    map[string]string
		body   string
		err    error
		status int
		code   string
	}{
		{"no If-Match", nil, body, nil, http.StatusPreconditionRequired, codeIfMatchMissing},
		{"no lines", map[string]string{"If-Match": `"3"`}, `{"lines":[]}`, nil, 422, codeInvalid},
		{"zero quantity on a new line", map[string]string{"If-Match": `"3"`}, `{"lines":[{"deliveryDate":"2026-03-08","qty":"0"}]}`, nil, 422, codeInvalid},
		{"negative line number", map[string]string{"If-Match": `"3"`}, `{"lines":[{"lineNo":-1,"deliveryDate":"2026-03-08","qty":"1"}]}`, nil, 422, codeInvalid},
		{"firm cut without reason", map[string]string{"If-Match": `"3"`}, body, &domain.Error{Code: domain.CodeFirmZone, Detail: "firm"}, 422, domain.CodeFirmZone},
		{"stale version", map[string]string{"If-Match": `"2"`}, body, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "stale"), 409, store.CodeVersionConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.sa.err = tc.err
			rec := h.do(call{method: "PUT", path: saPath + "/" + saID + "/schedule", duties: saMaintain, body: tc.body, headers: tc.hdr})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s", rec.Code, problemCode(t, rec), tc.status, tc.code)
			}
		})
	}
}

func TestRelease_CreatedExistingAndRejected(t *testing.T) {
	h := newHarness(t)
	path := saPath + "/" + saID + ":release"
	rec := h.do(call{method: "POST", path: path, duties: saMaintain, body: `{"kind":"JIT"}`, headers: idem("rel-1")})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := h.fake.sa; got.releaseKind != "JIT" || got.runDate.String() != "2026-03-01" || got.reset || got.releaseIdem.Key != "rel-1" {
		t.Fatalf("store got %+v", got)
	}
	var dto saReleaseDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if dto.Number != "REL-SA-00000001-2" || dto.CumulativeReleased != "300" || len(dto.Lines) != 1 || dto.Lines[0].Zone != "FIRM" {
		t.Errorf("release = %+v", dto)
	}

	h.fake.sa.existing = true
	rec = h.do(call{method: "POST", path: path, duties: saMaintain, body: `{"kind":"FRC","runDate":"2026-03-09","cumulativeReset":true}`, headers: idem("rel-2")})
	if rec.Code != http.StatusOK {
		t.Fatalf("unchanged schedule: status %d, want 200", rec.Code)
	}
	if got := h.fake.sa; got.releaseKind != "FRC" || got.runDate.String() != "2026-03-09" || !got.reset {
		t.Fatalf("store got %+v", got)
	}

	for _, tc := range []struct {
		name, path, body string
		duties           []string
		hdr              map[string]string
		err              error
		status           int
		code             string
	}{
		{"unknown action", saPath + "/" + saID + ":ship", `{"kind":"JIT"}`, saMaintain, idem("k"), nil, 404, codeUnknownAction},
		{"bad kind", path, `{"kind":"WEEKLY"}`, saMaintain, idem("k"), nil, 422, codeInvalid},
		{"bad run date", path, `{"kind":"JIT","runDate":"tomorrow"}`, saMaintain, idem("k"), nil, 422, codeInvalid},
		{"no key", path, `{"kind":"JIT"}`, saMaintain, nil, nil, 400, codeIdemRequired},
		{"inquire duty cannot release", path, `{"kind":"JIT"}`, saInquire, idem("k"), nil, 403, "AUTH-0003"},
		{"cumulative decrease", path, `{"kind":"JIT"}`, saMaintain, idem("k"), &domain.Error{Code: domain.CodeCumulativeDecrease, Detail: "down"}, 422, domain.CodeCumulativeDecrease},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fake.sa.err = tc.err
			rec := h.do(call{method: "POST", path: tc.path, duties: tc.duties, body: tc.body, headers: tc.hdr})
			if rec.Code != tc.status || problemCode(t, rec) != tc.code {
				t.Fatalf("got %d %s, want %d %s", rec.Code, problemCode(t, rec), tc.status, tc.code)
			}
		})
	}
}

func TestReleases_ListHeadersAndDocumentation(t *testing.T) {
	h := newHarness(t)
	rec := h.do(call{method: "GET", path: saPath + "/" + saID + "/releases", duties: saInquire})
	var page pageBody[saReleaseDTO]
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if rec.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].Lines != nil {
		t.Fatalf("status %d list %+v (headers only)", rec.Code, page)
	}
	rec = h.do(call{method: "GET", path: saPath + "/" + saID + "/releases/2", duties: saMaintain})
	var dto saReleaseDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &dto)
	if rec.Code != http.StatusOK || h.fake.sa.releaseNo != 2 || len(dto.Lines) != 1 {
		t.Fatalf("status %d release %+v", rec.Code, dto)
	}
	if rec := h.do(call{method: "GET", path: saPath + "/" + saID + "/releases/0", duties: saInquire}); rec.Code != 422 {
		t.Errorf("release 0 = %d", rec.Code)
	}
	h.fake.sa.err = httpx.NewProblem(http.StatusNotFound, store.CodeSAReleaseNotFound, "none")
	if rec := h.do(call{method: "GET", path: saPath + "/" + saID + "/releases/9", duties: saInquire}); rec.Code != 404 || problemCode(t, rec) != store.CodeSAReleaseNotFound {
		t.Errorf("missing release = %d %s", rec.Code, rec.Body)
	}
}

package api

import (
	"bytes"
	"encoding/json"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

func imageBody(t *testing.T, kind string, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	var b bytes.Buffer
	var err error
	if kind == "jpeg" {
		err = jpeg.Encode(&b, img, nil)
	} else {
		err = png.Encode(&b, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestDayRoutes_RequireDuties(t *testing.T) {
	h := newTestRouter()
	for _, c := range []struct{ method, path, duty string }{
		{http.MethodGet, "/v1/service/technicians/me/day", "fieldservice.inquire"},
		{http.MethodGet, "/v1/service/technicians/me/day", "fieldservice.work-order.maintain"},
		{http.MethodPut, "/v1/service/work-orders/" + uuid1 + "/captures/" + uuid1, "fieldservice.work-order.maintain"},
		{http.MethodGet, "/v1/service/work-orders/" + uuid1 + "/captures/" + uuid1, "fieldservice.work-order.execute"},
		{http.MethodPut, "/v1/service/work-orders/" + uuid1 + "/checklist", "fieldservice.work-order.execute"},
	} {
		if rec := do(t, h, c.method, c.path, "{}", nil, c.duty); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %s: status %d, want 403", c.method, c.path, c.duty, rec.Code)
		}
	}
}

func TestPutCapture_ValidatesTheUpload(t *testing.T) {
	h := newTestRouter()
	path := "/v1/service/work-orders/" + uuid1 + "/captures/" + uuid1
	now := time.Now().UTC().Format(time.RFC3339)
	q := "?technicianId=" + uuid1 + "&capturedAt=" + now
	for name, c := range map[string]struct {
		query string
		body  []byte
		code  int
	}{
		"no kind":            {q, imageBody(t, "png", 4, 4), http.StatusBadRequest},
		"half a position":    {q + "&kind=PHOTO&lat=10", imageBody(t, "png", 4, 4), http.StatusBadRequest},
		"future capture":     {"?technicianId=" + uuid1 + "&kind=PHOTO&capturedAt=" + time.Now().Add(time.Hour).UTC().Format(time.RFC3339), imageBody(t, "png", 4, 4), http.StatusBadRequest},
		"not an image":       {q + "&kind=PHOTO", []byte("hello"), http.StatusUnprocessableEntity},
		"photo too large":    {q + "&kind=PHOTO", imageBody(t, "png", 1601, 10), http.StatusUnprocessableEntity},
		"signature as jpeg":  {q + "&kind=SIGNATURE", imageBody(t, "jpeg", 40, 20), http.StatusUnprocessableEntity},
		"signature too wide": {q + "&kind=SIGNATURE", imageBody(t, "png", 2001, 10), http.StatusUnprocessableEntity},
	} {
		req := do(t, h, http.MethodPut, path+c.query, string(c.body), nil, "fieldservice.work-order.execute")
		if req.Code != c.code {
			t.Errorf("%s: status %d, want %d: %s", name, req.Code, c.code, req.Body.String())
		}
	}
}

func TestChecklistOf_GeneratesAndChecksItems(t *testing.T) {
	fields := map[string]string{}
	items := checklistOf(fields, []checklistRequest{{Text: " Lock out ", Required: true}, {ID: strings.ToUpper(uuid1), Text: "Test"}})
	if len(fields) != 0 || len(items) != 2 || !ids.Valid(items[0].ID) || items[0].Text != "Lock out" || items[1].ID != uuid1 {
		t.Fatalf("items %+v fields %v", items, fields)
	}
	fields = map[string]string{}
	checklistOf(fields, []checklistRequest{{ID: uuid1, Text: "a"}, {ID: uuid1, Text: "b"}, {Text: ""}})
	if fields["checklist[1]"] == "" || fields["checklist[2]"] == "" {
		t.Fatalf("fields %v", fields)
	}
}

// The technician's day, a photo and signature upload, and the PHOTO, CHECKLIST and SIGNATURE
// actions over HTTP against Postgres.
func TestAPI_TechnicianDayAndCapture(t *testing.T) {
	h, ctx := dbRouter(t)
	terr, le := ids.New(), ids.New()
	site, warehouse := ids.New(), ids.New()
	tech := decodeInto[store.Technician](t, call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"AMY","name":"Amy","legalEntityId":"`+le+`","territoryIds":["`+terr+
		`"],"timeZone":"UTC","shiftDays":[1,2,3,4,5,6,7],"truckSiteId":"`+site+`","truckWarehouseId":"`+warehouse+`"}`, map[string]string{"Idempotency-Key": "t"}).Body.String())
	if tech.TruckSiteID == nil || *tech.TruckWarehouseID != warehouse {
		t.Fatalf("truck %+v", tech)
	}
	if rec := call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"BAD","name":"Bad","legalEntityId":"`+le+`","territoryIds":["`+terr+
		`"],"timeZone":"UTC","truckSiteId":"`+site+`"}`, map[string]string{"Idempotency-Key": "t2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a truck with a site but no warehouse: %d", rec.Code)
	}
	line, item := ids.New(), ids.New()
	created := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", `{"customerPartyId":"`+ids.New()+`","legalEntityId":"`+le+`","territoryId":"`+terr+
		`","estDurationMinutes":60,"currency":"USD","address":"1 Main St","checklist":[{"id":"`+item+`","text":"Isolate power","required":true}],`+
		`"lines":[{"id":"`+line+`","kind":"PART","qty":"2","unitPrice":"10","description":"Seal kit","productId":"`+ids.New()+`","unit":"EA"}]}`, map[string]string{"Idempotency-Key": "w"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create %d %s", created.Code, created.Body.String())
	}
	wo := decodeInto[store.WorkOrder](t, created.Body.String())
	if len(wo.Checklist) != 1 || wo.Lines[0].ProductID == "" {
		t.Fatalf("work order %+v", wo)
	}
	if rec := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", `{"customerPartyId":"`+ids.New()+`","legalEntityId":"`+le+`","territoryId":"`+terr+
		`","estDurationMinutes":60,"currency":"USD","lines":[{"kind":"LABOR","qty":"1","productId":"`+ids.New()+`","unit":"EA"}]}`, map[string]string{"Idempotency-Key": "w2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("a product on a labor line: %d", rec.Code)
	}

	base := time.Now().UTC().Truncate(time.Minute)
	start := base.Add(time.Hour)
	path := "/v1/service/work-orders/" + wo.ID
	if rec := call(t, h, ctx, http.MethodPost, path+":assign", `{"technicianId":"`+tech.ID+`","start":"`+start.Format(time.RFC3339)+`"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK {
		t.Fatalf("assign: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, ctx, http.MethodPost, "/v1/service/dispatch/publish", `{"legalEntityId":"`+le+`","territoryId":"`+terr+`","date":"`+start.Format("2006-01-02")+`","timeZone":"UTC"}`,
		map[string]string{"Idempotency-Key": "p"}); rec.Code != http.StatusCreated {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body.String())
	}

	var day store.Day
	rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/"+tech.ID+"/day?date="+start.Format("2006-01-02"), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("day %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &day); err != nil || len(day.Visits) != 1 || day.Visits[0].Lines[0].Description != "Seal kit" || day.Technician.Truck == nil {
		t.Fatalf("day %+v %v", day, err)
	}
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/"+tech.ID+"/day?date=tomorrow", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad date: %d", rec.Code)
	}
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/nobody/day", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}

	// Device times sit a little before the server clock, never more than 5 minutes ahead of it.
	devBase := base.Add(-3 * time.Minute)
	photo, sig := ids.New(), ids.New()
	q := "?technicianId=" + tech.ID + "&capturedAt=" + devBase.Format(time.RFC3339)
	put := func(id, kind string, body []byte) *httptest.ResponseRecorder {
		return call(t, h, ctx, http.MethodPut, path+"/captures/"+id+q+"&kind="+kind+"&lat=41.5&lon=-87.2", string(body), nil)
	}
	png1 := imageBody(t, "png", 32, 24)
	if rec := put(photo, "PHOTO", png1); rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(photo, "PHOTO", png1); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("retried upload: %d %s", rec.Code, rec.Body.String())
	}
	if rec := put(photo, "PHOTO", imageBody(t, "png", 16, 16)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an id reused for other bytes: %d", rec.Code)
	}
	if rec := put(sig, "SIGNATURE", imageBody(t, "png", 300, 100)); rec.Code != http.StatusCreated {
		t.Fatalf("signature upload: %d %s", rec.Code, rec.Body.String())
	}
	got := call(t, h, ctx, http.MethodGet, path+"/captures/"+photo, "", nil)
	if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "image/png" || !bytes.Equal(got.Body.Bytes(), png1) {
		t.Fatalf("read back %d %s", got.Code, got.Header().Get("Content-Type"))
	}

	dt := func(m int) string { return devBase.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	batch := `{"technicianId":"` + tech.ID + `","actions":[` +
		`{"id":"` + ids.New() + `","kind":"TRAVEL_START","deviceTime":"` + dt(1) + `"},` +
		`{"id":"` + ids.New() + `","kind":"WORK_START","deviceTime":"` + dt(2) + `"},` +
		`{"id":"` + ids.New() + `","kind":"PHOTO","deviceTime":"` + dt(3) + `","captureIds":["` + photo + `"]},` +
		`{"id":"` + ids.New() + `","kind":"CHECKLIST","deviceTime":"` + dt(4) + `","itemId":"` + item + `","done":true},` +
		`{"id":"` + ids.New() + `","kind":"PART_USED","deviceTime":"` + dt(5) + `","lineId":"` + line + `","qty":"2"},` +
		`{"id":"` + ids.New() + `","kind":"SIGNATURE","deviceTime":"` + dt(6) + `","signedBy":"Pat","signatureRef":"capture:` + sig + `","latitude":41.5,"longitude":-87.2},` +
		`{"id":"` + ids.New() + `","kind":"COMPLETE","deviceTime":"` + dt(7) + `"}]}`
	rec = call(t, h, ctx, http.MethodPost, path+"/actions", batch, nil)
	res := decodeInto[syncResponse](t, rec.Body.String())
	if rec.Code != http.StatusOK || res.Applied != 7 || res.WorkOrder.Status != "COMPLETED" {
		t.Fatalf("sync %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, ctx, http.MethodGet, "/v1/service/technicians/"+tech.ID+"/day?date="+start.Format("2006-01-02"), "", nil)
	day = store.Day{}
	if err := json.Unmarshal(rec.Body.Bytes(), &day); err != nil || len(day.Visits) != 1 || !day.Visits[0].Signature.Signed || len(day.Visits[0].Photos) != 1 || !day.Visits[0].Checklist[0].Done {
		t.Fatalf("completed day %s", rec.Body.String())
	}

	// The checklist of a completed work order is closed.
	wo = decodeInto[store.WorkOrder](t, call(t, h, ctx, http.MethodGet, path, "", nil).Body.String())
	if rec := call(t, h, ctx, http.MethodPut, path+"/checklist", `{"items":[{"text":"late"}]}`, map[string]string{"If-Match": `"` + strconv.Itoa(wo.Version) + `"`}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("checklist after completion: %d", rec.Code)
	}
}

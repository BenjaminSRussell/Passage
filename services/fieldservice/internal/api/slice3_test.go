package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
	"github.com/BenjaminSRussell/passage/services/fieldservice/migrations"
)

// Requests rejected before the store: field validation, missing headers,
// missing duties and callers whose user id cannot be recorded.
func TestSlice3_ValidationBeforeStore(t *testing.T) {
	h := newTestRouter()
	key := map[string]string{"Idempotency-Key": "k1"}
	object := func(serial, warranty string) string {
		return `{"itemId":"` + uuid1 + `","serial":"` + serial + `","customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 + `","warranty":` + warranty + `}`
	}
	shipped := func(body string) string {
		return strings.Replace(body, `"warranty"`, `"shippedOn":"2026-10-23","warranty"`, 1)
	}
	shipment := `{"shipmentId":"` + uuid1 + `","shipDate":"2026-10-23","customerPartyId":"` + uuid1 + `","legalEntityId":"` + uuid1 +
		`","lines":[{"itemId":"` + uuid1 + `","serials":["A","A"],"warranty":{"startTrigger":"SHIP","months":12}}]}`
	activity := func(extra string) string {
		return `{"kind":"TASK","subject":"Call","ownerWorkerId":"` + uuid1 + `"` + extra + `}`
	}
	article := `{"title":"Pump noise","language":"en","solution":"replace bearing"}`
	tests := []struct {
		name, method, path, body string
		headers                  map[string]string
		duties                   []string
		status                   int
		code, field              string
	}{
		{"object serial with spaces", http.MethodPost, "/v1/service/service-objects", object(" S1", `{"startTrigger":"SHIP","months":12}`),
			key, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, "serial"},
		{"object warranty trigger unknown", http.MethodPost, "/v1/service/service-objects", object("S1", `{"startTrigger":"SOLD","months":12}`),
			key, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, "warranty"},
		{"object warranty over 20 years", http.MethodPost, "/v1/service/service-objects", object("S1", `{"startTrigger":"SHIP","months":241}`),
			key, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, "warranty"},
		{"object SHIP warranty without ship date", http.MethodPost, "/v1/service/service-objects", object("S1", `{"startTrigger":"SHIP","months":12}`),
			key, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, "shippedOn"},
		{"object without key", http.MethodPost, "/v1/service/service-objects", shipped(object("S1", `{"startTrigger":"SHIP","months":12}`)),
			nil, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, idempotencyKey},
		{"object without duty", http.MethodPost, "/v1/service/service-objects", shipped(object("S1", `{"startTrigger":"SHIP","months":12}`)),
			key, []string{dutyInquire}, http.StatusForbidden, "AUTH-0003", ""},
		{"shipment repeats a serial", http.MethodPost, "/v1/service/service-objects:receive-shipment", shipment,
			nil, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, "lines[0].serials"},
		{"install without If-Match", http.MethodPost, "/v1/service/service-objects/" + uuid1 + ":install", `{"installedOn":"2026-11-02"}`,
			nil, []string{dutyObject}, http.StatusPreconditionRequired, "precondition_required", ""},
		{"install bad date", http.MethodPost, "/v1/service/service-objects/" + uuid1 + ":install", `{"installedOn":"02/11/2026"}`,
			map[string]string{"If-Match": `"1"`}, []string{dutyObject}, http.StatusBadRequest, problemCodeValid, "installedOn"},
		{"object list bad status", http.MethodGet, "/v1/service/service-objects?status=LOST", "",
			nil, []string{dutyInquire}, http.StatusBadRequest, problemCodeValid, "status"},
		{"activity bad kind", http.MethodPost, "/v1/service/activities", `{"kind":"MEETING","subject":"x","ownerWorkerId":"` + uuid1 + `"}`,
			key, []string{dutyActivity}, http.StatusBadRequest, problemCodeValid, "kind"},
		{"activity ends before it starts", http.MethodPost, "/v1/service/activities",
			activity(`,"startAt":"2026-10-27T10:00:00Z","endAt":"2026-10-27T09:00:00Z"`), key, []string{dutyActivity}, http.StatusBadRequest, problemCodeValid, "endAt"},
		{"activity on unknown record type", http.MethodPost, "/v1/service/activities",
			activity(`,"associations":[{"entityType":"payroll_run","entityId":"` + uuid1 + `"}]`), key, []string{dutyActivity}, http.StatusBadRequest, problemCodeValid, "associations[0].entityType"},
		{"activity reminder too long", http.MethodPost, "/v1/service/activities", activity(`,"reminderMinutes":20000`),
			key, []string{dutyActivity}, http.StatusBadRequest, problemCodeValid, "reminderMinutes"},
		{"activity list needs owner or record", http.MethodGet, "/v1/service/activities", "",
			nil, []string{dutyActivity}, http.StatusBadRequest, problemCodeValid, "owner"},
		{"activity list record needs id", http.MethodGet, "/v1/service/activities?entityType=case", "",
			nil, []string{dutyActivity}, http.StatusBadRequest, problemCodeValid, "entityId"},
		{"complete without If-Match", http.MethodPost, "/v1/service/activities/" + uuid1 + ":complete", "",
			nil, []string{dutyActivity}, http.StatusPreconditionRequired, "precondition_required", ""},
		{"article needs a solution", http.MethodPost, "/v1/service/knowledge/articles", `{"title":"t","language":"en"}`,
			key, []string{dutyKbAuthor}, http.StatusBadRequest, problemCodeValid, "solution"},
		{"article bad language", http.MethodPost, "/v1/service/knowledge/articles", `{"title":"t","language":"English","solution":"s"}`,
			key, []string{dutyKbAuthor}, http.StatusBadRequest, problemCodeValid, "language"},
		{"article applicability not uuids", http.MethodPost, "/v1/service/knowledge/articles",
			`{"title":"t","language":"en","solution":"s","applicability":{"itemIds":["pump"]}}`, key, []string{dutyKbAuthor}, http.StatusBadRequest, problemCodeValid, "applicability.itemIds"},
		{"article author must be a user id", http.MethodPost, "/v1/service/knowledge/articles", article,
			key, []string{dutyKbAuthor}, http.StatusForbidden, "AUTH-0003", ""},
		{"publish needs KbPublish", http.MethodPost, "/v1/service/knowledge/articles/" + uuid1 + ":publish", "",
			map[string]string{"If-Match": `"1"`}, []string{dutyKbAuthor}, http.StatusForbidden, "AUTH-0003", ""},
		{"search limit above 20", http.MethodGet, "/v1/service/knowledge/articles/search?q=pump&limit=21", "",
			nil, []string{dutyKbRead}, http.StatusBadRequest, problemCodeValid, "limit"},
		{"vote needs helpful", http.MethodPost, "/v1/service/knowledge/articles/" + uuid1 + ":vote", `{}`,
			nil, []string{dutyKbRead}, http.StatusBadRequest, problemCodeValid, "helpful"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.body, tc.headers, tc.duties...)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			code, fields := problemOf(t, rec)
			if code != tc.code {
				t.Fatalf("code %q, want %q", code, tc.code)
			}
			if tc.field != "" && fields[tc.field] == "" {
				t.Fatalf("no error on %q: %v", tc.field, fields)
			}
		})
	}
}

// slice3Router mounts the routes over a migrated store; as returns a context
// for one user holding every slice-3 duty.
func slice3Router(t *testing.T) (http.Handler, func(user string) context.Context) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	duties := testkit.ScopeDuties("")
	for _, d := range []string{dutyObject, dutyInquire, dutyActivity, dutyKbRead, dutyKbAuthor, dutyKbPublish, dutyCaseCreate} {
		duties[d] = struct{}{}
	}
	as := func(user string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: user, Duties: duties})
	}
	if err := dbx.Migrate(as(ids.New()), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	New(store.New(pool)).Register(r)
	return r, as
}

// AC-45.12 over HTTP: the author cannot publish their own article
// (KB-PUB-001); another user can, and publishing version 2 reports KB-PUB-002.
func TestAPI_KnowledgePublishSegregationAndRetireWarning(t *testing.T) {
	h, as := slice3Router(t)
	author, reviewer := as(ids.New()), as(ids.New())
	created := call(t, h, author, http.MethodPost, "/v1/service/knowledge/articles",
		`{"title":"Pump noise","language":"en","symptom":"noise","solution":"replace bearing"}`, map[string]string{"Idempotency-Key": "kb-1"})
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var art store.Article
	_ = json.Unmarshal(created.Body.Bytes(), &art)
	path := "/v1/service/knowledge/articles/" + art.ID
	submitted := call(t, h, author, http.MethodPost, path+":submit", "", map[string]string{"If-Match": `"1"`})
	if submitted.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", submitted.Code, submitted.Body.String())
	}
	etag := submitted.Header().Get("ETag")
	own := call(t, h, author, http.MethodPost, path+":publish", "", map[string]string{"If-Match": etag})
	if code, _ := problemOf(t, own); own.Code != http.StatusUnprocessableEntity || code != "KB-PUB-001" {
		t.Fatalf("author publish: %d %s", own.Code, own.Body.String())
	}
	pub := call(t, h, reviewer, http.MethodPost, path+":publish", "", map[string]string{"If-Match": etag})
	if pub.Code != http.StatusOK {
		t.Fatalf("publish: %d %s", pub.Code, pub.Body.String())
	}
	var v1 publishView
	_ = json.Unmarshal(pub.Body.Bytes(), &v1)
	if len(v1.Warnings) != 0 || v1.Article.Status != "PUBLISHED" {
		t.Fatalf("v1 publish = %+v", v1)
	}
	next := call(t, h, author, http.MethodPost, path+":new-version", "", map[string]string{"If-Match": pub.Header().Get("ETag")})
	if next.Code != http.StatusCreated {
		t.Fatalf("new version: %d %s", next.Code, next.Body.String())
	}
	var v2 store.Article
	_ = json.Unmarshal(next.Body.Bytes(), &v2)
	v2path := "/v1/service/knowledge/articles/" + v2.ID
	edited := call(t, h, author, http.MethodPut, v2path, `{"title":"Pump noise at start","solution":"replace the bearing"}`,
		map[string]string{"If-Match": next.Header().Get("ETag")})
	if edited.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", edited.Code, edited.Body.String())
	}
	sub2 := call(t, h, author, http.MethodPost, v2path+":submit", "", map[string]string{"If-Match": edited.Header().Get("ETag")})
	pub2 := call(t, h, reviewer, http.MethodPost, v2path+":publish", "", map[string]string{"If-Match": sub2.Header().Get("ETag")})
	var res publishView
	_ = json.Unmarshal(pub2.Body.Bytes(), &res)
	if pub2.Code != http.StatusOK || len(res.Warnings) != 1 || !strings.HasPrefix(res.Warnings[0], "KB-PUB-002") || res.Article.Title != "Pump noise at start" {
		t.Fatalf("v2 publish: %d %s", pub2.Code, pub2.Body.String())
	}
	search := call(t, h, reviewer, http.MethodGet, "/v1/service/knowledge/articles/search?q=pump+noise", "", nil)
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), v2.ID) || strings.Contains(search.Body.String(), art.ID) {
		t.Fatalf("search: %d %s", search.Code, search.Body.String())
	}
}

// FR-45.1, FR-45.14 over HTTP: an INSTALL warranty starts at :install; an
// activity on the object is exported as an iCalendar VEVENT.
func TestAPI_ObjectInstallAndActivityCalendar(t *testing.T) {
	h, as := slice3Router(t)
	ctx := as(ids.New())
	customer, owner := ids.New(), ids.New()
	created := call(t, h, ctx, http.MethodPost, "/v1/service/service-objects", `{"itemId":"`+ids.New()+`","serial":"P-1","customerPartyId":"`+customer+
		`","legalEntityId":"`+ids.New()+`","warranty":{"startTrigger":"INSTALL","months":12,"laborCovered":true}}`, map[string]string{"Idempotency-Key": "obj-1"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var obj store.ServiceObject
	_ = json.Unmarshal(created.Body.Bytes(), &obj)
	if obj.WarrantyStartsOn != nil {
		t.Fatalf("warranty started before installation: %s", *obj.WarrantyStartsOn)
	}
	installed := call(t, h, ctx, http.MethodPost, "/v1/service/service-objects/"+obj.ID+":install", `{"installedOn":"2026-11-02"}`,
		map[string]string{"If-Match": created.Header().Get("ETag")})
	if installed.Code != http.StatusOK || !strings.Contains(installed.Body.String(), `"warrantyStartsOn":"2026-11-02"`) ||
		!strings.Contains(installed.Body.String(), `"warrantyEndsOn":"2027-11-02"`) {
		t.Fatalf("install: %d %s", installed.Code, installed.Body.String())
	}

	act := call(t, h, ctx, http.MethodPost, "/v1/service/activities", `{"kind":"APPOINTMENT","subject":"Commissioning visit","ownerWorkerId":"`+owner+
		`","startAt":"2026-11-02T14:00:00Z","endAt":"2026-11-02T15:30:00Z","reminderMinutes":30,"associations":[{"entityType":"service_object","entityId":"`+obj.ID+`"}]}`,
		map[string]string{"Idempotency-Key": "act-1"})
	if act.Code != http.StatusCreated {
		t.Fatalf("activity: %d %s", act.Code, act.Body.String())
	}
	onObject := call(t, h, ctx, http.MethodGet, "/v1/service/activities?entityType=service_object&entityId="+obj.ID, "", nil)
	if onObject.Code != http.StatusOK || !strings.Contains(onObject.Body.String(), "Commissioning visit") {
		t.Fatalf("activities on object: %d %s", onObject.Code, onObject.Body.String())
	}
	cal := call(t, h, ctx, http.MethodGet, "/v1/service/activities:calendar?owner="+owner, "", nil)
	body := cal.Body.String()
	if cal.Code != http.StatusOK || !strings.HasPrefix(cal.Header().Get("Content-Type"), "text/calendar") ||
		!strings.Contains(body, "DTSTART:20261102T140000Z\r\n") || !strings.Contains(body, "DTEND:20261102T153000Z\r\n") ||
		!strings.Contains(body, "TRIGGER:-PT30M\r\n") {
		t.Fatalf("calendar: %d %q\n%s", cal.Code, cal.Header().Get("Content-Type"), body)
	}
}

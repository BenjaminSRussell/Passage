package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// dbRouter mounts the routes over a migrated store. The principal has every
// service duty and no legal-entity scope.
func dbRouter(t *testing.T) (http.Handler, context.Context) {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	duties := map[string]struct{}{tenant.DutyAllScope: {}}
	for _, d := range []string{dutyAgreement, dutyCaseCreate, dutyCaseMaintain, dutyWorkOrder, dutyBilling, dutyInquire} {
		duties[d] = struct{}{}
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u", Duties: duties})
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	New(store.New(pool)).Register(r)
	return r, ctx
}

func call(t *testing.T, h http.Handler, ctx context.Context, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A retried work-order create whose lines carry no ids replays the first
// response: the request fingerprint is taken before the server fills in ids.
func TestAPI_WorkOrderCreateReplaysWithServerLineIDs(t *testing.T) {
	h, ctx := dbRouter(t)
	body := `{"customerPartyId":"` + ids.New() + `","legalEntityId":"` + ids.New() + `","territoryId":"` + ids.New() +
		`","estDurationMinutes":60,"currency":"USD","lines":[{"kind":"LABOR","qty":"2","unitPrice":"95.00","unitCost":"40"}]}`
	first := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", body, map[string]string{"Idempotency-Key": "wo-retry"})
	if first.Code != http.StatusCreated || first.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d ETag %q %s", first.Code, first.Header().Get("ETag"), first.Body.String())
	}
	again := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", body, map[string]string{"Idempotency-Key": "wo-retry"})
	if again.Code != http.StatusOK || again.Header().Get(replayedHeader) != "true" {
		t.Fatalf("retry: %d %s", again.Code, again.Body.String())
	}
	var a, b store.WorkOrder
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(again.Body.Bytes(), &b)
	if a.ID == "" || a.ID != b.ID || len(b.Lines) != 1 || b.Lines[0].ID != a.Lines[0].ID {
		t.Fatalf("replay returned %+v, want %+v", b, a)
	}
	changed := strings.Replace(body, `"qty":"2"`, `"qty":"3"`, 1)
	reuse := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", changed, map[string]string{"Idempotency-Key": "wo-retry"})
	if code, _ := problemOf(t, reuse); reuse.Code != http.StatusUnprocessableEntity || code != store.CodeIdempotencyReuse {
		t.Fatalf("changed body under the same key: %d %s", reuse.Code, reuse.Body.String())
	}

	list := call(t, h, ctx, http.MethodGet, "/v1/service/work-orders?status=UNSCHEDULED&limit=10", "", nil)
	var page store.Page[store.WorkOrder]
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || list.Code != http.StatusOK || len(page.Items) != 1 {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
}

// Reads of unknown ids are 404 with the documented codes, and list filters are validated.
func TestAPI_ReadsAndListFilters(t *testing.T) {
	h, ctx := dbRouter(t)
	for path, want := range map[string]string{
		"/v1/service/agreements/" + ids.New():   store.CodeAgreementNotFound,
		"/v1/service/cases/" + ids.New():        store.CodeCaseUnknown,
		"/v1/service/work-orders/" + ids.New():  store.CodeWOUnknown,
		"/v1/service/sla-profiles/" + ids.New(): store.CodeProfileNotFound,
	} {
		rec := call(t, h, ctx, http.MethodGet, path, "", nil)
		if code, _ := problemOf(t, rec); rec.Code != http.StatusNotFound || code != want {
			t.Errorf("GET %s: %d %s, want 404 %s", path, rec.Code, code, want)
		}
	}
	rec := call(t, h, ctx, http.MethodGet, "/v1/service/cases?slaState=LATE&cursor=nope&limit=0", "", nil)
	_, fields := problemOf(t, rec)
	if rec.Code != http.StatusBadRequest || fields["slaState"] == "" || fields["cursor"] == "" || fields["limit"] == "" {
		t.Fatalf("bad filters: %d %v", rec.Code, fields)
	}
	rec = call(t, h, ctx, http.MethodGet, "/v1/service/sla-profiles", "", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty profile list: %d %s", rec.Code, rec.Body.String())
	}
}

// A 24x7 profile needs no business hours in the request.
func TestAPI_AllDayProfileNeedsNoHours(t *testing.T) {
	h, ctx := dbRouter(t)
	rec := call(t, h, ctx, http.MethodPost, "/v1/service/sla-profiles",
		`{"code":"24X7","name":"Round the clock","timeZone":"UTC","allDay":true,"targets":[{"priority":1,"responseMinutes":15,"resolutionMinutes":240}]}`,
		map[string]string{"Idempotency-Key": "p-24x7"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create 24x7 profile: %d %s", rec.Code, rec.Body.String())
	}
}

// FR-45.5 over HTTP: reads carry the case ETag, a transition at that ETag
// pauses the clock and returns the next one, a keyed retry replays, and the
// KPIs and monitor answer for the tenant.
func TestAPI_CaseTransitionETagAndReplay(t *testing.T) {
	h, ctx := dbRouter(t)
	profile := call(t, h, ctx, http.MethodPost, "/v1/service/sla-profiles",
		`{"code":"H24","name":"24x7","timeZone":"UTC","allDay":true,"targets":[{"priority":2,"responseMinutes":60,"resolutionMinutes":480}]}`,
		map[string]string{"Idempotency-Key": "p"})
	var p store.SLAProfile
	if err := json.Unmarshal(profile.Body.Bytes(), &p); err != nil || profile.Code != http.StatusCreated {
		t.Fatalf("profile: %d %s", profile.Code, profile.Body.String())
	}
	opened := call(t, h, ctx, http.MethodPost, "/v1/service/cases", `{"customerPartyId":"`+ids.New()+`","legalEntityId":"`+ids.New()+
		`","channel":"PHONE","priority":2,"fallbackSlaProfileId":"`+p.ID+`"}`, map[string]string{"Idempotency-Key": "c"})
	var c store.Case
	if err := json.Unmarshal(opened.Body.Bytes(), &c); err != nil || opened.Code != http.StatusCreated || opened.Header().Get("ETag") != `"1"` {
		t.Fatalf("open: %d ETag %q %s", opened.Code, opened.Header().Get("ETag"), opened.Body.String())
	}
	read := call(t, h, ctx, http.MethodGet, "/v1/service/cases/"+c.ID, "", nil)
	if read.Header().Get("ETag") != `"1"` {
		t.Fatalf("read ETag %q", read.Header().Get("ETag"))
	}
	path := "/v1/service/cases/" + c.ID + ":transition"
	headers := map[string]string{"If-Match": `"1"`, "Idempotency-Key": "pause"}
	moved := call(t, h, ctx, http.MethodPost, path, `{"to":"WAITING_CUSTOMER"}`, headers)
	var m store.Case
	if err := json.Unmarshal(moved.Body.Bytes(), &m); err != nil || moved.Code != http.StatusOK || moved.Header().Get("ETag") != `"2"` ||
		m.SLAState != "PAUSED" || m.RespondedAt == nil {
		t.Fatalf("transition: %d ETag %q %s", moved.Code, moved.Header().Get("ETag"), moved.Body.String())
	}
	again := call(t, h, ctx, http.MethodPost, path, `{"to":"WAITING_CUSTOMER"}`, headers)
	if again.Code != http.StatusOK || again.Header().Get(replayedHeader) != "true" || again.Header().Get("ETag") != `"2"` {
		t.Fatalf("keyed retry: %d %v %s", again.Code, again.Header(), again.Body.String())
	}
	stale := call(t, h, ctx, http.MethodPost, path, `{"to":"IN_PROGRESS"}`, map[string]string{"If-Match": `"1"`})
	if code, _ := problemOf(t, stale); stale.Code != http.StatusPreconditionFailed || code != store.CodeVersionConflict {
		t.Fatalf("stale: %d %s", stale.Code, stale.Body.String())
	}
	closed := call(t, h, ctx, http.MethodPost, path, `{"to":"CLOSED"}`, map[string]string{"If-Match": `"2"`})
	if code, _ := problemOf(t, closed); closed.Code != http.StatusUnprocessableEntity || code != store.CodeCaseTransition {
		t.Fatalf("close an unresolved case: %d %s", closed.Code, closed.Body.String())
	}

	if rec := call(t, h, ctx, http.MethodPost, "/v1/service/sla:scan", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"scanned":0`) {
		t.Fatalf("scan of a paused case: %d %s", rec.Code, rec.Body.String())
	}
	kpis := call(t, h, ctx, http.MethodGet, "/v1/service/kpis?from=2000-01-01T00:00:00Z&to=2000-12-31T00:00:00Z", "", nil)
	if kpis.Code != http.StatusOK || !strings.Contains(kpis.Body.String(), `"casesOpened":0`) || !strings.Contains(kpis.Body.String(), `"firstTimeFixPct":null`) {
		t.Fatalf("empty KPI window: %d %s", kpis.Code, kpis.Body.String())
	}
}

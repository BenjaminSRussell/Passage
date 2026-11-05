package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// permitRouterNoDB serves the permit routes with a nil store: every case below
// is rejected by validation, a missing If-Match or a duty check before the store
// is touched, so the tests run without PASSAGE_TEST_DATABASE_URL.
func permitRouterNoDB() http.Handler {
	return New(nil, func(context.Context) error { return nil }).Routes()
}

func permitRequest(method, path, duties, body string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", ids.New())
	req.Header.Set("X-Passage-Duties", duties)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestPermitRoutes_ValidationBeforeStore(t *testing.T) {
	h := permitRouterNoDB()
	permitID := ids.New()
	issuer := DutyPermitIssuer
	tech := DutyTechnician
	key := map[string]string{"Idempotency-Key": "k-1"}
	cases := []struct {
		name   string
		method string
		path   string
		duties string
		body   string
		hdr    map[string]string
		want   int
	}{
		{"transition needs If-Match", http.MethodPost, "/v1/eam/permits/" + permitID + "/transition",
			issuer, `{"to":"APPROVED"}`, key, http.StatusPreconditionRequired},
		{"transition rejects REQUESTED target", http.MethodPost, "/v1/eam/permits/" + permitID + "/transition",
			issuer, `{"to":"REQUESTED"}`, map[string]string{"Idempotency-Key": "k-2", "If-Match": "1"}, http.StatusBadRequest},
		{"transition needs permit issuer duty", http.MethodPost, "/v1/eam/permits/" + permitID + "/transition",
			tech, `{"to":"APPROVED"}`, map[string]string{"Idempotency-Key": "k-3", "If-Match": "1"}, http.StatusForbidden},
		{"lock action must be known", http.MethodPost, "/v1/eam/permits/" + permitID + "/locks",
			tech, `{"action":"BOGUS","isolationPointId":"` + ids.New() + `"}`, key, http.StatusBadRequest},
		{"lock needs isolation point uuid", http.MethodPost, "/v1/eam/permits/" + permitID + "/locks",
			tech, `{"action":"APPLY","isolationPointId":"nope"}`, key, http.StatusBadRequest},
		{"lock needs technician duty", http.MethodPost, "/v1/eam/permits/" + permitID + "/locks",
			issuer, `{"action":"APPLY","isolationPointId":"` + ids.New() + `"}`, key, http.StatusForbidden},
		{"permit request needs work order uuid", http.MethodPost, "/v1/eam/permits",
			DutyPlanner, `{"workOrderId":"x","permitTypeCode":"HOT_WORK"}`, key, http.StatusBadRequest},
		{"permit request needs validity", http.MethodPost, "/v1/eam/permits",
			DutyPlanner, `{"workOrderId":"` + ids.New() + `","permitTypeCode":"HOT_WORK"}`, key, http.StatusBadRequest},
		{"permit type code is checked", http.MethodPost, "/v1/eam/permit-types",
			DutySetupAdmin, `{"code":"x","name":"Hot","approverRole":"Safety","maxValidityHours":12}`, key, http.StatusBadRequest},
		{"permit type validity bound", http.MethodPost, "/v1/eam/permit-types",
			DutySetupAdmin, `{"code":"HOT_WORK","name":"Hot","approverRole":"Safety","maxValidityHours":500}`, key, http.StatusBadRequest},
		{"isolation energy type is checked", http.MethodPost, "/v1/eam/assets/" + ids.New() + "/isolation-points",
			DutySetupAdmin, `{"energyType":"NUCLEAR","location":"MCC-1","method":"breaker","verificationStep":"try start","tagCode":"ISO-1"}`,
			key, http.StatusBadRequest},
		{"job permit requirement codes are checked", http.MethodPut, "/v1/eam/job-types/REPAIR/permits",
			DutySetupAdmin, `{"permitTypeCodes":["bad code"]}`, nil, http.StatusBadRequest},
		{"permit list status filter", http.MethodGet, "/v1/eam/permits?status=NOPE",
			DutyInquire, ``, nil, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, permitRequest(c.method, c.path, c.duties, c.body, c.hdr))
			if rec.Code != c.want {
				t.Fatalf("got %d want %d: %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestHTTP_PermitFlowAC4411 runs AC-44.11 over HTTP against a database: the
// requester, approver, technicians and issuer are different signed-in users
// (X-Passage-User), so separation of duties and personal locks come from the
// caller, not the body. Two of three verified locks keep the job from starting
// (EAM-PMT-002); three plus an ISSUED permit let it start; closing with a lock
// still applied returns EAM-PMT-003.
func TestHTTP_PermitFlowAC4411(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	h := New(store.New(pool), nil).Routes()
	all := "eam.asset.maintain,eam.setup.admin,eam.work.plan,eam.permit.issue,eam.work-order.execute,eam.inquire"
	send := func(user, method, path, body, key string, extra map[string]string) *httptest.ResponseRecorder {
		hdr := headers(all)
		hdr["X-Passage-User"] = user
		hdr["Idempotency-Key"] = key
		for k, v := range extra {
			hdr[k] = v
		}
		return call(t, h, method, path, body, hdr)
	}
	ifMatch := func(v int) map[string]string { return map[string]string{"If-Match": `"` + strconv.Itoa(v) + `"`} }

	var asset struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	decodeInto(t, send("admin", http.MethodPost, "/v1/eam/assets", `{"name":"Conveyor","assetType":"CONV","siteId":"`+testkit.TenantID(9)+`","criticality":3}`, "a1", nil),
		http.StatusCreated, &asset)
	decodeInto(t, send("admin", http.MethodPost, "/v1/eam/assets/"+asset.ID+"/activate", ``, "act", ifMatch(1)), http.StatusOK, &asset)
	decodeInto(t, send("admin", http.MethodPost, "/v1/eam/permit-types",
		`{"code":"ELECTRICAL_ISOLATION","name":"Electrical isolation","approverRole":"Area authority","maxValidityHours":12,"requiresIsolation":true}`, "pt", nil),
		http.StatusOK, &struct{}{})
	var jp struct{ PermitTypeCodes []string }
	decodeInto(t, send("admin", http.MethodPut, "/v1/eam/job-types/ELEC_REPAIR/permits", `{"permitTypeCodes":["ELECTRICAL_ISOLATION"]}`, "", nil),
		http.StatusOK, &jp)
	if len(jp.PermitTypeCodes) != 1 {
		t.Fatalf("job permits = %+v", jp)
	}
	points := make([]string, 3)
	for i := range points {
		var ip struct{ ID string }
		decodeInto(t, send("admin", http.MethodPost, "/v1/eam/assets/"+asset.ID+"/isolation-points",
			`{"energyType":"ELECTRICAL","location":"MCC-`+strconv.Itoa(i)+`","method":"breaker lock","verificationStep":"try start","tagCode":"ISO-`+strconv.Itoa(i)+`"}`,
			"ip"+strconv.Itoa(i), nil), http.StatusCreated, &ip)
		points[i] = ip.ID
	}

	var wo struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	decodeInto(t, send("planner", http.MethodPost, "/v1/eam/work-orders",
		`{"assetId":"`+asset.ID+`","workOrderType":"CORRECTIVE","currency":"USD","jobType":"ELEC_REPAIR"}`, "w1", nil), http.StatusCreated, &wo)
	for _, to := range []string{"SCHEDULED", "RELEASED"} {
		decodeInto(t, send("planner", http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"`+to+`"}`, "s"+to, ifMatch(wo.Version)),
			http.StatusOK, &wo)
	}

	var permit struct {
		ID          string `json:"id"`
		Status      string `json:"status"`
		Version     int    `json:"version"`
		RequestedBy string `json:"requestedBy"`
	}
	from := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	to := time.Now().UTC().Add(4 * time.Hour).Format(time.RFC3339)
	decodeInto(t, send("planner", http.MethodPost, "/v1/eam/permits",
		`{"workOrderId":"`+wo.ID+`","permitTypeCode":"ELECTRICAL_ISOLATION","validFrom":"`+from+`","validTo":"`+to+`"}`, "p1", nil),
		http.StatusCreated, &permit)
	if permit.RequestedBy != "planner" {
		t.Fatalf("requestedBy = %q, want the calling user", permit.RequestedBy)
	}
	transition := func(user, to, key string, version int) *httptest.ResponseRecorder {
		return send(user, http.MethodPost, "/v1/eam/permits/"+permit.ID+"/transition", `{"to":"`+to+`"}`, key, ifMatch(version))
	}
	expectCode(t, transition("planner", "APPROVED", "t0", permit.Version), http.StatusUnprocessableEntity, "EAM-PMT-005")
	decodeInto(t, transition("issuer", "APPROVED", "t1", permit.Version), http.StatusOK, &permit)

	lock := func(user, action, point, key string, want int) {
		t.Helper()
		rec := send(user, http.MethodPost, "/v1/eam/permits/"+permit.ID+"/locks", `{"action":"`+action+`","isolationPointId":"`+point+`"}`, key, nil)
		if rec.Code != want {
			t.Fatalf("%s %s by %s: %d %s", action, point, user, rec.Code, rec.Body.String())
		}
	}
	for i, p := range points[:2] {
		lock("tech-1", "APPLY", p, "la"+strconv.Itoa(i), http.StatusCreated)
		lock("tech-1", "VERIFY", p, "lv"+strconv.Itoa(i), http.StatusOK)
	}
	expectCode(t, transition("issuer", "ISSUED", "t2", permit.Version), http.StatusUnprocessableEntity, "EAM-PMT-002")
	expectCode(t, send("tech-1", http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"IN_PROGRESS"}`, "go0", ifMatch(wo.Version)),
		http.StatusUnprocessableEntity, "EAM-PMT-002")
	lock("tech-1", "APPLY", points[2], "la2", http.StatusCreated)
	lock("tech-1", "VERIFY", points[2], "lv2", http.StatusOK)
	decodeInto(t, transition("issuer", "ISSUED", "t3", permit.Version), http.StatusOK, &permit)
	if permit.Status != "ISSUED" {
		t.Fatalf("permit status = %s, want ISSUED", permit.Status)
	}
	var detail struct {
		Permit struct {
			LocksOpen int `json:"locksOpen"`
		} `json:"permit"`
		UnlockedPoints int `json:"unlockedPoints"`
	}
	got := send("tech-1", http.MethodGet, "/v1/eam/permits/"+permit.ID, ``, "", nil)
	decodeInto(t, got, http.StatusOK, &detail)
	if got.Header().Get("ETag") != `"`+strconv.Itoa(permit.Version)+`"` || detail.Permit.LocksOpen != 3 || detail.UnlockedPoints != 0 {
		t.Fatalf("permit detail etag %q = %+v", got.Header().Get("ETag"), detail)
	}
	decodeInto(t, send("tech-1", http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"IN_PROGRESS"}`, "go1", ifMatch(wo.Version)),
		http.StatusOK, &wo)
	decodeInto(t, send("tech-1", http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"ON_HOLD"}`, "hold", ifMatch(wo.Version)),
		http.StatusOK, &wo)
	for i, p := range points[:2] {
		lock("tech-1", "REMOVE", p, "lr"+strconv.Itoa(i), http.StatusOK)
	}
	expectCode(t, transition("issuer", "CLOSED", "t4", permit.Version), http.StatusUnprocessableEntity, "EAM-PMT-003")
	// Another user cannot remove tech-1's lock: locks are personal.
	expectCode(t, send("tech-2", http.MethodPost, "/v1/eam/permits/"+permit.ID+"/locks",
		`{"action":"REMOVE","isolationPointId":"`+points[2]+`"}`, "lr-other", nil), http.StatusUnprocessableEntity, "EAM-PMT-006")
	lock("tech-1", "REMOVE", points[2], "lr2", http.StatusOK)
	decodeInto(t, transition("issuer", "CLOSED", "t5", permit.Version), http.StatusOK, &permit)
	if permit.Status != "CLOSED" {
		t.Fatalf("permit status = %s, want CLOSED", permit.Status)
	}
}

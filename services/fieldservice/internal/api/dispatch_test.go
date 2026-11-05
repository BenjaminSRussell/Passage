package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

func decodeInto[T any](t *testing.T, body string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

// FR-45.7 over HTTP: register a technician, give the work order its scheduling data,
// assign it (a broken constraint is 422 with every code), read the board, publish the
// day, and replay the publish under its key.
func TestAPI_DispatchFlow(t *testing.T) {
	h, ctx := dbRouter(t)
	terr, le := ids.New(), ids.New()
	tech := call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"ANA","name":"Ana Ruiz","legalEntityId":"`+le+`","territoryIds":["`+terr+
		`"],"skills":["hvac"," electrical "],"timeZone":"America/Chicago","homeLatitude":41.88,"homeLongitude":-87.63}`, map[string]string{"Idempotency-Key": "tech-1"})
	if tech.Code != http.StatusCreated || tech.Header().Get("ETag") != `"1"` {
		t.Fatalf("create technician: %d %s", tech.Code, tech.Body.String())
	}
	ana := decodeInto[store.Technician](t, tech.Body.String())
	if strings.Join(ana.Skills, ",") != "ELECTRICAL,HVAC" || ana.ShiftStartMinute != 480 || ana.ShiftEndMinute != 1020 || ana.MaxDailyMinutes != 480 ||
		!ana.Active || len(ana.ShiftDays) != 5 {
		t.Fatalf("defaults and skill normalization: %+v", ana)
	}
	if dup := call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"ANA","name":"Other","legalEntityId":"`+le+`","territoryIds":["`+terr+
		`"],"timeZone":"UTC"}`, map[string]string{"Idempotency-Key": "tech-2"}); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate code: %d %s", dup.Code, dup.Body.String())
	}

	woBody := `{"customerPartyId":"` + ids.New() + `","legalEntityId":"` + le + `","territoryId":"` + terr + `","estDurationMinutes":120,"currency":"USD",` +
		`"requiredSkills":["HVAC","WELDING"],"address":"1 Main St","latitude":41.9,"longitude":-87.6}`
	created := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", woBody, map[string]string{"Idempotency-Key": "wo-1"})
	wo := decodeInto[store.WorkOrder](t, created.Body.String())
	if created.Code != http.StatusCreated || strings.Join(wo.RequiredSkills, ",") != "HVAC,WELDING" || wo.Address != "1 Main St" {
		t.Fatalf("create work order: %d %s", created.Code, created.Body.String())
	}
	assign := "/v1/service/work-orders/" + wo.ID + ":assign"
	body := `{"technicianId":"` + ana.ID + `","start":"2026-10-26T14:00:00Z"}`

	refused := call(t, h, ctx, http.MethodPost, assign, body, map[string]string{"If-Match": `"1"`})
	code, fields := problemOf(t, refused)
	if refused.Code != http.StatusUnprocessableEntity || code != "SVC-DSP-003" || fields["SVC-DSP-003"] == "" {
		t.Fatalf("missing skill: %d %s", refused.Code, refused.Body.String())
	}
	// Dropping the skill the technician lacks lets the assignment through.
	fixed := call(t, h, ctx, http.MethodPut, "/v1/service/work-orders/"+wo.ID+"/scheduling",
		`{"requiredSkills":["HVAC"],"address":"1 Main St","latitude":41.9,"longitude":-87.6,"estDurationMinutes":120}`, map[string]string{"If-Match": `"1"`})
	if fixed.Code != http.StatusOK || fixed.Header().Get("ETag") != `"2"` {
		t.Fatalf("scheduling: %d %s", fixed.Code, fixed.Body.String())
	}
	if none := call(t, h, ctx, http.MethodPost, assign, body, nil); none.Code != http.StatusPreconditionRequired {
		t.Fatalf("assign without If-Match: %d", none.Code)
	}
	ok := call(t, h, ctx, http.MethodPost, assign, body, map[string]string{"If-Match": `"2"`})
	scheduled := decodeInto[store.WorkOrder](t, ok.Body.String())
	if ok.Code != http.StatusOK || ok.Header().Get("ETag") != `"3"` || scheduled.Status != "SCHEDULED" || scheduled.TechnicianID == nil {
		t.Fatalf("assign: %d %s", ok.Code, ok.Body.String())
	}
	if edit := call(t, h, ctx, http.MethodPut, "/v1/service/work-orders/"+wo.ID+"/scheduling", `{"estDurationMinutes":30}`, map[string]string{"If-Match": `"3"`}); edit.Code != http.StatusUnprocessableEntity {
		t.Fatalf("editing a scheduled order: %d %s", edit.Code, edit.Body.String())
	}

	board := call(t, h, ctx, http.MethodGet, "/v1/service/dispatch/board?territoryId="+terr+"&date=2026-10-26&timeZone=America/Chicago", "", nil)
	b := decodeInto[store.Board](t, board.Body.String())
	if board.Code != http.StatusOK || len(b.Technicians) != 1 || len(b.Technicians[0].Assignments) != 1 || b.Technicians[0].BookedMinutes != 120 || len(b.Unscheduled) != 0 {
		t.Fatalf("board: %d %s", board.Code, board.Body.String())
	}
	if bad := call(t, h, ctx, http.MethodGet, "/v1/service/dispatch/board?territoryId=x&date=soon&timeZone=Mars/Base", "", nil); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad board query: %d", bad.Code)
	} else if _, f := problemOf(t, bad); f["territoryId"] == "" || f["date"] == "" || f["timeZone"] == "" {
		t.Fatalf("board field errors: %v", f)
	}

	pub := `{"legalEntityId":"` + le + `","territoryId":"` + terr + `","date":"2026-10-26","timeZone":"America/Chicago"}`
	run := call(t, h, ctx, http.MethodPost, "/v1/service/dispatch/publish", pub, map[string]string{"Idempotency-Key": "pub-1"})
	r := decodeInto[store.DispatchRun](t, run.Body.String())
	if run.Code != http.StatusCreated || r.DispatchedCount != 1 || r.Assignments[0].Sequence != 1 {
		t.Fatalf("publish: %d %s", run.Code, run.Body.String())
	}
	if again := call(t, h, ctx, http.MethodPost, "/v1/service/dispatch/publish", pub, map[string]string{"Idempotency-Key": "pub-1"}); again.Code != http.StatusOK || again.Header().Get(replayedHeader) != "true" {
		t.Fatalf("publish replay: %d %s", again.Code, again.Body.String())
	}
	if empty := call(t, h, ctx, http.MethodPost, "/v1/service/dispatch/publish", pub, map[string]string{"Idempotency-Key": "pub-2"}); empty.Code != http.StatusUnprocessableEntity {
		t.Fatalf("nothing left: %d %s", empty.Code, empty.Body.String())
	}
	if got := call(t, h, ctx, http.MethodGet, "/v1/service/dispatch/runs/"+r.ID, "", nil); got.Code != http.StatusOK {
		t.Fatalf("read run: %d", got.Code)
	}
	if list := call(t, h, ctx, http.MethodGet, "/v1/service/dispatch/runs?territoryId="+terr, "", nil); !strings.Contains(list.Body.String(), r.ID) {
		t.Fatalf("list runs: %d %s", list.Code, list.Body.String())
	}
	dispatched := call(t, h, ctx, http.MethodGet, "/v1/service/work-orders/"+wo.ID, "", nil)
	if w := decodeInto[store.WorkOrder](t, dispatched.Body.String()); w.Status != "DISPATCHED" {
		t.Fatalf("order after publish: %s", w.Status)
	}
	if unassign := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders/"+wo.ID+":unassign", "", map[string]string{"If-Match": dispatched.Header().Get("ETag")}); unassign.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unassign a dispatched order: %d", unassign.Code)
	}
}

// Validation of the request bodies, and the duties: reading needs fieldservice.inquire, writing
// technicians and assignments needs fieldservice.technician.dispatch.
func TestAPI_DispatchValidationAndDuties(t *testing.T) {
	h, ctx := dbRouter(t)
	bad := call(t, h, ctx, http.MethodPost, "/v1/service/technicians", `{"code":"a","name":"","legalEntityId":"x","territoryIds":[],"skills":["not ok!"],`+
		`"timeZone":"Local","shiftDays":[0,8],"shiftStartMinute":900,"shiftEndMinute":800,"maxDailyMinutes":5,"homeLatitude":95}`, map[string]string{"Idempotency-Key": "k"})
	_, f := problemOf(t, bad)
	for _, field := range []string{"code", "name", "legalEntityId", "territoryIds", "skills", "timeZone", "shiftDays", "shiftStartMinute", "maxDailyMinutes", "homeLatitude"} {
		if bad.Code != http.StatusBadRequest || f[field] == "" {
			t.Errorf("field %s: %d %v", field, bad.Code, f)
		}
	}
	wo := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders", `{"customerPartyId":"`+ids.New()+`","legalEntityId":"`+ids.New()+`","territoryId":"`+ids.New()+
		`","estDurationMinutes":30,"currency":"USD","windowStart":"2026-10-26T10:00:00Z","requiredSkills":["bad skill"],"latitude":10}`, map[string]string{"Idempotency-Key": "w"})
	_, f = problemOf(t, wo)
	if wo.Code != http.StatusBadRequest || f["windowStart"] == "" || f["requiredSkills"] == "" || f["latitude"] == "" {
		t.Fatalf("work order scheduling validation: %d %v", wo.Code, f)
	}
	if rec := call(t, h, ctx, http.MethodPost, "/v1/service/work-orders/"+ids.New()+":assign", `{"technicianId":"x"}`, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("assign validation: %d", rec.Code)
	}
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians/"+ids.New(), "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown technician: %d", rec.Code)
	}
	if rec := call(t, h, ctx, http.MethodGet, "/v1/service/technicians?active=maybe", "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad active filter: %d", rec.Code)
	}

	// A principal with only fieldservice.inquire reads but cannot dispatch.
	h2, ctx2 := dbRouter(t)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			p, _ := tenant.FromContext(ctx2)
			p.Duties = map[string]struct{}{dutyInquire: {}, tenant.DutyAllScope: {}}
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	r.Mount("/", h2)
	if rec := call(t, r, ctx2, http.MethodPost, "/v1/service/dispatch/publish", `{}`, map[string]string{"Idempotency-Key": "k"}); rec.Code != http.StatusForbidden {
		t.Fatalf("publish without fieldservice.technician.dispatch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, r, ctx2, http.MethodGet, "/v1/service/technicians", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("list with fieldservice.inquire: %d %s", rec.Code, rec.Body.String())
	}
}

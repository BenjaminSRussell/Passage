package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const reasonID = "0195f0a0-0000-7000-8000-000000000041"

func (f *fakeStore) CreateDowntimeReason(_ context.Context, in store.DowntimeReasonInput, by string, _ store.Idem) (store.DowntimeReason, error) {
	f.reasonIn = &in
	return store.DowntimeReason{ID: reasonID, Code: in.Code, Name: in.Name, Category: in.Category, Active: in.Active, Version: 1, CreatedBy: by}, nil
}

func (f *fakeStore) UpdateDowntimeReason(_ context.Context, id string, version int, in store.DowntimeReasonInput, by string) (store.DowntimeReason, error) {
	f.reasonIn, f.updateVersion = &in, version
	return store.DowntimeReason{ID: id, Code: "JAM", Name: in.Name, Category: in.Category, Active: in.Active, Version: version + 1, CreatedBy: by}, nil
}

func (f *fakeStore) GetDowntimeReason(_ context.Context, id string) (store.DowntimeReason, error) {
	return store.DowntimeReason{ID: id, Code: "JAM", Name: "Jam", Category: "unplanned", Active: true, Version: 2}, nil
}

func (f *fakeStore) ListDowntimeReasons(context.Context, bool, int) ([]store.DowntimeReason, error) {
	return []store.DowntimeReason{{ID: reasonID, Code: "JAM", Name: "Jam", Category: "unplanned", Active: true, Version: 1}}, nil
}

func TestDowntimeReason_CreateUpdateAndRules(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/downtime-reasons", `{"code":"CHG","name":"Changeover","category":"planned"}`, allDuty)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || f.reasonIn.Category != "planned" || !f.reasonIn.Active {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body.String(), f.reasonIn)
	}
	for name, body := range map[string]string{
		"no category":   `{"code":"A","name":"A"}`,
		"bad category":  `{"code":"A","name":"A","category":"other"}`,
		"empty name":    `{"code":"A","name":" ","category":"planned"}`,
		"long code":     `{"code":"ABCDEFGHIJABCDEFGHIJABCDEFGHIJABX","name":"A","category":"planned"}`,
		"missing code":  `{"name":"A","category":"planned"}`,
		"unknown field": `{"code":"A","name":"A","category":"planned","extra":1}`,
	} {
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/downtime-reasons", body, allDuty); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	path := "/v1/shopfloor/downtime-reasons/" + reasonID
	if rec := callWith(t, h, http.MethodPut, path, `{"name":"Jam","category":"unplanned"}`, allDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPut, path, `{"name":"Jam","category":"unplanned","active":false}`, allDuty, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || f.updateVersion != 2 || f.reasonIn.Active || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWith(t, h, http.MethodPut, "/v1/shopfloor/downtime-reasons/x", `{"name":"J","category":"planned"}`, allDuty, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeReasonNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestDowntimeReason_ReadsAndDuties(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/downtime-reasons?active=true", "", "shopfloor.view"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/downtime-reasons/"+reasonID, "", "shopfloor.view"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/downtime-reasons", `{"code":"A","name":"A","category":"planned"}`, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without the setup duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/downtime-reasons", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list without a duty: %d", rec.Code)
	}
}

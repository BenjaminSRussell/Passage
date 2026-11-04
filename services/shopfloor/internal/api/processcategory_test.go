package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const categoryID = "0195f0a0-0000-7000-8000-000000000051"

func (f *fakeStore) CreateProcessCategory(_ context.Context, in domain.CategoryInput, by string, _ store.Idem) (store.ProcessCategory, error) {
	f.categoryIn = &in
	return store.ProcessCategory{ID: categoryID, Code: in.Code, Name: in.Name, Destinations: in.Destinations, RejectOutOfLimit: in.RejectOutOfLimit,
		Characteristics: in.Characteristics, Active: in.Active, Version: 1, CreatedBy: by}, nil
}

func (f *fakeStore) UpdateProcessCategory(_ context.Context, id string, version int, in domain.CategoryInput, by string) (store.ProcessCategory, error) {
	f.categoryIn, f.updateVersion = &in, version
	return store.ProcessCategory{ID: id, Code: "quality-result", Name: in.Name, Destinations: in.Destinations, Active: in.Active, Version: version + 1, CreatedBy: by}, nil
}

func (f *fakeStore) GetProcessCategory(_ context.Context, id string) (store.ProcessCategory, error) {
	return store.ProcessCategory{ID: id, Code: "quality-result", Name: "Quality", Destinations: []string{"quality"}, Active: true, Version: 2}, nil
}

func (f *fakeStore) ListProcessCategories(context.Context, bool, int) ([]store.ProcessCategory, error) {
	return []store.ProcessCategory{{Code: "custom", Name: "custom", Destinations: []string{"kafka"}, Active: true, BuiltIn: true}}, nil
}

// ResolveProcessCategory serves the built-in defaults, which is what a tenant with no master rows gets.
func (f *fakeStore) ResolveProcessCategory(_ context.Context, code string) (domain.Category, error) {
	if c, ok := domain.CategoryByCode(code); ok {
		return c, nil
	}
	return domain.Category{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCategoryNotFound, "process category not found")
}

func (f *fakeStore) StoredProcessMessage(context.Context, string, string) (store.ProcessMessage, bool, error) {
	return store.ProcessMessage{}, false, nil
}

func (f *processFake) StoredProcessMessage(_ context.Context, source, sourceMessageID string) (store.ProcessMessage, bool, error) {
	for _, m := range f.msgs {
		if m.Source == source && m.SourceMessageID == sourceMessageID {
			return m, true, nil
		}
	}
	return store.ProcessMessage{}, false, nil
}

// inactiveCategoryFake refuses every category as inactive (SF-0165), as after a supervisor
// deactivated it, and keeps the messages already stored.
type inactiveCategoryFake struct{ processFake }

func (f *inactiveCategoryFake) ResolveProcessCategory(_ context.Context, code string) (domain.Category, error) {
	return domain.Category{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeCategoryInactive, "process category "+code+" is inactive")
}

func TestProcessCategory_CreateUpdateAndRules(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"code":"pi-weigh","name":"Weigh-in","destinations":["ebr","kafka"],"rejectOutOfLimit":true,` +
		`"characteristics":[{"name":"weight","type":"numeric","unit":"kg","min":"9.5","max":"10.5","required":true}]}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-categories", body, allDuty)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || !f.categoryIn.RejectOutOfLimit || !f.categoryIn.Active || len(f.categoryIn.Characteristics) != 1 {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body.String(), f.categoryIn)
	}
	for name, b := range map[string]string{
		"no destinations":   `{"code":"a","name":"A","destinations":[]}`,
		"unknown dest":      `{"code":"a","name":"A","destinations":["fax"]}`,
		"repeated dest":     `{"code":"a","name":"A","destinations":["ebr","ebr"]}`,
		"upper case code":   `{"code":"Pi","name":"A","destinations":["ebr"]}`,
		"empty name":        `{"code":"a","name":" ","destinations":["ebr"]}`,
		"limits on text":    `{"code":"a","name":"A","destinations":["ebr"],"characteristics":[{"name":"n","type":"text","min":"1"}]}`,
		"min above max":     `{"code":"a","name":"A","destinations":["ebr"],"characteristics":[{"name":"n","type":"numeric","min":"5","max":"1"}]}`,
		"duplicate name":    `{"code":"a","name":"A","destinations":["ebr"],"characteristics":[{"name":"n","type":"text"},{"name":"n","type":"text"}]}`,
		"unknown char type": `{"code":"a","name":"A","destinations":["ebr"],"characteristics":[{"name":"n","type":"date"}]}`,
		"unknown field":     `{"code":"a","name":"A","destinations":["ebr"],"extra":1}`,
	} {
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-categories", b, allDuty); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	path := "/v1/shopfloor/process-categories/" + categoryID
	if rec := callWith(t, h, http.MethodPut, path, `{"name":"Q","destinations":["quality"]}`, allDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPut, path, `{"name":"Q","destinations":["quality"],"active":false}`, allDuty, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || f.updateVersion != 2 || f.categoryIn.Active || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWith(t, h, http.MethodPut, "/v1/shopfloor/process-categories/x", `{"name":"Q","destinations":["quality"]}`, allDuty, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeCategoryNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestProcessCategory_ReadsAndDuties(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/process-categories?active=true", "", "shopfloor.view"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/process-categories/"+categoryID, "", "shopfloor.view"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-categories", `{"code":"a","name":"A","destinations":["ebr"]}`, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without the setup duty: %d", rec.Code)
	}
}

// A message names a category the master does not know: the store's SF-0164 reaches the caller as 422,
// and nothing is stored.
func TestProcessMessage_UnknownCategoryIsRefused(t *testing.T) {
	f := &processFake{}
	h := newRouter(f)
	body := `{"source":"manual","sourceMessageId":"m-1","categoryCode":"no-such","orderId":"` + orderID + `","occurredAt":"2026-10-21T08:00:00Z","values":[]}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages", body, processDuty)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeCategoryNotFound || len(f.msgs) != 0 {
		t.Fatalf("unknown category: %d %s", rec.Code, rec.Body.String())
	}
}

// A message stored before its category was deactivated is replayed by the edge: the replay still
// answers 200 with the stored message (idempotent by source message id), while a new message of
// the category is refused with SF-0165.
func TestProcessMessage_ReplayAfterCategoryDeactivated(t *testing.T) {
	f := &inactiveCategoryFake{}
	stored := f.store(domain.NewMessage{Source: domain.SourceEdge, SourceMessageID: "e-1", CategoryCode: "pi-weigh", OrderID: orderID}, domain.Category{Destinations: []string{domain.DestEBR}})
	h := newRouter(f)
	body := `{"source":"edge","sourceMessageId":"e-1","categoryCode":"pi-weigh","orderId":"` + orderID + `","occurredAt":"2026-10-21T08:00:00Z","values":[]}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages", body, processDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	var res store.ProcessIngest
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Duplicate || res.Message.ID != stored.ID {
		t.Fatalf("replay = %+v", res)
	}
	fresh := `{"source":"edge","sourceMessageId":"e-2","categoryCode":"pi-weigh","orderId":"` + orderID + `","occurredAt":"2026-10-21T08:00:00Z","values":[]}`
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/process-messages", fresh, processDuty); rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeCategoryInactive {
		t.Fatalf("new message of an inactive category: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.msgs) != 1 {
		t.Fatalf("stored messages = %d, want 1", len(f.msgs))
	}
}

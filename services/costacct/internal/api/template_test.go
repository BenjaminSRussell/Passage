package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/costacct/internal/store"
)

func (f *fakeStore) CreateMeasure(context.Context, string, string, store.StatisticalMeasure) (int, []byte, error) {
	f.calls++
	return http.StatusCreated, []byte(`{"id":"m"}`), nil
}

func (f *fakeStore) LoadMeasureValues(_ context.Context, _, _ string, in store.MeasureValuesRequest) (int, []byte, error) {
	f.calls++
	f.measureValues = in
	return http.StatusOK, []byte(`{"loaded":1}`), nil
}

func (f *fakeStore) ListMeasures(_ context.Context, le string) ([]store.StatisticalMeasure, error) {
	f.calls++
	f.filter = le
	return nil, nil
}

func (f *fakeStore) MeasureValues(_ context.Context, id, period string) ([]store.MeasureValue, error) {
	f.calls++
	f.filter = id + "|" + period
	return nil, nil
}

func (f *fakeStore) SaveTemplate(_ context.Context, _, _ string, in store.AllocationTemplate, expected int) (int, []byte, error) {
	f.calls++
	f.template, f.expected = in, expected
	return http.StatusCreated, []byte(`{"id":"t"}`), nil
}

func (f *fakeStore) ListTemplates(context.Context, string) ([]store.AllocationTemplate, error) {
	f.calls++
	return nil, nil
}

func (f *fakeStore) GetTemplate(context.Context, string) (store.AllocationTemplate, error) {
	f.calls++
	return store.AllocationTemplate{ID: objA}, nil
}

func (f *fakeStore) DryRunTemplate(_ context.Context, in store.TemplateRunRequest) (store.DryRunResult, error) {
	f.calls++
	f.templateRun = in
	return store.DryRunResult{TemplateID: in.TemplateID, Period: in.Period}, nil
}

func (f *fakeStore) RunTemplate(_ context.Context, _, _ string, in store.TemplateRunRequest) (int, []byte, error) {
	f.calls++
	f.templateRun = in
	return http.StatusCreated, []byte(`{"id":"tr"}`), nil
}

func (f *fakeStore) ListTemplateRuns(_ context.Context, le, tpl string) ([]store.TemplateRun, error) {
	f.calls++
	f.filter = le + "|" + tpl
	return nil, nil
}

func (f *fakeStore) GetTemplateRun(context.Context, string) (store.TemplateRun, error) {
	f.calls++
	return store.TemplateRun{ID: objA}, nil
}

const validTemplate = `{"legalEntityId":"` + entityID + `","code":"TPL-INSP","name":"Inspection","currency":"USD",` +
	`"senderCostObjectId":"` + objA + `","lines":[{"receiverCostObjectId":"` + objB + `","secondaryElementId":"` + elemID +
	`","formula":"25.00 × m.inspectionLots + 0.005 × b.MATERIAL"}]}`

// FR-38.17: a formula is parsed at save, so a broken one never reaches the store;
// the error is CO-0621 naming the template and line.
func TestTemplate_FormulaValidatedAtSave(t *testing.T) {
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/allocation-templates", validTemplate, DutyMaintain, "k"); rec.Code != http.StatusCreated {
		t.Fatalf("good template: status %d (%s)", rec.Code, rec.Body.String())
	}
	if st.expected != 0 || st.template.Code != "TPL-INSP" || len(st.template.Lines) != 1 {
		t.Errorf("store got %+v (expected version %d)", st.template, st.expected)
	}
	bad := strings.Replace(validTemplate, "25.00 × m.inspectionLots", "25.00 × (m.inspectionLots", 1)
	st = &fakeStore{}
	rec := serve(t, st, http.MethodPost, "/v1/controlling/allocation-templates", bad, DutyMaintain, "k2")
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != "CO-0621" || st.calls != 0 {
		t.Fatalf("bad formula: status %d code %q calls %d", rec.Code, problemCode(t, rec), st.calls)
	}
	if !strings.Contains(rec.Body.String(), "TPL-INSP line 1") {
		t.Errorf("detail does not name template and line: %s", rec.Body.String())
	}
}

func TestTemplate_ShapeValidation(t *testing.T) {
	cases := map[string]string{
		"receiver is sender": strings.Replace(validTemplate, `"receiverCostObjectId":"`+objB, `"receiverCostObjectId":"`+objA, 1),
		"lower-case code":    strings.Replace(validTemplate, "TPL-INSP", "tpl-insp", 1),
		"empty formula":      strings.Replace(validTemplate, "25.00 × m.inspectionLots + 0.005 × b.MATERIAL", " ", 1),
		"no lines":           `{"legalEntityId":"` + entityID + `","code":"TPL","name":"x","currency":"USD","senderCostObjectId":"` + objA + `","lines":[]}`,
		"bad currency":       strings.Replace(validTemplate, `"USD"`, `"usd"`, 1),
		"negative version":   strings.Replace(validTemplate, `"lines"`, `"expectedVersion":-1,"lines"`, 1),
		"unknown field":      strings.Replace(validTemplate, `"lines"`, `"version":3,"lines"`, 1),
	}
	for name, body := range cases {
		st := &fakeStore{}
		rec := serve(t, st, http.MethodPost, "/v1/controlling/allocation-templates", body, DutyMaintain, "k")
		if rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d (%s)", name, rec.Code, st.calls, rec.Body.String())
		}
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/allocation-templates", validTemplate, DutyRun, "k"); rec.Code != http.StatusForbidden {
		t.Errorf("save with controlling.run only: status %d", rec.Code)
	}
}

func TestMeasure_Validation(t *testing.T) {
	path := "/v1/controlling/statistical-measures"
	good := `{"legalEntityId":"` + entityID + `","code":"inspectionLots","name":"Inspection lots","unit":"lot"}`
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, good, DutyMaintain, "k"); rec.Code != http.StatusCreated {
		t.Fatalf("good measure: status %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, path, strings.Replace(good, "inspectionLots", "2lots", 1), DutyMaintain, "k2"); rec.Code != http.StatusBadRequest {
		t.Errorf("code starting with a digit: status %d", rec.Code)
	}
	vals := "/v1/controlling/statistical-measures/" + objA + "/values"
	rejected := map[string]string{
		"duplicate objects": `{"legalEntityId":"` + entityID + `","period":"2026-09","values":[{"costObjectId":"` + objB + `","value":"4"},{"costObjectId":"` + objB + `","value":"5"}]}`,
		"negative value":    `{"legalEntityId":"` + entityID + `","period":"2026-09","values":[{"costObjectId":"` + objB + `","value":"-4"}]}`,
		"five decimals":     `{"legalEntityId":"` + entityID + `","period":"2026-09","values":[{"costObjectId":"` + objB + `","value":"1.00001"}]}`,
		"bad period":        `{"legalEntityId":"` + entityID + `","period":"2026-13","values":[{"costObjectId":"` + objB + `","value":"4"}]}`,
		"no values":         `{"legalEntityId":"` + entityID + `","period":"2026-09","values":[]}`,
	}
	for name, body := range rejected {
		st := &fakeStore{}
		if rec := serve(t, st, http.MethodPost, vals, body, DutyMaintain, "k3"); rec.Code != http.StatusBadRequest || st.calls != 0 {
			t.Errorf("%s: status %d calls %d", name, rec.Code, st.calls)
		}
	}
	st := &fakeStore{}
	ok := `{"legalEntityId":"` + entityID + `","period":"2026-09","values":[{"costObjectId":"` + objB + `","value":"4"}]}`
	if rec := serve(t, st, http.MethodPost, vals, ok, DutyMaintain, "k4"); rec.Code != http.StatusOK || st.measureValues.MeasureID != objA ||
		len(st.measureValues.Values) != 1 || st.measureValues.Values[0].Value.String() != "4" {
		t.Errorf("load values: status %d store %+v", rec.Code, st.measureValues)
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, vals, "", DutyView, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("values without period: status %d", rec.Code)
	}
	st = &fakeStore{}
	if rec := serve(t, st, http.MethodGet, vals+"?period=2026-09", "", DutyView, ""); rec.Code != http.StatusOK || st.filter != objA+"|2026-09" {
		t.Errorf("values read: status %d filter %q", rec.Code, st.filter)
	}
}

func TestTemplateRun_DutiesAndDryRun(t *testing.T) {
	body := `{"templateId":"` + objA + `","period":"2026-09"}`
	st := &fakeStore{}
	if rec := serve(t, st, http.MethodPost, "/v1/controlling/template-allocations/runs", body, DutyRun, "k"); rec.Code != http.StatusCreated || st.templateRun.TemplateID != objA {
		t.Errorf("run: status %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/template-allocations/runs", body, DutyMaintain, "k2"); rec.Code != http.StatusForbidden {
		t.Errorf("run without controlling.run: status %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, "/v1/controlling/template-allocations/runs", body, DutyRun, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("run without Idempotency-Key: status %d", rec.Code)
	}
	dry := "/v1/controlling/allocation-templates/" + objA + ":dry-run"
	st = &fakeStore{}
	if rec := serve(t, st, http.MethodPost, dry, `{"period":"2026-09"}`, DutyMaintain, ""); rec.Code != http.StatusOK || st.templateRun.Period != "2026-09" {
		t.Errorf("dry run without key: status %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := serve(t, &fakeStore{}, http.MethodPost, dry, `{"period":"2026-13"}`, DutyMaintain, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("dry run bad period: status %d", rec.Code)
	}
	st = &fakeStore{}
	if rec := serve(t, st, http.MethodGet, "/v1/controlling/template-allocations/runs?legalEntityId="+entityID+"&templateId="+objA, "", DutyView, ""); rec.Code != http.StatusOK ||
		st.filter != entityID+"|"+objA || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("list runs: status %d filter %q body %s", rec.Code, st.filter, rec.Body.String())
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, "/v1/controlling/template-allocations/runs/nope", "", DutyView, ""); rec.Code != http.StatusNotFound ||
		problemCode(t, rec) != store.CodeTemplateRunNotFound {
		t.Errorf("malformed run id: status %d", rec.Code)
	}
}

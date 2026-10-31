package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	templatesPath = "/v1/integration/data-templates"
	templateDoc   = `{"name":"Retail defaults","entity":"customer","version":1,"defaults":{"segment":"retail"}}`
)

type templateBody struct {
	ID       string            `json:"id"`
	Entity   string            `json:"entity"`
	Name     string            `json:"name"`
	Version  int               `json:"version"`
	Checksum string            `json:"checksum"`
	Count    int               `json:"defaultCount"`
	Defaults map[string]string `json:"defaults"`
}

func publishTemplate(t *testing.T, r http.Handler, doc string, headers ...string) (*httptest.ResponseRecorder, templateBody) {
	t.Helper()
	rec := do(r, http.MethodPost, templatesPath, doc, writeDuty, true, headers...)
	var out templateBody
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestPublishTemplate_VersionsIdempotencyAndDuty(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	rec, first := publishTemplate(t, r, templateDoc)
	if rec.Code != http.StatusCreated || first.Count != 1 || len(first.Checksum) != 64 || rec.Header().Get("Location") != templatesPath+"/"+first.ID {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	reordered := `{"defaults":{"segment":"retail"},"version":1,"entity":"customer","name":" Retail defaults "}`
	if rec, again := publishTemplate(t, r, reordered); rec.Code != http.StatusOK || again.ID != first.ID {
		t.Fatalf("same template must replay: %d %s", rec.Code, rec.Body)
	}
	changed := strings.ReplaceAll(templateDoc, `"retail"`, `"wholesale"`)
	if rec, _ := publishTemplate(t, r, changed); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeTemplateVersionUsed) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec, _ := publishTemplate(t, r, strings.Replace(changed, `"version":1`, `"version":2`, 1)); rec.Code != http.StatusCreated {
		t.Fatalf("higher version must publish: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, templatesPath, templateDoc, duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("run duty must not publish: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, templatesPath, templateDoc, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", rec.Code)
	}
	if rec, _ := publishTemplate(t, r, changed, "Idempotency-Key", "k1"); rec.Code != http.StatusConflict {
		t.Fatalf("got %d", rec.Code)
	}
}

func TestPublishTemplate_Rejections(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	for name, tc := range map[string]struct {
		doc  string
		code int
		want string
	}{
		"malformed":     {`{`, http.StatusBadRequest, "COM-0001"},
		"unknown field": {`{"name":"a","entity":"e","version":1,"defaults":{"x":"y"},"extra":1}`, http.StatusBadRequest, "COM-0001"},
		"no name":       {`{"entity":"e","version":1,"defaults":{"x":"y"}}`, http.StatusUnprocessableEntity, domain.CodeTemplateInvalid},
		"no defaults":   {`{"name":"a","entity":"e","version":1,"defaults":{}}`, http.StatusUnprocessableEntity, domain.CodeTemplateInvalid},
		"version zero":  {`{"name":"a","entity":"e","version":0,"defaults":{"x":"y"}}`, http.StatusUnprocessableEntity, domain.CodeTemplateInvalid},
		"empty target":  {`{"name":"a","entity":"e","version":1,"defaults":{"":"y"}}`, http.StatusUnprocessableEntity, domain.CodeTemplateInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(r, http.MethodPost, templatesPath, tc.doc, writeDuty, true)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestTemplates_ListAndGet(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	_, first := publishTemplate(t, r, templateDoc)
	publishTemplate(t, r, `{"name":"Vendor defaults","entity":"vendor","version":1,"defaults":{"x":"y"}}`)
	rec := do(r, http.MethodGet, templatesPath+"?entity=customer", "", duties, true)
	var list struct{ Items []templateBody }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != http.StatusOK || len(list.Items) != 1 || list.Items[0].ID != first.ID || list.Items[0].Defaults != nil {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, templatesPath+"/"+first.ID, "", duties, true)
	var one templateBody
	_ = json.Unmarshal(rec.Body.Bytes(), &one)
	if rec.Code != http.StatusOK || one.Defaults["segment"] != "retail" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, templatesPath+"/00000000-0000-7000-8000-000000000999", "", duties, true); rec.Code != http.StatusNotFound {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, templatesPath+"?limit=0", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, templatesPath, "", "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}

func startRun(t *testing.T, r http.Handler, extra string) (*httptest.ResponseRecorder, runBody) {
	t.Helper()
	rec := do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mapping":`+mapping+extra+`}`, duties, true)
	return rec, decode(t, rec)
}

func TestRun_AppliesTemplateRecordsLineageAndRerunKeepsIt(t *testing.T) {
	rows := []domain.Record{
		{Key: "C1", Fields: map[string]any{"Name": "Acme"}},
		{Key: "C2", Fields: map[string]any{"Name": "Beta", "Group": "10"}},
		{Key: "C3", Fields: map[string]any{"Name": ""}},
	}
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{rows}, loader)
	_, tmpl := publishTemplate(t, r, templateDoc)
	rec, run := startRun(t, r, `,"dataTemplateId":"`+tmpl.ID+`"`)
	if rec.Code != http.StatusAccepted || run.Run.Counts["templated"] != 1 || run.Run.Counts["created"] != 2 || run.Run.Counts["rejected"] != 1 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"dataTemplateId":"`+tmpl.ID+`"`) {
		t.Fatalf("run must name its template: %s", rec.Body)
	}
	seg := map[string]any{}
	for _, it := range loader.Rows {
		seg[it.SourceKey] = it.Fields["segment"]
	}
	if seg["C1"] != "retail" || seg["C2"] != "retail" {
		t.Fatalf("loaded segments = %v", seg)
	}

	rec = do(r, http.MethodGet, runsPath+"/"+run.Run.ID+"/ledger?templated=true", "", duties, true)
	var ledger struct {
		Items []struct {
			SourceKey string   `json:"sourceKey"`
			Fields    []string `json:"defaultFromTemplate"`
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ledger)
	if rec.Code != http.StatusOK || len(ledger.Items) != 1 || ledger.Items[0].SourceKey != "C1" || ledger.Items[0].Fields[0] != "segment" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	rec = do(r, http.MethodGet, runsPath+"/"+run.Run.ID+"/ledger", "", duties, true)
	_ = json.Unmarshal(rec.Body.Bytes(), &ledger)
	if len(ledger.Items) != 2 {
		t.Fatalf("all ledger rows: %s", rec.Body)
	}

	rec = do(r, http.MethodPost, runsPath+"/"+run.Run.ID+":rerun", "", duties, true)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"dataTemplateId":"`+tmpl.ID+`"`) {
		t.Fatalf("rerun must keep the template: %d %s", rec.Code, rec.Body)
	}
}

func TestRun_TemplateRejections(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	_, wrongEntity := publishTemplate(t, r, `{"name":"v","entity":"vendor","version":1,"defaults":{"segment":"x"}}`)
	_, unknownTarget := publishTemplate(t, r, `{"name":"u","entity":"customer","version":1,"defaults":{"nope":"x"}}`)
	for name, tc := range map[string]struct {
		extra string
		code  int
		want  string
	}{
		"not a uuid":     {`,"dataTemplateId":"x"`, http.StatusBadRequest, `"dataTemplateId"`},
		"unknown":        {`,"dataTemplateId":"00000000-0000-7000-8000-000000000999"`, http.StatusUnprocessableEntity, api.CodeTemplateUnknown},
		"wrong entity":   {`,"dataTemplateId":"` + wrongEntity.ID + `"`, http.StatusUnprocessableEntity, domain.CodeTemplateInvalid},
		"unknown target": {`,"dataTemplateId":"` + unknownTarget.ID + `"`, http.StatusUnprocessableEntity, domain.CodeTemplateInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := startRun(t, r, tc.extra)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRunLedger_Rejections(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	for path, want := range map[string]int{
		runsPath + "/00000000-0000-7000-8000-000000000999/ledger":                 http.StatusNotFound,
		runsPath + "/00000000-0000-7000-8000-000000000999/ledger?templated=maybe": http.StatusBadRequest,
	} {
		if rec := do(r, http.MethodGet, path, "", duties, true); rec.Code != want {
			t.Fatalf("%s: got %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodGet, runsPath+"/x/ledger", "", "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("got %d", rec.Code)
	}
}

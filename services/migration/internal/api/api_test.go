package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/migration/internal/api"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

type rowsExtractor struct{ rows []domain.Record }

func (e rowsExtractor) Extract(context.Context, string) ([]domain.Record, error) { return e.rows, nil }

type failingExtractor struct{}

func (failingExtractor) Extract(context.Context, string) ([]domain.Record, error) {
	return nil, errors.New("sql server password=hunter2 unreachable")
}

const (
	runsPath = "/v1/integration/migration-runs"
	duties   = "migration.run,tenant.administer"
	mapping  = `{"entity":"customer","version":1,"fields":[{"source":"Name","target":"name","type":"string","required":true},{"source":"Group","target":"segment","type":"string","enum":{"10":"retail"}}]}`
	body     = `{"sourceSystem":"ax2012","mode":"rehearsal","mapping":` + mapping + `}`
)

func records() []domain.Record {
	return []domain.Record{
		{Key: "C1", Fields: map[string]any{"Name": "Acme", "Group": "10"}},
		{Key: "C2", Fields: map[string]any{"Name": "Beta", "Group": "10"}},
		{Key: "C3", Fields: map[string]any{"Name": "", "Group": "10"}},
	}
}

func router(s store.Store, ex domain.Extractor, loader *domain.MemLoader) *chi.Mux {
	factory := func(context.Context, string) (api.Backend, error) {
		return api.Backend{Extractor: ex, Loader: loader, Reader: loader}, nil
	}
	r := httpx.NewRouter()
	api.New(s, factory).Register(r)
	return r
}

func do(r http.Handler, method, path, payload, userDuties string, withTenant bool, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", userDuties)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

type runBody struct {
	JobID     string `json:"jobId"`
	StatusURL string `json:"statusUrl"`
	Run       struct {
		ID      string `json:"id"`
		Status  string `json:"status"`
		Counts  map[string]int
		Rejects []struct {
			SourceKey string `json:"sourceKey"`
			Rejects   []struct{ Code string }
		}
		ParentRunID string `json:"parentRunId"`
	}
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) runBody {
	t.Helper()
	var out runBody
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return out
}

func TestCreateRun_Rejections(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"no principal", body, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", body, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"malformed body", `{`, duties, true, http.StatusBadRequest, "COM-0001"},
		{"unknown field", `{"sourceSystem":"ax2012","x":1}`, duties, true, http.StatusBadRequest, "COM-0001"},
		{"bad source system and mode", `{"sourceSystem":"oracle","mode":"yolo","mapping":{}}`, duties, true, http.StatusBadRequest, `"sourceSystem"`},
		{"number for string", `{"sourceSystem":5,"mode":"rehearsal","mapping":{}}`, duties, true, http.StatusBadRequest, "COM-0001"},
		{"mapping missing", `{"sourceSystem":"ax2012","mode":"cutover"}`, duties, true, http.StatusBadRequest, `"mapping"`},
		{"mapping invalid", `{"sourceSystem":"ax2012","mode":"cutover","mapping":{"entity":"x","version":1,"fields":[{"source":"a","target":"b","type":"blob"}]}}`, duties, true, http.StatusUnprocessableEntity, domain.CodeMapInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
			rec := do(r, http.MethodPost, runsPath, tc.payload, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestCreateRun_LegalEntityScopedCallerForbidden(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	rec := do(r, http.MethodPost, runsPath, body, duties, true, "X-Passage-Legal-Entity", testkit.TenantID(7))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCreateRun_WithoutTenantAdministerForbidden(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	rec := do(r, http.MethodPost, runsPath, body, "migration.run", true)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") || !strings.Contains(rec.Body.String(), "tenant.administer") {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestCreateRun_AcceptsAndRecordsRejects(t *testing.T) {
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{records()}, loader)
	rec := do(r, http.MethodPost, runsPath, body, duties, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	got := decode(t, rec)
	if got.Run.Status != domain.StatusPartial || got.Run.Counts["created"] != 2 || got.Run.Counts["rejected"] != 1 || len(loader.Rows) != 2 {
		t.Fatalf("unexpected run %+v, loaded %d", got.Run, len(loader.Rows))
	}
	if len(got.Run.Rejects) != 1 || got.Run.Rejects[0].SourceKey != "C3" || got.Run.Rejects[0].Rejects[0].Code != domain.CodeMapRequired {
		t.Fatalf("reject not recorded with a MIG-MAP code: %+v", got.Run.Rejects)
	}
	if got.StatusURL != runsPath+"/"+got.Run.ID || got.JobID != got.Run.ID || rec.Header().Get("Location") != got.StatusURL {
		t.Fatalf("status url %q job %q", got.StatusURL, got.JobID)
	}
	if strings.Contains(rec.Body.String(), "Beta") {
		t.Fatalf("row values must not appear in the run body: %s", rec.Body)
	}
}

func TestCreateRun_IdempotencyKeyReplayAndReuse(t *testing.T) {
	loader := domain.NewMemLoader()
	s := store.NewMem()
	r := router(s, rowsExtractor{records()}, loader)
	first := do(r, http.MethodPost, runsPath, body, duties, true, "Idempotency-Key", "k1")
	replay := do(r, http.MethodPost, runsPath, body, duties, true, "Idempotency-Key", "k1")
	if decode(t, first).Run.ID != decode(t, replay).Run.ID || replay.Code != http.StatusAccepted {
		t.Fatalf("replay must return the original run: %s / %s", first.Body, replay.Body)
	}
	other := strings.Replace(body, "rehearsal", "cutover", 1)
	reuse := do(r, http.MethodPost, runsPath, other, duties, true, "Idempotency-Key", "k1")
	if reuse.Code != http.StatusUnprocessableEntity || !strings.Contains(reuse.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("got %d %s", reuse.Code, reuse.Body)
	}
}

func TestCreateRun_SameLoadTwiceChangesNothing(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()[:2]}, domain.NewMemLoader())
	do(r, http.MethodPost, runsPath, body, duties, true)
	second := decode(t, do(r, http.MethodPost, runsPath, body, duties, true))
	if second.Run.Counts["created"] != 0 || second.Run.Counts["updated"] != 0 || second.Run.Counts["unchanged"] != 2 {
		t.Fatalf("second identical load must change nothing: %+v", second.Run.Counts)
	}
}

func TestCreateRun_BackendFailures(t *testing.T) {
	t.Run("not configured is 501", func(t *testing.T) {
		r := httpx.NewRouter()
		api.New(store.NewMem(), api.NotConfigured).Register(r)
		rec := do(r, http.MethodPost, runsPath, body, duties, true)
		if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), api.CodeNotConfigured) {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("extractor error hides detail", func(t *testing.T) {
		r := router(store.NewMem(), failingExtractor{}, domain.NewMemLoader())
		rec := do(r, http.MethodPost, runsPath, body, duties, true)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hunter2") {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("store error is 500", func(t *testing.T) {
		s := store.NewMem()
		s.SaveErr = errors.New("disk full")
		r := router(s, rowsExtractor{records()}, domain.NewMemLoader())
		rec := do(r, http.MethodPost, runsPath, body, duties, true)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") {
			t.Fatalf("got %d %s", rec.Code, rec.Body)
		}
	})
}

func TestGetRun(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	created := decode(t, do(r, http.MethodPost, runsPath, body, duties, true))
	tests := []struct {
		name       string
		path       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"found", runsPath + "/" + created.Run.ID, duties, true, http.StatusOK, `"counts"`},
		{"unknown id", runsPath + "/00000000-0000-7000-8000-000000000099", duties, true, http.StatusNotFound, "AUTH-0404"},
		{"malformed id", runsPath + "/nope", duties, true, http.StatusNotFound, "AUTH-0404"},
		{"no principal", runsPath + "/" + created.Run.ID, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"no duty", runsPath + "/" + created.Run.ID, "org.read", true, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(r, http.MethodGet, tc.path, "", tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestRerun(t *testing.T) {
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{records()}, loader)
	created := decode(t, do(r, http.MethodPost, runsPath, body, duties, true))

	rec := do(r, http.MethodPost, runsPath+"/"+created.Run.ID+":rerun", "", duties, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	again := decode(t, rec)
	if again.Run.ParentRunID != created.Run.ID || again.Run.Counts["created"] != 0 || again.Run.Counts["updated"] != 0 || again.Run.Counts["rejected"] != 1 {
		t.Fatalf("rerun reprocesses only the exception queue: %+v", again.Run)
	}
	if len(loader.Rows) != 2 {
		t.Fatalf("rerun duplicated target rows: %d", len(loader.Rows))
	}

	missing := do(r, http.MethodPost, runsPath+"/00000000-0000-7000-8000-000000000099:rerun", "", duties, true)
	if missing.Code != http.StatusNotFound || !strings.Contains(missing.Body.String(), "AUTH-0404") {
		t.Fatalf("got %d %s", missing.Code, missing.Body)
	}
	noDuty := do(r, http.MethodPost, runsPath+"/"+created.Run.ID+":rerun", "", "org.read", true)
	if noDuty.Code != http.StatusForbidden {
		t.Fatalf("got %d", noDuty.Code)
	}
}

func TestParallelRunDiffsDeferred(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	rec := do(r, http.MethodPost, "/v1/integration/parallel-run-diffs", "{}", duties, true)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), api.CodeDeferred) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestProbes(t *testing.T) {
	down := store.NewMem()
	down.PingErr = errors.New("db down")
	tests := []struct {
		name       string
		store      *store.Mem
		path       string
		wantStatus int
	}{
		{"readyz ok", store.NewMem(), "/readyz", http.StatusOK},
		{"readyz fails closed when database is down", down, "/readyz", http.StatusServiceUnavailable},
		{"healthz stays up when database is down", down, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(router(tc.store, rowsExtractor{}, domain.NewMemLoader()), http.MethodGet, tc.path, "", "", false)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
				t.Fatalf("unexpected body %s", rec.Body)
			}
		})
	}
}

func TestMetrics_CountRunsRowsAndRejects(t *testing.T) {
	s := store.NewMem()
	factory := func(context.Context, string) (api.Backend, error) {
		l := domain.NewMemLoader()
		return api.Backend{Extractor: rowsExtractor{records()}, Loader: l, Reader: l}, nil
	}
	a := api.New(s, factory)
	r := httpx.NewRouter()
	a.Register(r)
	do(r, http.MethodPost, runsPath, body, duties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"migration_runs_created_total 1", "migration_rows_loaded_total 2", "migration_rows_rejected_total 1", "migration_runs_failed_total 0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, rec.Body)
		}
	}
}

func TestCreateRun_DryRunRecordsLedgerWithoutLoading(t *testing.T) {
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{records()}, loader)
	dryBody := strings.Replace(body, `"mode":"rehearsal",`, `"mode":"rehearsal","dryRun":true,`, 1)
	rec := do(r, http.MethodPost, runsPath, dryBody, duties, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	got := decode(t, rec)
	if got.Run.Counts["created"] != 2 || len(loader.Rows) != 0 {
		t.Fatalf("dry run created=%d loaded=%d", got.Run.Counts["created"], len(loader.Rows))
	}
	if again := do(r, http.MethodGet, got.StatusURL, "", duties, true); again.Code != http.StatusOK {
		t.Fatalf("dry run must be retrievable, got %d %s", again.Code, again.Body)
	}
}

func TestCreateRun_FailingMappingTestsStopBeforeBackend(t *testing.T) {
	invoked := false
	factory := func(context.Context, string) (api.Backend, error) {
		invoked = true
		return api.Backend{}, api.ErrNotConfigured
	}
	r := httpx.NewRouter()
	api.New(store.NewMem(), factory).Register(r)
	mapping := func(expect string) string {
		return `{"entity":"customer","version":1,"fields":[{"source":"Group","target":"segment","type":"string","enum":{"10":"retail"}}],` +
			`"tests":[{"name":"retail maps","input":{"Group":"10"},"expect":{"segment":"` + expect + `"}}]}`
	}
	rec := do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mapping":`+mapping("wholesale")+`}`, duties, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeMapTestFail) || !strings.Contains(rec.Body.String(), "retail maps") || invoked {
		t.Fatalf("got %d %s, backend invoked %v", rec.Code, rec.Body, invoked)
	}
	rec = do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mapping":`+mapping("retail")+`}`, duties, true)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), api.CodeNotConfigured) || !invoked {
		t.Fatalf("passing tests must proceed: got %d %s", rec.Code, rec.Body)
	}
}

func TestCreateRun_ReportsWarnedRows(t *testing.T) {
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{[]domain.Record{{Key: "C1", Fields: map[string]any{"Name": "Acme", "Group": "77"}}}}, loader)
	mapping := `{"entity":"customer","version":1,"fields":[{"source":"Group","target":"segment","type":"string","enum":{"10":"retail"},"enumDefault":"other"}]}`
	rec := do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mapping":`+mapping+`}`, duties, true)
	if rec.Code != http.StatusAccepted || len(loader.Rows) != 1 {
		t.Fatalf("got %d %s, loaded %d", rec.Code, rec.Body, len(loader.Rows))
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"warned":1`) || !strings.Contains(body, domain.CodeMapEnumDefault) || !strings.Contains(body, `"sourceKey":"C1"`) {
		t.Fatalf("warnings missing from run view: %s", body)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestCreateRun_ClientDisconnectStillRecordsRunAndLogsLifecycle(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	s := store.NewMem()
	loader := domain.NewMemLoader()
	r := router(s, rowsExtractor{records()}, loader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, runsPath, strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	got := decode(t, rec)
	if rec.Code != http.StatusAccepted || got.Run.Counts["created"] != 2 || len(loader.Rows) != 2 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if _, err := s.GetRun(context.Background(), got.Run.ID); err != nil {
		t.Fatalf("run not recorded after client disconnect: %v", err)
	}
	for _, want := range []string{"migration run started", "migration run finished", `"runId":"` + got.Run.ID + `"`, `"status":"partial"`, `"created":2`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

const (
	specsPath = "/v1/integration/mapping-specs"
	writeDuty = "migration.spec.write,tenant.administer"
	// specMapping is the shared mapping plus the one test case a published spec must carry.
	specMapping = `{"entity":"customer","version":1,"fields":[{"source":"Name","target":"name","type":"string","required":true},{"source":"Group","target":"segment","type":"string","enum":{"10":"retail"}}],"tests":[{"name":"retail","input":{"Name":"Acme","Group":"10"},"expect":{"name":"Acme","segment":"retail"}}]}`
)

type specBody struct {
	ID       string `json:"id"`
	Entity   string `json:"entity"`
	Version  int    `json:"version"`
	Checksum string `json:"checksum"`
	Fields   int    `json:"fieldCount"`
	Mapping  map[string]any
}

func publish(t *testing.T, r http.Handler, m string, headers ...string) (*httptest.ResponseRecorder, specBody) {
	t.Helper()
	rec := do(r, http.MethodPost, specsPath, `{"mapping":`+m+`}`, writeDuty, true, headers...)
	var out specBody
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestPublishSpec_VersionsChecksumAndDuty(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	rec, first := publish(t, r, specMapping)
	if rec.Code != http.StatusCreated || first.Version != 1 || first.Entity != "customer" || first.Fields != 2 || len(first.Checksum) != 64 || rec.Header().Get("Location") != specsPath+"/"+first.ID {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	reordered := `{"tests":[{"input":{"Group":"10","Name":"Acme"},"name":"retail","expect":{"segment":"retail","name":"Acme"}}],
		"fields":[{"source":"Name","target":"name","type":"string","required":true},{"source":"Group","target":"segment","type":"string","enum":{"10":"retail"}}],"version":1,"entity":"customer"}`
	rec, again := publish(t, r, reordered)
	if rec.Code != http.StatusOK || again.ID != first.ID || again.Checksum != first.Checksum {
		t.Fatalf("key order must not change the checksum: %d %s", rec.Code, rec.Body)
	}
	changed := strings.ReplaceAll(specMapping, `"retail"`, `"wholesale"`)
	if rec, _ := publish(t, r, changed); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeSpecVersionUsed) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec, _ := publish(t, r, strings.Replace(changed, `"version":1`, `"version":2`, 1)); rec.Code != http.StatusCreated {
		t.Fatalf("a higher version must publish: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, specsPath, `{"mapping":`+specMapping+`}`, duties, true); rec.Code != http.StatusForbidden {
		t.Fatalf("run duty must not publish: %d", rec.Code)
	}
	if rec := do(r, http.MethodPost, specsPath, `{"mapping":`+specMapping+`}`, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no principal: %d", rec.Code)
	}
}

func TestPublishSpec_Rejections(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	failing := `{"entity":"customer","version":1,"fields":[{"source":"Name","target":"name","type":"string","required":true}],"tests":[{"name":"uppercase","input":{"Name":"a"},"expect":{"name":"A"}}]}`
	unknownOp := `{"entity":"customer","version":1,"fields":[{"source":"Name","target":"name","type":"string"}],"rejectWhen":[{"field":"name","op":"like","value":"x","code":"CUST-X"}],"tests":[{"name":"ok","input":{"Name":"a"},"expect":{"name":"a"}}]}`
	rejectedByCondition := `{"entity":"customer","version":1,"fields":[{"source":"Name","target":"name","type":"string"}],"rejectWhen":[{"field":"name","op":"match","value":"^TMP","code":"CUST-TEMP"}],"tests":[{"name":"temp loads","input":{"Name":"TMP-1"},"expect":{"name":"TMP-1"}}]}`
	badExpr := `{"entity":"customer","version":1,"fields":[{"rule":"expr","expr":"Qty *","target":"n","type":"decimal"}],"tests":[{"name":"ok","input":{"Qty":"1"},"expect":{"n":"1"}}]}`
	exprFails := `{"entity":"customer","version":1,"fields":[{"rule":"expr","expr":"Qty * 2","target":"n","type":"decimal"}],"tests":[{"name":"doubles","input":{"Qty":"2"},"expect":{"n":"5"}}]}`
	for name, tc := range map[string]struct {
		payload string
		status  int
		want    string
	}{
		"expression syntax":     {`{"mapping":` + badExpr + `}`, http.StatusUnprocessableEntity, "ends early"},
		"expression fails test": {`{"mapping":` + exprFails + `}`, http.StatusUnprocessableEntity, "doubles"},
		"missing":               {`{}`, http.StatusBadRequest, `"mapping"`},
		"malformed":             {`{`, http.StatusBadRequest, "COM-0001"},
		"invalid":               {`{"mapping":{"entity":"x","version":0,"fields":[]}}`, http.StatusUnprocessableEntity, domain.CodeMapInvalid},
		"failing test":          {`{"mapping":` + failing + `}`, http.StatusUnprocessableEntity, domain.CodeMapTestFail},
		"no tests":              {`{"mapping":` + mapping + `}`, http.StatusUnprocessableEntity, "at least one test case"},
		"unknown rejectWhen op": {`{"mapping":` + unknownOp + `}`, http.StatusUnprocessableEntity, domain.CodeMapUnknownFn},
		"rejectWhen fails test": {`{"mapping":` + rejectedByCondition + `}`, http.StatusUnprocessableEntity, "temp loads"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := do(r, http.MethodPost, specsPath, tc.payload, writeDuty, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
		})
	}
	s := store.NewMem()
	s.SaveErr = errors.New("disk full")
	r2 := router(s, rowsExtractor{}, domain.NewMemLoader())
	if rec, _ := publish(t, r2, specMapping); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store error: %d", rec.Code)
	}
}

func TestPublishSpec_ExpressionRuleStoresAndEchoesTheDocument(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{}, domain.NewMemLoader())
	doc := `{"entity":"customer","version":1,"fields":[{"source":"Name","target":"name","type":"string"},{"rule":"expr","expr":"concat(upper(Name), '-', len(Name))","target":"label","type":"string"}],"rejectWhen":[{"field":"label","op":"expr","value":"len(label) > 20","code":"CUST-LABEL-LONG"}],"tests":[{"name":"label","input":{"Name":"acme"},"expect":{"name":"acme","label":"ACME-4"}},{"name":"long","input":{"Name":"a very long customer name"},"expectRejects":["CUST-LABEL-LONG"]}]}`
	rec, spec := publish(t, r, doc)
	if rec.Code != http.StatusCreated || spec.Fields != 2 {
		t.Fatalf("publish: %d %s", rec.Code, rec.Body)
	}
	get := do(r, http.MethodGet, specsPath+"/"+spec.ID, "", duties, true)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `concat(upper(Name)`) || !strings.Contains(get.Body.String(), `"op":"expr"`) {
		t.Fatalf("the stored mapping must keep both expressions: %d %s", get.Code, get.Body)
	}
}

func TestSpecs_ListGetAndRunFromSpec(t *testing.T) {
	loader := domain.NewMemLoader()
	r := router(store.NewMem(), rowsExtractor{records()}, loader)
	_, spec := publish(t, r, specMapping)

	list := do(r, http.MethodGet, specsPath+"?entity=customer&limit=5", "", duties, true)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), spec.ID) || strings.Contains(list.Body.String(), `"mapping"`) {
		t.Fatalf("list must hold the spec without its body: %d %s", list.Code, list.Body)
	}
	if empty := do(r, http.MethodGet, specsPath+"?entity=vendor", "", duties, true); !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must be an array: %s", empty.Body)
	}
	if bad := do(r, http.MethodGet, specsPath+"?limit=0", "", duties, true); bad.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", bad.Code)
	}
	if get := do(r, http.MethodGet, specsPath+"/"+spec.ID, "", duties, true); get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"mapping"`) {
		t.Fatalf("get: %d %s", get.Code, get.Body)
	}
	if miss := do(r, http.MethodGet, specsPath+"/"+testkit.TenantID(5), "", duties, true); miss.Code != http.StatusNotFound {
		t.Fatalf("missing spec: %d", miss.Code)
	}

	rec := do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mappingSpecId":"`+spec.ID+`"}`, duties, true)
	got := decode(t, rec)
	if rec.Code != http.StatusAccepted || got.Run.Counts["created"] != 2 || !strings.Contains(rec.Body.String(), `"mappingSpecId":"`+spec.ID+`"`) {
		t.Fatalf("run from spec: %d %s", rec.Code, rec.Body)
	}
	rerun := do(r, http.MethodPost, runsPath+"/"+got.Run.ID+":rerun", "", duties, true)
	if rerun.Code != http.StatusAccepted || !strings.Contains(rerun.Body.String(), spec.ID) {
		t.Fatalf("rerun must keep the spec link: %d %s", rerun.Code, rerun.Body)
	}
	for name, payload := range map[string]string{
		"both":     `{"sourceSystem":"ax2012","mode":"rehearsal","mapping":` + mapping + `,"mappingSpecId":"` + spec.ID + `"}`,
		"not uuid": `{"sourceSystem":"ax2012","mode":"rehearsal","mappingSpecId":"x"}`,
	} {
		if rec := do(r, http.MethodPost, runsPath, payload, duties, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	unknown := do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mappingSpecId":"`+testkit.TenantID(5)+`"}`, duties, true)
	if unknown.Code != http.StatusUnprocessableEntity || !strings.Contains(unknown.Body.String(), api.CodeSpecUnknown) || !strings.Contains(unknown.Body.String(), `"mappingSpecId"`) {
		t.Fatalf("unknown spec: %d %s", unknown.Code, unknown.Body)
	}

	// One Idempotency-Key cannot replay a run for a different mapping spec version.
	_, v2 := publish(t, r, strings.Replace(specMapping, `"version":1`, `"version":2`, 1))
	keyed := func(id string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, runsPath, `{"sourceSystem":"ax2012","mode":"rehearsal","mappingSpecId":"`+id+`"}`, duties, true, "Idempotency-Key", "spec-run-1")
	}
	if rec := keyed(spec.ID); rec.Code != http.StatusAccepted {
		t.Fatalf("keyed run: %d %s", rec.Code, rec.Body)
	}
	if rec := keyed(v2.ID); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "idempotency_key_reuse") {
		t.Fatalf("key reused with another spec must be rejected: %d %s", rec.Code, rec.Body)
	}
}

func TestListRuns_FiltersAndValidation(t *testing.T) {
	r := router(store.NewMem(), rowsExtractor{records()}, domain.NewMemLoader())
	if rec := do(r, http.MethodGet, runsPath, "", duties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	created := decode(t, do(r, http.MethodPost, runsPath, body, duties, true))
	rec := do(r, http.MethodGet, runsPath+"?status=partial&entity=customer", "", duties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), created.Run.ID) {
		t.Fatalf("filtered: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, runsPath+"?status=failed", "", duties, true); strings.Contains(rec.Body.String(), created.Run.ID) {
		t.Fatalf("status filter leaked: %s", rec.Body)
	}
	if rec := do(r, http.MethodGet, runsPath+"?status=bogus", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, runsPath+"?limit=abc", "", duties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, runsPath, "", "org.read", true); rec.Code != http.StatusForbidden {
		t.Fatalf("duty: %d", rec.Code)
	}
}

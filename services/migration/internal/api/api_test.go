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
	duties   = "migration.run"
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

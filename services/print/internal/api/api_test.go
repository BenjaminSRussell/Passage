package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/api"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
)

// memStore is an in-memory store.Store for handler tests (no database needed).
type memStore struct {
	mu       sync.Mutex
	printers []domain.Printer
	rules    []domain.RoutingRule
	jobs     map[string]domain.Job
	keys     map[string]string // idempotency key -> job id
	hashes   map[string]string
	order    []string // job ids in creation order
	events   int
	pingErr  error
}

func newMemStore() *memStore {
	return &memStore{jobs: map[string]domain.Job{}, keys: map[string]string{}, hashes: map[string]string{}}
}

func (m *memStore) Ping(context.Context) error { return m.pingErr }

func (m *memStore) CreatePrinter(_ context.Context, p domain.Printer, _ string) (domain.Printer, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, q := range m.printers {
		if q.Code == p.Code {
			return domain.Printer{}, false, httpx.NewProblem(http.StatusConflict, domain.CodeDuplicatePrinter, "duplicate")
		}
	}
	p.ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", len(m.printers)+100)
	m.printers = append(m.printers, p)
	return p, true, nil
}

func (m *memStore) ListPrinters(context.Context) ([]domain.Printer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.Printer{}, m.printers...), nil
}

func (m *memStore) QueueDevices(_ context.Context, queue string) ([]domain.Printer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Printer
	for _, p := range m.printers {
		if p.QueueCode == queue {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *memStore) CreateJob(_ context.Context, in store.NewJob) (domain.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.keys[in.IdempotencyKey]; ok {
		if m.hashes[in.IdempotencyKey] != in.RequestHash {
			return domain.Job{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reused")
		}
		return m.jobs[id], false, nil
	}
	m.keys[in.IdempotencyKey] = in.Job.ID
	m.hashes[in.IdempotencyKey] = in.RequestHash
	m.jobs[in.Job.ID] = in.Job
	m.order = append(m.order, in.Job.ID)
	return in.Job, true, nil
}

func (m *memStore) ListJobs(_ context.Context, f store.JobFilter) ([]domain.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Job{}
	for i := len(m.order) - 1; i >= 0 && len(out) < f.Limit; i-- {
		j := m.jobs[m.order[i]]
		if (f.Status == "" || j.Status == f.Status) && (f.QueueCode == "" || j.QueueCode == f.QueueCode) &&
			(f.PrinterCode == "" || j.PrinterCode == f.PrinterCode) {
			out = append(out, j)
		}
	}
	return out, nil
}

func (m *memStore) CompleteJob(_ context.Context, j domain.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.jobs[j.ID]
	if cur.Status != domain.JobQueued {
		return domain.ErrTerminal
	}
	m.jobs[j.ID] = j
	m.events++
	return nil
}

func (m *memStore) GetJob(_ context.Context, id string) (domain.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return domain.Job{}, httpx.NewProblem(http.StatusNotFound, store.CodeJobNotFound, "print job not found")
	}
	return j, nil
}

// fakeSender refuses delivery to printers listed in down and records what it sent.
type fakeSender struct {
	down map[string]bool
	sent []string
}

func (f *fakeSender) Send(_ context.Context, p domain.Printer, zpl string) error {
	if f.down[p.Code] {
		return errors.New("connection refused")
	}
	f.sent = append(f.sent, p.Code)
	_ = zpl
	return nil
}

func router(s store.Store, sender api.Sender) http.Handler {
	r := httpx.NewRouter()
	api.New(s, sender).Register(r)
	return r
}

func call(t *testing.T, h http.Handler, method, path, body, duties, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-User", "worker-7")
	req.Header.Set("X-Passage-Duties", duties)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const lpBody = `{"templateCode":"LP_LABEL","queueCode":"DOCK","data":{"lp":"LP-000123","item":"WIDGET","lot":"L42","expiry":"2027-06-30","sscc":"340123456789012340"}}`

func seedDock(t *testing.T, h http.Handler) {
	t.Helper()
	for i, code := range []string{"DOCK-1", "DOCK-2"} {
		body := fmt.Sprintf(`{"code":%q,"name":"Dock","deviceType":"ZPL_II","dpi":203,"host":"10.0.4.%d","queueCode":"DOCK","failoverRank":%d}`, code, 20+i, i+1)
		if rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "WMS_PRINT_ADMIN", "reg-"+code); rec.Code != http.StatusCreated {
			t.Fatalf("register %s: %d %s", code, rec.Code, rec.Body)
		}
	}
}

func decodeJob(t *testing.T, rec *httptest.ResponseRecorder) domain.Job {
	t.Helper()
	var j domain.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
		t.Fatalf("decode job: %v (%s)", err, rec.Body)
	}
	return j
}

func TestCreateJob_PrintsOnFirstPrinter(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	seedDock(t, h)
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	j := decodeJob(t, rec)
	if j.Status != domain.JobPrinted || j.PrinterCode != "DOCK-1" || j.Attempts != 1 {
		t.Fatalf("job = %+v", j)
	}
	if strings.Contains(rec.Body.String(), "^XA") {
		t.Fatal("ZPL leaked in the response body")
	}
}

func TestCreateJob_FailsOverWhenFirstPrinterIsDown(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{"DOCK-1": true}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-2"))
	if j.Status != domain.JobPrinted || j.PrinterCode != "DOCK-2" || j.Attempts != 3 {
		t.Fatalf("failover job = %+v", j)
	}
}

func TestCreateJob_FailedWhenNoPrinterAcceptsAndSurfacesCode(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{"DOCK-1": true, "DOCK-2": true}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-3"))
	if j.Status != domain.JobFailed || j.LastError != domain.CodeUnreachable {
		t.Fatalf("job = %+v", j)
	}
}

func TestCreateJob_NoRoutedPrinterFailsWithMOB0112(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-4"))
	if j.Status != domain.JobFailed || j.LastError != domain.CodeNoPrinter {
		t.Fatalf("job = %+v", j)
	}
}

func TestCreateJob_ReplayReturnsStoredJobWithoutPrinting(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	first := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-5"))
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-5")
	if rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status %d headers %v", rec.Code, rec.Header())
	}
	if again := decodeJob(t, rec); again.ID != first.ID {
		t.Fatalf("replay created a new job %s", again.ID)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("replay printed again: %v", sender.sent)
	}
}

func TestCreateJob_ReusedKeyWithDifferentBodyIsRefused(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	seedDock(t, h)
	call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-6")
	other := strings.Replace(lpBody, "WIDGET", "GADGET", 1)
	if rec := call(t, h, http.MethodPost, "/print/v1/jobs", other, "WMS_PRINT", "job-6"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", rec.Code)
	}
}

func TestCreateJob_ReprintReusesStoredZPL(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	orig := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "WMS_PRINT", "job-7"))
	body := fmt.Sprintf(`{"queueCode":"DOCK","reprintOf":%q}`, orig.ID)
	re := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", body, "WMS_PRINT", "job-8"))
	if re.ID == orig.ID || re.ReprintOf == nil || *re.ReprintOf != orig.ID || re.TemplateCode != orig.TemplateCode {
		t.Fatalf("reprint = %+v", re)
	}
	if len(sender.sent) != 2 {
		t.Fatalf("reprint not sent: %v", sender.sent)
	}
}

func TestCreateJob_ValidationAndDuties(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	seedDock(t, h)
	cases := []struct {
		name   string
		body   string
		duties string
		key    string
		want   int
	}{
		{"missing idempotency key", lpBody, "WMS_PRINT", "", http.StatusBadRequest},
		{"unknown template", `{"templateCode":"NOPE","queueCode":"DOCK","data":{}}`, "WMS_PRINT", "k-a", http.StatusBadRequest},
		{"missing field", `{"templateCode":"LP_LABEL","queueCode":"DOCK","data":{"lp":"x"}}`, "WMS_PRINT", "k-b", http.StatusBadRequest},
		{"zpl injection in data", strings.Replace(lpBody, "WIDGET", "^XA", 1), "WMS_PRINT", "k-c", http.StatusBadRequest},
		{"bad sscc", strings.Replace(lpBody, "340123456789012340", "340123456789012341", 1), "WMS_PRINT", "k-d", http.StatusBadRequest},
		{"no duty", lpBody, "", "k-e", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := call(t, h, http.MethodPost, "/print/v1/jobs", c.body, c.duties, c.key); rec.Code != c.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.want, rec.Body)
			}
		})
	}
}

func TestPrinterRegistration_ValidatesAndDefaultsRawPort(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	bad := `{"code":"x","name":"","deviceType":"LASER","dpi":150,"host":"","queueCode":"DOCK","failoverRank":0}`
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", bad, "WMS_PRINT_ADMIN", "p-bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	ok := `{"code":"PRT-1","name":"Pack","deviceType":"ZPL_II","dpi":300,"host":"10.0.5.9","queueCode":"PACK","failoverRank":1}`
	rec := call(t, h, http.MethodPost, "/print/v1/printers", ok, "WMS_PRINT_ADMIN", "p-ok")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"port":9100`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/printers", "", "WMS_PRINT", ""); rec.Code != http.StatusOK {
		t.Fatalf("list status %d", rec.Code)
	}
}

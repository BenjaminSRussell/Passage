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
	"time"

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
	in.Job.CreatedAt = time.Now()
	in.Job.UpdatedAt = in.Job.CreatedAt
	m.jobs[in.Job.ID] = in.Job
	m.order = append(m.order, in.Job.ID)
	return in.Job, true, nil
}

func (m *memStore) ListJobs(_ context.Context, f store.JobFilter) ([]domain.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.Job{}
	for i := len(m.order) - 1; i >= 0 && len(out) < f.Limit; i-- {
		j, ok := m.jobs[m.order[i]]
		if !ok {
			continue // purged
		}
		if (f.Status == "" || j.Status == f.Status) && (f.QueueCode == "" || j.QueueCode == f.QueueCode) &&
			(f.PrinterCode == "" || j.PrinterCode == f.PrinterCode) {
			out = append(out, j)
		}
	}
	return out, nil
}

// PrinterStatuses mirrors the Postgres query: queue depth is QUEUED jobs on the printer's
// queue, state comes from the newest job the printer handled.
func (m *memStore) PrinterStatuses(context.Context) ([]domain.PrinterStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PrinterStatus{}
	for _, p := range m.printers {
		st := domain.PrinterStatus{PrinterID: p.ID, Code: p.Code, QueueCode: p.QueueCode, State: domain.StateIdle}
		var last *domain.Job
		for _, id := range m.order {
			j, ok := m.jobs[id]
			if !ok {
				continue
			}
			if j.QueueCode == p.QueueCode && j.Status == domain.JobQueued {
				st.QueueDepth++
			}
			if j.PrinterCode == p.Code {
				job := j
				last = &job
			}
		}
		if last != nil {
			st.State = domain.PrinterState(last.Status)
			st.LastError = last.LastError
			at := last.UpdatedAt
			st.LastJobAt = &at
		}
		out = append(out, st)
	}
	return out, nil
}

func (m *memStore) PurgeJobs(_ context.Context, cutoff time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, j := range m.jobs {
		if j.CreatedAt.Before(cutoff) && j.Status != domain.JobQueued {
			delete(m.jobs, id)
			n++
		}
	}
	return n, nil
}

// backdate moves a stored job into the past so retention tests can age it.
func (m *memStore) backdate(id string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[id]
	j.CreatedAt = at
	m.jobs[id] = j
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
	down     map[string]bool
	sent     []string // printer codes that accepted a payload
	payloads []string // the payloads they accepted
}

func (f *fakeSender) Send(_ context.Context, p domain.Printer, zpl string) error {
	if f.down[p.Code] {
		return errors.New("connection refused")
	}
	f.sent = append(f.sent, p.Code)
	f.payloads = append(f.payloads, zpl)
	return nil
}

func router(s store.Store, sender api.Sender, opts ...api.Option) http.Handler {
	r := httpx.NewRouter()
	api.New(s, sender, opts...).Register(r)
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
		if rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "print.admin", "reg-"+code); rec.Code != http.StatusCreated {
			t.Fatalf("register %s: %d %s", code, rec.Code, rec.Body)
		}
	}
}

// registerMedia registers one 203 dpi ZPL printer per media size on the DOCK
// queue, in failover order (DOCK-1, DOCK-2, ...).
func registerMedia(t *testing.T, h http.Handler, medias ...string) {
	t.Helper()
	for i, media := range medias {
		code := fmt.Sprintf("DOCK-%d", i+1)
		body := fmt.Sprintf(`{"code":%q,"name":"Dock","deviceType":"ZPL_II","dpi":203,"mediaSize":%q,"host":"10.0.4.%d","queueCode":"DOCK","failoverRank":%d}`,
			code, media, 20+i, i+1)
		if rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "print.admin", "reg-"+code); rec.Code != http.StatusCreated {
			t.Fatalf("register %s: %d %s", code, rec.Code, rec.Body)
		}
	}
}

const cartonBody = `{"templateCode":"GS1_CARTON_LABEL","queueCode":"DOCK","data":{"sscc":"340123456789012340","shipToName":"DOCK 9","poNumber":"PO-7","shipToPostal":"90210"}}`

// FR-32.7 / FR-32.13: a label that does not fit the first device's media is a
// failed attempt there, so the job fails over and is sized for the device that
// prints it. The GS1 carton label is 4 x 6 in only (no 1 in layout).
func TestCreateJob_FailsOverWhenLabelDoesNotFitDevice(t *testing.T) {
	sender := &fakeSender{}
	h := router(newMemStore(), sender)
	registerMedia(t, h, domain.Media2x1In, domain.Media4x6In)
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", cartonBody, "print.job.submit", "carton-fit-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create job: %d %s", rec.Code, rec.Body)
	}
	job := decodeJob(t, rec)
	if job.Status != domain.JobPrinted || job.PrinterCode != "DOCK-2" {
		t.Fatalf("want PRINTED on DOCK-2 after failover, got %s on %q", job.Status, job.PrinterCode)
	}
	// The misfit is refused before any bytes reach DOCK-1: only DOCK-2 accepted a payload.
	if len(sender.sent) != 1 || sender.sent[0] != "DOCK-2" {
		t.Fatalf("a label that does not fit the 2 x 1 in device must not be sent to it: sent %v", sender.sent)
	}
	if !strings.HasPrefix(sender.payloads[0], "^XA\n^PW812\n^LL1218\n") {
		t.Fatalf("DOCK-2 payload not sized for 4 x 6 in: %q", sender.payloads[0])
	}
	// The refusal is not retried: one attempt on DOCK-1, one on DOCK-2.
	if job.Attempts != 2 {
		t.Fatalf("want 2 attempts (refused on DOCK-1, printed on DOCK-2), got %d", job.Attempts)
	}
}

// FR-32.7: a 2 x 1 in device prints the LP label's compact layout, so the first
// device in the queue takes the job; the stored payload stays the standard one.
func TestCreateJob_OneInchDevicePrintsCompactLayout(t *testing.T) {
	sender := &fakeSender{}
	ms := newMemStore()
	h := router(ms, sender)
	registerMedia(t, h, domain.Media2x1In, domain.Media4x6In)
	job := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "lp-small-1"))
	if job.Status != domain.JobPrinted || job.PrinterCode != "DOCK-1" || job.Attempts != 1 {
		t.Fatalf("want PRINTED on the 2 x 1 in DOCK-1 without failover, got %s on %q after %d attempts", job.Status, job.PrinterCode, job.Attempts)
	}
	if len(sender.payloads) != 1 {
		t.Fatalf("want one payload, got %d", len(sender.payloads))
	}
	payload := sender.payloads[0]
	for _, part := range []string{"^XA\n^PW406\n^LL203\n", "^FO20,88^BCN,50,N,N,N^FD>;>800340123456789012340^FS", "(00) 340123456789012340"} {
		if !strings.Contains(payload, part) {
			t.Fatalf("2 x 1 payload lacks %q: %q", part, payload)
		}
	}
	if strings.Contains(payload, "^BCN,100") {
		t.Fatalf("the 4 x 6 bar code reached a 2 x 1 label: %q", payload)
	}
	if stored := ms.jobs[job.ID].ZPL; !strings.Contains(stored, "^FO40,220^BCN,100,Y,N,N") {
		t.Fatalf("stored payload should be the standard layout: %q", stored)
	}
}

// BR-32.8 with FR-32.7: a reprint has no template data, so it reuses the stored
// 4 x 6 in payload, which a 2 x 1 in device refuses; the queue fails over.
func TestCreateJob_ReprintOnOneInchDeviceFailsOver(t *testing.T) {
	sender := &fakeSender{}
	h := router(newMemStore(), sender)
	registerMedia(t, h, domain.Media2x1In, domain.Media4x6In)
	orig := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "lp-orig"))
	body := fmt.Sprintf(`{"queueCode":"DOCK","reprintOf":%q}`, orig.ID)
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", body, "print.job.submit", "lp-reprint")
	if rec.Code != http.StatusCreated {
		t.Fatalf("reprint: %d %s", rec.Code, rec.Body)
	}
	if j := decodeJob(t, rec); j.Status != domain.JobPrinted || j.PrinterCode != "DOCK-2" || j.Attempts != 2 {
		t.Fatalf("want the reprint PRINTED on DOCK-2 after one refusal, got %+v", j)
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
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-1")
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
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-2"))
	if j.Status != domain.JobPrinted || j.PrinterCode != "DOCK-2" || j.Attempts != 3 {
		t.Fatalf("failover job = %+v", j)
	}
}

func TestCreateJob_FailedWhenNoPrinterAcceptsAndSurfacesCode(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{"DOCK-1": true, "DOCK-2": true}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-3"))
	if j.Status != domain.JobFailed || j.LastError != domain.CodeUnreachable {
		t.Fatalf("job = %+v", j)
	}
}

func TestCreateJob_NoRoutedPrinterFailsWithMOB0112(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-4"))
	if j.Status != domain.JobFailed || j.LastError != domain.CodeNoPrinter {
		t.Fatalf("job = %+v", j)
	}
}

func TestCreateJob_ReplayReturnsStoredJobWithoutPrinting(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	first := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-5"))
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-5")
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
	call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-6")
	other := strings.Replace(lpBody, "WIDGET", "GADGET", 1)
	if rec := call(t, h, http.MethodPost, "/print/v1/jobs", other, "print.job.submit", "job-6"); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", rec.Code)
	}
}

func TestCreateJob_ReprintReusesStoredZPL(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	orig := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "job-7"))
	body := fmt.Sprintf(`{"queueCode":"DOCK","reprintOf":%q}`, orig.ID)
	re := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", body, "print.job.submit", "job-8"))
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
		{"missing idempotency key", lpBody, "print.job.submit", "", http.StatusBadRequest},
		{"unknown template", `{"templateCode":"NOPE","queueCode":"DOCK","data":{}}`, "print.job.submit", "k-a", http.StatusBadRequest},
		{"missing field", `{"templateCode":"LP_LABEL","queueCode":"DOCK","data":{"lp":"x"}}`, "print.job.submit", "k-b", http.StatusBadRequest},
		{"zpl injection in data", strings.Replace(lpBody, "WIDGET", "^XA", 1), "print.job.submit", "k-c", http.StatusBadRequest},
		{"bad sscc", strings.Replace(lpBody, "340123456789012340", "340123456789012341", 1), "print.job.submit", "k-d", http.StatusBadRequest},
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
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", bad, "print.admin", "p-bad"); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	ok := `{"code":"PRT-1","name":"Pack","deviceType":"ZPL_II","dpi":300,"host":"10.0.5.9","queueCode":"PACK","failoverRank":1}`
	rec := call(t, h, http.MethodPost, "/print/v1/printers", ok, "print.admin", "p-ok")
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"port":9100`) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/printers", "", "print.job.submit", ""); rec.Code != http.StatusOK {
		t.Fatalf("list status %d", rec.Code)
	}
}

func newRouterFor(a *api.API) http.Handler {
	r := httpx.NewRouter()
	a.Register(r)
	return r
}

func httptestGet(h http.Handler, path string) string {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Body.String()
}

func TestCreateJob_ItemLabelComposesGS1FromFields(t *testing.T) {
	ms := newMemStore()
	h := router(ms, &fakeSender{down: map[string]bool{}})
	seedDock(t, h)
	item := `{"templateCode":"ITEM_LABEL","queueCode":"DOCK","data":{"gtin":"09501101020917","lot":"123ABC","expiry":"261231","serial":"SN0007"}}`
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", item, "print.job.submit", "item-1"))
	if j.Status != domain.JobPrinted || j.TemplateCode != domain.TemplateItem {
		t.Fatalf("item label job = %+v", j)
	}
	if want := ">;>8" + "0109501101020917" + "1012>63ABC>8>5" + "17261231" + "21>6SN>50007"; !strings.Contains(ms.jobs[j.ID].ZPL, want) {
		t.Fatalf("stored ZPL lacks the GS1 element string %q:\n%s", want, ms.jobs[j.ID].ZPL)
	}
	bad := strings.Replace(item, "09501101020917", "09501101020918", 1)
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", bad, "print.job.submit", "item-2")
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), domain.CodeInvalid) {
		t.Fatalf("bad GTIN: %d %s, want 400 %s", rec.Code, rec.Body, domain.CodeInvalid)
	}
}

func TestCreateJob_CartonLabelNeedsShipTo(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	seedDock(t, h)
	carton := `{"templateCode":"GS1_CARTON_LABEL","queueCode":"DOCK","data":{"sscc":"340123456789012340","poNumber":"PO-7781","shipToPostal":"60601"}}`
	if rec := call(t, h, http.MethodPost, "/print/v1/jobs", carton, "print.job.submit", "carton-1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("carton without shipToName: %d %s, want 400", rec.Code, rec.Body)
	}
	carton = strings.Replace(carton, `"sscc"`, `"shipToName":"ACME DC","sscc"`, 1)
	if j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", carton, "print.job.submit", "carton-2")); j.Status != domain.JobPrinted {
		t.Fatalf("carton label status %s, want PRINTED", j.Status)
	}
}

func TestCreatePrinter_DefaultsPortByDeviceType(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	for _, c := range []struct {
		deviceType string
		port       int
	}{{"ZPL_II", 9100}, {"PCL_6", 9100}, {"PDF_IPP", 631}} {
		body := fmt.Sprintf(`{"code":"P-%s","name":"Office","deviceType":%q,"dpi":600,"host":"10.0.9.1","queueCode":"OFFICE-%s","failoverRank":1}`, c.deviceType, c.deviceType, c.deviceType)
		rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "print.admin", "port-"+c.deviceType)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", c.deviceType, rec.Code, rec.Body)
		}
		var p domain.Printer
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		if p.Port != c.port {
			t.Errorf("%s port = %d, want %d", c.deviceType, p.Port, c.port)
		}
	}
}

// A queue whose only device holds media too small for the label fails the job with
// PRN-0007 (not MOB-0111, the device was never unreachable) and sends nothing.
func TestCreateJob_FailsWithMediaMismatchWhenNoDeviceFits(t *testing.T) {
	sender := &fakeSender{}
	h := router(newMemStore(), sender)
	body := `{"code":"DOCK-1","name":"Dock","deviceType":"ZPL_II","dpi":300,"mediaSize":"3X1_IN","host":"10.0.4.20","queueCode":"DOCK","failoverRank":1}`
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "print.admin", "reg-small"); rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	// The carton label is 4 x 6 in only; the LP label would print its 1 in layout here.
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", cartonBody, "print.job.submit", "carton-misfit-1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create job: %d %s", rec.Code, rec.Body)
	}
	job := decodeJob(t, rec)
	if job.Status != domain.JobFailed || job.LastError != domain.CodeMediaMismatch || job.Attempts != 1 {
		t.Fatalf("want FAILED %s after 1 attempt, got %s %q after %d", domain.CodeMediaMismatch, job.Status, job.LastError, job.Attempts)
	}
	if len(sender.sent) != 0 {
		t.Fatalf("nothing may be sent to a device the label does not fit: %v", sender.sent)
	}
}

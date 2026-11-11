package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/print/internal/api"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
)

// In-memory queue management for handler tests; the Postgres store is covered by the DB suite.
func (m *memStore) GetPrinter(_ context.Context, id string) (domain.Printer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.printers {
		if p.ID == id {
			return p, nil
		}
	}
	return domain.Printer{}, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found")
}

func (m *memStore) UpdatePrinter(_ context.Context, in domain.Printer) (domain.Printer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.printers {
		if p.ID != in.ID || p.RetiredAt != nil {
			continue
		}
		for _, o := range m.printers {
			if o.ID != in.ID && o.RetiredAt == nil && o.QueueCode == in.QueueCode && o.FailoverRank == in.FailoverRank {
				return domain.Printer{}, httpx.NewProblem(http.StatusConflict, domain.CodeRankTaken, "rank taken")
			}
		}
		in.Code, in.DeviceType, in.CreatedAt = p.Code, p.DeviceType, p.CreatedAt
		m.printers[i] = in
		return in, nil
	}
	return domain.Printer{}, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found or already retired")
}

func (m *memStore) RetirePrinter(_ context.Context, id string) (domain.Printer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.printers {
		if p.ID == id && p.RetiredAt == nil {
			now := time.Now()
			m.printers[i].RetiredAt = &now
			return m.printers[i], nil
		}
	}
	return domain.Printer{}, httpx.NewProblem(http.StatusNotFound, domain.CodePrinterNotFound, "printer not found or already retired")
}

func (m *memStore) UpdateRoutingRule(_ context.Context, r domain.RoutingRule) (domain.RoutingRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, o := range m.rules {
		if o.ID == r.ID {
			m.rules[i] = r
			return r, nil
		}
	}
	return domain.RoutingRule{}, httpx.NewProblem(http.StatusNotFound, domain.CodeRuleNotFound, "routing rule not found")
}

func (m *memStore) DeleteRoutingRule(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, o := range m.rules {
		if o.ID == id {
			m.rules = append(m.rules[:i], m.rules[i+1:]...)
			return nil
		}
	}
	return httpx.NewProblem(http.StatusNotFound, domain.CodeRuleNotFound, "routing rule not found")
}

func (m *memStore) ClaimStaleJobs(_ context.Context, olderThan time.Time, limit int) ([]domain.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Job
	for _, id := range m.order {
		j, ok := m.jobs[id]
		if ok && j.Status == domain.JobQueued && j.UpdatedAt.Before(olderThan) && len(out) < limit {
			j.UpdatedAt = time.Now()
			m.jobs[id] = j
			out = append(out, j)
		}
	}
	return out, nil
}

func printerID(t *testing.T, h http.Handler, code string) string {
	t.Helper()
	rec := call(t, h, http.MethodGet, "/print/v1/printers?includeRetired=true", "", "print.job.submit", "")
	var listed struct {
		Items []domain.Printer `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	for _, p := range listed.Items {
		if p.Code == code {
			return p.ID
		}
	}
	t.Fatalf("printer %s not listed", code)
	return ""
}

func TestPrinter_EditMoveAndRetire(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	seedDock(t, h)
	dock1 := printerID(t, h, "DOCK-1")

	rec := call(t, h, http.MethodPatch, "/print/v1/printers/"+dock1, `{"name":"Dock door 1","host":"10.0.9.9","queueCode":"OFFICE"}`, "print.admin", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	var p domain.Printer
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Name != "Dock door 1" || p.QueueCode != "OFFICE" || p.Host != "10.0.9.9" || p.Code != "DOCK-1" {
		t.Fatalf("edited printer = %+v (%v)", p, err)
	}
	// An invalid field is refused and nothing changes.
	if rec := call(t, h, http.MethodPatch, "/print/v1/printers/"+dock1, `{"dpi":96}`, "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid dpi: %d", rec.Code)
	}
	// Moving onto a taken rank conflicts (DOCK-2 holds rank 2 on DOCK).
	if rec := call(t, h, http.MethodPatch, "/print/v1/printers/"+dock1, `{"queueCode":"DOCK","failoverRank":2}`, "print.admin", ""); rec.Code != http.StatusConflict {
		t.Fatalf("rank taken: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPatch, "/print/v1/printers/"+dock1, `{"name":"x"}`, "print.job.submit", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("worker edited a printer: %d", rec.Code)
	}

	// Retire DOCK-2: the queue now has no device and the job fails with MOB-0112/0111 path.
	dock2 := printerID(t, h, "DOCK-2")
	if rec := call(t, h, http.MethodPost, "/print/v1/printers/"+dock2+":retire", "", "print.admin", ""); rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/printers/"+dock2+":retire", "", "print.admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second retire: %d, want 404", rec.Code)
	}
	if rec := call(t, h, http.MethodPatch, "/print/v1/printers/"+dock2, `{"name":"x"}`, "print.admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("edit retired: %d, want 404", rec.Code)
	}
	list := call(t, h, http.MethodGet, "/print/v1/printers", "", "print.job.submit", "")
	var listed struct {
		Items []domain.Printer `json:"items"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil || len(listed.Items) != 1 {
		t.Fatalf("active list = %+v (%v)", listed, err)
	}
	job := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", lpBody, "print.job.submit", "after-retire"))
	if job.Status != domain.JobFailed || len(sender.sent) != 0 {
		t.Fatalf("job after queue emptied = %+v, sent %v", job, sender.sent)
	}
}

func TestRoutingRule_EditAndDelete(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	rec := call(t, h, http.MethodPost, "/print/v1/routing-rules", `{"documentType":"PICK_LIST","queueCode":"OFFICE","sequence":1}`, "print.admin", "rr-e1")
	var rule domain.RoutingRule
	if err := json.Unmarshal(rec.Body.Bytes(), &rule); err != nil || rule.ID == "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	path := "/print/v1/routing-rules/" + rule.ID
	rec = call(t, h, http.MethodPut, path, `{"documentType":"PICK_LIST","zone":"A","queueCode":"DOCK","sequence":2}`, "print.admin", "")
	var edited domain.RoutingRule
	if err := json.Unmarshal(rec.Body.Bytes(), &edited); err != nil || rec.Code != http.StatusOK || edited.QueueCode != "DOCK" || edited.Zone != "A" || edited.ID != rule.ID {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodPut, path, `{"documentType":"PICK_LIST","queueCode":"DOCK","sequence":0}`, "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid edit: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, path, "", "print.job.submit", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("worker deleted a rule: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, path, "", "print.admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, path, "", "print.admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d, want 404", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, "/print/v1/routing-rules/not-a-uuid", "", "print.admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d, want 404", rec.Code)
	}
}

// §6.32.16: a job left QUEUED (the process died after insert) is dispatched by the sweep, once.
func TestSweep_DispatchesStaleQueuedJobsOnce(t *testing.T) {
	st := newMemStore()
	sender := &fakeSender{down: map[string]bool{}}
	h := router(st, sender)
	seedDock(t, h)
	tpl, _ := domain.Template("LP_LABEL")
	zpl, err := domain.Render(tpl, map[string]string{"lp": "LP-000123", "item": "WIDGET", "lot": "L42", "expiry": "2027-06-30", "sscc": "340123456789012340"})
	if err != nil {
		t.Fatal(err)
	}
	st.queueStale("stale-1", "DOCK", zpl, time.Now().Add(-time.Hour))
	st.queueStale("fresh-1", "DOCK", zpl, time.Now())

	rec := call(t, h, http.MethodPost, "/print/v1/jobs:sweep", "", "print.admin", "sweep-1")
	var res api.SweepResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || rec.Code != http.StatusOK || res.Claimed != 1 || res.Printed != 1 || res.Failed != 0 {
		t.Fatalf("sweep: %d %s", rec.Code, rec.Body)
	}
	if len(sender.sent) != 1 || sender.sent[0] != "DOCK-1" {
		t.Fatalf("sent %v, want one label to DOCK-1", sender.sent)
	}
	j, _ := st.GetJob(context.Background(), "stale-1")
	if j.Status != domain.JobPrinted {
		t.Fatalf("stale job = %+v", j)
	}
	fresh, _ := st.GetJob(context.Background(), "fresh-1")
	if fresh.Status != domain.JobQueued {
		t.Fatalf("fresh job was swept: %+v", fresh)
	}
	again := call(t, h, http.MethodPost, "/print/v1/jobs:sweep", "", "print.admin", "sweep-2")
	if err := json.Unmarshal(again.Body.Bytes(), &res); err != nil || res.Claimed != 0 || len(sender.sent) != 1 {
		t.Fatalf("second sweep claimed %d, sent %v", res.Claimed, sender.sent)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/jobs:sweep", "", "print.job.submit", "sweep-3"); rec.Code != http.StatusForbidden {
		t.Fatalf("worker swept: %d", rec.Code)
	}
}

var _ store.Store = (*memStore)(nil)

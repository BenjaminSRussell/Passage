package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
)

// In-memory routing rules for handler tests; the Postgres store is covered by the DB suite.
func (m *memStore) CreateRoutingRule(_ context.Context, r domain.RoutingRule, _ string) (domain.RoutingRule, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = fmt.Sprintf("00000000-0000-0000-0001-%012d", len(m.rules)+1)
	m.rules = append(m.rules, r)
	return r, true, nil
}

func (m *memStore) ListRoutingRules(context.Context) ([]domain.RoutingRule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]domain.RoutingRule{}, m.rules...), nil
}

var _ store.Store = (*memStore)(nil)

func registerOn(t *testing.T, h http.Handler, code, queue string) {
	t.Helper()
	body := fmt.Sprintf(`{"code":%q,"name":"Office","deviceType":"ZPL_II","dpi":203,"host":"10.0.6.%d","queueCode":%q,"failoverRank":1}`, code, len(code), queue)
	if rec := call(t, h, http.MethodPost, "/print/v1/printers", body, "print.admin", "reg-"+code); rec.Code != http.StatusCreated {
		t.Fatalf("register %s: %d %s", code, rec.Code, rec.Body)
	}
}

func TestRoutingRules_CreateValidateAndDuties(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	ok := `{"documentType":"PICK_LIST","queueCode":"OFFICE","sequence":1}`
	if rec := call(t, h, http.MethodPost, "/print/v1/routing-rules", ok, "print.admin", "rr-1"); rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body)
	}
	bad := `{"documentType":"INVOICE","queueCode":"OFFICE","sequence":0}`
	if rec := call(t, h, http.MethodPost, "/print/v1/routing-rules", bad, "print.admin", "rr-2"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid rule status %d, want 400", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/routing-rules", ok, "print.job.submit", "rr-3"); rec.Code != http.StatusForbidden {
		t.Fatalf("worker created a rule: %d", rec.Code)
	}
	rec := call(t, h, http.MethodGet, "/print/v1/routing-rules", "", "print.job.submit", "")
	var listed struct {
		Items []domain.RoutingRule `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil || rec.Code != http.StatusOK || len(listed.Items) != 1 {
		t.Fatalf("list status %d items %+v err %v", rec.Code, listed, err)
	}
}

func TestCreateJob_RoutesByDocumentTypeWhenNoQueueGiven(t *testing.T) {
	sender := &fakeSender{down: map[string]bool{}}
	h := router(newMemStore(), sender)
	registerOn(t, h, "OFF-1", "OFFICE")
	if rec := call(t, h, http.MethodPost, "/print/v1/routing-rules", `{"documentType":"LP_LABEL","queueCode":"OFFICE","sequence":1}`, "print.admin", "rr-4"); rec.Code != http.StatusCreated {
		t.Fatalf("rule status %d", rec.Code)
	}
	body := `{"templateCode":"LP_LABEL","documentType":"LP_LABEL","data":{"lp":"LP-000123","item":"WIDGET","lot":"L42","expiry":"2027-06-30","sscc":"340123456789012340"}}`
	j := decodeJob(t, call(t, h, http.MethodPost, "/print/v1/jobs", body, "print.job.submit", "job-r1"))
	if j.QueueCode != "OFFICE" || j.Status != domain.JobPrinted || j.PrinterCode != "OFF-1" {
		t.Fatalf("routed job = %+v", j)
	}
}

func TestCreateJob_NoMatchingRuleIsMOB0112(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	body := `{"templateCode":"LP_LABEL","documentType":"LP_LABEL","data":{"lp":"LP-000123","item":"WIDGET","lot":"L42","expiry":"2027-06-30","sscc":"340123456789012340"}}`
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", body, "print.job.submit", "job-r2")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422: %s", rec.Code, rec.Body)
	}
	var p map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p["code"] != domain.CodeNoPrinter {
		t.Fatalf("problem = %v (err %v), want code %s", p, err, domain.CodeNoPrinter)
	}
}

func TestCreateJob_NeedsQueueOrDocumentType(t *testing.T) {
	h := router(newMemStore(), &fakeSender{down: map[string]bool{}})
	rec := call(t, h, http.MethodPost, "/print/v1/jobs", `{"templateCode":"LP_LABEL","data":{}}`, "print.job.submit", "job-r3")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

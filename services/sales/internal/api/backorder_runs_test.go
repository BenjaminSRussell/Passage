package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

type runStore struct {
	fakeStore
	created   *store.NewBackorderRun
	committed int
	filter    *store.BackorderRunFilter
	err       error
}

func (s *runStore) CreateBackorderRun(_ context.Context, in store.NewBackorderRun, _ time.Time) (store.BackorderRun, bool, error) {
	s.created = &in
	return store.BackorderRun{ID: ids.New(), Status: "PREVIEW", Version: 1, Strategy: in.Strategy, DeconfirmedCount: 2, Allocated: decimal.NewFromInt(40)}, true, s.err
}

func (s *runStore) ListBackorderRuns(_ context.Context, f store.BackorderRunFilter) ([]store.BackorderRun, error) {
	s.filter = &f
	return []store.BackorderRun{}, s.err
}

func (s *runStore) GetBackorderRun(_ context.Context, id string) (store.BackorderRun, error) {
	return store.BackorderRun{ID: id, Status: "COMMITTED", Version: 2}, s.err
}

func (s *runStore) CommitBackorderRun(_ context.Context, id string, version int, _ string, _ time.Time) (store.BackorderRun, error) {
	s.committed = version
	return store.BackorderRun{ID: id, Status: "COMMITTED", Version: version + 1}, s.err
}

func runBody(extra string) string {
	return `{"legalEntityId":"` + testLE + `","productId":"` + ids.New() + `","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() +
		`","strategy":"REDISTRIBUTE","supply":"40","supplyDate":"2027-03-10"` + extra + `}`
}

func TestBackorderRunHandlers(t *testing.T) {
	s := &runStore{}
	key := map[string]string{"Idempotency-Key": "run-1"}
	if rec := serveBody(t, s, http.MethodPost, backorderRuns, runBody(""), principal(dutyRead), key); rec.Code != http.StatusForbidden {
		t.Fatalf("needs sales.backorder.run: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, backorderRuns, runBody(""), principal(dutyBackorder), nil); rec.Code != http.StatusBadRequest || s.created != nil {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	customer := ids.New()
	for name, bad := range map[string]string{
		"strategy":   strings.Replace(runBody(""), "REDISTRIBUTE", "BEST", 1),
		"supply":     strings.Replace(runBody(""), `"40"`, `"-1"`, 1),
		"date":       strings.Replace(runBody(""), "2027-03-10", "soon", 1),
		"threshold":  runBody(`,"loseThreshold":10`),
		"priority":   runBody(`,"defaultPriorityClass":10`),
		"customer":   runBody(`,"priorityClasses":{"c1":2}`),
		"class":      runBody(`,"priorityClasses":{"` + customer + `":0}`),
		"product id": strings.Replace(runBody(""), `"productId":"`, `"productId":"x`, 1),
	} {
		if rec := serveBody(t, s, http.MethodPost, backorderRuns, bad, principal(dutyBackorder), key); rec.Code != http.StatusBadRequest || s.created != nil {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec := serveBody(t, s, http.MethodPost, backorderRuns, runBody(`,"priorityClasses":{"`+customer+`":1}`), principal(dutyBackorder), key)
	if rec.Code != http.StatusCreated || s.created == nil || s.created.LoseThreshold != 3 || s.created.DefaultPriority != 5 || s.created.Priorities[customer] != 1 ||
		s.created.IdempotencyKey != "run-1" || s.created.RequestHash == "" {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body, s.created)
	}
	if rec.Header().Get("ETag") != `"1"` || !strings.Contains(rec.Body.String(), `"warningCode":"SLS-0015"`) {
		t.Fatalf("response %s %v", rec.Body, rec.Header())
	}

	id := ids.New()
	if rec := serve(t, s, http.MethodGet, backorderRuns+"/"+id, principal(), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("get needs sales.order.read: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, backorderRuns+"/x", principal(dutyRead), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, backorderRuns+"/"+id, principal(dutyRead), nil); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d %v", rec.Code, rec.Header())
	}
	if rec := serve(t, s, http.MethodGet, backorderRuns+"?legalEntityId="+testLE+"&status=PREVIEW&limit=5", principal(dutyRead), nil); rec.Code != http.StatusOK || s.filter == nil || s.filter.Status != "PREVIEW" || s.filter.Limit != 5 {
		t.Fatalf("list: %d %+v", rec.Code, s.filter)
	}
	for _, q := range []string{"status=DONE", "productId=p", "limit=0"} {
		if rec := serve(t, s, http.MethodGet, backorderRuns+"?legalEntityId="+testLE+"&"+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}

	path := backorderRuns + "/" + id + ":commit"
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyRead), map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusForbidden {
		t.Fatalf("commit needs sales.backorder.run: %d", rec.Code)
	}
	if rec := serveBody(t, s, http.MethodPost, path, "", principal(dutyBackorder), nil); rec.Code != http.StatusPreconditionRequired || s.committed != 0 {
		t.Fatalf("If-Match is required: %d", rec.Code)
	}
	rec = serveBody(t, s, http.MethodPost, path, "", principal(dutyBackorder), map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || s.committed != 1 || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("commit: %d %s %d", rec.Code, rec.Body, s.committed)
	}
	s.err = httpx.NewProblem(http.StatusConflict, store.CodeRunStale, "order SO-1 line 1 changed since the preview; run a new preview")
	rec = serveBody(t, s, http.MethodPost, path, "", principal(dutyBackorder), map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), store.CodeRunStale) || rec.Header().Get("ETag") != "" {
		t.Fatalf("stale commit is a 409 problem without an ETag: %d %s %v", rec.Code, rec.Body, rec.Header())
	}
}

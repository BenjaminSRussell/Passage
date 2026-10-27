package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
	"github.com/BenjaminSRussell/passage/services/production/internal/store"
	"github.com/BenjaminSRussell/passage/services/production/migrations"
)

// The HTTP surface over a real database: create, the AC-21.2 refusal, estimate,
// the AC-21.3 cost read, and the order list. Skipped without PASSAGE_TEST_DATABASE_URL.
func TestHTTP_OrderThroughTheDatabase(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	New(store.New(pool), Config{RAFTolerancePct: decimal.Zero}).Register(r)
	all := []string{DutyRead, DutyWrite, DutyRelease, DutyJournalPost, DutyEnd, DutyCostRead, DutySetupWrite, DutySetupRead}

	rec := do(r, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(nil), duties: all,
		headers: map[string]string{idempotencyName: "http-create"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var order store.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil {
		t.Fatal(err)
	}
	if order.LegalEntityID != entityID || len(order.Lines) != 2 || order.Lines[1].LineNo != "30.1" {
		t.Fatalf("created order: %+v", order)
	}

	// AC-21.2: :start on a Created order is 409 PRODUCTION_ILLEGAL_TRANSITION.
	start := commandCall(order.ID, "start", "", all...)
	start.headers["If-Match"] = `"1"`
	expectProblem(t, do(r, start), http.StatusConflict, domain.CodeIllegalTransition)

	est := commandCall(order.ID, "estimate", "", all...)
	est.headers["If-Match"] = `"1"`
	est.headers[idempotencyName] = "http-estimate"
	if rec := do(r, est); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("estimate: %d %s", rec.Code, rec.Body.String())
	}

	// AC-21.3: planned material = Σ line qty × unit cost: 315 × 10 + 200 × 4 = 3,950.00.
	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID + "/costs", duties: all})
	var costs costView
	if err := json.Unmarshal(rec.Body.Bytes(), &costs); err != nil || costs.Totals[domain.GroupMaterial] != "3950.00" {
		t.Fatalf("costs: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders?status=Estimated", duties: all})
	var page store.OrderPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != order.ID {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}

	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID + "/status-log", duties: all})
	var log struct {
		Items []store.StatusLogEntry `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil || len(log.Items) != 1 || log.Items[0].TraceID == "" {
		t.Fatalf("status log: %d %s", rec.Code, rec.Body.String())
	}

	// FR-21.13, FR-21.5, FR-21.11: a posting profile, then a picking list proposed, posted and costed.
	if rec := do(r, call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: profileBody(nil), duties: all}); rec.Code != http.StatusCreated {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body.String())
	}
	version := 2
	for i, cmd := range []string{"schedule", "release", "start"} {
		c := commandCall(order.ID, cmd, "", all...)
		c.headers["If-Match"] = fmt.Sprintf(`"%d"`, version)
		c.headers[idempotencyName] = fmt.Sprintf("http-%d", i)
		if rec := do(r, c); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", cmd, rec.Code, rec.Body.String())
		}
		version++
	}
	rec = do(r, journalCall(order.ID, `{"type":"picking-list"}`, all...))
	var j store.Journal
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || rec.Code != http.StatusCreated || len(j.Lines) != 2 || j.Posted {
		t.Fatalf("create journal: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, call{method: http.MethodPost, path: "/v1/production/journals/" + j.ID + ":post", duties: all, headers: map[string]string{idempotencyName: "http-post"}})
	if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil || rec.Code != http.StatusOK || !j.Posted || len(j.Postings) != 4 {
		t.Fatalf("post journal: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID + "/costs", duties: all})
	if err := json.Unmarshal(rec.Body.Bytes(), &costs); err != nil || costs.ActualTotal != "3950.00" || costs.WIPBalance != "3950.00" {
		t.Fatalf("costs after picking: %s", rec.Body.String())
	}
	rec = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID + "/journals", duties: all})
	var journals struct {
		Items []store.Journal `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &journals); err != nil || len(journals.Items) != 1 || journals.Items[0].ID != j.ID {
		t.Fatalf("journals: %s", rec.Body.String())
	}

	// FR-21.18: a Started order's BOM is locked; 4.21.14: the WIP report shows the picked material.
	edit := editCall(order.ID, `{"lines":[{"op":"change","lineNo":"10","qtyPer":"4"}]}`, all...)
	edit.headers["If-Match"] = do(r, call{method: http.MethodGet, path: "/v1/production/orders/" + order.ID, duties: all}).Header().Get("ETag")
	expectProblem(t, do(r, edit), http.StatusConflict, domain.CodeEditNotAllowed)
	rec = do(r, call{method: http.MethodGet, path: "/v1/production/wip-balances?status=Started", duties: all})
	var wip wipReportView
	if err := json.Unmarshal(rec.Body.Bytes(), &wip); err != nil || len(wip.Items) != 1 || wip.Items[0].WIPBalance != "3950.00" ||
		wip.Items[0].Consumption != "3950.00" || wip.Totals[0].Orders != 1 {
		t.Fatalf("wip report: %d %s", rec.Code, rec.Body.String())
	}
}

// FR-21.6 over HTTP and a real database: a Started order's operation is finished
// and reopened through its route, a replay answers the same order, and a Created
// order refuses the command.
func TestHTTP_OperationCommandsThroughTheDatabase(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	New(store.New(pool), Config{EstimateOnCreate: true}).Register(r)
	all := []string{DutyRead, DutyWrite, DutyRelease, DutyJournalPost, DutySetupWrite}
	if rec := do(r, call{method: http.MethodPut, path: "/v1/production/posting-profiles", body: profileBody(nil), duties: all}); rec.Code != http.StatusCreated {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body.String())
	}
	rec := do(r, call{method: http.MethodPost, path: "/v1/production/orders", body: createBody(nil), duties: all,
		headers: map[string]string{idempotencyName: "http-op-create"}})
	var order store.Order
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	// Estimate on create: Estimated at version 2.
	if order.Status != domain.Estimated || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("estimate on create: %s %s", order.Status, rec.Header().Get("ETag"))
	}
	finish := func(key, ifMatch string) *httptest.ResponseRecorder {
		return do(r, call{method: http.MethodPost, path: "/v1/production/orders/" + order.ID + "/operations/10:finish", duties: all,
			headers: map[string]string{"If-Match": ifMatch, idempotencyName: key}})
	}
	expectProblem(t, finish("http-op-early", `"2"`), http.StatusConflict, domain.CodeOperationState)

	version := 2
	for i, cmd := range []string{"schedule", "release", "start"} {
		c := commandCall(order.ID, cmd, "", all...)
		c.headers["If-Match"] = fmt.Sprintf(`"%d"`, version)
		c.headers[idempotencyName] = fmt.Sprintf("http-op-%d", i)
		if rec := do(r, c); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", cmd, rec.Code, rec.Body.String())
		}
		version++
	}
	rec = finish("http-op-finish", fmt.Sprintf(`"%d"`, version))
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || rec.Code != http.StatusOK || order.Operations[0].Status != domain.OperFinished ||
		rec.Header().Get("ETag") != fmt.Sprintf(`"%d"`, version+1) {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body.String())
	}
	if rec := finish("http-op-finish", fmt.Sprintf(`"%d"`, version)); rec.Code != http.StatusOK || rec.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(r, call{method: http.MethodPost, path: "/v1/production/orders/" + order.ID + "/operations/10:reopen", duties: all,
		headers: map[string]string{"If-Match": fmt.Sprintf(`"%d"`, version+1), idempotencyName: "http-op-reopen"}})
	if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil || rec.Code != http.StatusOK || order.Operations[0].Status != domain.OperOpen {
		t.Fatalf("reopen: %d %s", rec.Code, rec.Body.String())
	}
}

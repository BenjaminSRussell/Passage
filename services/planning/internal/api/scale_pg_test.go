package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
	"github.com/BenjaminSRussell/passage/services/planning/migrations"
)

// EnvAPIScaleItems turns on TestRunAPI_ScalePostgres at that many items (a
// positive multiple of 20; the issue's scale is 50000). It is skipped by
// default and also needs PASSAGE_TEST_DATABASE_URL and
// PASSAGE_TEST_APP_DATABASE_URL; see README.md "Scale run through the API".
const EnvAPIScaleItems = "PASSAGE_PLANNING_API_SCALE_ITEMS"

// EnvAPIScaleRuns is the number of runs (default 2, which also compares their
// plan hashes). Each 50k-item version takes about 4.5 GB on disk, so a host
// with less than about 10 GB free sets 1.
const EnvAPIScaleRuns = "PASSAGE_PLANNING_API_SCALE_RUNS"

// TestRunAPI_ScalePostgres is AC2 of issue #22 through the HTTP API (issue
// #479): the mrp-50k snapshot of the engine scale test (domain.ScaleSnapshot)
// is sent as one asynchronous POST /v1/planning/plans/{planCode}:run
// (Prefer: respond-async) over a loopback HTTP server with the production
// router (httpx.NewRouter, so its 30 s request timeout applies to every
// request), and the version is persisted to Postgres through the application
// role under RLS. The POST must answer 202 within the request timeout; the
// test then polls GET /plan-runs/{id} until the run succeeded. A second run
// with a fresh Idempotency-Key must store the same plan hash. Each run must
// finish within the AC2 budget of 10 min; the poll waits up to an hour, so a
// slower run still reports its time.
func TestRunAPI_ScalePostgres(t *testing.T) {
	raw := os.Getenv(EnvAPIScaleItems)
	if raw == "" {
		t.Skipf("%s not set; skipping the API scale run", EnvAPIScaleItems)
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 20 || n%20 != 0 {
		t.Fatalf("%s=%q: want a positive multiple of 20", EnvAPIScaleItems, raw)
	}
	runs := 2
	if v := os.Getenv(EnvAPIScaleRuns); v != "" {
		if runs, err = strconv.Atoi(v); err != nil || runs < 1 {
			t.Fatalf("%s=%q: want a positive integer", EnvAPIScaleRuns, v)
		}
	}
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	router := httpx.NewRouter()
	a := New(store.New(app))
	a.Register(router)
	srv := httptest.NewServer(router)
	defer srv.Close()
	defer a.StopRuns(time.Minute)

	began := time.Now()
	body, err := json.Marshal(scaleRunRequest(domain.ScaleSnapshot(n)))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d items: request body %.1f MiB, built in %s", n, float64(len(body))/(1<<20), time.Since(began).Round(time.Millisecond))

	const budget = 10 * time.Minute
	client := &http.Client{Timeout: time.Hour}
	get := func(url string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Legal-Entity", scaleLegalEntity)
		req.Header.Set("X-Passage-Duties", dutyRun)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, out
	}
	var hashes []string
	for run := 1; run <= runs; run++ {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/planning/plans/MRP-SCALE:run", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Legal-Entity", scaleLegalEntity)
		req.Header.Set("X-Passage-Duties", dutyRun)
		req.Header.Set("Prefer", "respond-async")
		req.Header.Set(idempotencyKey, fmt.Sprintf("scale-%d-%d", n, run))
		began := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("run %d: %v after %s", run, err, time.Since(began).Round(time.Millisecond))
		}
		out, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		accepted := time.Since(began)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("run %d: status %d after %s: %.500s", run, resp.StatusCode, accepted.Round(time.Millisecond), out)
		}
		location := resp.Header.Get("Location")
		t.Logf("run %d: 202 after %s, polling %s", run, accepted.Round(time.Millisecond), location)
		var pr store.PlanRun
		lastLog := time.Now()
		for {
			time.Sleep(2 * time.Second)
			code, out := get(srv.URL + location)
			if code != http.StatusOK {
				t.Fatalf("run %d: GET %s = %d: %.500s", run, location, code, out)
			}
			if err := json.Unmarshal(out, &pr); err != nil {
				t.Fatal(err)
			}
			if pr.Status != store.RunRunning {
				break
			}
			if time.Since(lastLog) >= 30*time.Second {
				t.Logf("run %d: %s, %s, %d of %d rows", run, time.Since(began).Round(time.Second), pr.Phase, pr.RowsWritten, pr.RowsTotal)
				lastLog = time.Now()
			}
			if time.Since(began) > time.Hour {
				t.Fatalf("run %d still running after an hour", run)
			}
		}
		took := time.Since(began)
		if pr.Status != store.RunSucceeded || pr.Version == nil {
			t.Fatalf("run %d: %s after %s: %+v", run, pr.Status, took.Round(time.Millisecond), pr.Problem)
		}
		v := *pr.Version
		t.Logf("run %d: %s end to end (202 after %s, engine %d ms), version %d, %d planned orders, %d messages, %d rows, hash %.16s",
			run, took.Round(time.Millisecond), accepted.Round(time.Millisecond), v.DurationMs, v.Number, v.PlannedOrders, v.Messages, pr.RowsWritten, v.PlanHash)
		if v.PlannedOrders == 0 {
			t.Fatal("the run planned no orders")
		}
		if accepted >= httpx.RequestTimeout {
			t.Errorf("run %d: 202 after %s; want it within the %s request timeout", run, accepted, httpx.RequestTimeout)
		}
		if took >= budget {
			t.Errorf("run %d took %s; want < %s", run, took, budget)
		}
		hashes = append(hashes, v.PlanHash)
	}
	for _, h := range hashes[1:] {
		if h != hashes[0] {
			t.Fatalf("plan hashes differ across API runs: %v", hashes)
		}
	}
	var orders, pegs, nets int64
	if err := admin.QueryRow(context.Background(), `select (select count(*) from planned_order), (select count(*) from pegging), (select count(*) from net_requirement)`).Scan(&orders, &pegs, &nets); err != nil {
		t.Fatal(err)
	}
	var size string
	if err := admin.QueryRow(context.Background(), `select pg_size_pretty(sum(pg_total_relation_size(c.oid))) from pg_class c join pg_namespace s on s.oid = c.relnamespace where s.nspname = current_schema() and c.relkind = 'r'`).Scan(&size); err != nil {
		t.Fatal(err)
	}
	t.Logf("stored over all versions: %d planned orders, %d pegs, %d net requirement rows, %s on disk", orders, pegs, nets, size)
}

const scaleLegalEntity = "00000000-0000-4000-8000-0000000000e1"

// scaleRunRequest puts a domain snapshot into the run request's wire shape;
// item ids become deterministic UUIDs and day offsets dates from asOf.
func scaleRunRequest(items []domain.Item) runRequest {
	const asOf = "2026-11-02"
	day0, _ := time.Parse(dateLayout, asOf)
	date := func(d int) string { return day0.AddDate(0, 0, d).Format(dateLayout) }
	uuidOf := make(map[string]string, len(items))
	for i, it := range items {
		uuidOf[it.ID] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
	}
	reqs := func(rs []domain.Requirement) []requirementDTO {
		out := make([]requirementDTO, len(rs))
		for i, r := range rs {
			prio := r.Priority
			out[i] = requirementDTO{Ref: r.Ref, RefType: r.RefType, Date: date(r.Date), Qty: r.Qty, Priority: &prio}
		}
		return out
	}
	req := runRequest{AsOf: asOf, SiteID: "00000000-0000-4000-8000-0000000051e1", LegalEntityID: scaleLegalEntity, Items: make([]itemDTO, len(items))}
	for i, it := range items {
		lead := it.LeadTimeDays
		comps := make([]bomDTO, len(it.Components))
		for j, c := range it.Components {
			comps[j] = bomDTO{ItemID: uuidOf[c.ItemID], Qty: c.Qty}
		}
		req.Items[i] = itemDTO{
			ItemID:       uuidOf[it.ID],
			Coverage:     coverageDTO{Code: it.Coverage, PeriodDays: it.PeriodDays, ReorderMin: it.ReorderMin, ReorderMax: it.ReorderMax},
			OnHand:       it.OnHand,
			SafetyStock:  it.SafetyStock,
			OrderType:    it.OrderType,
			LeadTimeDays: &lead,
			Lot:          lotDTO{MinOrderQty: it.MinOrderQty, MultipleQty: it.MultipleQty, MaxOrderQty: it.MaxOrderQty},
			Demand:       reqs(it.Demand),
			Receipts:     reqs(it.Receipts),
			Components:   comps,
		}
	}
	return req
}

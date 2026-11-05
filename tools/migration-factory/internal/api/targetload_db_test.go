package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target/targettest"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/migrations"
)

const (
	leA   = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	leB   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	custA = "11111111-1111-4111-8111-111111111111"
	custB = "22222222-2222-4222-8222-222222222222"
)

type loadEnv struct {
	h     http.Handler
	admin *pgxpool.Pool
	fake  *targettest.Fake
	st    *store.Postgres
	tgt   *target.Receivables
}

// newLoadEnv serves the API over a migrated schema with the real receivables client pointed at the fake.
func newLoadEnv(t *testing.T, attempts int, mutate func(*target.Config)) loadEnv {
	t.Helper()
	var admin, app *pgxpool.Pool
	if os.Getenv(testkit.EnvAppURL) != "" {
		admin, app = testkit.DBWithApp(t)
	} else {
		admin = testkit.DB(t)
		app = admin
	}
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	fake := targettest.New(t)
	cfg := target.Config{BaseURL: fake.URL, Signer: targettest.HeaderSigner{Name: "migration-factory"}, Attempts: attempts, Backoff: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }}
	if mutate != nil {
		mutate(&cfg)
	}
	st := store.New(app)
	tgt := target.NewReceivables(cfg)
	return loadEnv{h: New(st).WithTarget(tgt).Routes(), admin: admin, fake: fake, st: st, tgt: tgt}
}

func (e loadEnv) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, e.h, request{method: method, path: path, tenant: tenantA, duties: allDuties, le: leA, body: body})
}

// start posts an extract and returns the new run.
func (e loadEnv) start(t *testing.T, body string) runDetailDTO {
	t.Helper()
	rec := e.do(t, "POST", runsPath, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start run: %d %s", rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec)
}

// load posts the load step of a run and returns the load summary.
func (e loadEnv) load(t *testing.T, runID string) targetLoadDTO {
	t.Helper()
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+runID+"/load", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("load: %d %s", rec.Code, rec.Body)
	}
	return decode[targetLoadDTO](t, rec)
}

func (e loadEnv) detail(t *testing.T, runID string) targetLoadDetailDTO {
	t.Helper()
	rec := e.do(t, "GET", "/api/v1/migration/runs/"+runID+"/target-load", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("target-load: %d %s", rec.Code, rec.Body)
	}
	return decode[targetLoadDetailDTO](t, rec)
}

func (e loadEnv) scalar(t *testing.T, sql string, args ...any) int {
	t.Helper()
	return count(t, e.admin, sql, args...)
}

// loadRecord is one AR invoice of the loadBody extract.
func loadRecord(id, amount string, extra string) string {
	return fmt.Sprintf(`{"legacyId":%q,"party":"C001","account":"1100","currency":"USD","amount":%q,"documentDate":"2026-01-05","dueDate":"2026-02-04"%s}`, id, amount, extra)
}

func loadBody(records []string, loadExtra string) string {
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","parties":["C001","V001"],`+
		`"load":{"target":"receivables","legalEntityId":%q,"customerIds":{"C001":%q}%s},"records":[%s]}`, leA, custA, loadExtra, strings.Join(records, ","))
}

func nRecords(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = loadRecord(fmt.Sprintf("OI-%05d", i), "10.25", "")
	}
	return out
}

func TestOpenItemsLoad_SendsInBatchesOfAThousandAndReconciles(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(2500), ""))
	if run.Status != "RECONCILED" || run.TargetLoad == nil || run.TargetLoad.Status != "STAGED" || run.TargetLoad.Staged != 2500 || run.TargetLoad.Pending != 2500 ||
		run.TargetLoad.ImportSource != "ax2012.usmf" || run.TargetLoad.LegalEntityID != leA {
		t.Fatalf("run %+v load %+v", run.runDTO, run.TargetLoad)
	}
	if len(e.fake.Calls()) != 0 {
		t.Fatal("starting a run must not call the target")
	}
	got := e.load(t, run.RunID)
	if got.Status != "RECONCILED" || !got.Clean || got.Created != 2500 || got.Pending != 0 || got.Rejected != 0 || got.Attempts != 1 {
		t.Fatalf("load %+v", got)
	}
	batches := e.fake.Batches()
	if len(batches) != 3 || batches[0].Rows != 1000 || batches[1].Rows != 1000 || batches[2].Rows != 500 {
		t.Fatalf("batches %+v", batches)
	}
	keys := map[string]bool{}
	for _, b := range batches {
		if !strings.HasPrefix(b.IdempotencyKey, "mf-load-"+run.RunID+"-") {
			t.Fatalf("idempotency key %q", b.IdempotencyKey)
		}
		keys[b.IdempotencyKey] = true
	}
	if len(keys) != 3 {
		t.Fatalf("each chunk has its own key: %v", keys)
	}
	if e.fake.Held("ax2012.usmf") != 2500 {
		t.Fatalf("target holds %d", e.fake.Held("ax2012.usmf"))
	}
	d := e.detail(t, run.RunID)
	if len(d.Reconciliation) != 4 || len(d.Rejects) != 0 {
		t.Fatalf("detail %+v", d)
	}
	for _, c := range d.Reconciliation {
		if c.Status != "MATCH" {
			t.Fatalf("reconciliation line %+v", c)
		}
		if c.Name == "target.receivables.debit_open" && (c.Source != "25625.000000" || c.Target != "25625.000000") {
			t.Fatalf("debit total %+v", c)
		}
	}
	// The open-item id the target assigned is in legacy_xref, once.
	id, _ := e.fake.Item("ax2012.usmf", "OI-00042")
	if n := e.scalar(t, `select count(*) from legacy_xref where source_id = 'OI-00042' and passage_id = $1::uuid and target_confirmed`, id); n != 1 {
		t.Fatalf("legacy_xref does not hold the target id %s", id)
	}
	if n := e.scalar(t, `select count(*) from legacy_xref where target_confirmed`); n != 2500 {
		t.Fatalf("%d confirmed xrefs", n)
	}
	x := e.do(t, "GET", "/api/v1/migration/xref?system=AX2012&company=USMF&table=open_items&id=OI-00042", "")
	if x.Code != 200 || !strings.Contains(x.Body.String(), id) || !strings.Contains(x.Body.String(), `"targetConfirmed":true`) {
		t.Fatalf("xref lookup %d %s", x.Code, x.Body)
	}
	if n := e.scalar(t, `select count(*) from outbox where type = 'dev.passage.migration.pack.loaded.v1' and aggregate_id = $1`, run.RunID); n != 1 {
		t.Fatalf("pack.loaded events = %d", n)
	}
	var raw []byte
	if err := e.admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.migration.pack.loaded.v1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Data struct {
			Status  string `json:"status"`
			Created int    `json:"created"`
			Match   int    `json:"totals_match"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Data.Status != "RECONCILED" || ev.Data.Created != 2500 || ev.Data.Match != 4 {
		t.Fatalf("event %s (%v)", raw, err)
	}
	if rd := decode[runDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID, "")); rd.TargetLoad == nil || rd.TargetLoad.Status != "RECONCILED" || len(rd.ControlTotals) < 4 {
		t.Fatalf("run detail %+v", rd.TargetLoad)
	}
}

func TestOpenItemsLoad_RerunOfAnUnchangedSnapshotLoadsNothing(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	body := loadBody(nRecords(30), "")
	first := e.start(t, body)
	if got := e.load(t, first.RunID); got.Created != 30 || got.Status != "RECONCILED" {
		t.Fatalf("first load %+v", got)
	}
	callsAfterFirst := len(e.fake.Calls())

	second := e.start(t, body)
	if second.RunID == first.RunID || second.RecordsLoaded != 0 || second.AlreadyPresent != 30 {
		t.Fatalf("rerun %+v", second.runDTO)
	}
	got := e.load(t, second.RunID)
	if got.Skipped != 30 || got.Created != 0 || got.Updated != 0 || got.Status != "RECONCILED" || !got.Clean {
		t.Fatalf("rerun load %+v", got)
	}
	posts := 0
	for _, c := range e.fake.Calls()[callsAfterFirst:] {
		if c.Method == http.MethodPost {
			posts++
		}
	}
	if posts != 0 {
		t.Fatalf("an unchanged snapshot sent %d batches", posts)
	}
	// Loading the same run again is a no-op too.
	if again := e.load(t, second.RunID); again.Skipped != 30 || again.Attempts != 2 {
		t.Fatalf("second pass %+v", again)
	}

	// One item changed in the legacy ledger: only it is sent, as an update.
	changed := nRecords(30)
	changed[7] = loadRecord("OI-00007", "9.00", "")
	third := e.start(t, loadBody(changed, ""))
	got = e.load(t, third.RunID)
	if got.Updated != 1 || got.Skipped != 29 || got.Created != 0 || got.Status != "RECONCILED" {
		t.Fatalf("delta load %+v", got)
	}
	if last := e.fake.Batches(); last[len(last)-1].Rows != 1 {
		t.Fatalf("delta batch %+v", last[len(last)-1])
	}
	if n := e.scalar(t, `select count(*) from legacy_xref`); n != 30 {
		t.Fatalf("legacy_xref has %d rows", n)
	}
}

func TestOpenItemsLoad_RetriesTransientFailuresThenResumes(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(5), ""))

	// Two failures inside the attempts: the load still completes.
	e.fake.FailNext(http.StatusServiceUnavailable, http.StatusTooManyRequests)
	got := e.load(t, run.RunID)
	if got.Status != "RECONCILED" || got.Created != 5 {
		t.Fatalf("load after retries %+v", got)
	}
	calls := e.fake.Calls()
	if len(calls) < 4 || calls[0].Status != 503 || calls[1].Status != 429 || calls[2].Status != 201 {
		t.Fatalf("calls %+v", calls)
	}
	if calls[0].IdempotencyKey == "" || calls[0].IdempotencyKey != calls[1].IdempotencyKey || calls[1].IdempotencyKey != calls[2].IdempotencyKey {
		t.Fatalf("the retries must reuse the Idempotency-Key: %+v", calls[:3])
	}

	// The target stays down past the attempts: rows stay pending and the pass is PARTIAL.
	run2 := e.start(t, loadBody(append(nRecords(5), loadRecord("NEW-1", "3.00", "")), ""))
	e.fake.FailNext(503, 503, 503)
	got = e.load(t, run2.RunID)
	if got.Status != "PARTIAL" || got.Pending != 1 || got.Skipped != 5 || got.Clean || !strings.Contains(got.LastError, "gave up after 3 attempts") {
		t.Fatalf("outage pass %+v", got)
	}
	if e.fake.Held("ax2012.usmf") != 5 {
		t.Fatal("nothing from the failed batch may be stored")
	}
	failedKey := e.fake.Calls()[len(e.fake.Calls())-1].IdempotencyKey
	got = e.load(t, run2.RunID)
	if got.Status != "RECONCILED" || got.Pending != 0 || got.Created != 1 || got.Attempts != 2 {
		t.Fatalf("resumed pass %+v", got)
	}
	if last := e.fake.Batches(); last[len(last)-1].IdempotencyKey != failedKey {
		t.Fatalf("the resumed batch has a different key than the one that failed: %q vs %q", last[len(last)-1].IdempotencyKey, failedKey)
	}
}

func TestOpenItemsLoad_RecordsTargetRejectsAndOpenAPAsNotAvailable(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	records := []string{
		loadRecord("OK-1", "100.00", ""),
		loadRecord("BAD-AMT", "10.00", `,"originalAmount":"5.00"`), // open amount above the original: the target refuses it
		loadRecord("OK-2", "-40.00", ""),                           // credit side
		fmt.Sprintf(`{"legacyId":"AP-1","party":"V001","account":"2100","currency":"USD","amount":"-300.00","ledger":"AP","documentDate":"2026-01-01","dueDate":"2026-02-01"}`),
		`{"legacyId":"NODATE","party":"C001","account":"1100","currency":"USD","amount":"1.00"}`,
		loadRecord("OI-DUP", "2.00", ""),
	}
	run := e.start(t, loadBody(records, ""))
	got := e.load(t, run.RunID)
	if got.Status != "RECONCILED" || got.Clean || got.Created != 3 || got.Rejected != 2 || got.NotAvailable != 1 || got.Pending != 0 {
		t.Fatalf("load %+v", got)
	}
	d := e.detail(t, run.RunID)
	byID := map[string]targetRowDTO{}
	for _, r := range d.Rejects {
		byID[r.LegacyID] = r
	}
	if r := byID["BAD-AMT"]; r.Code != "AR-OPENING-INVALID" || r.Status != "REJECTED" || !strings.Contains(r.Detail, "openAmount") {
		t.Fatalf("target refusal %+v", byID)
	}
	if r := byID["NODATE"]; r.Code != "MIG-LOAD-FIELD-REQUIRED" || r.Status != "REJECTED" {
		t.Fatalf("local refusal %+v", byID)
	}
	if r := byID["AP-1"]; r.Code != "MIG-TARGET-NOT-AVAILABLE" || r.Status != "NOT_AVAILABLE" || r.Ledger != "AP" {
		t.Fatalf("open AP %+v", byID)
	}
	// Reconciliation covers what was accepted: 100.00 + 2.00 debit, 40.00 credit.
	for _, c := range d.Reconciliation {
		if c.Status != "MATCH" {
			t.Fatalf("line %+v", c)
		}
		if c.Name == "target.receivables.net_open" && c.Target != "62.000000" {
			t.Fatalf("net %+v", c)
		}
	}
	// Rejected and unavailable rows keep their provisional xref id.
	if n := e.scalar(t, `select count(*) from legacy_xref where not target_confirmed`); n != 3 {
		t.Fatalf("%d unconfirmed xrefs, want 3 (BAD-AMT, AP-1, NODATE)", n)
	}
	rows := e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/target-rows?status=REJECTED&limit=1", "")
	var page struct {
		Items []targetRowDTO `json:"items"`
	}
	if err := json.Unmarshal(rows.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("rows page %d %s", rows.Code, rows.Body)
	}
	next := e.do(t, "GET", fmt.Sprintf("/api/v1/migration/runs/%s/target-rows?status=REJECTED&after=%d", run.RunID, page.Items[0].Seq), "")
	if !strings.Contains(next.Body.String(), "NODATE") || strings.Contains(next.Body.String(), page.Items[0].LegacyID) {
		t.Fatalf("second page %s", next.Body)
	}

	// A key settled in Passage: the next snapshot's change is refused with the target's code, and the pack is not clean.
	e.fake.Settled["ax2012.usmf|OK-1"] = true
	run2 := e.start(t, loadBody([]string{loadRecord("OK-1", "90.00", "")}, ""))
	got = e.load(t, run2.RunID)
	if got.Rejected != 1 || got.Clean {
		t.Fatalf("settled item %+v", got)
	}
	if r := e.detail(t, run2.RunID).Rejects; len(r) != 1 || r[0].Code != "AR-OPENING-SETTLED" {
		t.Fatalf("rejects %+v", r)
	}
	if got.Status != "RECONCILED" {
		t.Fatalf("a refused update leaves the target as the factory knows it: %+v", got)
	}
}

func TestOpenItemsLoad_ReconciliationMismatchFlagsThePack(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(10), ""))
	e.fake.Drift = mustDec("0.01")
	got := e.load(t, run.RunID)
	if got.Status != "MISMATCH" || got.Clean {
		t.Fatalf("a cent of drift must flag the pack: %+v", got)
	}
	d := e.detail(t, run.RunID)
	mismatch := map[string]totalDTO{}
	for _, c := range d.Reconciliation {
		if c.Status == "MISMATCH" {
			mismatch[c.Name] = c
		}
	}
	if len(mismatch) != 2 || mismatch["target.receivables.debit_open"].Delta != "0.010000" || mismatch["target.receivables.net_open"].Source != "102.500000" {
		t.Fatalf("mismatches %+v", mismatch)
	}
	var raw []byte
	if err := e.admin.QueryRow(context.Background(), `select payload from outbox where type = 'dev.passage.migration.pack.loaded.v1'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var ev struct {
		Data struct {
			Status   string `json:"status"`
			Mismatch int    `json:"totals_mismatch"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Data.Status != "MISMATCH" || ev.Data.Mismatch != 2 {
		t.Fatalf("event %s (%v)", raw, err)
	}
	// Fixed at the target: the next pass replaces the lines in place.
	e.fake.Drift = mustDec("0")
	got = e.load(t, run.RunID)
	if got.Status != "RECONCILED" || !got.Clean {
		t.Fatalf("after the fix %+v", got)
	}
	if n := e.scalar(t, `select count(*) from control_total where run_id = $1 and name like 'target.%'`, run.RunID); n != 4 {
		t.Fatalf("%d target control totals, want 4 (replaced, not appended)", n)
	}
	if n := e.scalar(t, `select count(*) from control_total where run_id = $1 and name like 'target.%' and status = 'MISMATCH'`, run.RunID); n != 0 {
		t.Fatal("stale mismatch left behind")
	}
}

func TestOpenItemsLoad_NotConfiguredAnswersClearly(t *testing.T) {
	h, _ := dbServer(t) // no target set
	body := loadBody(nRecords(3), "")
	rec := do(t, h, request{method: "POST", path: runsPath, tenant: tenantA, duties: allDuties, le: leA, body: body})
	if rec.Code != http.StatusCreated {
		t.Fatalf("a run can be staged without a target: %d %s", rec.Code, rec.Body)
	}
	run := decode[runDetailDTO](t, rec)
	load := do(t, h, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/load", tenant: tenantA, duties: allDuties, le: leA})
	if load.Code != http.StatusServiceUnavailable || problemCode(t, load) != CodeTargetNotConfigured || !strings.Contains(load.Body.String(), "RECEIVABLES_URL") {
		t.Fatalf("load: %d %s", load.Code, load.Body)
	}
	if d := decode[targetLoadDetailDTO](t, do(t, h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/target-load", tenant: tenantA, duties: allDuties, le: leA})); d.Status != "STAGED" || d.Pending != 3 {
		t.Fatalf("still staged: %+v", d.targetLoadDTO)
	}
}

func TestOpenItemsLoad_TargetRefusesTheFactory(t *testing.T) {
	e := newLoadEnv(t, 3, func(c *target.Config) { c.Signer = readOnlySigner{} })
	run := e.start(t, loadBody(nRecords(3), ""))
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/load", "")
	if rec.Code != http.StatusBadGateway || problemCode(t, rec) != CodeTargetDenied {
		t.Fatalf("load: %d %s", rec.Code, rec.Body)
	}
	d := e.detail(t, run.RunID)
	if d.Status != "FAILED" || d.Pending != 3 || d.Rejected != 0 || !strings.Contains(d.LastError, "refused") {
		t.Fatalf("a denied token is a configuration problem, not 3 rejects: %+v", d.targetLoadDTO)
	}
	if e.scalar(t, `select count(*) from target_loaded`) != 0 {
		t.Fatal("nothing was loaded")
	}
}

func TestOpenItemsLoad_UnreadableTotalsAreNotReported_AsReconciled(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	e.fake.DenyImports = true
	run := e.start(t, loadBody(nRecords(3), ""))
	got := e.load(t, run.RunID)
	if got.Status != "UNRECONCILED" || got.Clean || got.Created != 3 || got.LastError == "" {
		t.Fatalf("%+v", got)
	}
	e.fake.DenyImports = false
	posts := len(e.fake.Batches())
	if got = e.load(t, run.RunID); got.Status != "RECONCILED" || got.Created != 3 || !got.Clean || len(e.fake.Batches()) != posts {
		t.Fatalf("second pass reads the totals and sends nothing: %+v", got)
	}
}

func TestOpenItemsLoad_RequestRules(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	good := nRecords(1)
	cases := map[string]string{
		"no legal entity":     strings.Replace(loadBody(good, ""), leA, "", 1),
		"bad target":          strings.Replace(loadBody(good, ""), `"target":"receivables"`, `"target":"payables"`, 1),
		"bad import source":   loadBody(good, `,"importSource":"has space"`),
		"long import source":  loadBody(good, `,"importSource":"`+strings.Repeat("x", 41)+`"`),
		"bad customer map":    strings.Replace(loadBody(good, ""), custA, "C-1", 1),
		"bad ledger":          loadBody([]string{loadRecord("A", "1", `,"ledger":"GL"`)}, ""),
		"bad original amount": loadBody([]string{loadRecord("A", "1", `,"originalAmount":"1e3"`)}, ""),
		"unknown field":       loadBody([]string{loadRecord("A", "1", `,"colour":"red"`)}, ""),
		"fields without load": `{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","parties":["C001"],"records":[` + loadRecord("A", "1", "") + `]}`,
	}
	for name, body := range cases {
		if rec := e.do(t, "POST", runsPath, body); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s", name, rec.Code, rec.Body)
		}
	}
	// A legal entity outside the caller's scope.
	other := strings.Replace(loadBody(good, ""), leA, leB, 1)
	if rec := e.do(t, "POST", runsPath, other); rec.Code != http.StatusForbidden {
		t.Errorf("foreign legal entity: %d %s", rec.Code, rec.Body)
	}
	// No receivables fields and no load object is the old extract, untouched.
	plain := e.do(t, "POST", runsPath, openItemsBody(2))
	if plain.Code != http.StatusCreated || decode[runDetailDTO](t, plain).TargetLoad != nil {
		t.Fatalf("plain run %d %s", plain.Code, plain.Body)
	}
	run := decode[runDetailDTO](t, plain)
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/load", ""); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeNoTarget {
		t.Fatalf("load of a run with no target: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/target-load", ""); rec.Code != http.StatusConflict {
		t.Fatalf("target-load of a run with no target: %d", rec.Code)
	}
	if rec := e.do(t, "GET", "/api/v1/migration/runs/00000000-0000-7000-8000-0000000000ff/target-load", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown run: %d", rec.Code)
	}
	if rec := e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/target-rows?status=NOPE", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status filter: %d", rec.Code)
	}
}

func TestOpenItemsLoad_OneLoadAtATimeAndScopedToTenantAndLegalEntity(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(3), ""))
	// Another request holds the lease.
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, LegalEntityID: leA, UserID: "u1", Duties: map[string]struct{}{}})
	if _, _, err := e.st.ClaimTargetLoad(ctx, run.RunID, "u1", time.Minute); err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/load", "")
	if rec.Code != http.StatusConflict || problemCode(t, rec) != CodeLoadBusy {
		t.Fatalf("busy: %d %s", rec.Code, rec.Body)
	}
	if _, err := e.admin.Exec(context.Background(), `update target_load set lease_until = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if got := e.load(t, run.RunID); got.Status != "RECONCILED" {
		t.Fatalf("an expired lease can be taken over: %+v", got)
	}
	// Another tenant, and another legal entity of the same tenant, see nothing.
	foreign := do(t, e.h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/target-load", tenant: tenantB, duties: allDuties, le: leA})
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("other tenant: %d %s", foreign.Code, foreign.Body)
	}
	if os.Getenv(testkit.EnvAppURL) != "" {
		wrongLE := do(t, e.h, request{method: "GET", path: "/api/v1/migration/runs/" + run.RunID + "/target-load", tenant: tenantA, duties: allDuties, le: leB})
		if wrongLE.Code != http.StatusConflict { // the run is visible, its load (held to legal entity A) is not
			t.Fatalf("other legal entity: %d %s", wrongLE.Code, wrongLE.Body)
		}
	}
}

func TestLegacyXref_OnlyTheTargetIdConfirmationMayUpdateARow(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(2), ""))
	ctx := context.Background()
	for name, sql := range map[string]string{
		"change the amount":    `update legacy_xref set amount = 1 where source_id = 'OI-00000'`,
		"change the legacy id": `update legacy_xref set source_id = 'X' where source_id = 'OI-00000'`,
		"confirm without id":   `update legacy_xref set target_confirmed = false where source_id = 'OI-00000'`,
		"move to another run":  fmt.Sprintf(`update legacy_xref set run_id = '%s', target_confirmed = true where source_id = 'OI-00000'`, "00000000-0000-7000-8000-0000000000ee"),
	} {
		if _, err := e.admin.Exec(ctx, sql); err == nil {
			t.Errorf("%s: the update was allowed", name)
		}
	}
	e.load(t, run.RunID)
	if _, err := e.admin.Exec(ctx, `update legacy_xref set passage_id = gen_random_uuid() where source_id = 'OI-00000'`); err == nil {
		t.Error("the target id may be confirmed once, not changed again")
	}
}

// readOnlySigner signs tokens that hold the read duty only, as a grant table without receivables.import.write would.
type readOnlySigner struct{}

func (readOnlySigner) ApplyDuties(req *http.Request, p tenant.Principal, aud, legalEntity string, _ []string) (bool, error) {
	return targettest.HeaderSigner{Name: "migration-factory"}.ApplyDuties(req, p, aud, legalEntity, []string{"receivables.view"})
}

func mustDec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// flakyRecordStore loses the factory's record of the next chunks, as a crash between the target's answer and the
// factory's write would.
type flakyRecordStore struct {
	*store.Postgres
	fail int
}

func (f *flakyRecordStore) RecordTargetChunk(ctx context.Context, runID string, lease time.Duration, outs []store.RowOutcome) error {
	if f.fail > 0 {
		f.fail--
		return errors.New("the factory database went away")
	}
	return f.Postgres.RecordTargetChunk(ctx, runID, lease, outs)
}

func TestOpenItemsLoad_CrashAfterTheTargetStoredTheBatchIsRecoveredByTheNextLoad(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(4), ""))
	crashing := New(&flakyRecordStore{Postgres: e.st, fail: 1}).WithTarget(e.tgt).Routes()
	rec := do(t, crashing, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/load", tenant: tenantA, duties: allDuties, le: leA})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("crashed load: %d %s", rec.Code, rec.Body)
	}
	if e.fake.Held("ax2012.usmf") != 4 {
		t.Fatal("the target stored the batch before the crash")
	}
	// The pass was ended, not left holding the lease, and the rows are still pending.
	if d := e.detail(t, run.RunID); d.Status != "PARTIAL" || d.Pending != 4 || d.Created != 0 {
		t.Fatalf("after the crash %+v", d.targetLoadDTO)
	}
	if n := e.scalar(t, `select count(*) from legacy_xref where target_confirmed`); n != 0 {
		t.Fatalf("%d xrefs confirmed by a pass that was not recorded", n)
	}
	got := e.load(t, run.RunID)
	if got.Status != "RECONCILED" || !got.Clean || got.Unchanged != 4 || got.Created != 0 || got.Pending != 0 {
		t.Fatalf("the resend must come back UNCHANGED and reconcile: %+v", got)
	}
	if e.fake.Held("ax2012.usmf") != 4 {
		t.Fatal("the resend duplicated items")
	}
	id, _ := e.fake.Item("ax2012.usmf", "OI-00003")
	if n := e.scalar(t, `select count(*) from legacy_xref where source_id = 'OI-00003' and passage_id = $1::uuid and target_confirmed`, id); n != 1 {
		t.Fatal("legacy_xref does not hold the id the target assigned before the crash")
	}
}

func TestOpenItemsLoad_LostOrGarbledAnswersKeepTheRowsPending(t *testing.T) {
	e := newLoadEnv(t, 2, nil)
	run := e.start(t, loadBody(nRecords(3), ""))
	// The target stored the rows but the answer was lost: the retry carries the same key and is answered UNCHANGED.
	e.fake.LoseAnswers = 1
	got := e.load(t, run.RunID)
	if got.Status != "RECONCILED" || got.Unchanged != 3 || got.Rejected != 0 {
		t.Fatalf("lost answer %+v", got)
	}
	calls := e.fake.Calls()
	if calls[0].Status != http.StatusGatewayTimeout || calls[1].IdempotencyKey != calls[0].IdempotencyKey {
		t.Fatalf("calls %+v", calls)
	}

	// A 2xx that does not describe the batch is not a reject of every row: they stay pending for the next load.
	run2 := e.start(t, loadBody(append(nRecords(3), loadRecord("NEW-1", "1.00", "")), ""))
	e.fake.GarbleAnswers = 1
	got = e.load(t, run2.RunID)
	if got.Status != "PARTIAL" || got.Pending != 1 || got.Rejected != 0 || !strings.Contains(got.LastError, "does not belong") && !strings.Contains(got.LastError, "results for") {
		t.Fatalf("garbled answer %+v", got)
	}
	if got = e.load(t, run2.RunID); got.Status != "RECONCILED" || got.Unchanged != 1 || got.Pending != 0 {
		t.Fatalf("after a garbled answer the row is resent: %+v", got)
	}
}

func TestOpenItemsLoad_WholeRequestRefusalFailsTheLoadAndKeepsTheRows(t *testing.T) {
	e := newLoadEnv(t, 3, func(c *target.Config) { c.BaseURL += "/wrong-prefix" })
	run := e.start(t, loadBody(nRecords(3), ""))
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/load", "")
	if rec.Code != http.StatusBadGateway || problemCode(t, rec) != CodeTargetRefusedBatch {
		t.Fatalf("load: %d %s", rec.Code, rec.Body)
	}
	if d := e.detail(t, run.RunID); d.Status != "FAILED" || d.Pending != 3 || d.Rejected != 0 {
		t.Fatalf("a misrouted request is not 3 rejects: %+v", d.targetLoadDTO)
	}
}

func TestOpenItemsLoad_ReconciliationComparesTheWholeImportSource(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	first := e.start(t, loadBody(nRecords(5), ""))
	if got := e.load(t, first.RunID); got.Status != "RECONCILED" {
		t.Fatalf("first %+v", got)
	}
	// A second run of the same company adds items under the same import source. receivables totals the whole
	// import source, and so does the expected side: the second run reconciles with both runs' items.
	second := e.start(t, loadBody(append(nRecords(5), loadRecord("NEW-1", "4.00", "")), ""))
	got := e.load(t, second.RunID)
	if got.Status != "RECONCILED" || got.Created != 1 || got.Skipped != 5 {
		t.Fatalf("second %+v", got)
	}
	for _, c := range e.detail(t, second.RunID).Reconciliation {
		if c.Name == "target.receivables.item_count" && (c.Source != "6.000000" || c.Target != "6.000000") {
			t.Fatalf("count %+v", c)
		}
	}

	// The first run is now an older snapshot: loading it again would write it back over the second.
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+first.RunID+"/load", ""); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeLoadSuperseded {
		t.Fatalf("older run: %d %s", rec.Code, rec.Body)
	}

	// Another run of the same import source and legal entity while this one is loading: refused, so neither reads
	// the target's totals in the middle of the other's batches.
	third := e.start(t, loadBody(nRecords(5), ""))
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, LegalEntityID: leA, UserID: "u1", Duties: map[string]struct{}{}})
	if _, _, err := e.st.ClaimTargetLoad(ctx, second.RunID, "u1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+third.RunID+"/load", ""); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeLoadBusy {
		t.Fatalf("concurrent load of the same import source: %d %s", rec.Code, rec.Body)
	}

	// Another legacy company may not load under the same import source: the keys and totals would mix.
	otherCompany := strings.Replace(loadBody(nRecords(1), `,"importSource":"ax2012.usmf"`), `"sourceCompany":"USMF"`, `"sourceCompany":"DEMF"`, 1)
	if rec := e.do(t, "POST", runsPath, otherCompany); rec.Code != http.StatusConflict || problemCode(t, rec) != CodeImportSourceTaken {
		t.Fatalf("other company on the same import source: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "POST", runsPath, strings.Replace(loadBody(nRecords(1), ""), `"sourceCompany":"USMF"`, `"sourceCompany":"DEMF"`, 1)); rec.Code != http.StatusCreated {
		t.Fatalf("other company on its own import source: %d %s", rec.Code, rec.Body)
	}
}

func TestOpenItemsLoad_TotalsBeyondOnePageAreUnreconciled(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	e.fake.ExtraGroups = 500
	run := e.start(t, loadBody(nRecords(2), ""))
	if got := e.load(t, run.RunID); got.Status != "UNRECONCILED" || !strings.Contains(got.LastError, "cannot be read completely") {
		t.Fatalf("a full page of import groups must not be compared: %+v", got)
	}
}

func TestOpenItemsLoad_StagedRowsAreHeldToTheLegalEntity(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skip("row-level security needs the passage_app role (" + testkit.EnvAppURL + ")")
	}
	e := newLoadEnv(t, 3, nil)
	run := e.start(t, loadBody(nRecords(3), ""))
	inB := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, LegalEntityID: leB, UserID: "u2", Duties: map[string]struct{}{}})
	rows, err := e.st.ListTargetRows(inB, run.RunID, store.TargetRowFilter{Limit: 50})
	if err != nil || len(rows) != 0 {
		t.Fatalf("legal entity B reads %d staged rows of A (%v)", len(rows), err)
	}
	inA := tenant.With(context.Background(), tenant.Principal{TenantID: tenantA, LegalEntityID: leA, UserID: "u1", Duties: map[string]struct{}{}})
	if rows, err := e.st.ListTargetRows(inA, run.RunID, store.TargetRowFilter{Limit: 50}); err != nil || len(rows) != 3 {
		t.Fatalf("legal entity A reads %d rows (%v)", len(rows), err)
	}
}

// H-S8-3: an open_items run that holds payables lines is staged for them in the go/no-go, a run of
// receivables lines only is not; the readiness lists the pack as staged only in the first case.
func TestOpenItemsLoad_PayablesLinesMakeThePackStagedInTheGoNoGo(t *testing.T) {
	e := newLoadEnv(t, 3, nil)
	plan := func() planDTO {
		body := `{"t0":"2027-01-01T00:00:00Z","packs":["open_items"],"entities":["USMF"],"abortOffsetHours":36}`
		return decode[planDTO](t, do(t, e.h, request{method: "POST", path: "/api/v1/cutover/plans/generate", tenant: tenantA, duties: "migration.cutover.manage", body: body, user: "manager"}))
	}()
	readiness := func() readinessDTO {
		r := do(t, e.h, request{method: "GET", path: "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go", tenant: tenantA, duties: "migration.cutover.manage"})
		if r.Code != http.StatusOK {
			t.Fatalf("readiness %d %s", r.Code, r.Body)
		}
		return decode[readinessDTO](t, r)
	}

	run := e.start(t, loadBody([]string{loadRecord("OK-1", "100.00", "")}, ""))
	e.load(t, run.RunID)
	if rd := readiness(); len(rd.StagedPacks) != 0 {
		t.Fatalf("receivables only: %+v", rd.StagedPacks)
	}

	ap := `{"legacyId":"AP-1","party":"V001","account":"2100","currency":"USD","amount":"-300.00","ledger":"AP","documentDate":"2026-01-01","dueDate":"2026-02-01"}`
	run = e.start(t, loadBody([]string{loadRecord("OK-2", "50.00", ""), ap}, ""))
	e.load(t, run.RunID)
	rd := readiness()
	if len(rd.StagedPacks) != 1 || rd.StagedPacks[0].Pack != "open_items" || !strings.Contains(rd.StagedPacks[0].Reason, "payables") {
		t.Fatalf("mixed run: %+v", rd.StagedPacks)
	}
	// A re-run of the same extract stages the payables lines again (they are re-staged with every
	// accepted record), so the pack stays staged; the count is the run's AP rows.
	run = e.start(t, loadBody([]string{loadRecord("OK-2", "50.00", ""), ap}, ""))
	e.load(t, run.RunID)
	if rd := readiness(); len(rd.StagedPacks) != 1 || rd.StagedPacks[0].Pack != "open_items" {
		t.Fatalf("re-run of the mixed extract: %+v", rd.StagedPacks)
	}
	if n := count(t, e.admin, `select payables_staged from pack_run where run_id = $1`, run.RunID); n != 1 {
		t.Fatalf("payables_staged = %d, want 1", n)
	}
	// A run stored before the count was kept (NULL) is staged until backfilled; the 0014 backfill
	// counts its AP rows from target_row.
	if _, err := e.admin.Exec(context.Background(), `update pack_run set payables_staged = null where run_id = $1`, run.RunID); err != nil {
		t.Fatal(err)
	}
	if rd := readiness(); len(rd.StagedPacks) != 1 || !strings.Contains(rd.StagedPacks[0].Reason, "before payables lines were counted") {
		t.Fatalf("uncounted run: %+v", rd.StagedPacks)
	}
	backfill, err := fs.ReadFile(migrations.FS, "0014_payables_staged_backfill.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(context.Background(), e.admin, func(tx pgx.Tx) error { _, err := tx.Exec(context.Background(), string(backfill)); return err }); err != nil {
		t.Fatal(err)
	}
	if n := count(t, e.admin, `select payables_staged from pack_run where run_id = $1`, run.RunID); n != 1 {
		t.Fatalf("backfilled payables_staged = %d, want 1", n)
	}
	if n := count(t, e.admin, `select count(*) from pack_run where payables_staged is null`); n != 0 {
		t.Fatalf("%d runs left uncounted", n)
	}
	// A waiver for the pack is accepted as a waiver; the GO is refused only for the other open items.
	w := `{"decision":"GO","waivers":[{"pack":"open_items","reason":"AP opened by hand in payables"}]}`
	r := do(t, e.h, request{method: "POST", path: "/api/v1/cutover/plans/" + plan.PlanID + "/go-no-go", tenant: tenantA, duties: "migration.cutover.manage", user: "alice", key: "k-waive-ap", body: w})
	if r.Code != http.StatusUnprocessableEntity || strings.Contains(r.Body.String(), "nothing to waive") || !strings.Contains(r.Body.String(), "checklist items not met") {
		t.Fatalf("waiver: %d %s", r.Code, r.Body)
	}
	// packs_reconciled stays open: the waiver excuses the staging, not a run older than the legacy freeze.
	if !strings.Contains(r.Body.String(), "packs_reconciled") {
		t.Fatalf("packs_reconciled must stay open for a run before the freeze: %s", r.Body)
	}
}

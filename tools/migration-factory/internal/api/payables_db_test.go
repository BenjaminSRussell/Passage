package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target/targettest"
)

const (
	vendA = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	vendB = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
)

type payEnv struct {
	loadEnv
	pay  *targettest.FakePayables
	ptgt *target.Payables
}

// newPayEnv serves the API with the receivables fake and the payables fake behind their real clients.
func newPayEnv(t *testing.T, attempts int) payEnv {
	t.Helper()
	e := newLoadEnv(t, attempts, nil)
	pay := targettest.NewPayables(t)
	pay.Vendors = map[string]bool{vendA: true}
	ptgt := target.NewPayables(target.Config{BaseURL: pay.URL, Signer: targettest.HeaderSigner{Name: "migration-factory"}, Attempts: attempts, Backoff: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }})
	e.h = New(e.st).WithTarget(e.tgt).WithPayables(ptgt).Routes()
	return payEnv{loadEnv: e, pay: pay, ptgt: ptgt}
}

func apRecordJSON(id, amount string, extra string) string {
	return fmt.Sprintf(`{"legacyId":%q,"party":"V001","account":"2100","currency":"USD","amount":%q,"ledger":"AP","documentDate":"2026-01-05","dueDate":"2026-02-04"%s}`, id, amount, extra)
}

// payBody is an extract with AR lines and AP lines and a load that names the payables vendors.
func payBody(records []string) string {
	return fmt.Sprintf(`{"sourceSystem":"AX2012","sourceCompany":"USMF","cutoffTs":"2026-12-31T00:00:00Z","freezeTs":"2026-12-31T06:00:00Z","parties":["C001","V001"],`+
		`"load":{"target":"receivables","legalEntityId":%q,"customerIds":{"C001":%q},"payables":{"vendorIds":{"V001":%q}}},"records":[%s]}`, leA, custA, vendA, strings.Join(records, ","))
}

func apRecords(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = apRecordJSON(fmt.Sprintf("AP-%05d", i), "-20.50", "")
	}
	return out
}

func (e payEnv) loadAP(t *testing.T, runID string, want int) payablesLoadDTO {
	t.Helper()
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+runID+"/load-payables", "")
	if rec.Code != want {
		t.Fatalf("load-payables: %d %s", rec.Code, rec.Body)
	}
	if want != http.StatusOK {
		return payablesLoadDTO{}
	}
	return decode[payablesLoadDTO](t, rec)
}

func (e payEnv) payDetail(t *testing.T, runID string) payablesLoadDetailDTO {
	t.Helper()
	rec := e.do(t, "GET", "/api/v1/migration/runs/"+runID+"/payables-load", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("payables-load: %d %s", rec.Code, rec.Body)
	}
	return decode[payablesLoadDetailDTO](t, rec)
}

func (e payEnv) stagedAP(t *testing.T, runID string) int {
	return e.scalar(t, `select payables_staged from pack_run where run_id = $1`, runID)
}

func TestPayablesLoad_SendsBatchesOfAThousandAndReconciles(t *testing.T) {
	e := newPayEnv(t, 3)
	records := append(apRecords(2300), loadRecord("AR-1", "100.00", ""), loadRecord("AR-2", "-40.00", ""))
	run := e.start(t, payBody(records))
	if run.PayablesLoad == nil || run.PayablesLoad.Staged != 2300 || run.PayablesLoad.Pending != 2300 || run.PayablesLoad.Status != "STAGED" {
		t.Fatalf("payables load at start: %+v", run.PayablesLoad)
	}
	if run.TargetLoad == nil || run.TargetLoad.Staged != 2 || run.TargetLoad.Pending != 2 || run.TargetLoad.NotAvailable != 0 {
		t.Fatalf("the receivables load counts only its own rows: %+v", run.TargetLoad)
	}
	if n := e.stagedAP(t, run.RunID); n != 2300 {
		t.Fatalf("payables_staged = %d before the load", n)
	}
	if len(e.pay.Calls()) != 0 {
		t.Fatal("starting a run must not call payables")
	}

	// The receivables load does not touch the AP rows and is clean with them present.
	if ar := e.load(t, run.RunID); ar.Status != "RECONCILED" || !ar.Clean || ar.Created != 2 || ar.Pending != 0 {
		t.Fatalf("receivables load %+v", ar)
	}
	if n := e.stagedAP(t, run.RunID); n != 2300 {
		t.Fatalf("receivables load must not clear the AP staging: %d", n)
	}

	got := e.loadAP(t, run.RunID, http.StatusOK)
	if got.Status != "RECONCILED" || !got.Clean || got.Loaded != 2300 || got.Pending != 0 || got.Rejected != 0 || got.Attempts != 1 || got.Target != "payables" {
		t.Fatalf("load %+v", got)
	}
	if keys := e.pay.Batches(); len(keys) != 3 || e.pay.Held() != 2300 {
		t.Fatalf("batches %v held %d", keys, e.pay.Held())
	}
	for _, c := range e.pay.Calls() {
		if c.Method == http.MethodPost && !strings.HasPrefix(c.IdempotencyKey, "mf-ap-ax2012.usmf-") {
			t.Fatalf("idempotency key %q", c.IdempotencyKey)
		}
		if len(c.Duties) != 1 || (c.Duties[0] != "opening-item.import" && c.Duties[0] != "opening-item.import.read") {
			t.Fatalf("each token carries one duty of the grant: %+v", c)
		}
	}
	d := e.payDetail(t, run.RunID)
	if len(d.Reconciliation) != 2 || len(d.Rejects) != 0 {
		t.Fatalf("detail %+v", d)
	}
	for _, c := range d.Reconciliation {
		if c.Status != "MATCH" {
			t.Fatalf("line %+v", c)
		}
		if c.Name == "target.payables.total_minor" && (c.Source != "4715000.000000" || c.Target != "4715000.000000") {
			t.Fatalf("2300 x 20.50 USD is 4,715,000 cents: %+v", c)
		}
		if c.Name == "target.payables.item_count" && c.Source != "2300.000000" {
			t.Fatalf("count %+v", c)
		}
	}
	// Reconciled with no rejects: the pack is no longer staged for AP.
	if n := e.stagedAP(t, run.RunID); n != 0 {
		t.Fatalf("payables_staged = %d after a clean AP load", n)
	}
	if n := e.scalar(t, `select count(*) from outbox where type = 'dev.passage.migration.pack.loaded.v1' and payload->'data'->>'target' = 'payables'`); n != 1 {
		t.Fatalf("pack.loaded events for payables = %d", n)
	}
	if rd := decode[runDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID, "")); rd.PayablesLoad == nil || rd.PayablesLoad.Status != "RECONCILED" {
		t.Fatalf("run detail %+v", rd.PayablesLoad)
	}
}

func TestPayablesLoad_RefusedRowsAreRejectedAndTheRestOfTheBatchLoads(t *testing.T) {
	e := newPayEnv(t, 3)
	records := []string{
		apRecordJSON("AP-1", "-10.00", ""),
		apRecordJSON("AP-VENDOR", "-20.00", fmt.Sprintf(`,"vendorId":%q`, vendB)), // not in the party vendor master
		apRecordJSON("AP-2", "-30.00", ""),
		apRecordJSON("AP-DEBIT", "5.00", ""), // a debit balance never leaves the factory
		`{"legacyId":"AP-NODATE","party":"V001","account":"2100","currency":"USD","amount":"-1.00","ledger":"AP"}`,
	}
	run := e.start(t, payBody(records))
	got := e.loadAP(t, run.RunID, http.StatusOK)
	if got.Status != "RECONCILED" || got.Clean || got.Loaded != 2 || got.Rejected != 3 || got.Pending != 0 {
		t.Fatalf("load %+v", got)
	}
	d := e.payDetail(t, run.RunID)
	codes := map[string]string{}
	for _, r := range d.Rejects {
		codes[r.LegacyID] = r.Code
	}
	if codes["AP-VENDOR"] != "AP-OPB-001" || codes["AP-DEBIT"] != "MIG-LOAD-AP-DEBIT-BALANCE" || codes["AP-NODATE"] != "MIG-LOAD-FIELD-REQUIRED" || len(codes) != 3 {
		t.Fatalf("rejects %+v", d.Rejects)
	}
	for _, r := range d.Rejects {
		if r.LegacyID == "AP-VENDOR" && !strings.Contains(r.Detail, "vendor") {
			t.Fatalf("payables' reason is kept: %+v", r)
		}
	}
	// Two accepted rows were stored, in a second, smaller batch after the first was refused.
	if e.pay.Held() != 2 || len(e.pay.Batches()) != 1 {
		t.Fatalf("payables holds %d items in %v", e.pay.Held(), e.pay.Batches())
	}
	// A rejected line keeps the pack staged: only a waiver can excuse it.
	if n := e.stagedAP(t, run.RunID); n != 3 {
		t.Fatalf("payables_staged = %d, want the 3 rejected lines", n)
	}
}

func TestPayablesLoad_RerunOfAnUnchangedSnapshotSendsNothing(t *testing.T) {
	e := newPayEnv(t, 3)
	body := payBody(apRecords(5))
	first := e.start(t, body)
	e.loadAP(t, first.RunID, http.StatusOK)
	posts := e.pay.Posts()

	// The same run again: nothing pending, nothing sent.
	again := e.loadAP(t, first.RunID, http.StatusOK)
	if again.Status != "RECONCILED" || e.pay.Posts() != posts || again.Attempts != 2 {
		t.Fatalf("rerun %+v posts %d -> %d", again, posts, e.pay.Posts())
	}

	// A later run of the same snapshot: its rows equal what payables holds, so they are skipped.
	second := e.start(t, body)
	if second.RunID == first.RunID {
		t.Fatal("a new extract starts a new run")
	}
	got := e.loadAP(t, second.RunID, http.StatusOK)
	if got.Status != "RECONCILED" || got.Skipped != 5 || got.Loaded != 0 || e.pay.Posts() != posts {
		t.Fatalf("later run %+v posts %d -> %d", got, posts, e.pay.Posts())
	}
	if e.pay.Held() != 5 || len(e.pay.Batches()) != 1 {
		t.Fatalf("payables holds %d in %v", e.pay.Held(), e.pay.Batches())
	}
}

func TestPayablesLoad_OutageLeavesRowsPendingAndTheNextLoadResumes(t *testing.T) {
	e := newPayEnv(t, 2)
	run := e.start(t, payBody(apRecords(3)))
	e.pay.FailNext(503, 503)
	got := e.loadAP(t, run.RunID, http.StatusOK)
	if got.Status != "PARTIAL" || got.Pending != 3 || got.Loaded != 0 || got.LastError == "" {
		t.Fatalf("outage %+v", got)
	}
	if n := e.stagedAP(t, run.RunID); n != 3 {
		t.Fatalf("payables_staged = %d", n)
	}
	got = e.loadAP(t, run.RunID, http.StatusOK)
	if got.Status != "RECONCILED" || got.Loaded != 3 || got.Pending != 0 || got.Attempts != 2 {
		t.Fatalf("resume %+v", got)
	}
	if n := e.stagedAP(t, run.RunID); n != 0 {
		t.Fatalf("payables_staged = %d", n)
	}
}

func TestPayablesLoad_ReconciliationMismatchKeepsThePackStaged(t *testing.T) {
	e := newPayEnv(t, 3)
	e.pay.Drift = 1 // payables reports one cent more than the factory sent
	run := e.start(t, payBody(apRecords(4)))
	got := e.loadAP(t, run.RunID, http.StatusOK)
	if got.Status != "MISMATCH" || got.Clean {
		t.Fatalf("load %+v", got)
	}
	d := e.payDetail(t, run.RunID)
	mismatched := 0
	for _, c := range d.Reconciliation {
		if c.Status != "MATCH" {
			mismatched++
		}
	}
	if mismatched != 1 {
		t.Fatalf("reconciliation %+v", d.Reconciliation)
	}
	if n := e.stagedAP(t, run.RunID); n != 4 {
		t.Fatalf("a mismatch keeps the AP staged: %d", n)
	}
}

func TestPayablesLoad_RequestRules(t *testing.T) {
	e := newPayEnv(t, 2)
	// A run without load.payables keeps its AP rows NOT_AVAILABLE and has no AP load.
	plain := e.start(t, loadBody([]string{loadRecord("AR-1", "10.00", ""), apRecordJSON("AP-1", "-5.00", "")}, ""))
	if plain.PayablesLoad != nil || plain.TargetLoad == nil || plain.TargetLoad.NotAvailable != 1 {
		t.Fatalf("plain run %+v %+v", plain.PayablesLoad, plain.TargetLoad)
	}
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+plain.RunID+"/load-payables", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-1553") {
		t.Fatalf("no payables load: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "GET", "/api/v1/migration/runs/"+plain.RunID+"/payables-load", ""); rec.Code != http.StatusConflict {
		t.Fatalf("GET without payables load: %d %s", rec.Code, rec.Body)
	}
	// A vendor id that is not a UUID is refused at the start.
	bad := strings.Replace(payBody(apRecords(1)), vendA, "not-a-uuid", 1)
	if rec := e.do(t, "POST", runsPath, bad); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "vendorIds") {
		t.Fatalf("bad vendor id: %d %s", rec.Code, rec.Body)
	}
	// The duty to load is migration.factory.run; reading needs migration.factory.view.
	run := e.start(t, payBody(apRecords(1)))
	ro := do(t, e.h, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/load-payables", tenant: tenantA, duties: "migration.factory.view", le: leA})
	if ro.Code != http.StatusForbidden {
		t.Fatalf("view-only load: %d", ro.Code)
	}
	// Another tenant cannot see or load the run.
	other := do(t, e.h, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/load-payables", tenant: "00000000-0000-7000-8000-00000000000b", duties: allDuties, le: leA})
	if other.Code != http.StatusNotFound && other.Code != http.StatusConflict {
		t.Fatalf("other tenant: %d %s", other.Code, other.Body)
	}
}

func TestPayablesLoad_NotConfiguredAndRefusedTokenAnswerClearly(t *testing.T) {
	e := newPayEnv(t, 2)
	run := e.start(t, payBody(apRecords(2)))
	off := New(e.st).WithTarget(e.tgt).WithPayables(target.NewPayables(target.Config{})).Routes()
	rec := do(t, off, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/load-payables", tenant: tenantA, duties: allDuties, le: leA})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "MIG-1550") {
		t.Fatalf("not configured: %d %s", rec.Code, rec.Body)
	}
	// Payables refuses the service token (403): the load stops as FAILED with MIG-1551 and keeps the rows pending.
	e.pay.FailNext(403)
	rec = e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/load-payables", "")
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "MIG-1551") {
		t.Fatalf("refused token: %d %s", rec.Code, rec.Body)
	}
	if got := e.payDetail(t, run.RunID); got.Status != "FAILED" || got.Pending != 2 {
		t.Fatalf("after a refused token %+v", got.payablesLoadDTO)
	}
	// The next load, with the grant in place, loads them.
	if got := e.loadAP(t, run.RunID, http.StatusOK); got.Status != "RECONCILED" || got.Loaded != 2 {
		t.Fatalf("after the fix %+v", got)
	}
}

func TestPayablesLoad_ReferencesHeldByAnotherBatchAreRejected(t *testing.T) {
	e := newPayEnv(t, 2)
	first := e.start(t, payBody([]string{apRecordJSON("AP-1", "-10.00", ""), apRecordJSON("AP-2", "-20.00", "")}))
	e.loadAP(t, first.RunID, http.StatusOK)
	// A later extract changes AP-1 (another amount) and adds AP-3: payables cannot update an imported reference, so
	// AP-1 is refused AP-OPB-004 and AP-3 still loads.
	run := e.start(t, payBody([]string{apRecordJSON("AP-1", "-15.00", ""), apRecordJSON("AP-2", "-20.00", ""), apRecordJSON("AP-3", "-5.00", "")}))
	got := e.loadAP(t, run.RunID, http.StatusOK)
	if got.Rejected != 1 || got.Loaded != 1 || got.Skipped != 1 || got.Status != "RECONCILED" {
		t.Fatalf("load %+v", got)
	}
	d := e.payDetail(t, run.RunID)
	if len(d.Rejects) != 1 || d.Rejects[0].LegacyID != "AP-1" || d.Rejects[0].Code != "AP-OPB-004" {
		t.Fatalf("rejects %+v", d.Rejects)
	}
}

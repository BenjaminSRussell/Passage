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
	prodBolt = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	prodPump = "ffffffff-ffff-4fff-8fff-ffffffffffff"
	acctUSD  = "99999999-9999-4999-8999-999999999991"
	acctEUR  = "99999999-9999-4999-8999-999999999992"
)

type openEnv struct {
	loadEnv
	inv *targettest.FakeOpening
	trs *targettest.FakeOpening
}

func newOpenEnv(t *testing.T, attempts int) openEnv {
	t.Helper()
	e := newLoadEnv(t, attempts, nil)
	inv, trs := targettest.NewInventoryOpening(t), targettest.NewTreasuryOpening(t)
	cfg := func(url string) target.Config {
		return target.Config{BaseURL: url, Signer: targettest.HeaderSigner{Name: "migration-factory"}, Attempts: attempts, Backoff: time.Millisecond,
			Sleep: func(context.Context, time.Duration) error { return nil }}
	}
	e.h = New(e.st).WithTarget(e.tgt).WithInventory(target.NewInventory(cfg(inv.URL))).WithTreasury(target.NewTreasury(cfg(trs.URL))).Routes()
	return openEnv{loadEnv: e, inv: inv, trs: trs}
}

func (e openEnv) startPack(t *testing.T, path, body string) runDetailDTO {
	t.Helper()
	rec := e.do(t, "POST", path, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start run: %d %s", rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec)
}

func (e openEnv) openingLoad(t *testing.T, runID, body string, want int) openingLoadDTO {
	t.Helper()
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+runID+"/opening-load", body)
	if rec.Code != want {
		t.Fatalf("opening-load: %d %s", rec.Code, rec.Body)
	}
	if want != http.StatusOK {
		return openingLoadDTO{}
	}
	return decode[openingLoadDTO](t, rec)
}

func stockLoadBody(extra string) string {
	return fmt.Sprintf(`{"legalEntityId":%q,"postingDate":"2026-12-31","products":{"BOLT-10":%q,"PUMP-1":%q}%s}`, leA, prodBolt, prodPump, extra)
}

func TestOpeningLoad_StockGoesToTheInventoryLedgerAndReconciles(t *testing.T) {
	e := newOpenEnv(t, 3)
	run := e.startPack(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	got := e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK)
	if got.Status != "RECONCILED" || !got.Clean || got.Loaded != 3 || got.Pending != 0 || got.Rejected != 0 || got.Target != "inventory" || got.Staged != 3 {
		t.Fatalf("load %+v", got)
	}
	if keys := e.inv.Batches(); len(keys) != 1 || !strings.HasPrefix(keys[0], "mf-onhand-AX2012-") || !strings.Contains(keys[0], "-USD-") {
		t.Fatalf("batches %v", keys)
	}
	detail := decode[openingLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/opening-load", ""))
	names := map[string]string{}
	for _, c := range detail.Reconciliation {
		names[c.Name] = c.Status
	}
	if names["target.onhand.item_count"] != "MATCH" || names["target.onhand.quantity"] != "MATCH" || names["target.onhand.value"] != "MATCH" {
		t.Fatalf("reconciliation %+v", detail.Reconciliation)
	}
	for _, c := range e.inv.Calls() {
		if len(c.Duties) != 1 || (c.Duties[0] != "inventory.import.write" && c.Duties[0] != "inventory.import.read") {
			t.Fatalf("each token carries one duty of the grant: %+v", c)
		}
	}

	// A second call sends nothing: the target holds every balance.
	posts := e.inv.Posts()
	again := e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK)
	if again.Status != "RECONCILED" || again.Attempts != 2 || e.inv.Posts() != posts {
		t.Fatalf("second load %+v, posts %d -> %d", again, posts, e.inv.Posts())
	}
	if n := e.scalar(t, `select count(*) from opening_load_row where run_id = $1 and status = 'LOADED'`, run.RunID); n != 3 {
		t.Fatalf("loaded rows %d", n)
	}
}

func TestOpeningLoad_RejectsWhatItCannotMapOrTheTargetRefusesAndKeepsTheRest(t *testing.T) {
	e := newOpenEnv(t, 3)
	run := e.startPack(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	e.inv.Refuse["OH-2"] = true
	// PUMP-1 has no product id: OH-3 is rejected by the factory; OH-2 by the target; OH-1 loads.
	body := fmt.Sprintf(`{"legalEntityId":%q,"postingDate":"2026-12-31","products":{"BOLT-10":%q}}`, leA, prodBolt)
	got := e.openingLoad(t, run.RunID, body, http.StatusOK)
	if got.Loaded != 1 || got.Rejected != 2 || got.Pending != 0 || got.Clean || got.Status != "RECONCILED" {
		t.Fatalf("load %+v", got)
	}
	detail := decode[openingLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/opening-load", ""))
	codes := map[string]string{}
	for _, r := range detail.Rejects {
		codes[r.LegacyID] = r.Code
	}
	if codes["OH-3"] != "MIG-REF-UNRESOLVED" || codes["OH-2"] != "INV-OPB-001" || len(codes) != 2 {
		t.Fatalf("rejects %+v", detail.Rejects)
	}
	// The mapping is added and the target stops refusing: the next call loads the rest.
	delete(e.inv.Refuse, "OH-2")
	got = e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK)
	if got.Loaded != 3 || got.Rejected != 0 || !got.Clean {
		t.Fatalf("retry %+v", got)
	}
}

func TestOpeningLoad_AThousandLinesPerBatchAndMismatchIsNotHidden(t *testing.T) {
	e := newOpenEnv(t, 3)
	records := make([]string, 1200)
	for i := range records {
		records[i] = onhandRec(fmt.Sprintf("OH-%05d", i), "BOLT-10", fmt.Sprintf("S%d", i), "", "2", "5.00")
	}
	run := e.startPack(t, onhandPath, onhandBody("USMF", "", records...))
	got := e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK)
	if got.Loaded != 1200 || !got.Clean || len(e.inv.Batches()) != 2 {
		t.Fatalf("load %+v batches %v", got, e.inv.Batches())
	}
	e.inv.Drift = 1
	if drifted := e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK); drifted.Status != "MISMATCH" || drifted.Clean {
		t.Fatalf("a target holding more than was sent: %+v", drifted)
	}
}

func TestOpeningLoad_AnOutageLeavesRowsPendingAndTheRetryIsAReplay(t *testing.T) {
	e := newOpenEnv(t, 2)
	run := e.startPack(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	e.inv.FailNext(503, 503)
	got := e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK)
	if got.Status != "PARTIAL" || got.Pending != 3 || got.Loaded != 0 {
		t.Fatalf("outage %+v", got)
	}
	got = e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK)
	if got.Status != "RECONCILED" || got.Loaded != 3 || !got.Clean {
		t.Fatalf("retry %+v", got)
	}
}

func TestOpeningLoad_BankBalancesGoToTreasury(t *testing.T) {
	e := newOpenEnv(t, 3)
	run := e.startPack(t, bankPath, bankBody("1000", "", cleanBank...))
	body := fmt.Sprintf(`{"legalEntityId":%q,"asOf":"2026-12-31","bankAccounts":{"OPER-USD":%q,"PAY-EUR":%q,"SAV-USD":%q}}`, leA, acctUSD, acctEUR, "99999999-9999-4999-8999-999999999993")
	got := e.openingLoad(t, run.RunID, body, http.StatusOK)
	if got.Status != "RECONCILED" || !got.Clean || got.Loaded != 3 || got.Target != "treasury" {
		t.Fatalf("load %+v", got)
	}
	detail := decode[openingLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/opening-load", ""))
	byKey := map[string]string{}
	for _, c := range detail.Reconciliation {
		byKey[c.Name+"/"+c.Key["currency"]] = c.Source + "=" + c.Target
	}
	// -1250.50 + 50.25 USD = -1200.25 -> -120025 minor; 900 EUR -> 90000.
	if byKey["target.bank_balances.total_minor/USD"] != "-120025.000000=-120025.000000" || byKey["target.bank_balances.total_minor/EUR"] != "90000.000000=90000.000000" {
		t.Fatalf("reconciliation %+v", byKey)
	}
	var posted int
	posted = e.scalar(t, `select count(*) from opening_load where run_id = $1 and status = 'RECONCILED' and rejected = 0 and pending = 0`, run.RunID)
	if posted != 1 {
		t.Fatal("the load is not recorded as reconciled")
	}
}

func TestOpeningLoad_RefusalsAreExplained(t *testing.T) {
	e := newOpenEnv(t, 3)
	run := e.startPack(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/opening-load", `{"legalEntityId":"x","postingDate":"2026-12-31"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad legal entity: %d", rec.Code)
	}
	if rec := e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/opening-load", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "MIG-1563") {
		t.Fatalf("before any load: %d %s", rec.Code, rec.Body)
	}
	plain := newLoadEnv(t, 1, nil)
	plainRun := openEnv{loadEnv: plain}.startPack(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	if rec := plain.do(t, "POST", "/api/v1/migration/runs/"+plainRun.RunID+"/opening-load", stockLoadBody("")); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "MIG-1560") {
		t.Fatalf("no inventory target: %d %s", rec.Code, rec.Body)
	}
}

// A timeout after the target stored the batch, then a mapping change, then a rerun: the first batch is sent again unchanged and
// answered as a replay, and the rest forms new batches; nothing is opened twice.
func TestOpeningLoad_AnAmbiguousOutcomeThenAMappingChangeNeverDoubleLoads(t *testing.T) {
	e := newOpenEnv(t, 1)
	run := e.startPack(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	e.inv.LoseNext(1)
	if got := e.openingLoad(t, run.RunID, stockLoadBody(""), http.StatusOK); got.Status != "PARTIAL" || got.Pending != 3 {
		t.Fatalf("lost response: %+v", got)
	}
	// PUMP-1 now maps to another product, which would change the rows (and the batch key) if the batch were formed again.
	body := fmt.Sprintf(`{"legalEntityId":%q,"postingDate":"2026-12-31","products":{"BOLT-10":%q,"PUMP-1":%q}}`, leA, prodBolt, "12121212-1212-4212-8212-121212121212")
	got := e.openingLoad(t, run.RunID, body, http.StatusOK)
	if got.Status != "RECONCILED" || got.Loaded != 3 || !got.Clean {
		t.Fatalf("rerun: %+v", got)
	}
	if keys := e.inv.Batches(); len(keys) != 1 {
		t.Fatalf("the target holds %d batches, want the one that was sent: %v", len(keys), keys)
	}
}

func TestOpeningLoad_TreasuryAmbiguousOutcomeThenAMappingChangeRecovers(t *testing.T) {
	e := newOpenEnv(t, 1)
	run := e.startPack(t, bankPath, bankBody("1000", "", cleanBank...))
	a3 := "99999999-9999-4999-8999-999999999993"
	body := func(sav string) string {
		return fmt.Sprintf(`{"legalEntityId":%q,"asOf":"2026-12-31","bankAccounts":{"OPER-USD":%q,"PAY-EUR":%q,"SAV-USD":%q}}`, leA, acctUSD, acctEUR, sav)
	}
	e.trs.LoseNext(1)
	if got := e.openingLoad(t, run.RunID, body(a3), http.StatusOK); got.Status != "PARTIAL" {
		t.Fatalf("lost response: %+v", got)
	}
	got := e.openingLoad(t, run.RunID, body("99999999-9999-4999-8999-999999999994"), http.StatusOK)
	if got.Status != "RECONCILED" || got.Loaded != 3 || !got.Clean || len(e.trs.Batches()) != 1 {
		t.Fatalf("rerun: %+v batches %v", got, e.trs.Batches())
	}
}

package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/replay"
)

func slDocJSON(seq int, id, area, kind, date, key, amount string) string {
	return fmt.Sprintf(`{"seq":%d,"documentId":%q,"area":%q,"kind":%q,"postingDate":%q,"key":%q,"currency":"USD","amount":%q}`, seq, id, area, kind, date, key, amount)
}

func slBody(period string, journals []string, subledger []string, extra string) string {
	return fmt.Sprintf(`{"period":%q,"sourceSystem":"AX2012","legalEntityId":%q,"ledgerId":"L-US","revenue":"100000","documents":[%s],"subledger":[%s]%s}`,
		period, le1, strings.Join(journals, ","), strings.Join(subledger, ","), extra)
}

// 49-4: the subledger documents of a period are replayed through the area ports beside the journals, and the AR, AP,
// INV and FA tuples are diffed in the same parallel run.
func TestParallelReplay_SubledgerDocumentsAreDiffedBesideTheTrialBalance(t *testing.T) {
	_, admin, st, ledger, _ := ledgerServer(t)
	ports := replay.Ports{}
	mem := map[domain.Area]*replay.MemoryPort{}
	for _, a := range domain.SubledgerAreas {
		mem[a] = replay.NewMemoryPort()
		ports[a] = mem[a]
	}
	h := New(st).WithLedger(ledger).WithSubledgers(ports).Routes()
	path := "/api/v1/migration/parallel-runs/replay"
	journals := []string{replayDoc(1, "GJ-1", "2026-03-05", "1100", "4000", "500")}
	docs := []string{
		slDocJSON(1, "AR-1", "AR", "INVOICE", "2026-03-05", "C001", "500"),
		slDocJSON(2, "AR-2", "AR", "PAYMENT", "2026-03-20", "C001", "-200"),
		slDocJSON(3, "AP-1", "AP", "INVOICE", "2026-03-06", "V001", "75.25"),
		slDocJSON(4, "IN-1", "INV", "RECEIPT", "2026-03-07", "WIDGET@MAIN", "310"),
		slDocJSON(5, "FA-1", "FA", "ACQUISITION", "2026-03-08", "PRESS-1", "12000"),
	}
	body := slBody("2026-03", journals, docs, "")
	rec := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: body, user: "engineer", le: le1})
	if rec.Code != http.StatusCreated {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	run := decode[replayDTO](t, rec)
	if run.Posted != 6 || len(run.Rejected) != 0 || run.UnexplainedCount != 0 || run.DocumentsReplayed != 6 || !run.CanSignOff {
		t.Fatalf("replay %+v", run)
	}
	if n := count(t, admin, `select documents_replayed from parallel_run where parallel_run_id = $1::uuid`, run.ParallelRunID); n != 6 {
		t.Fatalf("documents_replayed = %d", n)
	}
	// The same replay again returns the stored run and posts nothing twice.
	again := decode[replayDTO](t, do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: body, le: le1}))
	if again.ParallelRunID != run.ParallelRunID || mem[domain.AreaAR].Posts() != 2 || len(ledger.Entries()) != 1 {
		t.Fatalf("second replay: run %s vs %s, AR posts %d, %d entries", again.ParallelRunID, run.ParallelRunID, mem[domain.AreaAR].Posts(), len(ledger.Entries()))
	}

	// April: the payables subledger refuses a document; it is listed with its area and its amount differs.
	mem[domain.AreaAP].Refuse("AP-9", "AP-0409")
	april := slBody("2026-04", nil, []string{
		slDocJSON(1, "AP-8", "AP", "INVOICE", "2026-04-02", "V001", "100"),
		slDocJSON(2, "AP-9", "AP", "INVOICE", "2026-04-03", "V001", "60"),
	}, "")
	rec = do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: april, le: le1})
	bad := decode[replayDTO](t, rec)
	if rec.Code != http.StatusCreated || bad.Posted != 1 || len(bad.Rejected) != 1 || bad.Rejected[0].DocumentID != "AP-9" || bad.Rejected[0].Area != "AP" || bad.Rejected[0].Code != "AP-0409" {
		t.Fatalf("april replay (subledger only): %d %s", rec.Code, rec.Body)
	}
	if bad.UnexplainedCount != 1 || bad.CanSignOff || bad.Variances[0].Area != "AP" || bad.Variances[0].Key != "V001|USD|2026-04" ||
		bad.Variances[0].Legacy != "160.000000" || bad.Variances[0].Passage != "100.000000" {
		t.Fatalf("variances %+v", bad.Variances)
	}

	// May: the legacy ledger's own movement is the legacy side, so a document missing from the replay file shows.
	may := slBody("2026-05", nil, []string{slDocJSON(1, "AR-5", "AR", "INVOICE", "2026-05-02", "C001", "100")},
		`,"legacySubledger":[{"area":"AR","key":"C001","currency":"USD","amount":"130"}]`)
	m := decode[replayDTO](t, do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: may, le: le1}))
	if m.UnexplainedCount != 1 || m.Variances[0].Legacy != "130.000000" || m.Variances[0].Passage != "100.000000" {
		t.Fatalf("legacy extract variance %+v", m.Variances)
	}
}

func TestParallelReplay_SubledgerRules(t *testing.T) {
	_, _, st, ledger, _ := ledgerServer(t)
	path := "/api/v1/migration/parallel-runs/replay"
	oneIn := func(month string) []string {
		return []string{slDocJSON(1, "AR-1", "AR", "INVOICE", "2026-"+month+"-05", "C001", "5")}
	}
	one := oneIn("03")
	post := func(h http.Handler, body string) *reply {
		rec := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: allDuties, body: body, le: le1})
		return &reply{rec.Code, rec.Body.String()}
	}
	// No port for the area: nothing is posted, 503 MIG-1534.
	noPorts := New(st).WithLedger(ledger).Routes()
	if r := post(noPorts, slBody("2026-03", nil, one, "")); r.code != http.StatusServiceUnavailable || !strings.Contains(r.body, CodeSubledgerNotConfigured) {
		t.Fatalf("no port: %d %s", r.code, r.body)
	}
	ports := replay.Ports{domain.AreaAR: replay.NewMemoryPort()}
	h := New(st).WithLedger(ledger).WithSubledgers(ports).Routes()
	// An outage is a 502 the replay can be repeated after.
	down := replay.NewMemoryPort()
	down.FailPosts(errors.New("connection reset"))
	hd := New(st).WithLedger(ledger).WithSubledgers(replay.Ports{domain.AreaAR: down}).Routes()
	if r := post(hd, slBody("2026-03", nil, one, "")); r.code != http.StatusBadGateway || !strings.Contains(r.body, CodeSubledgerUnavailable) {
		t.Fatalf("outage: %d %s", r.code, r.body)
	}
	down.FailPosts(nil)
	if r := post(hd, slBody("2026-03", nil, one, "")); r.code != http.StatusCreated {
		t.Fatalf("repeat: %d %s", r.code, r.body)
	}
	// Validation: a payment must be negative, an unknown area and a date outside the period are refused.
	for name, doc := range map[string]string{
		"payment sign": slDocJSON(1, "X", "AR", "PAYMENT", "2026-04-05", "C001", "5"),
		"area TB":      slDocJSON(1, "X", "TB", "INVOICE", "2026-04-05", "C001", "5"),
		"area GL":      slDocJSON(1, "X", "GL", "INVOICE", "2026-04-05", "C001", "5"),
		"other period": slDocJSON(1, "X", "AR", "INVOICE", "2026-05-05", "C001", "5"),
		"amount":       slDocJSON(1, "X", "AR", "INVOICE", "2026-04-05", "C001", "five"),
	} {
		if r := post(h, slBody("2026-04", nil, []string{doc}, "")); r.code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, r.code, r.body)
		}
	}
	// Journals still need the ledger and ledgerId; an empty replay is refused.
	if r := post(h, slBody("2026-04", nil, nil, "")); r.code != http.StatusUnprocessableEntity {
		t.Fatalf("empty replay: %d %s", r.code, r.body)
	}
	noLedger := New(st).WithSubledgers(ports).Routes()
	if r := post(noLedger, slBody("2026-06", nil, oneIn("06"), "")); r.code != http.StatusCreated {
		t.Fatalf("subledger only needs no ledger: %d %s", r.code, r.body)
	}
	if r := post(noLedger, slBody("2026-07", []string{replayDoc(1, "GJ", "2026-07-02", "1100", "4000", "1")}, oneIn("07"), "")); r.code != http.StatusServiceUnavailable {
		t.Fatalf("journals without a ledger: %d %s", r.code, r.body)
	}
}

type reply struct {
	code int
	body string
}

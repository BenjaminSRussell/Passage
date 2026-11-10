package api_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/store"
	"github.com/BenjaminSRussell/passage/services/consolidation/migrations"
)

const (
	leUS = "00000000-0000-0000-0000-0000000000a1"
	leDE = "00000000-0000-0000-0000-0000000000a2"
)

func groupsAPI(t *testing.T) (*api.API, *ledgerclient.Fake) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	fake := ledgerclient.NewFake()
	return api.New(st).WithGroups(st).WithLedger(fake), fake
}

type idOut struct{ ID string }

type unitOut struct {
	ID, LegalEntityID, LedgerID, Currency, CurrentPercent string
	Ownership                                             []struct {
		ID, HolderUnitID, EffectiveFrom, EffectiveTo, Percent string
	}
}

type groupOut struct {
	ID, Code, Name, Currency string
	Units                    []unitOut
}

func postLedger(t *testing.T, f *ledgerclient.Fake, le, ledger, cur, date, key string, lines ...ledgerclient.Line) {
	t.Helper()
	if _, err := f.Post(context.Background(), ledgerclient.Entry{LegalEntityID: le, LedgerID: ledger, DocumentID: key, PostingDate: date,
		Currency: cur, IdempotencyKey: key, SourceType: "test", Lines: lines}); err != nil {
		t.Fatal(err)
	}
}

func line(account string, credit bool, amount string) ledgerclient.Line {
	return ledgerclient.Line{Account: account, Credit: credit, Amount: decimal.RequireFromString(amount)}
}

func TestGroups_StructureOwnershipAndMapping(t *testing.T) {
	a, _ := groupsAPI(t)
	const base = "/v1/consol/groups"
	rec := call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`)
	g := created[groupOut](t, rec)
	if g.Code != "CRP" || rec.Header().Get("Location") != base+"/"+g.ID {
		t.Fatalf("group = %+v", g)
	}
	if replay := call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`); replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	if dup := call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g2", `{"code":"CRP","name":"Other","currency":"USD"}`); dup.Code != http.StatusConflict || !strings.Contains(dup.Body.String(), "CNS-0925") {
		t.Fatalf("duplicate code = %d %s", dup.Code, dup.Body.String())
	}
	if rec := call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "", `{"code":"X"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key = %d", rec.Code)
	}

	us := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "",
		fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	de := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "",
		fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR"}`, leDE)))
	if again := call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "",
		fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)); again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "CNS-0926") {
		t.Fatalf("same entity twice = %d %s", again.Code, again.Body.String())
	}

	own := base + "/" + g.ID + "/units/" + de.ID + "/ownership"
	created[unitOut](t, call(a, http.MethodPost, own, tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2025-01-01","percent":"60"}`))
	changed := created[unitOut](t, call(a, http.MethodPost, own, tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2026-01-01","percent":"80"}`))
	if len(changed.Ownership) != 2 || changed.Ownership[0].EffectiveTo != "2026-01-01" || changed.Ownership[1].Percent != "80.0000" || changed.CurrentPercent != "80.0000" {
		t.Fatalf("history = %+v", changed)
	}
	if over := call(a, http.MethodPost, own, tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"holderUnitId":%q,"effectiveFrom":"2026-02-01","percent":"30"}`, us.ID)); over.Code != http.StatusUnprocessableEntity || !strings.Contains(over.Body.String(), "CNS-0904") {
		t.Fatalf("110 percent = %d %s", over.Code, over.Body.String())
	}
	if self := call(a, http.MethodPost, own, tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"holderUnitId":%q,"effectiveFrom":"2026-02-01","percent":"10"}`, de.ID)); self.Code != http.StatusBadRequest {
		t.Fatalf("a unit holding itself = %d %s", self.Code, self.Body.String())
	}

	got := decode[groupOut](t, call(a, http.MethodGet, base+"/"+g.ID, tenantID, api.DutyView, "", ""))
	if len(got.Units) != 2 || got.Units[1].LegalEntityID != leDE || len(got.Units[1].Ownership) != 2 {
		t.Fatalf("group = %+v", got)
	}
	list := decode[struct {
		Items []struct {
			Code      string
			UnitCount int
		}
	}](t, call(a, http.MethodGet, base, tenantID, api.DutyView, "", ""))
	if len(list.Items) != 1 || list.Items[0].UnitCount != 2 {
		t.Fatalf("list = %+v", list)
	}
	if nf := call(a, http.MethodGet, base+"/"+tenantID, tenantID, api.DutyView, "", ""); nf.Code != http.StatusNotFound || !strings.Contains(nf.Body.String(), "CNS-0924") {
		t.Fatalf("unknown group = %d %s", nf.Code, nf.Body.String())
	}

	mapURL := base + "/" + g.ID + "/account-map"
	body := `{"accounts":[{"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet"},{"mainAccount":"4000","consolidationAccount":"REVENUE","accountType":"income"}]}`
	if rec := call(a, http.MethodPut, mapURL, tenantID, api.DutyConfigure, "", body); rec.Code != http.StatusOK {
		t.Fatalf("set mapping = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(a, http.MethodPut, mapURL, tenantID, api.DutyConfigure, "", `{"accounts":[{"mainAccount":"1000","consolidationAccount":"A","accountType":"x"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad mapping = %d", rec.Code)
	}
	m := decode[struct {
		Accounts []struct{ MainAccount, ConsolidationAccount string }
	}](t, call(a, http.MethodGet, mapURL, tenantID, api.DutyView, "", ""))
	if len(m.Accounts) != 2 {
		t.Fatalf("mapping = %+v", m)
	}
}

func TestGroups_AnEntityScopedCallerIsRefused(t *testing.T) {
	a, _ := groupsAPI(t)
	if rec := callAs(a, true, http.MethodGet, "/v1/consol/groups", tenantID, api.DutyView, "", ""); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CNS-0923") {
		t.Fatalf("scoped = %d %s", rec.Code, rec.Body.String())
	}
}

func TestCollection_FromTheLedgerToAConsolidatedTrialBalance(t *testing.T) {
	a, fake := groupsAPI(t)
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR"}`, leDE)))
	mapping := `{"accounts":[
	  {"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet"},
	  {"mainAccount":"3000","consolidationAccount":"EQUITY","accountType":"balance_sheet"},
	  {"mainAccount":"4000","consolidationAccount":"REVENUE","accountType":"income"}]}`
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", mapping); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d", rec.Code)
	}
	// US: opening equity in 2025, then 2026 sales of 200 in cash.
	postLedger(t, fake, leUS, "L-US", "USD", "2025-12-15", "us-1", line("1000", false, "1000"), line("3000", true, "1000"))
	postLedger(t, fake, leUS, "L-US", "USD", "2026-03-10", "us-2", line("1000", false, "200"), line("4000", true, "200"))
	// DE: 100 EUR of equity and 50 EUR of sales; 9000 is not mapped.
	postLedger(t, fake, leDE, "L-DE", "EUR", "2025-12-15", "de-1", line("1000", false, "100"), line("3000", true, "100"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-03-12", "de-2", line("1000", false, "50"), line("4000", true, "50"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-03-13", "de-3", line("9000", false, "7"), line("1000", true, "7"))

	path := base + "/" + g.ID + "/collections"
	body := `{"period":"2026-03","rates":[{"currency":"EUR","average":"1.08","closing":"1.10"}]}`
	if rec := call(a, http.MethodPost, path, tenantID, api.DutyRun, "c1", `{"period":"2026-03"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CNS-0927") {
		t.Fatalf("missing rate = %d %s", rec.Code, rec.Body.String())
	}
	rec := call(a, http.MethodPost, path, tenantID, api.DutyRun, "c2", body)
	c := created[struct {
		ID            string
		Version       int
		LineCount     int
		UnmappedCount int
	}](t, rec)
	if c.Version != 1 || c.UnmappedCount != 1 || c.LineCount != 7 {
		t.Fatalf("collection = %+v %s", c, rec.Body.String())
	}
	if rp := call(a, http.MethodPost, path, tenantID, api.DutyRun, "c2", body); rp.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d", rp.Code)
	}

	tb := decode[struct {
		Total           string
		UnitDifferences map[string]string
		Rows            []struct {
			Account, Total string
			Units          map[string]string
		}
		Unmapped []struct{ MainAccount, GroupAmount string }
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+c.ID+"/trial-balance", tenantID, api.DutyView, "", ""))
	// DE cash: (100+50-7) EUR; the 50 EUR of sales is income (average), the rest balance sheet (closing).
	want := map[string]string{"CASH": "1357.30", "EQUITY": "-1110.00", "REVENUE": "-254.00"}
	if len(tb.Rows) != 3 {
		t.Fatalf("rows = %+v", tb.Rows)
	}
	for _, row := range tb.Rows {
		if row.Total != want[row.Account] {
			t.Errorf("%s = %s, want %s", row.Account, row.Total, want[row.Account])
		}
	}
	if len(tb.Unmapped) != 1 || tb.Unmapped[0].MainAccount != "9000" || tb.Unmapped[0].GroupAmount != "7.70" {
		t.Errorf("unmapped = %+v", tb.Unmapped)
	}
	list := decode[struct{ Items []idOut }](t, call(a, http.MethodGet, path, tenantID, api.DutyView, "", ""))
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}
	rerun := created[struct{ Version int }](t, call(a, http.MethodPost, path, tenantID, api.DutyRun, "c3", body))
	if rerun.Version != 2 {
		t.Fatalf("rerun version = %d", rerun.Version)
	}
}

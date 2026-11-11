package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
)

const mappingWithRoles = `{"accounts":[
  {"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet","role":"asset"},
  {"mainAccount":"1500","consolidationAccount":"IC_REC","accountType":"balance_sheet","role":"ic_receivable"},
  {"mainAccount":"2000","consolidationAccount":"PAYABLES","accountType":"balance_sheet","role":"liability"},
  {"mainAccount":"3000","consolidationAccount":"EQUITY","accountType":"balance_sheet","role":"equity"},
  {"mainAccount":"4000","consolidationAccount":"REVENUE","accountType":"income"},
  {"mainAccount":"4100","consolidationAccount":"IC_REVENUE","accountType":"income","role":"ic_sales"},
  {"mainAccount":"5000","consolidationAccount":"COGS","accountType":"income"},
  {"mainAccount":"5100","consolidationAccount":"IC_COGS","accountType":"income","role":"ic_cost"}]}`

type derivedRunOut struct {
	ID, GroupID, Period, Currency, CollectionID string
	Version, UnitCount                          int
}

func TestRun_DerivedFromAStoredCollection(t *testing.T) {
	a, fake := groupsAPI(t)
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	us := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	de := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR"}`, leDE)))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+de.ID+"/ownership", tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2024-01-01","percent":"80"}`))
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", mappingWithRoles); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":1`) {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body.String())
	}

	// The German unit: equity 700,000 and 2025's profit already closed into it; it holds 1,000,000 of assets, 300,000 of liabilities.
	postLedger(t, fake, leDE, "L-DE", "EUR", "2025-01-05", "de-1", line("1000", false, "1000000"), line("2000", true, "300000"), line("3000", true, "700000"))
	// 2026 (Jan to Mar): sales 500,000 of which 80,000 to the parent, costs 380,000 of which 60,000 of IC goods; paid in cash.
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-02-10", "de-2", line("1000", false, "420000"), line("4000", true, "420000"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-02-11", "de-3", line("1500", false, "80000"), line("4100", true, "80000"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-02-12", "de-4", line("5000", false, "320000"), line("1000", true, "320000"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-02-13", "de-5", line("5100", false, "60000"), line("1000", true, "60000"))
	postLedger(t, fake, leUS, "L-US", "USD", "2025-01-05", "us-1", line("1000", false, "5000000"), line("3000", true, "5000000"))

	collect := base + "/" + g.ID + "/collections"
	// The collection of the previous year end gives the unit's opening net assets and opening rate.
	prev := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-prev", `{"period":"2025-12","rates":[{"currency":"EUR","average":"1.06","closing":"1.04"}]}`))
	cur := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-cur", `{"period":"2026-03","rates":[{"currency":"EUR","average":"1.08","closing":"1.10"}]}`))

	// Without terms the run cannot know the investment: CNS-0930.
	body := fmt.Sprintf(`{"collectionId":%q}`, cur.ID)
	if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r0", body); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CNS-0930") {
		t.Fatalf("run without terms = %d %s", rec.Code, rec.Body.String())
	}
	terms := `{"investmentCost":"600000","shareCapital":"300000","preAcquisitionRe":"100000","historicalRate":"1.05","sellerIsSubsidiary":true}`
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+de.ID+"/terms", tenantID, api.DutyConfigure, "", terms); rec.Code != http.StatusOK {
		t.Fatalf("terms = %d %s", rec.Code, rec.Body.String())
	}
	listed := decode[struct {
		Items []struct{ UnitID, InvestmentCost, HistoricalRate string }
	}](t, call(a, http.MethodGet, base+"/"+g.ID+"/unit-terms", tenantID, api.DutyView, "", ""))
	if len(listed.Items) != 1 || listed.Items[0].UnitID != de.ID || listed.Items[0].InvestmentCost != "600000" {
		t.Fatalf("unit terms = %+v", listed)
	}
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+de.ID+"/terms", tenantID, api.DutyConfigure, "", `{"investmentCost":"-1","historicalRate":"1.05"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative investment = %d", rec.Code)
	}

	rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r1",
		fmt.Sprintf(`{"collectionId":%q,"ic":[{"unit":%q,"unsoldShare":"0.25","payableGroup":"90000"}]}`, cur.ID, de.ID))
	run := created[derivedRunOut](t, rec)
	if run.CollectionID != cur.ID || run.GroupID != g.ID || run.Period != "2026-03" || run.Currency != "USD" || run.UnitCount != 1 || run.Version != 1 {
		t.Fatalf("run = %+v %s", run, rec.Body.String())
	}
	if replay := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r1", fmt.Sprintf(`{"collectionId":%q,"ic":[{"unit":%q,"unsoldShare":"0.25","payableGroup":"90000"}]}`, cur.ID, de.ID)); replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}

	// The stored statements use what the collection holds: net income 120,000 EUR, opening 700,000 EUR at 1.04, closing 820,000 EUR at 1.10.
	st := decode[struct {
		Statements []struct{ Statement, Line, Amount string }
	}](t, call(a, http.MethodGet, "/v1/consol/runs/"+run.ID+"/statements", tenantID, api.DutyView, "", ""))
	lines := map[string]string{}
	for _, l := range st.Statements {
		lines[l.Statement+"/"+l.Line] = l.Amount
	}
	if lines["net_assets/OPENING_NET_ASSETS"] != "728000.00" || lines["net_assets/CLOSING_NET_ASSETS"] != "902000.00" || lines["income/UNIT_NET_INCOME"] != "129600.00" {
		t.Fatalf("statements = %v; want opening 700,000 x 1.04, closing 820,000 x 1.10 and income 120,000 x 1.08", lines)
	}
	run2 := created[derivedRunOut](t, call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r2", fmt.Sprintf(`{"collectionId":%q}`, cur.ID)))
	if run2.Version != 2 {
		t.Fatalf("a second run of the collection is version %d, want 2", run2.Version)
	}

	// Refusals: units alongside a collection, another group's header, an unknown collection.
	for name, b := range map[string]string{
		"units and collection": fmt.Sprintf(`{"collectionId":%q,"units":[{"id":"x"}]}`, cur.ID),
		"other period":         fmt.Sprintf(`{"collectionId":%q,"period":"2026-04"}`, cur.ID),
		"bad ic unit":          fmt.Sprintf(`{"collectionId":%q,"ic":[{"unit":"nope"}]}`, cur.ID),
		"bad decimal":          fmt.Sprintf(`{"collectionId":%q,"ic":[{"unit":%q,"icSales":"lots"}]}`, cur.ID, de.ID),
	} {
		if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "rx", b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "rx", `{"collectionId":"`+tenantID+`"}`); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "CNS-0928") {
		t.Fatalf("unknown collection = %d %s", rec.Code, rec.Body.String())
	}

	// A group with no collection of the previous year end has no opening rate for the euro unit: the run says so, and takes the rate
	// from the request when one is given.
	_ = prev
	_ = us
	cd := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-2027", `{"period":"2027-02","rates":[{"currency":"EUR","average":"1.08","closing":"1.10"}]}`))
	if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r-27", fmt.Sprintf(`{"collectionId":%q}`, cd.ID)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "opening rate") {
		t.Fatalf("no previous year end = %d %s; want 422 asking for the opening rate", rec.Code, rec.Body.String())
	}
	if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r-27b", fmt.Sprintf(`{"collectionId":%q,"openingRates":[{"currency":"EUR","rate":"1.09"}]}`, cd.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("with an opening rate = %d %s", rec.Code, rec.Body.String())
	}
}

func TestUnits_EditFiscalYearRemoveAndMappingVersions(t *testing.T) {
	a, fake := groupsAPI(t)
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	us := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	de := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR","fiscalYearStartMonth":4}`, leDE)))
	if us.ID == "" || de.ID == "" {
		t.Fatal("units were not created")
	}
	unitURL := base + "/" + g.ID + "/units/" + de.ID

	// Edit: a new ledger and fiscal year; an empty or invalid change is refused.
	edited := decode[struct {
		LedgerID             string
		FiscalYearStartMonth int
	}](t, call(a, http.MethodPatch, unitURL, tenantID, api.DutyConfigure, "", `{"ledgerId":"L-DE-2","fiscalYearStartMonth":7}`))
	if edited.LedgerID != "L-DE-2" || edited.FiscalYearStartMonth != 7 {
		t.Fatalf("edited = %+v", edited)
	}
	for name, b := range map[string]string{"empty": `{}`, "month": `{"fiscalYearStartMonth":13}`, "currency": `{"currency":"euro"}`, "ledger": `{"ledgerId":" "}`, "unknown": `{"owner":"x"}`} {
		if rec := call(a, http.MethodPatch, unitURL, tenantID, api.DutyConfigure, "", b); rec.Code != http.StatusBadRequest {
			t.Errorf("patch %s: %d, want 400", name, rec.Code)
		}
	}
	if rec := call(a, http.MethodPatch, base+"/"+g.ID+"/units/"+g.ID, tenantID, api.DutyConfigure, "", `{"ledgerId":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown unit = %d, want 404", rec.Code)
	}
	if rec := call(a, http.MethodPatch, unitURL, tenantID, api.DutyView, "", `{"ledgerId":"x"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("patch with the view duty = %d, want 403", rec.Code)
	}

	// The unit's own fiscal year picks where its income starts: with a year from July the March 2026 period starts in July 2025.
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", `{"accounts":[{"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet"},{"mainAccount":"4000","consolidationAccount":"REVENUE","accountType":"income"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d", rec.Code)
	}
	postLedger(t, fake, leDE, "L-DE-2", "EUR", "2025-05-10", "de-may", line("1000", false, "10"), line("4000", true, "10"))
	postLedger(t, fake, leDE, "L-DE-2", "EUR", "2025-08-10", "de-aug", line("1000", false, "20"), line("4000", true, "20"))
	postLedger(t, fake, leDE, "L-DE-2", "EUR", "2026-02-10", "de-feb", line("1000", false, "30"), line("4000", true, "30"))
	c := created[struct{ ID string }](t, call(a, http.MethodPost, base+"/"+g.ID+"/collections", tenantID, api.DutyRun, "c1", `{"period":"2026-03","rates":[{"currency":"EUR","average":"2","closing":"2"}]}`))
	cd := decode[struct {
		Lines []struct{ Unit, MainAccount, UnitAmount string }
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+c.ID, tenantID, api.DutyView, "", ""))
	income := ""
	for _, l := range cd.Lines {
		if l.Unit == de.ID && l.MainAccount == "4000" {
			income = l.UnitAmount
		}
	}
	if income != "-50.00" && income != "-50.000000" && income != "-50" {
		t.Fatalf("income from the fiscal year start = %q, want -50 (August and February, not May)", income)
	}

	// Collections were made from the unit: its currency is fixed and it cannot be removed.
	if rec := call(a, http.MethodPatch, unitURL, tenantID, api.DutyConfigure, "", `{"currency":"GBP"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CNS-0931") {
		t.Fatalf("currency change = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(a, http.MethodDelete, unitURL, tenantID, api.DutyConfigure, "", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CNS-0931") {
		t.Fatalf("remove a collected unit = %d %s", rec.Code, rec.Body.String())
	}
	// A unit nothing refers to is removed with its history.
	extra := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", `{"legalEntityId":"00000000-0000-0000-0000-0000000000a3","ledgerId":"L-X","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+extra.ID+"/ownership", tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2025-01-01","percent":"100"}`))
	if rec := call(a, http.MethodDelete, base+"/"+g.ID+"/units/"+extra.ID, tenantID, api.DutyConfigure, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	got := decode[groupOut](t, call(a, http.MethodGet, base+"/"+g.ID, tenantID, api.DutyView, "", ""))
	if len(got.Units) != 2 {
		t.Fatalf("units after the removal = %+v", got.Units)
	}

	// The mapping is versioned: every PUT is a new version, an older one stays readable, and a collection names its version.
	second := `{"accounts":[{"mainAccount":"1000","consolidationAccount":"BANK","accountType":"balance_sheet","role":"asset"}]}`
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", second); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"version":2`) {
		t.Fatalf("second mapping = %d %s", rec.Code, rec.Body.String())
	}
	latest := decode[struct {
		Version  int
		Accounts []struct{ MainAccount, ConsolidationAccount, Role string }
	}](t, call(a, http.MethodGet, base+"/"+g.ID+"/account-map", tenantID, api.DutyView, "", ""))
	if latest.Version != 2 || len(latest.Accounts) != 1 || latest.Accounts[0].ConsolidationAccount != "BANK" || latest.Accounts[0].Role != "asset" {
		t.Fatalf("latest = %+v", latest)
	}
	first := decode[struct {
		Version  int
		Accounts []struct{ MainAccount string }
	}](t, call(a, http.MethodGet, base+"/"+g.ID+"/account-map?version=1", tenantID, api.DutyView, "", ""))
	if first.Version != 1 || len(first.Accounts) != 2 {
		t.Fatalf("version 1 = %+v", first)
	}
	vs := decode[struct {
		Items []struct{ Version, Accounts int }
	}](t, call(a, http.MethodGet, base+"/"+g.ID+"/account-map/versions", tenantID, api.DutyView, "", ""))
	if len(vs.Items) != 2 || vs.Items[0].Version != 2 || vs.Items[1].Accounts != 2 {
		t.Fatalf("versions = %+v", vs)
	}
	if rec := call(a, http.MethodGet, base+"/"+g.ID+"/account-map?version=zero", tenantID, api.DutyView, "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad version = %d", rec.Code)
	}
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", `{"accounts":[{"mainAccount":"1","consolidationAccount":"A","accountType":"income","role":"asset"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("asset role on an income account = %d, want 400", rec.Code)
	}
}

func TestCollection_ValidationsReadTheBalanceSheetByRole(t *testing.T) {
	a, fake := groupsAPI(t)
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	us := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	de := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"USD"}`, leDE)))
	call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", `{"accounts":[
	  {"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet","role":"asset"},
	  {"mainAccount":"2000","consolidationAccount":"PAY","accountType":"balance_sheet","role":"liability"},
	  {"mainAccount":"3000","consolidationAccount":"EQ","accountType":"balance_sheet","role":"equity"},
	  {"mainAccount":"4000","consolidationAccount":"REV","accountType":"income"}]}`)
	// US balances. DE has an asset that no liability, equity or profit explains, because 2500 is not mapped.
	postLedger(t, fake, leUS, "L-US", "USD", "2026-01-05", "us-1", line("1000", false, "1000"), line("2000", true, "400"), line("3000", true, "600"))
	postLedger(t, fake, leDE, "L-DE", "USD", "2026-01-05", "de-1", line("1000", false, "1000"), line("3000", true, "900"), line("2500", true, "100"))
	c := created[struct{ ID string }](t, call(a, http.MethodPost, base+"/"+g.ID+"/collections", tenantID, api.DutyRun, "c1", `{"period":"2026-03"}`))
	v := decode[struct {
		Blocked bool
		Units   []struct {
			Unit, Status string
			Results      []struct {
				Rule, Difference string
				Failed           bool
			}
		}
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+c.ID+"/validations", tenantID, api.DutyView, "", ""))
	byUnit := map[string]string{}
	diff := map[string]string{}
	for _, u := range v.Units {
		byUnit[u.Unit], diff[u.Unit] = u.Status, u.Results[0].Difference
	}
	if !v.Blocked || byUnit[us.ID] != "Completed" || byUnit[de.ID] != "Error" || diff[de.ID] != "100.00" {
		t.Fatalf("validations = %+v; want the US unit complete and the German unit failing by the 100.00 the unmapped account holds", v)
	}
	if rec := call(a, http.MethodGet, "/v1/consol/collections/"+c.ID+"/validations", tenantID, api.DutyRun, "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("with the run duty only = %d, want 403", rec.Code)
	}
}

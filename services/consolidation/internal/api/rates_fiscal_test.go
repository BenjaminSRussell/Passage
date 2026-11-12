package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/fxclient"
	"github.com/BenjaminSRussell/passage/platform/orgclient"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
)

func TestCollection_RatesComeFromTheRateTables(t *testing.T) {
	a, fake := groupsAPI(t)
	rates := fxclient.Fake{}
	rates.Set("average", "EUR", "USD", decimal.RequireFromString("1.08"))
	a.WithRates(rates, "", "")
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR"}`, leDE)))
	mapping := `{"accounts":[{"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet"},{"mainAccount":"3000","consolidationAccount":"EQUITY","accountType":"balance_sheet"}]}`
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", mapping); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d", rec.Code)
	}
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-03-10", "de-1", line("1000", false, "100"), line("3000", true, "100"))
	path := base + "/" + g.ID + "/collections"

	// The closing rate is missing from the tables: the collection names the pair and the type, and stores nothing.
	rec := call(a, http.MethodPost, path, tenantID, api.DutyRun, "c1", `{"period":"2026-03"}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "closing rate from EUR to USD") {
		t.Fatalf("missing closing rate = %d %s", rec.Code, rec.Body.String())
	}
	rates.Set("closing", "EUR", "USD", decimal.RequireFromString("1.10"))
	c := created[struct{ ID string }](t, call(a, http.MethodPost, path, tenantID, api.DutyRun, "c2", `{"period":"2026-03"}`))
	d := decode[struct {
		Lines []struct{ MainAccount, Rate, GroupAmount string }
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+c.ID, tenantID, api.DutyView, "", ""))
	if len(d.Lines) != 2 || d.Lines[0].Rate != "1.1" || d.Lines[0].GroupAmount != "110.00" {
		t.Fatalf("lines = %+v", d.Lines)
	}

	// A rate in the request wins over the tables for its currency.
	c2 := created[struct{ ID string }](t, call(a, http.MethodPost, path, tenantID, api.DutyRun, "c3", `{"period":"2026-03","rates":[{"currency":"EUR","average":"2","closing":"3"}]}`))
	d2 := decode[struct {
		Lines []struct{ GroupAmount string }
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+c2.ID, tenantID, api.DutyView, "", ""))
	if d2.Lines[0].GroupAmount != "300.00" {
		t.Fatalf("explicit rate lines = %+v", d2.Lines)
	}
}

func TestCollection_IncomeStartsWithTheOrgFiscalYear(t *testing.T) {
	a, fake := groupsAPI(t)
	// The entity's calendar starts in July, though the unit was set up with the calendar year.
	a.WithFiscalCalendar(orgclient.Fake{StartMonth: map[string]int{leUS: 7}})
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	mapping := `{"accounts":[{"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet"},{"mainAccount":"4000","consolidationAccount":"REVENUE","accountType":"income"}]}`
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", mapping); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d", rec.Code)
	}
	postLedger(t, fake, leUS, "L-US", "USD", "2026-05-10", "us-a", line("1000", false, "60"), line("4000", true, "60")) // last fiscal year
	postLedger(t, fake, leUS, "L-US", "USD", "2026-08-10", "us-b", line("1000", false, "40"), line("4000", true, "40")) // this fiscal year
	postLedger(t, fake, leUS, "L-US", "USD", "2026-11-10", "us-c", line("1000", false, "25"), line("4000", true, "25")) // this fiscal year
	c := created[struct{ ID string }](t, call(a, http.MethodPost, base+"/"+g.ID+"/collections", tenantID, api.DutyRun, "c1", `{"period":"2026-11"}`))
	tb := decode[struct {
		Rows []struct{ Account, Total string }
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+c.ID+"/trial-balance", tenantID, api.DutyView, "", ""))
	got := map[string]string{}
	for _, r := range tb.Rows {
		got[r.Account] = r.Total
	}
	// Cash is cumulative (125); revenue is only the 65 since 1 July.
	if got["CASH"] != "125.00" || got["REVENUE"] != "-65.00" {
		t.Fatalf("trial balance = %+v", got)
	}
}

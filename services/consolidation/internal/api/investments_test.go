package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
)

const mappingWithDividends = `{"accounts":[
  {"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet","role":"asset"},
  {"mainAccount":"3000","consolidationAccount":"EQUITY","accountType":"balance_sheet","role":"equity"},
  {"mainAccount":"3900","consolidationAccount":"DIVIDENDS","accountType":"balance_sheet","role":"dividend_paid"},
  {"mainAccount":"4000","consolidationAccount":"REVENUE","accountType":"income"},
  {"mainAccount":"4100","consolidationAccount":"IC_REVENUE","accountType":"income","role":"ic_sales"},
  {"mainAccount":"5100","consolidationAccount":"IC_COGS","accountType":"income","role":"ic_cost"}]}`

func TestRun_DividendsLayersAndTheEquityMethodOverAStoredCollection(t *testing.T) {
	a, fake := groupsAPI(t)
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	sub := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"USD"}`, leDE)))
	assoc := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", `{"legalEntityId":"00000000-0000-0000-0000-0000000000a3","ledgerId":"L-JV","currency":"USD"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+sub.ID+"/ownership", tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2024-01-01","percent":"80"}`))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+assoc.ID+"/ownership", tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2024-01-01","percent":"30"}`))
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", mappingWithDividends); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+assoc.ID+"/terms", tenantID, api.DutyConfigure, "", `{"investmentCost":"500","historicalRate":"1","method":"sideways"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown method = %d", rec.Code)
	}
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+assoc.ID+"/terms", tenantID, api.DutyConfigure, "", `{"investmentCost":"500","historicalRate":"1","method":"equity"}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"method":"equity"`) {
		t.Fatalf("equity terms = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+sub.ID+"/terms", tenantID, api.DutyConfigure, "", `{"investmentCost":"900","shareCapital":"1000","historicalRate":"1","sellerIsSubsidiary":true}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"method":"full"`) {
		t.Fatalf("full terms = %d %s", rec.Code, rec.Body.String())
	}

	// The subsidiary: equity 1000, 2026 profit 200 (IC sales 100 at cost 60), 50 of dividends declared.
	postLedger(t, fake, leDE, "L-DE", "USD", "2025-01-05", "de-1", line("1000", false, "1000"), line("3000", true, "1000"))
	postLedger(t, fake, leDE, "L-DE", "USD", "2026-02-10", "de-2", line("1000", false, "300"), line("4000", true, "200"), line("4100", true, "100"))
	postLedger(t, fake, leDE, "L-DE", "USD", "2026-02-11", "de-3", line("5100", false, "60"), line("1000", true, "60"))
	postLedger(t, fake, leDE, "L-DE", "USD", "2026-03-01", "de-4", line("3900", false, "50"), line("1000", true, "50"))
	// The associate: equity 1000, profit 200 in 2026, 100 of dividends declared.
	postLedger(t, fake, "00000000-0000-0000-0000-0000000000a3", "L-JV", "USD", "2025-01-05", "jv-1", line("1000", false, "1000"), line("3000", true, "1000"))
	postLedger(t, fake, "00000000-0000-0000-0000-0000000000a3", "L-JV", "USD", "2026-02-10", "jv-2", line("1000", false, "200"), line("4000", true, "200"))
	postLedger(t, fake, "00000000-0000-0000-0000-0000000000a3", "L-JV", "USD", "2026-03-01", "jv-3", line("3900", false, "100"), line("1000", true, "100"))
	postLedger(t, fake, leUS, "L-US", "USD", "2025-01-05", "us-1", line("1000", false, "5000"), line("3000", true, "5000"))
	// Grant the ledger fake the third legal entity.
	fake.AddLedger("L-JV", "00000000-0000-0000-0000-0000000000a3")

	collect := base + "/" + g.ID + "/collections"
	prev := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-prev", `{"period":"2025-12"}`))
	cur := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-cur", `{"period":"2026-03"}`))
	_ = prev

	// IC layers: 10 units of IC goods at 12 legal cost, group cost 6 (profit 60), and 5 units with no group cost on the 40 percent margin.
	layerURL := base + "/" + g.ID + "/units/" + sub.ID + "/layers/2026-03"
	if rec := call(a, http.MethodPut, layerURL, tenantID, api.DutyConfigure, "", `{"layers":[{"ref":"r1","quantity":"-1","legalUnitCost":"12"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative layer = %d", rec.Code)
	}
	if rec := call(a, http.MethodPut, layerURL, tenantID, api.DutyConfigure, "", `{"layers":[{"ref":"r1","quantity":"10","legalUnitCost":"12","groupUnitCost":"6"},{"ref":"r2","quantity":"5","legalUnitCost":"10"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("layers = %d %s", rec.Code, rec.Body.String())
	}
	got := decode[struct {
		Items []struct {
			Unit   string
			Layers []struct{ Ref, GroupUnitCost string }
		}
	}](t, call(a, http.MethodGet, base+"/"+g.ID+"/layers/2026-03", tenantID, api.DutyView, "", ""))
	if len(got.Items) != 1 || got.Items[0].Unit != sub.ID || len(got.Items[0].Layers) != 2 || got.Items[0].Layers[0].GroupUnitCost != "6" || got.Items[0].Layers[1].GroupUnitCost != "" {
		t.Fatalf("layers listed = %+v", got)
	}

	// With the dividend and the layers, no unit needs the request to say anything about them.
	rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r1", fmt.Sprintf(`{"collectionId":%q,"openingRates":[]}`, cur.ID))
	run := created[struct {
		ID        string
		UnitCount int
		Warnings  []string
	}](t, rec)
	if run.UnitCount != 2 || len(run.Warnings) != 1 || !strings.Contains(run.Warnings[0], "CNS-0908") {
		t.Fatalf("run = %+v %s; want the subsidiary and the associate, and the margin-fallback warning", run, rec.Body.String())
	}

	// E6 for the subsidiary: 50 declared, 80 percent to the parent (40.00), 20 percent to the NCI (10.00).
	// E3 from the layers: 10 x (12 - 6) = 60 plus 5 x 10 x 40 percent = 20: 80, not the unsold share's.
	entries := decode[struct {
		Items []struct{ Unit, Rule, Account, Debit, Credit string }
	}](t, call(a, http.MethodGet, "/v1/consol/runs/"+run.ID, tenantID, api.DutyView, "", ""))
	_ = entries
	st := decode[struct {
		Statements []struct{ Statement, Line, Amount string }
	}](t, call(a, http.MethodGet, "/v1/consol/runs/"+run.ID+"/statements", tenantID, api.DutyView, "", ""))
	lines := map[string]string{}
	for _, l := range st.Statements {
		lines[l.Statement+"/"+l.Line] = l.Amount
	}
	// The subsidiary earned 240 (revenue 200 and IC sales 100 less IC cost 60): consolidated income is that less E3's 80 plus the group's 30
	// percent of the associate's 200. The 50 dividend left the net assets (1000 + 240 - 50 = 1190) without being a translation difference.
	if lines["income/UNREALIZED_PROFIT_E3"] != "-80.00" || lines["income/SHARE_OF_PROFIT_ASSOCIATES"] != "60.00" || lines["income/CONSOLIDATED_NET_INCOME"] != "220.00" ||
		lines["net_assets/DIVIDENDS_DECLARED"] != "-50.00" || lines["net_assets/CTA"] != "0.00" || lines["net_assets/CLOSING_NET_ASSETS"] != "1190.00" {
		t.Fatalf("statements = %v", lines)
	}

	// The investments register: the subsidiary at cost, the associate at 500 + 60 - 30 (30 percent of its 100 dividend) = 530.
	reg := decode[struct {
		RunID    string
		Currency string
		Items    []struct{ Unit, Method, OwnershipPct, Cost, OpeningCarrying, ShareOfProfit, Dividends, ClosingCarrying string }
	}](t, call(a, http.MethodGet, "/v1/consol/runs/"+run.ID+"/investments", tenantID, api.DutyView, "", ""))
	if len(reg.Items) != 2 || reg.Currency != "USD" {
		t.Fatalf("register = %+v", reg)
	}
	by := map[string]struct{ Unit, Method, OwnershipPct, Cost, OpeningCarrying, ShareOfProfit, Dividends, ClosingCarrying string }{}
	for _, it := range reg.Items {
		by[it.Unit] = it
	}
	if s := by[sub.ID]; s.Method != "full" || s.Cost != "900.00" || s.ClosingCarrying != "900.00" || s.Dividends != "40.00" {
		t.Errorf("subsidiary line = %+v", s)
	}
	if as := by[assoc.ID]; as.Method != "equity" || as.OpeningCarrying != "500.00" || as.ShareOfProfit != "60.00" || as.Dividends != "30.00" || as.ClosingCarrying != "530.00" {
		t.Errorf("associate line = %+v", as)
	}
	latest := decode[struct {
		RunID string
		Items []struct{ Unit string }
	}](t, call(a, http.MethodGet, base+"/"+g.ID+"/investments?period=2026-03", tenantID, api.DutyView, "", ""))
	if latest.RunID != run.ID || len(latest.Items) != 2 {
		t.Fatalf("latest register = %+v", latest)
	}
	if rec := call(a, http.MethodGet, base+"/"+g.ID+"/investments", tenantID, api.DutyView, "", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no period = %d", rec.Code)
	}
	if empty := decode[struct{ Items []struct{ Unit string } }](t, call(a, http.MethodGet, base+"/"+g.ID+"/investments?period=2030-01", tenantID, api.DutyView, "", "")); len(empty.Items) != 0 {
		t.Fatalf("a period without a run has %d lines", len(empty.Items))
	}

	// The next fiscal year starts the associate from the closing carrying amount of the register at the previous year end: run December
	// 2026 (530), then March 2027 with a profit of 100 in 2027 (30 percent is 30): 530 + 30 = 560.
	dec26 := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-dec", `{"period":"2026-12"}`))
	if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r-dec", fmt.Sprintf(`{"collectionId":%q}`, dec26.ID)); rec.Code != http.StatusCreated {
		t.Fatalf("december run = %d %s", rec.Code, rec.Body.String())
	}
	postLedger(t, fake, "00000000-0000-0000-0000-0000000000a3", "L-JV", "USD", "2027-02-10", "jv-4", line("1000", false, "100"), line("4000", true, "100"))
	postLedger(t, fake, leDE, "L-DE", "USD", "2027-02-10", "de-5", line("1000", false, "10"), line("4000", true, "10"))
	mar27 := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c-mar27", `{"period":"2027-03"}`))
	run27 := created[struct{ ID string }](t, call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r-27", fmt.Sprintf(`{"collectionId":%q}`, mar27.ID)))
	reg27 := decode[struct {
		Items []struct{ Unit, OpeningCarrying, ShareOfProfit, ClosingCarrying string }
	}](t, call(a, http.MethodGet, "/v1/consol/runs/"+run27.ID+"/investments", tenantID, api.DutyView, "", ""))
	for _, it := range reg27.Items {
		if it.Unit == assoc.ID && (it.OpeningCarrying != "530.00" || it.ShareOfProfit != "30.00" || it.ClosingCarrying != "560.00") {
			t.Errorf("associate in 2027 = %+v, want opening 530.00, share 30.00, closing 560.00", it)
		}
	}
	// An equity unit the group controls is refused with the reason.
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+assoc.ID+"/ownership", tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2026-06-01","percent":"60"}`))
	if rec := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r-27b", fmt.Sprintf(`{"collectionId":%q}`, mar27.ID)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "50 percent") {
		t.Fatalf("controlled associate = %d %s", rec.Code, rec.Body.String())
	}
}

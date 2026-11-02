package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/api"
	"github.com/BenjaminSRussell/passage/services/consolidation/internal/store"
	"github.com/BenjaminSRussell/passage/services/consolidation/migrations"
)

const runsPath = "/v1/consol/runs"

const runBody = `{"groupId":"G1","period":"2026-06","currency":"USD","units":[{
  "id":"S1","ownershipPct":"80",
  "rates":{"closing":"1.10","average":"1.08","opening":"1.05","historical":"1.05"},
  "icSales":"100000","icCost":"70000","unsoldShare":"0.4","icReceivable":"100000",
  "payableGroup":"108500","shareCapital":"500000","preAcqRe":"200000",
  "investmentCost":"620000","netIncome":"150000","sellerIsSubsidiary":true,
  "openingNetAssets":"700000","closingNetAssets":"850000"}]}`

func created[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func runsAPI(t *testing.T) *api.API {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return api.New(store.New(pool))
}

func call(a *api.API, method, path, tenant, duties, key, body string) *httptest.ResponseRecorder {
	return callAs(a, false, method, path, tenant, duties, key, body)
}

func callAs(a *api.API, leScoped bool, method, path, tenant, duties, key, body string) *httptest.ResponseRecorder {
	r := httpx.NewRouter()
	a.Register(r)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Passage-User", "user-7")
	if tenant != "" {
		req.Header.Set("X-Passage-Tenant", tenant)
	}
	if duties != "" {
		if !leScoped {
			duties += ",org.switcher.all_scope,tenant.administer"
			req.Header.Set("X-Passage-LegalEntity-Scope", "ALL")
		} else {
			req.Header.Set("X-Passage-Legal-Entity", "00000000-0000-0000-0000-0000000000e1")
		}
		req.Header.Set("X-Passage-Duties", duties)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

type runOut struct {
	ID, GroupID, Period, Currency, Status, InputsHash, CreatedBy, CTAAmount string
	Version, UnitCount, EliminationCount                                    int
}

func TestRuns_StartListGetStatements(t *testing.T) {
	a := runsAPI(t)
	rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "k1", runBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start = %d %s", rec.Code, rec.Body.String())
	}
	started := created[runOut](t, rec)
	if started.Version != 1 || started.Status != "completed" || started.CreatedBy != "user-7" || started.CTAAmount != "38000.00" ||
		started.UnitCount != 1 || started.EliminationCount != 17 || rec.Header().Get("Location") != runsPath+"/"+started.ID {
		t.Fatalf("started = %+v", started)
	}

	replay := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "k1", runBody)
	if replay.Code != http.StatusCreated || replay.Header().Get("Idempotent-Replayed") != "true" || created[runOut](t, replay).ID != started.ID {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	other := strings.Replace(runBody, `"netIncome":"150000"`, `"netIncome":"160000"`, 1)
	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "k1", other); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "CNS-0922") {
		t.Fatalf("key reuse = %d %s", rec.Code, rec.Body.String())
	}
	rerun := created[runOut](t, call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "k2", runBody))
	if rerun.Version != 2 || rerun.ID == started.ID || rerun.InputsHash != started.InputsHash {
		t.Fatalf("rerun = %+v", rerun)
	}

	list := decode[struct{ Items []runOut }](t, call(a, http.MethodGet, runsPath+"?groupId=G1&period=2026-06&limit=1", tenantID, api.DutyView, "", ""))
	if len(list.Items) != 1 || list.Items[0].ID != rerun.ID {
		t.Fatalf("list = %+v", list)
	}

	got := decode[struct {
		runOut
		Balances     []struct{ Unit, Account, Amount string }
		Eliminations []struct{ Unit, Rule, Account, Debit, Credit string }
	}](t, call(a, http.MethodGet, runsPath+"/"+started.ID, tenantID, api.DutyView, "", ""))
	if got.ID != started.ID || len(got.Balances) != 6 || len(got.Eliminations) != 17 || got.Balances[2].Account != "CTA" || got.Balances[2].Amount != "38000.00" {
		t.Fatalf("detail = %+v", got)
	}

	st := decode[struct {
		runOut
		Statements []struct{ Statement, Line, Amount string }
	}](t, call(a, http.MethodGet, runsPath+"/"+started.ID+"/statements", tenantID, api.DutyView, "", ""))
	amounts := map[string]string{}
	for _, l := range st.Statements {
		amounts[l.Statement+"/"+l.Line] = l.Amount
	}
	if st.Version != 1 || amounts["income/CONSOLIDATED_NET_INCOME"] != "149040.00" || amounts["income/NCI_PROFIT"] != "29808.00" || amounts["net_assets/CTA"] != "38000.00" {
		t.Fatalf("statements = %+v", amounts)
	}
}

func TestRuns_Errors(t *testing.T) {
	a := runsAPI(t)
	missing := "/00000000-0000-0000-0000-0000000000aa"
	cases := []struct {
		name                                    string
		method, path, tenant, duties, key, body string
		want                                    int
		code                                    string
	}{
		{"no idempotency key", http.MethodPost, runsPath, tenantID, api.DutyRun, "", runBody, 400, "COM-0001"},
		{"view duty cannot start", http.MethodPost, runsPath, tenantID, api.DutyView, "k", runBody, 403, ""},
		{"run duty cannot read", http.MethodGet, runsPath, tenantID, api.DutyRun, "", "", 403, ""},
		{"no tenant", http.MethodGet, runsPath, "", api.DutyView, "", "", 401, ""},
		{"bad period", http.MethodPost, runsPath, tenantID, api.DutyRun, "k", strings.Replace(runBody, "2026-06", "2026-6", 1), 400, "COM-0001"},
		{"no units", http.MethodPost, runsPath, tenantID, api.DutyRun, "k", `{"groupId":"G1","period":"2026-06","currency":"USD","units":[]}`, 400, "COM-0001"},
		{"missing opening rate", http.MethodPost, runsPath, tenantID, api.DutyRun, "k", strings.Replace(runBody, `"opening":"1.05",`, "", 1), 400, "COM-0001"},
		{"bad decimal", http.MethodPost, runsPath, tenantID, api.DutyRun, "k", strings.Replace(runBody, `"700000"`, `"7e5"`, 1), 400, "COM-0001"},
		{"foreign currency unit", http.MethodPost, runsPath, tenantID, api.DutyRun, "k", strings.Replace(runBody, `"id":"S1",`, `"id":"S1","currency":"EUR",`, 1), 400, "COM-0001"},
		{"bargain purchase", http.MethodPost, runsPath, tenantID, api.DutyRun, "k", strings.Replace(runBody, `"620000"`, `"1"`, 1), 422, "CNS-0920"},
		{"unknown run", http.MethodGet, runsPath + missing, tenantID, api.DutyView, "", "", 404, "CNS-0921"},
		{"unknown statements", http.MethodGet, runsPath + missing + "/statements", tenantID, api.DutyView, "", "", 404, "CNS-0921"},
		{"malformed id", http.MethodGet, runsPath + "/nope", tenantID, api.DutyView, "", "", 404, "CNS-0921"},
		{"bad limit", http.MethodGet, runsPath + "?limit=0", tenantID, api.DutyView, "", "", 400, "COM-0001"},
		{"bad cursor", http.MethodGet, runsPath + "?after=x", tenantID, api.DutyView, "", "", 400, "COM-0001"},
	}
	for _, c := range cases {
		rec := call(a, c.method, c.path, c.tenant, c.duties, c.key, c.body)
		if rec.Code != c.want || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body.String(), c.want, c.code)
		}
	}
	if rec := call(a, http.MethodGet, runsPath, tenantID, api.DutyView, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty list = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRuns_OtherTenantSeesNothing(t *testing.T) {
	a := runsAPI(t)
	started := created[runOut](t, call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "k1", runBody))
	other := "01890a5d-ac96-774b-bcce-b302099a8099"
	if rec := call(a, http.MethodGet, runsPath+"/"+started.ID, other, api.DutyView, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other tenant read = %d", rec.Code)
	}
	var list struct{ Items []runOut }
	if err := json.Unmarshal(call(a, http.MethodGet, runsPath, other, api.DutyView, "", "").Body.Bytes(), &list); err != nil || len(list.Items) != 0 {
		t.Fatalf("other tenant list = %+v err=%v", list, err)
	}
}

func TestRuns_LegalEntityScopedCallersAreRefused(t *testing.T) {
	a := runsAPI(t)
	started := created[runOut](t, call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "k1", runBody))
	cases := []struct{ method, path, duty, key, body string }{
		{http.MethodPost, runsPath, api.DutyRun, "k2", runBody},
		{http.MethodGet, runsPath, api.DutyView, "", ""},
		{http.MethodGet, runsPath + "/" + started.ID, api.DutyView, "", ""},
		{http.MethodGet, runsPath + "/" + started.ID + "/statements", api.DutyView, "", ""},
	}
	for _, c := range cases {
		rec := callAs(a, true, c.method, c.path, tenantID, c.duty+",org.switcher.all_scope,tenant.administer", c.key, c.body)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CNS-0923") {
			t.Errorf("%s %s as a legal-entity caller = %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := call(a, http.MethodGet, runsPath, tenantID, api.DutyView, "", ""); rec.Code != http.StatusOK {
		t.Errorf("an all-entity caller = %d", rec.Code)
	}
	if rec := callAs(a, false, http.MethodGet, runsPath, tenantID, api.DutyView, "", ""); rec.Code != http.StatusOK {
		t.Errorf("all-entity read = %d", rec.Code)
	}
}

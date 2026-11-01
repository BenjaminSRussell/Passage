package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

const (
	apiEntity  = "00000000-0000-0000-0000-0000000000e1"
	glDuties   = "EamAssetMaintain,EamPlanner,EamTechnician,EamInquire,EamSetupAdmin"
	apiTenant  = 61
	otherEntID = "00000000-0000-0000-0000-0000000000e2"
)

func glHeaders(duties string) map[string]string {
	h := headers(duties)
	h["X-Passage-Tenant"] = testkit.TenantID(apiTenant)
	h["X-Passage-Legal-Entity"] = apiEntity
	return h
}

func TestGL_ValidationAndAuthorizationBeforeTheStore(t *testing.T) {
	h := New(nil, nil).WithLedger(ledgerclient.NewFake()).Routes()
	ledgerID := ids.New()
	cases := []struct {
		name, method, path, body, duties string
		status                           int
	}{
		{"post needs the planner duty", http.MethodPost, "/v1/eam/gl-postings", `{}`, "EamInquire", http.StatusForbidden},
		{"profile needs the setup duty", http.MethodPut, "/v1/eam/posting-profiles", `{}`, "EamPlanner", http.StatusForbidden},
		{"post without a ledger", http.MethodPost, "/v1/eam/gl-postings", `{"currency":"USD"}`, "EamPlanner", http.StatusBadRequest},
		{"post with a bad currency", http.MethodPost, "/v1/eam/gl-postings", `{"ledgerId":"` + ledgerID + `","currency":"usd"}`, "EamPlanner", http.StatusBadRequest},
		{"post for another legal entity", http.MethodPost, "/v1/eam/gl-postings", `{"ledgerId":"` + ledgerID + `","currency":"USD","legalEntityId":"` + otherEntID + `"}`, "EamPlanner", http.StatusBadRequest},
		{"post with a bad site", http.MethodPost, "/v1/eam/gl-postings", `{"ledgerId":"` + ledgerID + `","currency":"USD","siteId":"x"}`, "EamPlanner", http.StatusBadRequest},
		{"profile with a bad type", http.MethodPut, "/v1/eam/posting-profiles", `{"workOrderType":"NOPE","partsExpenseAccount":"1","laborExpenseAccount":"1","partsOffsetAccount":"1","laborOffsetAccount":"1"}`, "EamSetupAdmin", http.StatusBadRequest},
		{"profile with a bad account", http.MethodPut, "/v1/eam/posting-profiles", `{"partsExpenseAccount":"a b","laborExpenseAccount":"1","partsOffsetAccount":"1","laborOffsetAccount":"1"}`, "EamSetupAdmin", http.StatusBadRequest},
		{"reconciliation needs a period as YYYY-MM", http.MethodGet, "/v1/eam/gl-reconciliation?ledgerId=" + ledgerID + "&currency=USD&periodTo=2026-13", ``, "EamInquire", http.StatusBadRequest},
		{"reconciliation needs a currency", http.MethodGet, "/v1/eam/gl-reconciliation?ledgerId=" + ledgerID, ``, "EamInquire", http.StatusBadRequest},
	}
	for _, c := range cases {
		if rec := call(t, h, c.method, c.path, c.body, glHeaders(c.duties)); rec.Code != c.status {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, rec.Body, c.status)
		}
	}
	unconfigured := New(nil, nil).Routes()
	expectCode(t, call(t, unconfigured, http.MethodPost, "/v1/eam/gl-postings", `{}`, glHeaders(glDuties)), http.StatusServiceUnavailable, CodeGLUnavailable)
	expectCode(t, call(t, unconfigured, http.MethodGet, "/v1/eam/gl-reconciliation", ``, glHeaders(glDuties)), http.StatusServiceUnavailable, CodeGLUnavailable)
}

func TestGL_HTTPFlowProfileBookingAndReconciliation(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	fake, ledgerID := ledgerclient.NewFake(), ids.New()
	s := store.NewWithClock(pool, func() time.Time { return time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC) })
	h := New(s, s.Ready).WithLedger(fake).Routes()
	send := func(method, path, body, key string, extra map[string]string) *httptest.ResponseRecorder {
		hdr := glHeaders(glDuties)
		hdr["Idempotency-Key"] = key
		for k, v := range extra {
			hdr[k] = v
		}
		return call(t, h, method, path, body, hdr)
	}

	var asset struct {
		ID string `json:"id"`
	}
	decodeInto(t, send(http.MethodPost, "/v1/eam/assets", `{"name":"Pump","assetType":"PUMP","siteId":"`+testkit.TenantID(9)+`","criticality":3}`, "a1", nil), http.StatusCreated, &asset)
	decodeInto(t, send(http.MethodPost, "/v1/eam/assets/"+asset.ID+"/activate", ``, "act", map[string]string{"If-Match": `"1"`}), http.StatusOK, &asset)
	var wo struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	decodeInto(t, send(http.MethodPost, "/v1/eam/work-orders", `{"assetId":"`+asset.ID+`","workOrderType":"INSPECTION","currency":"USD","jobType":"LUBE"}`, "w1", nil), http.StatusCreated, &wo)
	decodeInto(t, send(http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/labor", `{"hours":"8","rate":"45.00"}`, "l1", nil), http.StatusCreated, &wo)
	if err := dbx.WithTenantTx(testkit.Ctx(testkit.TenantID(apiTenant)), pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update work_order set parts_cost = 350 where id = $1`, wo.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	version := wo.Version
	for _, to := range []string{"SCHEDULED", "RELEASED", "IN_PROGRESS"} {
		var got struct {
			Version int `json:"version"`
		}
		decodeInto(t, send(http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"`+to+`"}`, "s"+to, map[string]string{"If-Match": `"` + strconv.Itoa(version) + `"`}), http.StatusOK, &got)
		version = got.Version
	}
	decodeInto(t, send(http.MethodPost, "/v1/eam/work-orders/"+wo.ID+"/status", `{"to":"COMPLETED","actualStart":"2026-10-20T08:00:00Z","actualEnd":"2026-10-20T16:00:00Z"}`, "sdone",
		map[string]string{"If-Match": `"` + strconv.Itoa(version) + `"`}), http.StatusOK, &wo)

	profile := `{"workOrderType":"ALL","partsExpenseAccount":"6100","laborExpenseAccount":"6110","partsOffsetAccount":"1400","laborOffsetAccount":"5900"}`
	if rec := send(http.MethodPut, "/v1/eam/posting-profiles", profile, "p1", nil); rec.Code != http.StatusCreated {
		t.Fatalf("create profile: %d %s", rec.Code, rec.Body)
	}
	expectCode(t, send(http.MethodPut, "/v1/eam/posting-profiles", profile, "p2", nil), http.StatusPreconditionRequired, "precondition_required")
	if rec := send(http.MethodPut, "/v1/eam/posting-profiles", profile, "p3", map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusOK {
		t.Fatalf("replace profile: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Items       []store.PostingProfile `json:"items"`
		Placeholder store.PostingProfile   `json:"placeholder"`
	}
	decodeInto(t, send(http.MethodGet, "/v1/eam/posting-profiles", ``, "", nil), http.StatusOK, &list)
	if len(list.Items) != 1 || list.Items[0].Version != 2 || !list.Placeholder.Placeholder {
		t.Fatalf("profiles %+v", list)
	}

	body := `{"legalEntityId":"` + apiEntity + `","ledgerId":"` + ledgerID + `","currency":"USD"}`
	var posted store.GLPostResult
	decodeInto(t, send(http.MethodPost, "/v1/eam/gl-postings", body, "g1", nil), http.StatusOK, &posted)
	if len(posted.Booked) != 1 || posted.Booked[0].Lines != 4 {
		t.Fatalf("booked %+v", posted)
	}
	var rec struct {
		Reconciled bool `json:"reconciled"`
		Accounts   []struct{ Account, Subledger, GL, Difference string }
	}
	decodeInto(t, send(http.MethodGet, "/v1/eam/gl-reconciliation?ledgerId="+ledgerID+"&currency=USD&periodTo=2026-10", ``, "", nil), http.StatusOK, &rec)
	if !rec.Reconciled || len(rec.Accounts) != 4 || rec.Accounts[0].Account != "1400" || rec.Accounts[0].GL != "-350.00" || rec.Accounts[2].Account != "6100" || rec.Accounts[2].GL != "350.00" || rec.Accounts[3].Subledger != "360.00" {
		t.Fatalf("reconciliation %+v", rec)
	}

	failing := New(s, s.Ready).WithLedger(balanceRefused{fake}).Routes()
	expectCode(t, call(t, failing, http.MethodGet, "/v1/eam/gl-reconciliation?ledgerId="+ledgerID+"&currency=USD", ``, glHeaders(glDuties)), http.StatusBadGateway, CodeGLUnavailable)
}

type balanceRefused struct{ ledgerclient.Ledger }

func (balanceRefused) Balance(context.Context, ledgerclient.BalanceQuery) (ledgerclient.Balance, error) {
	return ledgerclient.Balance{}, &ledgerclient.Error{Op: "balance", Status: http.StatusForbidden, Code: "AUTH-0003"}
}

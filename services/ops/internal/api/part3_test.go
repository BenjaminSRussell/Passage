package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
	"github.com/BenjaminSRussell/passage/services/ops/migrations"
)

type fakeSLO struct {
	samples []domain.SLOSample
	err     error
}

func (f *fakeSLO) Samples(context.Context, time.Duration) ([]domain.SLOSample, error) {
	return f.samples, f.err
}

type env3 struct {
	*env
	api  *API
	priv ed25519.PrivateKey
}

func newEnv3(t *testing.T, now time.Time, cfg func(*Config)) *env3 {
	t.Helper()
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	operator := ids.New()
	st := store.New(pool).WithOperator(operator)
	if err := st.Seed(context.Background(), []domain.Region{{Name: "us-east", Role: domain.Primary}, {Name: "eu-west", Role: domain.Passive}}); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Region: "us-east", MaxLagSeconds: 300, OperatorTenantID: operator, LicensePublicKey: pub, Now: func() time.Time { return now }}
	if cfg != nil {
		cfg(&c)
	}
	a := New(st, c)
	r := httpx.NewRouterWithoutTenant()
	a.Register(r)
	return &env3{env: &env{router: r, tenant: operator, pool: pool, admin: admin}, api: a, priv: priv}
}

// outbox returns the event types written for an aggregate id, oldest first (read as the owner: the outbox has tenant RLS).
func (e *env3) outbox(t *testing.T, aggregateID string) []string {
	t.Helper()
	rows, err := e.admin.Query(context.Background(), `select type from outbox where aggregate_id = $1 order by created_at, id`, aggregateID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func decodeJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
}

func TestProvisionEnforcesResidencyAndCompletesWithItsEvent(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), nil)
	tid := ids.New()
	body := func(residency, region string) string {
		return `{"tenantId":"` + tid + `","name":"Cedar Ridge Pumps","residency":"` + residency + `","region":"` + region + `"}`
	}
	if rec := e.do("POST", "/v1/ops/tenants", "alice", DutyView, "k1", body("EU", "eu-west")); rec.Code != http.StatusForbidden {
		t.Fatalf("provision needs its duty: %d", rec.Code)
	}
	if rec := e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "", body("EU", "eu-west")); rec.Code != http.StatusBadRequest {
		t.Fatalf("Idempotency-Key is required: %d", rec.Code)
	}
	// An EU tenant may not land in a US region, and a region the platform does not run is refused too (OPS-1705).
	rec := e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "k1", body("EU", "us-east"))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "OPS-1705") {
		t.Fatalf("EU into us-east = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "k1", body("EU", "eu-central")); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not deployed") {
		t.Fatalf("undeployed region = %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "k1", body("EU", "eu-west"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("provision = %d %s", rec.Code, rec.Body)
	}
	var p store.Provision
	decodeJSON(t, rec, &p)
	if p.Status != domain.ProvisionRunning || p.Region != "eu-west" || p.Residency != "EU" || p.Tier != "PROD" || len(p.Plan) != 5 {
		t.Fatalf("provision = %+v", p)
	}
	if rec := e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "k1", body("EU", "eu-west")); rec.Code != http.StatusOK {
		t.Fatalf("replay = %d", rec.Code)
	}
	if rec := e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "k2", body("EU", "eu-west")); rec.Code != http.StatusConflict {
		t.Fatalf("second provision of the tenant = %d %s", rec.Code, rec.Body)
	}

	step := func(action, status string) *httptest.ResponseRecorder {
		return e.do("PUT", "/v1/ops/tenants/"+tid+"/provision-steps/"+action, "runner", DutyProvision, "", `{"status":"`+status+`"}`)
	}
	if rec := step("realm", "FAILED"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"FAILED"`) {
		t.Fatalf("failed step = %d %s", rec.Code, rec.Body)
	}
	for _, a := range []string{"realm", "databases", "kafka-acls", "lakehouse"} {
		if rec := step(a, "DONE"); rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", a, rec.Code, rec.Body)
		}
	}
	// The license step needs an installed licence.
	if rec := step("license", "DONE"); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "OPS-1724") {
		t.Fatalf("license step without a licence = %d %s", rec.Code, rec.Body)
	}
	if len(e.outbox(t, tid)) != 0 {
		t.Fatal("no event before the last step")
	}
	if rec := e.put("/v1/ops/tenants/"+tid+"/license", "alice", DutyLicenseInstall, "0", signedLicense(e.priv, tid, "2030-12-31", 9)); rec.Code != http.StatusOK {
		t.Fatalf("install = %d %s", rec.Code, rec.Body)
	}
	rec = step("license", "DONE")
	decodeJSON(t, rec, &p)
	if p.Status != domain.ProvisionDone || p.ProvisionedAt == nil {
		t.Fatalf("provision after the last step = %+v", p)
	}
	step("license", "DONE") // a repeat writes no second event
	got := e.outbox(t, tid)
	if len(got) != 2 || got[0] != store.EventLicenseChanged || got[1] != store.EventTenantProvision {
		t.Fatalf("events = %v", got)
	}
	var payload []byte
	if err := e.admin.QueryRow(context.Background(), `select payload from outbox where type = $1`, store.EventTenantProvision).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		TenantID string `json:"tenantid"`
		Data     struct {
			Region string `json:"region"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &evt); err != nil || evt.TenantID != tid || evt.Data.Region != "eu-west" {
		t.Fatalf("event = %s", payload)
	}
	if rec := e.do("GET", "/v1/ops/tenants", "alice", DutyView, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Cedar Ridge Pumps") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/licenses", "alice", DutyView, "", ""); !strings.Contains(rec.Body.String(), `"tenantName":"Cedar Ridge Pumps"`) {
		t.Fatalf("licence list names the tenant: %s", rec.Body)
	}
}

const testRates = `{"cpu_core_hours":"0.04","memory_gb_hours":"0.005","storage_gb":"0.10","kafka_gb":"0.02","lakehouse_gb":"0.023","ai_tokens":"0.000002"}`
const testUsage = `{"cpu_core_hours":"1000","memory_gb_hours":"4000","storage_gb":"500","kafka_gb":"200","lakehouse_gb":"300","ai_tokens":"1000000"}`

func TestCostStatementRecordReadCSVAndMonthList(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), func(c *Config) {
		r, err := domain.ParseCostRates(testRates)
		if err != nil {
			t.Fatal(err)
		}
		c.CostRates = &r
	})
	tid := ids.New()
	path := "/v1/ops/tenants/" + tid + "/costs/2026-10"
	body := `{"quantities":` + testUsage + `,"usdPerEur":"1.0850","modelVersion":"q-2026-10"}`
	if rec := e.do("PUT", path, "job", DutyView, "", body); rec.Code != http.StatusForbidden {
		t.Fatalf("record needs its duty: %d", rec.Code)
	}
	if rec := e.do("PUT", path, "job", DutyCostRecord, "", `{"quantities":{"cpu_core_hours":"1"},"usdPerEur":"1.08","modelVersion":"v"}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "COST-1902") {
		t.Fatalf("missing dimensions = %d %s", rec.Code, rec.Body)
	}
	rec := e.do("PUT", path, "job", DutyCostRecord, "", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("record = %d %s", rec.Code, rec.Body)
	}
	var st store.CostRecord
	decodeJSON(t, rec, &st)
	// The figures of docs/ops/tools/test_cost_meter.py.
	if st.CostUSD != "137.65" || st.CostEUR != "126.86" || st.Lines["cpu_core_hours"] != "40.00" {
		t.Fatalf("statement = %+v", st)
	}
	if rec := e.do("GET", path+"?format=csv", "alice", DutyView, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "total_usd,,137.65") {
		t.Fatalf("csv = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/costs?month=2026-10", "alice", DutyView, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"clusterCostUsd":"137.65"`) {
		t.Fatalf("month list = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/tenants/"+tid+"/costs/2026-09", "alice", DutyView, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown month = %d", rec.Code)
	}
}

func TestCostRecordWithoutRatesIs503(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), nil)
	rec := e.do("PUT", "/v1/ops/tenants/"+ids.New()+"/costs/2026-10", "job", DutyCostRecord, "", `{"quantities":`+testUsage+`,"usdPerEur":"1.08","modelVersion":"v"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "COST-1904") {
		t.Fatalf("no rates = %d %s", rec.Code, rec.Body)
	}
}

func TestLicenseUsageDrivesTheHourlyCheckAndItsEvent(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	e := newEnv3(t, now, nil)
	tid := ids.New()
	if rec := e.put("/v1/ops/tenants/"+tid+"/license", "alice", DutyLicenseInstall, "0", signedLicense(e.priv, tid, "2027-06-30", 9)); rec.Code != http.StatusOK {
		t.Fatalf("install = %d %s", rec.Code, rec.Body)
	}
	if got := e.outbox(t, tid); len(got) != 1 || got[0] != store.EventLicenseChanged {
		t.Fatalf("install event = %v", got)
	}
	usage := `{"activeFull":12,"activeOperational":3,"activeSelfservice":40}`
	if rec := e.do("PUT", "/v1/ops/tenants/"+tid+"/license/usage", "reporter", DutyView, "", usage); rec.Code != http.StatusForbidden {
		t.Fatalf("usage needs its duty: %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/tenants/"+tid+"/license/usage", "reporter", DutyLicenseReport, "", `{"activeFull":-1,"activeOperational":0,"activeSelfservice":0}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative count = %d", rec.Code)
	}
	if rec := e.do("PUT", "/v1/ops/tenants/"+tid+"/license/usage", "reporter", DutyLicenseReport, "", usage); rec.Code != http.StatusOK {
		t.Fatalf("usage = %d %s", rec.Code, rec.Body)
	}
	// 12 active full users on a 9-user licence: GRACE, from the reported count without a query parameter.
	if rec := e.do("GET", "/v1/ops/tenants/"+tid+"/license", "alice", DutyView, "", ""); !strings.Contains(rec.Body.String(), `"state":"GRACE"`) {
		t.Fatalf("license with reported usage = %s", rec.Body)
	}
	e.api.CheckOnce(context.Background())
	e.api.CheckOnce(context.Background()) // unchanged: no second event
	if got := e.outbox(t, tid); len(got) != 2 {
		t.Fatalf("events after the checks = %v", got)
	}
	rec := e.do("GET", "/v1/ops/licenses", "alice", DutyView, "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"activeFull":12`) || !strings.Contains(rec.Body.String(), `"lastState":"GRACE"`) {
		t.Fatalf("licences = %d %s", rec.Code, rec.Body)
	}
}

func TestSLOReportAndBreachEventOncePerBreach(t *testing.T) {
	p95slow, p95fast := 3.1, 0.12
	src := &fakeSLO{samples: []domain.SLOSample{
		{Service: "ledger", Requests: 10000, Failed: 50, P95: &p95fast},
		{Service: "billing", Requests: 10000, Failed: 1, P95: &p95slow},
		{Service: "quiet", Requests: 0},
	}}
	e := newEnv3(t, time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), func(c *Config) { c.SLOSource = src })
	if rec := e.do("GET", "/v1/ops/slo?window=2h", "alice", DutyView, "", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad window = %d", rec.Code)
	}
	rec := e.do("GET", "/v1/ops/slo", "alice", DutyView, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("slo = %d %s", rec.Code, rec.Body)
	}
	var rep domain.SLOReport
	decodeJSON(t, rec, &rep)
	if rep.Window != "24h" || rep.Breached != 2 || len(rep.Services) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	byName := map[string]domain.SLOLine{}
	for _, l := range rep.Services {
		byName[l.Service] = l
	}
	if !byName["ledger"].AvailabilityBreach || byName["ledger"].LatencyBreach || !byName["billing"].LatencyBreach || byName["quiet"].Breached || !byName["quiet"].NoTrafficInWindow {
		t.Fatalf("lines = %+v", byName)
	}
	e.api.CheckOnce(context.Background())
	e.api.CheckOnce(context.Background())
	if got := append(e.outbox(t, "ledger"), e.outbox(t, "billing")...); len(got) != 2 || got[0] != store.EventSLOBreached {
		t.Fatalf("breach events = %v", got)
	}
	rec = e.do("GET", "/v1/ops/slo", "alice", DutyView, "", "")
	if !strings.Contains(rec.Body.String(), `"breachedSince":"2026-11-12T10:00:00Z"`) {
		t.Fatalf("breachedSince missing: %s", rec.Body)
	}
	src.err = errors.New("down")
	if rec := e.do("GET", "/v1/ops/slo", "alice", DutyView, "", ""); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "OPS-1725") {
		t.Fatalf("source down = %d %s", rec.Code, rec.Body)
	}
}

func TestSLOWithoutPrometheusIs503(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), nil)
	if rec := e.do("GET", "/v1/ops/slo", "alice", DutyView, "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no source = %d %s", rec.Code, rec.Body)
	}
}

func TestFailoverCompletedEventAfterTheLastStep(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), nil)
	if rec := e.do("POST", "/v1/ops/failover", "alice", DutyFailover, "evt-9", `{"to":"eu-west"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("failover = %d %s", rec.Code, rec.Body)
	}
	rec := e.do("GET", "/v1/ops/failovers/evt-9", "alice", DutyView, "", "")
	var d store.FailoverDetail
	decodeJSON(t, rec, &d)
	if len(d.Plan.Steps) == 0 {
		t.Fatalf("plan = %+v", d)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, s := range d.Plan.Steps {
		if len(e.outbox(t, "evt-9")) != 0 {
			t.Fatalf("event before step %d", i)
		}
		body := `{"order":` + string(rune('1'+i)) + `,"region":"` + s.Region + `","status":"DONE","startedAt":"` + now + `","finishedAt":"` + now + `"}`
		if rec := e.do("PUT", "/v1/ops/failovers/evt-9/steps/"+s.Action, "runner", DutyFailover, "", body); rec.Code >= 300 {
			t.Fatalf("step %s = %d %s", s.Action, rec.Code, rec.Body)
		}
	}
	if got := e.outbox(t, "evt-9"); len(got) != 1 || got[0] != store.EventFailoverComplete {
		t.Fatalf("events = %v", got)
	}
}

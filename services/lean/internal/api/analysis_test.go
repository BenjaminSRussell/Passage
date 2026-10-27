package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/lean/internal/domain"
	"github.com/BenjaminSRussell/passage/services/lean/internal/store"
)

const (
	mixID  = "66666666-0000-4000-8000-000000000001"
	fillID = "66666666-0000-4000-8000-000000000002"
	vendID = "66666666-0000-4000-8000-000000000003"
)

var (
	analysisPath  = "/v1/lean/kanban-rules/" + ruleID + "/analysis"
	backflushPath = periodsPath + "/" + periodID + ":backflush"
	exportPath    = periodsPath + ":export"
)

func ptr[T any](v T) *T { return &v }

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func clock(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func TestRuleAnalysis_KPIOverThePeriod(t *testing.T) {
	fs := &fakeStore{activity: store.RuleActivity{
		Rule: store.Rule{Code: "SAUCE-FIX", Replenishment: "fixed", NumberOfKanbans: ptr(4), ActiveKanbans: 3, LeadTimeH: 8},
		Cycles: []domain.Cycle{
			{PlannedAt: clock("2026-09-02T08:00:00Z"), Due: clock("2026-09-02T16:00:00Z"), CompletedAt: *clock("2026-09-02T14:00:00Z")},
			{PlannedAt: clock("2026-09-03T08:00:00Z"), Due: clock("2026-09-03T16:00:00Z"), CompletedAt: *clock("2026-09-03T19:00:00Z")},
		},
		OverdueOpen: 1,
	}}
	rec := call{method: "GET", path: analysisPath + "?from=2026-09-01&to=2026-09-30", duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if fs.gotRange[0].Format(store.DateLayout) != "2026-09-01" || fs.gotRange[1].Format(store.DateLayout) != "2026-09-30" {
		t.Fatalf("range %v", fs.gotRange)
	}
	var got RuleAnalysis
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// 6 h and 11 h → 8.50 h average; one of two cycles late → 50.00 %.
	if got.RuleID != ruleID || got.RuleCode != "SAUCE-FIX" || got.Cycles != 2 || got.AvgLeadTimeH == nil || *got.AvgLeadTimeH != "8.50" ||
		got.TargetLeadTimeH != 8 || got.LateCycles != 1 || got.LatePct == nil || *got.LatePct != "50.00" ||
		got.ActiveKanbans != 3 || got.MaxKanbans == nil || *got.MaxKanbans != 4 || got.OverdueOpen != 1 || got.From != "2026-09-01" {
		t.Fatalf("analysis %s", rec.Body)
	}
}

func TestRuleAnalysis_DefaultsAndNothingMeasured(t *testing.T) {
	fs := &fakeStore{activity: store.RuleActivity{Rule: store.Rule{Code: "SAUCE-EVT", Replenishment: "event"}}}
	rec := call{method: "GET", path: analysisPath, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !fs.gotRange[1].Equal(today) || !fs.gotRange[0].Equal(today.AddDate(0, 0, -29)) {
		t.Fatalf("default range %v", fs.gotRange)
	}
	// An event rule has no maximum, and nothing measured is null, not zero.
	body := rec.Body.String()
	for _, want := range []string{`"cycles":0`, `"avgLeadTimeH":null`, `"latePct":null`, `"maxKanbans":null`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestRuleAnalysis_RefusesBadInput(t *testing.T) {
	cases := map[string]struct{ query, field string }{
		"backwards":   {"?from=2026-09-30&to=2026-09-01", "to"},
		"too long":    {"?from=2025-08-31&to=2026-09-01", "from"},
		"bad from":    {"?from=01/09/2026&to=2026-09-30", "from"},
		"bad to":      {"?to=tomorrow", "to"},
		"default end": {"?from=2020-01-01", "from"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call{method: "GET", path: analysisPath + c.query, duties: DutyRead}.do(newRouter(fs))
			if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors[c.field] == "" {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
			if !fs.gotRange[0].IsZero() {
				t.Fatal("store called for a bad period")
			}
		})
	}
	// A full year of 366 days is allowed.
	if rec := (call{method: "GET", path: analysisPath + "?from=2024-01-01&to=2024-12-31", duties: DutyRead}).do(newRouter(&fakeStore{})); rec.Code != http.StatusOK {
		t.Fatalf("leap year: %d %s", rec.Code, rec.Body)
	}
	if rec := (call{method: "GET", path: "/v1/lean/kanban-rules/nope/analysis", duties: DutyRead}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := (call{method: "GET", path: analysisPath, duties: DutyKanbanWork}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("without lean.read: %d", rec.Code)
	}
	fs := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeRuleNotFound, "no rule")}
	if rec := (call{method: "GET", path: analysisPath, duties: DutyRead}).do(newRouter(fs)); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "LEAN-0102" {
		t.Fatalf("missing rule: %d %s", rec.Code, rec.Body)
	}
}

func periodWork() store.PeriodWork {
	return store.PeriodWork{
		Period: store.Period{ID: periodID, FlowID: flowID, PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30", Currency: "USD", Status: "open"},
		Activities: []domain.ActivityWork{
			{ActivityID: mixID, Sequence: 1, Name: "Mix", Kind: "process", CycleTimeS: 45, Jobs: 10, Qty: dec("250")},
			{ActivityID: fillID, Sequence: 2, Name: "Fill", Kind: "process", CycleTimeS: 30, Jobs: 0, Qty: dec("0")},
			{ActivityID: vendID, Sequence: 3, Name: "Label", Kind: "service", CycleTimeS: 20, Jobs: 10, Qty: dec("250")},
		},
	}
}

func TestBackflush_PreviewsConversionFromCompletedJobs(t *testing.T) {
	fs := &fakeStore{work: periodWork()}
	body := `{"rates":[{"activityId":"` + mixID + `","ratePerHour":"90"}]}`
	// A read-only preview: no Idempotency-Key needed.
	rec := call{method: "POST", path: backflushPath, body: body, duties: DutyFlowSettle}.do(newRouter(fs))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if fs.gotFlowID != flowID || fs.gotPeriod != periodID {
		t.Fatalf("store got %s %s", fs.gotFlowID, fs.gotPeriod)
	}
	var got BackflushPreview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Mix: 45 s × 250 = 3.125 h × 90 = 281.25 USD; Fill had no jobs; Label is a vendor service.
	if got.ConversionCost != "281.25" || got.Currency != "USD" || len(got.Activities) != 3 {
		t.Fatalf("preview %s", rec.Body)
	}
	mix, fill, label := got.Activities[0], got.Activities[1], got.Activities[2]
	if mix.Hours != "3.1250" || mix.Cost == nil || *mix.Cost != "281.25" || mix.RatePerHour == nil || *mix.RatePerHour != "90" || mix.ProcessedQty != "250" {
		t.Fatalf("mix %+v", mix)
	}
	if fill.Cost == nil || *fill.Cost != "0.00" || fill.RatePerHour != nil || label.Cost != nil || label.RatePerHour != nil {
		t.Fatalf("fill %+v label %+v", fill, label)
	}
}

func TestBackflush_RefusesBadRatesAndChecksDuty(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"missing rate": {`{"rates":[]}`, "Mix completed 10 jobs"},
		"duplicate":    {`{"rates":[{"activityId":"` + mixID + `","ratePerHour":"1"},{"activityId":"` + mixID + `","ratePerHour":"2"}]}`, "more than one rate"},
		"bad id":       {`{"rates":[{"activityId":"mix","ratePerHour":"1"}]}`, "COM-0001"},
		"no amount":    {`{"rates":[{"activityId":"` + mixID + `"}]}`, "COM-0001"},
		"service":      {`{"rates":[{"activityId":"` + mixID + `","ratePerHour":"1"},{"activityId":"` + vendID + `","ratePerHour":"1"}]}`, "service activity"},
		"negative":     {`{"rates":[{"activityId":"` + mixID + `","ratePerHour":"-1"}]}`, "zero or more"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := call{method: "POST", path: backflushPath, body: c.body, duties: DutyFlowSettle}.do(newRouter(&fakeStore{work: periodWork()}))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.want) {
				t.Fatalf("status %d %s", rec.Code, rec.Body)
			}
		})
	}
	if rec := (call{method: "POST", path: backflushPath, body: `{"rates":[]}`, duties: DutyRead}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("read duty previewed a backflush: %d", rec.Code)
	}
	if rec := (call{method: "POST", path: periodsPath + "/nope:backflush", body: `{"rates":[]}`, duties: DutyFlowSettle}).do(newRouter(&fakeStore{})); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad period id: %d", rec.Code)
	}
	fs := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodePeriodNotFound, "no period")}
	if rec := (call{method: "POST", path: backflushPath, body: `{"rates":[]}`, duties: DutyFlowSettle}).do(newRouter(fs)); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "LEAN-0110" {
		t.Fatalf("missing period: %d %s", rec.Code, rec.Body)
	}
}

func TestExportPeriods_CSVOldestFirst(t *testing.T) {
	settled := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	fs := &fakeStore{periods: []store.Period{ // the store lists latest first
		{PeriodStart: "2026-09-01", PeriodEnd: "2026-09-30", Currency: "USD", Status: "open"},
		{PeriodStart: "2026-08-01", PeriodEnd: "2026-08-31", Currency: "USD", Status: "settled", MaterialCost: ptr("900.10"), ConversionCost: ptr("300.00"),
			OutputValue: ptr("1150.25"), Variance: ptr("49.85"), Kanbans: ptr(10), SettledAt: &settled, SettledBy: ptr("=HYPERLINK(1)")},
	}}
	rec := call{method: "GET", path: exportPath, duties: DutyRead}.do(newRouter(fs))
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || fs.gotLimit != store.MaxList {
		t.Fatalf("status %d %v limit %d", rec.Code, rec.Header(), fs.gotLimit)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows %v %v", rows, err)
	}
	if strings.Join(rows[1], ",") != "2026-08-01,2026-08-31,settled,USD,900.10,300.00,1150.25,49.85,10,2026-09-02T10:00:00Z,'=HYPERLINK(1)," {
		t.Fatalf("settled row %q", rows[1])
	}
	if strings.Join(rows[2], ",") != "2026-09-01,2026-09-30,open,USD,,,,,,,," {
		t.Fatalf("open row %q", rows[2])
	}
	if rec := (call{method: "GET", path: exportPath, duties: DutyKanbanWork}).do(newRouter(&fakeStore{})); rec.Code != http.StatusForbidden {
		t.Fatalf("without lean.read: %d", rec.Code)
	}
	fs = &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeFlowNotFound, "no flow")}
	if rec := (call{method: "GET", path: exportPath, duties: DutyRead}).do(newRouter(fs)); rec.Code != http.StatusNotFound || problemOf(t, rec).Code != "LEAN-0101" {
		t.Fatalf("missing flow: %d %s", rec.Code, rec.Body)
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	resourceID = "0195f0a0-0000-7000-8000-000000000010"
	siteID     = "0195f0a0-0000-7000-8000-000000000011"
	downtimeID = "0195f0a0-0000-7000-8000-000000000012"
	floorDuty  = DutySetup + "," + DutyIngest + "," + DutyRegister + "," + DutyView + "," + DutyExport
)

// The base fake answers floor calls with empty results; floorFake records them.
func (f *fakeStore) RegisterMachine(_ context.Context, m store.Machine, _ store.Idem) (store.Machine, error) {
	return m, nil
}

func (f *fakeStore) ListMachines(context.Context, int) ([]store.Machine, error) {
	return []store.Machine{}, nil
}

func (f *fakeStore) IngestMachineEvents(context.Context, time.Time, []store.MachineEventIn) (store.IngestResult, error) {
	return store.IngestResult{}, nil
}

func (f *fakeStore) ListDowntimes(context.Context, store.DowntimeFilter) ([]store.Downtime, error) {
	return []store.Downtime{}, nil
}

func (f *fakeStore) CodeDowntime(_ context.Context, id, _, _ string, _ store.Idem) (store.Downtime, error) {
	return store.Downtime{ID: id}, nil
}

func (f *fakeStore) OpenAndon(context.Context, string) ([]store.Andon, error) {
	return []store.Andon{}, nil
}

func (f *fakeStore) GetAndonSettings(context.Context) (store.AndonSettingsView, error) {
	return store.AndonSettingsView{AndonSettings: domain.DefaultAndonSettings(), Default: true}, nil
}
func (f *fakeStore) PutAndonSettings(_ context.Context, set domain.AndonSettings, by string) (store.AndonSettingsView, error) {
	return store.AndonSettingsView{AndonSettings: set, UpdatedBy: by}, nil
}
func (f *fakeStore) SweepAndon(context.Context, time.Time) (store.AndonSweepResult, error) {
	return store.AndonSweepResult{}, nil
}

func (f *fakeStore) ClearAndon(_ context.Context, id, _, _ string, _ time.Time, _ store.Idem) (store.Andon, error) {
	return store.Andon{ID: id}, nil
}

func (f *fakeStore) LoadOEEStreams(context.Context, []string, time.Time, time.Time) (map[string]domain.OEEStream, error) {
	return nil, nil
}

func (f *fakeStore) ExportPay(_ context.Context, from, to, by string, _ store.Idem) (store.PayExport, error) {
	return store.PayExport{FromDate: from, ToDate: to, CreatedBy: by}, nil
}

func (f *fakeStore) ListPayExports(context.Context, int) ([]store.PayExport, error) {
	return []store.PayExport{}, nil
}

func (f *fakeStore) GetPayExport(_ context.Context, id string) (store.PayExport, error) {
	return store.PayExport{}, httpx.NewProblem(http.StatusNotFound, store.CodeExportNotFound, "export batch not found")
}

type floorFake struct {
	*fakeStore
	machines      []store.Machine
	ingested      []store.MachineEventIn
	downFilter    store.DowntimeFilter
	coded         []string
	andonSite     string
	streams       map[string]domain.OEEStream
	streamWindow  [2]time.Time
	exportRange   [3]string
	exportErr     error
	ingestCalls   int
	exportCalls   int
	registerCalls int
}

func newFloorFake() *floorFake { return &floorFake{fakeStore: &fakeStore{}} }

func (f *floorFake) RegisterMachine(_ context.Context, m store.Machine, _ store.Idem) (store.Machine, error) {
	f.registerCalls++
	m.ID = resourceID
	f.machines = append(f.machines, m)
	return m, nil
}

func (f *floorFake) IngestMachineEvents(_ context.Context, _ time.Time, items []store.MachineEventIn) (store.IngestResult, error) {
	f.ingestCalls++
	f.ingested = append(f.ingested, items...)
	return store.IngestResult{Accepted: len(items), Gaps: []store.GapReport{{MachineID: "M-101", From: 3, To: 4}}, AndonsRaised: 1}, nil
}

func (f *floorFake) ListDowntimes(_ context.Context, filter store.DowntimeFilter) ([]store.Downtime, error) {
	f.downFilter = filter
	return []store.Downtime{{ID: downtimeID, ResourceID: resourceID, Category: "unplanned"}}, nil
}

func (f *floorFake) CodeDowntime(_ context.Context, id, reason, category string, _ store.Idem) (store.Downtime, error) {
	f.coded = append(f.coded, id+"|"+reason+"|"+category)
	return store.Downtime{ID: id, ResourceID: resourceID, Category: "unplanned", ReasonCode: &reason}, nil
}

func (f *floorFake) OpenAndon(_ context.Context, site string) ([]store.Andon, error) {
	f.andonSite = site
	return []store.Andon{{ID: regID, ResourceID: resourceID, Kind: "downtime", Severity: "warning", StartedAt: time.Now()}}, nil
}

func (f *floorFake) LoadOEEStreams(_ context.Context, _ []string, from, to time.Time) (map[string]domain.OEEStream, error) {
	f.streamWindow = [2]time.Time{from, to}
	return f.streams, nil
}

func (f *floorFake) ExportPay(_ context.Context, from, to, by string, _ store.Idem) (store.PayExport, error) {
	f.exportCalls++
	f.exportRange = [3]string{from, to, by}
	if f.exportErr != nil {
		return store.PayExport{}, f.exportErr
	}
	return store.PayExport{BatchID: regID, FromDate: from, ToDate: to, LineCount: 2, CreatedBy: by, CSV: "batchId\n"}, nil
}

func TestMachines_CreateValidatesAndReturns201(t *testing.T) {
	fs := newFloorFake()
	h := newRouter(fs)
	body := fmt.Sprintf(`{"machineId":" M-101 ","resourceId":%q,"siteId":%q,"idealCycleSeconds":"1.5"}`, resourceID, siteID)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/machines", body, floorDuty)
	if rec.Code != http.StatusCreated || len(fs.machines) != 1 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if m := fs.machines[0]; m.Code != "M-101" || !m.IdealCycleSeconds.Equal(decimal.RequireFromString("1.5")) || *m.SiteID != siteID {
		t.Fatalf("stored %+v", m)
	}

	bad := `{"machineId":"","resourceId":"x","siteId":"y","idealCycleSeconds":"0.0001"}`
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/machines", bad, floorDuty)
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if rec.Code != http.StatusBadRequest || len(p.Errors) != 4 {
		t.Fatalf("validation: %d %+v", rec.Code, p.Errors)
	}
	for _, ideal := range []string{`"0"`, `"-1"`, `"86401"`, `60`} {
		rec = call(t, h, http.MethodPost, "/v1/shopfloor/machines",
			fmt.Sprintf(`{"machineId":"M","resourceId":%q,"idealCycleSeconds":%s}`, resourceID, ideal), floorDuty)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("idealCycleSeconds %s accepted: %d", ideal, rec.Code)
		}
	}
	rec = callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, floorDuty, nil)
	if rec.Code != http.StatusBadRequest || fs.registerCalls != 1 {
		t.Fatalf("missing Idempotency-Key: %d, calls %d", rec.Code, fs.registerCalls)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machines", body, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("without setup duty: %d", rec.Code)
	}
}

func TestMachineEvents_ValidatesEveryEventAndReportsGaps(t *testing.T) {
	fs := newFloorFake()
	h := newRouter(fs)
	good := `{"events":[{"machineId":"M-101","seq":1,"kind":"state","ts":"2026-10-21T08:00:00Z","state":"RUNNING"},
		{"machineId":"M-101","seq":2,"kind":"measurement","ts":"2026-10-21T08:00:01Z","measurement":{"name":"temp_c","value":71.4,"unit":"Cel"}}]}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", good, DutyIngest)
	if rec.Code != http.StatusOK || len(fs.ingested) != 2 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if m := fs.ingested[1]; m.MeasurementName == nil || *m.MeasurementName != "temp_c" || !m.MeasurementValue.Equal(decimal.RequireFromString("71.4")) {
		t.Fatalf("measurement %+v", m)
	}
	var res store.IngestResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Accepted != 2 || len(res.Gaps) != 1 || res.AndonsRaised != 1 {
		t.Fatalf("result %+v", res)
	}

	bad := `{"events":[{"machineId":"","seq":0,"kind":"bogus","state":"BROKEN","goodCount":-1,"jobId":"x"},
		{"machineId":"M-1","seq":1,"kind":"state","ts":"2026-10-21T08:00:00Z"},
		{"machineId":"M-1","seq":2,"kind":"alarm","ts":"2026-10-21T08:00:00Z","goodCount":3},
		{"machineId":"M-1","seq":3,"kind":"measurement","ts":"2026-10-21T08:00:00Z"}]}`
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", bad, DutyIngest)
	var p httpx.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	for _, field := range []string{"events[0].machineId", "events[0].seq", "events[0].kind", "events[0].ts", "events[0].state",
		"events[0].goodCount", "events[0].jobId", "events[1].state", "events[2].goodCount", "events[3].measurement"} {
		if _, ok := p.Errors[field]; !ok {
			t.Errorf("no error for %s: %+v", field, p.Errors)
		}
	}
	if rec.Code != http.StatusBadRequest || fs.ingestCalls != 1 {
		t.Fatalf("invalid batch reached the store: %d, calls %d", rec.Code, fs.ingestCalls)
	}
	many := `{"events":[` + strings.TrimSuffix(strings.Repeat(`{"machineId":"M","seq":1,"kind":"alarm","ts":"2026-10-21T08:00:00Z"},`, maxIngest+1), ",") + `]}`
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", many, DutyIngest); rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized batch: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", good, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("without ingest duty: %d", rec.Code)
	}
}

func TestDowntimes_ListAndCode(t *testing.T) {
	fs := newFloorFake()
	h := newRouter(fs)
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/downtimes?open=true&resourceId="+resourceID, "", DutyView)
	if rec.Code != http.StatusOK || !fs.downFilter.OpenOnly || fs.downFilter.ResourceID != resourceID {
		t.Fatalf("list %d %+v", rec.Code, fs.downFilter)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/downtimes?open=maybe", "", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad open filter: %d", rec.Code)
	}

	path := "/v1/shopfloor/downtimes/" + downtimeID + ":code"
	rec = call(t, h, http.MethodPost, path, `{"reasonCode":" JAM ","category":"planned"}`, DutyRegister)
	if rec.Code != http.StatusOK || len(fs.coded) != 1 || fs.coded[0] != downtimeID+"|JAM|planned" {
		t.Fatalf("code %d %v %s", rec.Code, fs.coded, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, path, `{"reasonCode":"","category":"later"}`, DutyRegister); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reason: %d", rec.Code)
	}
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/downtimes/nope:code", `{"reasonCode":"JAM"}`, DutyRegister)
	if rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeDowntimeNotFound {
		t.Fatalf("malformed id: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, path, `{"reasonCode":"JAM"}`, DutyRegister, nil); rec.Code != http.StatusBadRequest || len(fs.coded) != 1 {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
}

func TestAndon_ListsOpenAlertsBySite(t *testing.T) {
	fs := newFloorFake()
	h := newRouter(fs)
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/andon?siteId="+siteID, "", DutyView)
	var out itemsResponse[store.Andon]
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || len(out.Items) != 1 || fs.andonSite != siteID {
		t.Fatalf("andon %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/andon?siteId=x", "", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad site id: %d", rec.Code)
	}
}

// AC-25.4 over HTTP: the BR-25.5 shift example in one 60-minute-bucket window
// of eight hours gives the factor values of the formula.
func TestOEE_SeriesAndValidation(t *testing.T) {
	fs := newFloorFake()
	t0 := time.Date(2026, 10, 21, 6, 0, 0, 0, time.UTC)
	fs.streams = map[string]domain.OEEStream{resourceID: {
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           domain.MachineRunning,
		Events: []domain.MachineEvent{
			{Seq: 1, Kind: domain.EventCount, At: t0.Add(10 * time.Minute), GoodCount: 361, RejectCount: 19},
			{Seq: 2, Kind: domain.EventState, At: t0.Add(7 * time.Hour), State: domain.MachineDown},
		},
	}}
	h := newRouter(fs)
	other := "0195f0a0-0000-7000-8000-000000000099"
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/oee?resourceIds="+resourceID+","+other+"&from=2026-10-21T02:00:00-04:00&to=2026-10-21T14:00:00Z&bucket=60m", "", DutyView)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if !fs.streamWindow[0].Equal(t0) || fs.streamWindow[0].Location() != time.UTC {
		t.Fatalf("window %v", fs.streamWindow)
	}
	var out itemsResponse[oeeItem]
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Items) != 8 {
		t.Fatalf("buckets %d: %s", len(out.Items), rec.Body.String())
	}
	planned, run, total, good := decimal.Zero, decimal.Zero, int64(0), int64(0)
	for _, b := range out.Items {
		planned, run = planned.Add(b.PlannedMin), run.Add(b.RunMin)
		total, good = total+b.TotalCount, good+b.GoodCount
	}
	shift, err := domain.Compute(domain.OEEInput{ScheduledMinutes: planned, UnplannedDownMinutes: planned.Sub(run),
		IdealCycleSeconds: decimal.NewFromInt(60), TotalCount: total, GoodCount: good})
	if err != nil || !shift.Availability.Equal(decimal.RequireFromString("0.875")) || !shift.OEE.Equal(decimal.RequireFromString("0.7521")) {
		t.Fatalf("shift roll-up from buckets %+v, %v", shift, err)
	}
	if last := out.Items[7]; !last.RunMin.IsZero() || !last.DownMin.Equal(decimal.NewFromInt(60)) {
		t.Fatalf("last bucket %+v", last)
	}

	for _, q := range []string{
		"resourceIds=x&from=2026-10-21T00:00:00Z&to=2026-10-21T01:00:00Z",
		"resourceIds=" + resourceID + "&from=2026-10-21T01:00:00Z&to=2026-10-21T01:00:00Z",
		"resourceIds=" + resourceID + "&from=2026-10-01T00:00:00Z&to=2026-10-21T00:00:00Z",
		"resourceIds=" + resourceID + "&from=2026-10-21T00:00:00Z&to=2026-10-21T01:00:00Z&bucket=10m",
		"resourceIds=" + resourceID + "&from=2026-10-21T00:00:00Z&to=2026-10-21T00:20:00Z",
		"resourceIds=" + resourceID + "&from=yesterday&to=2026-10-21T00:15:00Z",
	} {
		if rec := call(t, h, http.MethodGet, "/v1/shopfloor/oee?"+q, "", DutyView); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", q, rec.Code)
		}
	}
}

func TestPayExport_RangeUserAndKey(t *testing.T) {
	fs := newFloorFake()
	h := newRouter(fs)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/pay:export", `{"from":"2026-10-01","to":"2026-10-31"}`, DutyExport)
	if rec.Code != http.StatusCreated || fs.exportRange != [3]string{"2026-10-01", "2026-10-31", "supervisor-1"} {
		t.Fatalf("export %d %v %s", rec.Code, fs.exportRange, rec.Body.String())
	}
	for _, body := range []string{`{"from":"2026-10-31","to":"2026-10-01"}`, `{"from":"2026-10-01","to":"2026-11-01"}`, `{"from":"oct","to":"2026-10-01"}`} {
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/pay:export", body, DutyExport); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	fs.exportErr = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeNothingToExport, "nothing")
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/pay:export", `{"from":"2026-10-01","to":"2026-10-02"}`, DutyExport)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeNothingToExport {
		t.Fatalf("nothing to export: %d", rec.Code)
	}
	calls := fs.exportCalls
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/pay:export", `{"from":"2026-10-01","to":"2026-10-02"}`, DutyExport, nil); rec.Code != http.StatusBadRequest || fs.exportCalls != calls {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/pay:export", `{"from":"2026-10-01","to":"2026-10-02"}`, DutyApprove); rec.Code != http.StatusForbidden {
		t.Fatalf("approve duty alone exported: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pay-exports/"+regID, "", DutyExport); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeExportNotFound {
		t.Fatalf("unknown batch: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/pay-exports", "", DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("export list without export duty: %d", rec.Code)
	}
}

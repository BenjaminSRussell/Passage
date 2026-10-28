package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const (
	partyID    = "0195f0a0-0000-7000-8000-000000000020"
	kioskDuty  = DutyKioskLogin
	rollupDuty = DutyOEERollup
)

// The base fake answers the kiosk, clocked-in and rollup calls with nothing.
func (f *fakeStore) SaveOEEBuckets(_ context.Context, r store.OEERollup) (store.OEERollupResult, error) {
	return store.OEERollupResult{BucketMinutes: r.BucketMinutes, Computed: len(r.Rows)}, nil
}

func (f *fakeStore) BadgeLogin(context.Context, string) (store.KioskSession, error) {
	return store.KioskSession{}, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeBadgeUnknown, "badge is not recognized")
}

func (f *fakeStore) ClockedIn(context.Context, int) ([]store.ClockedWorker, error) {
	return []store.ClockedWorker{}, nil
}

// kioskFake records kiosk, clocked-in and rollup calls on top of the floor fake.
type kioskFake struct {
	*floorFake
	badges      map[string]store.KioskSession
	badgeErr    error
	badgeSeen   string
	clocked     []store.ClockedWorker
	clockLimit  int
	rollups     []store.OEERollup
	machineList []store.Machine
}

func newKioskFake() *kioskFake { return &kioskFake{floorFake: newFloorFake()} }

func (f *kioskFake) BadgeLogin(_ context.Context, badge string) (store.KioskSession, error) {
	f.badgeSeen = badge
	if f.badgeErr != nil {
		return store.KioskSession{}, f.badgeErr
	}
	if s, ok := f.badges[badge]; ok {
		return s, nil
	}
	return f.fakeStore.BadgeLogin(context.Background(), badge)
}

func (f *kioskFake) ClockedIn(_ context.Context, limit int) ([]store.ClockedWorker, error) {
	f.clockLimit = limit
	return f.clocked, nil
}

func (f *kioskFake) SaveOEEBuckets(_ context.Context, r store.OEERollup) (store.OEERollupResult, error) {
	f.rollups = append(f.rollups, r)
	return store.OEERollupResult{BucketMinutes: r.BucketMinutes, Computed: len(r.Rows), Changed: len(r.Rows)}, nil
}

func (f *kioskFake) ListMachines(context.Context, int) ([]store.Machine, error) {
	return f.machineList, nil
}

func metrics(t *testing.T, a *API) string {
	t.Helper()
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

// FR-25.1: a scanned badge resolves to the active worker with the clock status
// and the idle timeout; an unknown badge is SF-0001 and counted; a malformed
// badge is a validation error; a store failure is not reported as SF-0001.
func TestBadgeLogin_ResolvesWorkerOrRefuses(t *testing.T) {
	in := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	fs := newKioskFake()
	fs.badges = map[string]store.KioskSession{"B-7": {WorkerID: workerID, PartyID: partyID, Name: "Ana Silva", ClockedInAt: &in}}
	a := New(fs)
	r := httpx.NewRouter()
	a.Register(r)

	rec := call(t, r, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":" B-7 "}`, kioskDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if fs.badgeSeen != "B-7" {
		t.Fatalf("badge looked up as %q, want the trimmed scan", fs.badgeSeen)
	}
	var out struct {
		WorkerID           string     `json:"workerId"`
		PartyID            string     `json:"partyId"`
		Name               string     `json:"name"`
		ClockedInAt        *time.Time `json:"clockedInAt"`
		IdleTimeoutMinutes int        `json:"idleTimeoutMinutes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.WorkerID != workerID || out.PartyID != partyID || out.Name != "Ana Silva" || out.IdleTimeoutMinutes != 10 ||
		out.ClockedInAt == nil || !out.ClockedInAt.Equal(in) {
		t.Fatalf("session %+v", out)
	}

	rec = call(t, r, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":"NOPE"}`, kioskDuty)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeBadgeUnknown {
		t.Fatalf("unknown badge: %d %s", rec.Code, rec.Body.String())
	}
	if m := metrics(t, a); !strings.Contains(m, "shopfloor_kiosk_badge_rejected_total 1\n") || !strings.Contains(m, "shopfloor_failed_total 0\n") {
		t.Fatalf("metrics after a refused badge:\n%s", m)
	}
	for _, body := range []string{`{"badgeId":""}`, `{"badgeId":"   "}`, `{"badgeId":"` + strings.Repeat("x", 65) + `"}`, `{}`} {
		if rec := call(t, r, http.MethodPost, "/v1/shopfloor/sessions:badge", body, kioskDuty); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
	if rec := call(t, r, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":"B-7"}`, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("without the kiosk login duty: %d", rec.Code)
	}

	fs.badgeErr = errors.New("connection reset")
	rec = call(t, r, http.MethodPost, "/v1/shopfloor/sessions:badge", `{"badgeId":"B-7"}`, kioskDuty)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("store failure: %d %s", rec.Code, rec.Body.String())
	}
	if m := metrics(t, a); !strings.Contains(m, "shopfloor_kiosk_badge_rejected_total 1\n") || !strings.Contains(m, "shopfloor_failed_total 1\n") {
		t.Fatalf("metrics after a store failure:\n%s", m)
	}
}

// FR-25.14: who is clocked in, with their open jobs; an empty board is an empty
// array; the limit is bounded.
func TestClockedIn_ListsWorkersAndJobs(t *testing.T) {
	at := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	fs := newKioskFake()
	fs.clocked = []store.ClockedWorker{{WorkerID: workerID, Name: "Ana Silva", Active: true, ClockedInAt: at,
		Jobs: []store.CurrentJob{{JobID: jobID, StartedAt: at.Add(30 * time.Minute)}}}}
	h := newRouter(fs)
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/clocked-in?limit=20", "", DutyView)
	if rec.Code != http.StatusOK || fs.clockLimit != 20 {
		t.Fatalf("status %d limit %d: %s", rec.Code, fs.clockLimit, rec.Body.String())
	}
	var out itemsResponse[store.ClockedWorker]
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].Name != "Ana Silva" || len(out.Items[0].Jobs) != 1 || out.Items[0].Jobs[0].JobID != jobID {
		t.Fatalf("items %+v", out.Items)
	}
	empty := call(t, newRouter(&fakeStore{}), http.MethodGet, "/v1/shopfloor/clocked-in", "", DutyView)
	if empty.Code != http.StatusOK || !strings.Contains(empty.Body.String(), `"items":[]`) {
		t.Fatalf("empty board: %d %s", empty.Code, empty.Body.String())
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/clocked-in?limit=0", "", DutyView); rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/clocked-in", "", DutyRegister); rec.Code != http.StatusForbidden {
		t.Fatalf("without the view duty: %d", rec.Code)
	}
}

// BR-25.6 through the API: a machine is registered with the default idle
// threshold, or one inside 30 to 3600 seconds.
func TestMachines_IdleThresholdDefaultAndBounds(t *testing.T) {
	fs := newFloorFake()
	h := newRouter(fs)
	base := `"machineId":"M-102","resourceId":"` + resourceID + `","idealCycleSeconds":"60"`
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machines", "{"+base+"}", floorDuty); rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if fs.machines[0].IdleThresholdSeconds != 300 {
		t.Fatalf("default idle threshold %d", fs.machines[0].IdleThresholdSeconds)
	}
	for _, v := range []string{"29", "3601", "-1", "0"} {
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/machines", "{"+base+`,"idleThresholdSeconds":`+v+"}", floorDuty)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "idleThresholdSeconds") {
			t.Errorf("idle %s: %d %s", v, rec.Code, rec.Body.String())
		}
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machines", "{"+base+`,"idleThresholdSeconds":3600}`, floorDuty); rec.Code != http.StatusCreated {
		t.Fatalf("idle 3600: %d", rec.Code)
	}
	if got := fs.machines[len(fs.machines)-1].IdleThresholdSeconds; got != 3600 {
		t.Fatalf("idle threshold stored %d", got)
	}
}

// An OEE window that reaches past now books nothing after now, and a RUNNING
// machine with no count since the last one is idle up to now (BR-25.6).
func TestOEE_WindowIntoTheFutureStopsAtNow(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	fs := newFloorFake()
	fs.streams = map[string]domain.OEEStream{resourceID: {
		IdealCycleSeconds: decimal.NewFromInt(60),
		IdleThreshold:     5 * time.Minute,
		Initial:           domain.MachineRunning,
		Events: []domain.MachineEvent{
			{Seq: 1, Kind: domain.EventCount, At: t0.Add(4 * time.Minute), GoodCount: 4},
			{Seq: 2, Kind: domain.EventCount, At: t0.Add(8 * time.Minute), GoodCount: 4},
		},
	}}
	h := newRouterAt(fs, func() time.Time { return t0.Add(20 * time.Minute) })
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/oee?resourceIds="+resourceID+"&from=2026-10-21T08:00:00Z&to=2026-10-21T09:00:00Z&bucket=15m", "", DutyView)
	var out itemsResponse[oeeItem]
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusOK || len(out.Items) != 2 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	// 08:00-08:08 run, 08:08-08:20 idle (no count for 12 minutes), nothing after 08:20.
	first, second := out.Items[0], out.Items[1]
	if !first.PlannedMin.Equal(decimal.NewFromInt(15)) || !first.RunMin.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("first bucket %+v", first)
	}
	if !second.PlannedMin.Equal(decimal.NewFromInt(5)) || !second.RunMin.IsZero() {
		t.Fatalf("second bucket %+v", second)
	}
}

// FR-25.10 rollup: the window is validated, the buckets are computed the way
// GET oee computes them and handed to the store with the machine's ideal cycle;
// without resourceIds every registered machine is rolled up.
func TestRollupOEE_ComputesAndStoresBuckets(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	fs := newKioskFake()
	fs.streams = map[string]domain.OEEStream{resourceID: {
		IdealCycleSeconds: decimal.NewFromInt(60),
		Initial:           domain.MachineRunning,
		Events:            []domain.MachineEvent{{Seq: 1, Kind: domain.EventCount, At: t0.Add(10 * time.Minute), GoodCount: 9, RejectCount: 1}},
	}}
	fs.machineList = []store.Machine{{ResourceID: resourceID}}
	a := New(fs)
	a.now = func() time.Time { return t0.Add(2 * time.Hour) }
	h := httpx.NewRouter()
	a.Register(h)

	body := `{"resourceIds":["` + resourceID + `","` + resourceID + `"],"from":"2026-10-21T08:00:00Z","to":"2026-10-21T09:00:00Z","bucket":"15m"}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/oee:rollup", body, rollupDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res store.OEERollupResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.BucketMinutes != 15 || res.Computed != 4 || res.Changed != 4 {
		t.Fatalf("result %+v", res)
	}
	got := fs.rollups[0]
	if len(got.ResourceIDs) != 1 || got.BucketMinutes != 15 || !got.From.Equal(t0) || !got.To.Equal(t0.Add(time.Hour)) || len(got.Rows) != 4 {
		t.Fatalf("rollup %+v", got)
	}
	if first := got.Rows[0]; first.ResourceID != resourceID || !first.IdealCycleSeconds.Equal(decimal.NewFromInt(60)) ||
		first.Bucket.TotalCount != 10 || !first.Bucket.Start.Equal(t0) {
		t.Fatalf("first row %+v", first)
	}
	if m := metrics(t, a); !strings.Contains(m, "shopfloor_oee_buckets_changed_total 4\n") {
		t.Fatalf("metrics:\n%s", m)
	}

	// Without resourceIds the registered machines are rolled up.
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/oee:rollup", `{"from":"2026-10-21T08:00:00Z","to":"2026-10-21T08:15:00Z"}`, rollupDuty)
	if rec.Code != http.StatusOK || len(fs.rollups) != 2 || len(fs.rollups[1].ResourceIDs) != 1 || fs.rollups[1].ResourceIDs[0] != resourceID {
		t.Fatalf("all machines: %d %+v", rec.Code, fs.rollups)
	}

	for name, bad := range map[string]string{
		"from not on a bucket boundary": `{"from":"2026-10-21T08:05:00Z","to":"2026-10-21T08:20:00Z"}`,
		"partial bucket":                `{"from":"2026-10-21T08:00:00Z","to":"2026-10-21T08:20:00Z"}`,
		"over 24 hours":                 `{"from":"2026-10-20T08:00:00Z","to":"2026-10-21T08:15:00Z"}`,
		"ends in the future":            `{"from":"2026-10-21T10:00:00Z","to":"2026-10-21T10:15:00Z"}`,
		"unknown bucket":                `{"from":"2026-10-21T08:00:00Z","to":"2026-10-21T09:00:00Z","bucket":"10m"}`,
		"empty window":                  `{"from":"2026-10-21T08:00:00Z","to":"2026-10-21T08:00:00Z"}`,
		"malformed resource":            `{"resourceIds":["x"],"from":"2026-10-21T08:00:00Z","to":"2026-10-21T08:15:00Z"}`,
		"malformed time":                `{"from":"today","to":"2026-10-21T08:15:00Z"}`,
	} {
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/oee:rollup", bad, rollupDuty); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}
	if len(fs.rollups) != 2 {
		t.Fatalf("an invalid window reached the store: %d rollups", len(fs.rollups))
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/oee:rollup", body, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("without the rollup duty: %d", rec.Code)
	}
}

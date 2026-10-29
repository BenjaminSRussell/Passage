package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

var (
	steward = map[string]string{tenant.HeaderDuties: DutyCatalogSteer}
	archive = map[string]string{tenant.HeaderDuties: DutyArchiveAdmin}
	author  = map[string]string{tenant.HeaderDuties: DutyAuthor}
)

const ruleBody = `{"assetFqn":"core.vendor","ruleType":"COMPLETENESS","expression":"tax_id","severity":"HIGH","ownerUserId":"0193d6a0-0000-7000-8000-000000000001"}`

const scheduleBody = `{"name":"Weekly revenue","targetType":"DASHBOARD","targetId":"finance-overview","cron":"0 6 * * 1","timezone":"Europe/Berlin","format":"PDF","destination":{"type":"EMAIL","targets":["cfo@example.com"]}}`

func with(base map[string]string, extra ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		out[extra[i]] = extra[i+1]
	}
	return out
}

func problemCode(t *testing.T, body string) httpx.Problem {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("not a problem: %s", body)
	}
	return p
}

// Each slice-2 route group requires its own duty (§9.46.17).
func TestSlice2_DutiesEnforced(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/catalog/dq-rules"},
		{http.MethodGet, "/v1/catalog/dq-rules/scores"},
		{http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id"},
		{http.MethodGet, "/v1/archive/partitions"},
		{http.MethodPost, "/v1/archive/partitions/0193d6a0-0000-7000-8000-000000000001/restore"},
		{http.MethodGet, "/v1/analytics/schedules"},
		{http.MethodPost, "/v1/analytics/schedules"},
	}
	for _, c := range cases {
		// AnalyticsView alone opens none of them.
		if rec := do(t, h, c.method, c.path, "{}", viewer); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with AnalyticsView: status %d, want 403", c.method, c.path, rec.Code)
		}
	}
	// Archive restore is not opened by the catalog steward duty, and vice versa.
	if rec := do(t, h, http.MethodGet, "/v1/archive/partitions", "", steward); rec.Code != http.StatusForbidden {
		t.Errorf("steward on archive: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/catalog/dq-rules", "", archive); rec.Code != http.StatusForbidden {
		t.Errorf("archive admin on dq rules: %d", rec.Code)
	}
}

// Validation runs before the store: these requests never reach the (nil) database.
func TestSlice2_ValidationBeforeStore(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		name         string
		method, path string
		body         string
		headers      map[string]string
		status       int
		code, field  string
	}{
		{"rule body not json", http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id", `{"assetFqn":`, steward, 400, domain.CodeValidation, ""},
		{"rule unknown field", http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id", `{"owner":"x"}`, steward, 400, domain.CodeValidation, ""},
		{"rule bad id in path", http.MethodPut, "/v1/catalog/dq-rules/Vendor-Tax", ruleBody, steward, 400, domain.CodeValidation, "ruleId"},
		{"rule float-like threshold", http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id",
			strings.Replace(ruleBody, `"severity"`, `"thresholdPct":"ninety","severity"`, 1), steward, 400, domain.CodeValidation, "thresholdPct"},
		{"rule threshold over 100", http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id",
			strings.Replace(ruleBody, `"severity"`, `"thresholdPct":"100.5","severity"`, 1), steward, 400, domain.CodeValidation, "thresholdPct"},
		{"rule broken regex", http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id",
			strings.Replace(ruleBody, `"COMPLETENESS","expression":"tax_id"`, `"VALIDITY","expression":"(["`, 1), steward, 400, domain.CodeValidation, "expression"},
		{"rule weak etag", http.MethodPut, "/v1/catalog/dq-rules/vendor_tax_id", ruleBody, with(steward, "If-Match", `W/"2"`), 400, domain.CodeValidation, "If-Match"},
		{"rule unknown id", http.MethodGet, "/v1/catalog/dq-rules/Not-An-Id", "", steward, 404, domain.CodeNotFound, ""},
		{"rules bad asset", http.MethodGet, "/v1/catalog/dq-rules?asset=Vendor", "", steward, 400, domain.CodeValidation, "asset"},
		{"scores bad limit", http.MethodGet, "/v1/catalog/dq-rules/scores?limit=1000", "", steward, 400, domain.CodeValidation, "limit"},
		{"scores bad rule", http.MethodGet, "/v1/catalog/dq-rules/scores?rule=A", "", steward, 400, domain.CodeValidation, "rule"},
		{"partitions bad status", http.MethodGet, "/v1/archive/partitions?status=GONE", "", archive, 400, domain.CodeValidation, "status"},
		{"partitions bad domain", http.MethodGet, "/v1/archive/partitions?domain=Ledger", "", archive, 400, domain.CodeValidation, "domain"},
		{"schedule without key", http.MethodPost, "/v1/analytics/schedules", scheduleBody, author, 400, domain.CodeValidation, "Idempotency-Key"},
		{"schedule bad cron", http.MethodPost, "/v1/analytics/schedules",
			strings.Replace(scheduleBody, `0 6 * * 1`, `0 6 * *`, 1), with(author, headerIdempotency, "k1"), 400, domain.CodeValidation, "cron"},
		{"schedule bad zone and format", http.MethodPost, "/v1/analytics/schedules",
			strings.Replace(strings.Replace(scheduleBody, "Europe/Berlin", "Mars/Olympus", 1), `"PDF"`, `"DOCX"`, 1),
			with(author, headerIdempotency, "k1"), 400, domain.CodeValidation, "timezone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.body, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			p := problemCode(t, rec.Body.String())
			if p.Code != tc.code {
				t.Fatalf("code %s, want %s", p.Code, tc.code)
			}
			if tc.field != "" && p.Errors[tc.field] == "" {
				t.Fatalf("problem must name field %s: %s", tc.field, rec.Body.String())
			}
		})
	}
}

func dbServer(t *testing.T, now time.Time) http.Handler {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	a := New(store.New(pool))
	a.now = func() time.Time { return now }
	r := httpx.NewRouter()
	a.Register(r)
	return tenant.Middleware(r)
}

func TestDQRules_PutWithETagThenScores(t *testing.T) {
	h := dbServer(t, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	path := "/v1/catalog/dq-rules/vendor_tax_id"

	rec := do(t, h, http.MethodPut, path, ruleBody, steward)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d etag %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var rule domain.DQRule
	if err := json.Unmarshal(rec.Body.Bytes(), &rule); err != nil || rule.Status != domain.RuleActive || rule.ThresholdPct.String() != "98" {
		t.Fatalf("defaults: %+v %v", rule, err)
	}
	// Thresholds travel as decimal strings.
	if !strings.Contains(rec.Body.String(), `"thresholdPct":"98"`) {
		t.Fatalf("threshold must be a decimal string: %s", rec.Body.String())
	}

	if rec := do(t, h, http.MethodPut, path, ruleBody, steward); rec.Code != http.StatusPreconditionRequired ||
		problemCode(t, rec.Body.String()).Code != domain.CodePreconditionRequired {
		t.Fatalf("replace without If-Match: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, path, ruleBody, with(steward, "If-Match", `"5"`)); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %d %s", rec.Code, rec.Body.String())
	}
	muted := strings.Replace(ruleBody, `}`, `,"muted":true}`, 1)
	rec = do(t, h, http.MethodPut, path, muted, with(steward, "If-Match", `"1"`))
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"muteUntil":"2026-10-28T08:00:00Z"`) {
		t.Fatalf("mute: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}

	rec = do(t, h, http.MethodGet, path, "", steward)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
	if rec := do(t, h, http.MethodGet, "/v1/catalog/dq-rules/vendor_iban", "", steward); rec.Code != http.StatusNotFound {
		t.Fatalf("missing rule: %d", rec.Code)
	}
	var list DQRulesResponse
	rec = do(t, h, http.MethodGet, "/v1/catalog/dq-rules?asset=core.vendor", "", steward)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/v1/catalog/dq-rules/scores?rule=vendor_tax_id", "", steward)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("no scores yet must be an empty list: %d %s", rec.Code, rec.Body.String())
	}
}

func TestArchivePartitions_ListGetRestore(t *testing.T) {
	now := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testTenant})
	pending, err := st.RegisterArchivePartition(ctx, "ledger", "journal_line", "2024-02", false, false)
	if err != nil {
		t.Fatal(err)
	}
	p, err := st.RegisterArchivePartition(ctx, "ledger", "journal_line", "2024-01", false, false)
	if err != nil {
		t.Fatal(err)
	}
	hash := domain.ContentHash([]string{"row"})
	if _, err := st.RecordArchiveVerification(ctx, p.ID, 1, 1, hash, hash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DropArchivePartition(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	a := New(st)
	a.now = func() time.Time { return now }
	r := httpx.NewRouter()
	a.Register(r)
	h := tenant.Middleware(r)

	var list PartitionsResponse
	rec := do(t, h, http.MethodGet, "/v1/archive/partitions?status=DROPPED", "", archive)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].ID != p.ID {
		t.Fatalf("list DROPPED: %s", rec.Body.String())
	}
	restore := "/v1/archive/partitions/" + p.ID + "/restore"
	for i := range 2 {
		rec = do(t, h, http.MethodPost, restore, "", archive)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"RESTORED"`) ||
			!strings.Contains(rec.Body.String(), `"restoreUntil":"2026-11-24T04:00:00Z"`) {
			t.Fatalf("restore #%d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, http.MethodPost, "/v1/archive/partitions/"+pending.ID+"/restore", "", archive)
	if rec.Code != http.StatusConflict || problemCode(t, rec.Body.String()).Code != domain.CodeStateConflict {
		t.Fatalf("restore PENDING: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/archive/partitions/"+p.ID, "", archive); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/archive/partitions/nope", "", archive); rec.Code != http.StatusNotFound {
		t.Fatalf("get malformed id: %d", rec.Code)
	}
}

func TestSchedules_CreateReplayAndList(t *testing.T) {
	h := dbServer(t, time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC))
	headers := with(author, headerIdempotency, "sched-1")

	rec := do(t, h, http.MethodPost, "/v1/analytics/schedules", scheduleBody, headers)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotency-Replayed") != "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created domain.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil || created.NextRunAt == nil ||
		!created.NextRunAt.Equal(time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("created: %+v %v", created, err)
	}
	rec = do(t, h, http.MethodPost, "/v1/analytics/schedules", scheduleBody, headers)
	var replayed domain.Schedule
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotency-Replayed") != "true" ||
		json.Unmarshal(rec.Body.Bytes(), &replayed) != nil || replayed.ID != created.ID {
		t.Fatalf("replay: %d %q %s", rec.Code, rec.Header().Get("Idempotency-Replayed"), rec.Body.String())
	}
	other := strings.Replace(scheduleBody, "Weekly revenue", "Daily revenue", 1)
	rec = do(t, h, http.MethodPost, "/v1/analytics/schedules", other, headers)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec.Body.String()).Code != domain.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %d %s", rec.Code, rec.Body.String())
	}
	var list SchedulesResponse
	rec = do(t, h, http.MethodGet, "/v1/analytics/schedules", "", author)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 {
		t.Fatalf("list: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/v1/analytics/schedules/"+created.ID, "", author)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("get: %d %q", rec.Code, rec.Header().Get("ETag"))
	}
}

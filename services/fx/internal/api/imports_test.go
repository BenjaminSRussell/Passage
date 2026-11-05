package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fx/internal/api"
	"github.com/BenjaminSRussell/passage/services/fx/internal/importer"
	"github.com/BenjaminSRussell/passage/services/fx/internal/provider"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

type fakeImporter struct {
	res   importer.Result
	err   error
	calls []string

	history bool
}

func (f *fakeImporter) SupportsHistory() bool { return f.history }

func (f *fakeImporter) Run(_ context.Context, rateType string, date time.Time) (importer.Result, error) {
	f.calls = append(f.calls, rateType+"@"+date.Format(time.DateOnly))
	return f.res, f.err
}

func TestImportNow(t *testing.T) {
	tenantID := testkit.TenantID(1)
	const ecbPath = "/v1/fx/providers/ecb:import"
	const oxrPath = "/v1/fx/providers/openexchangerates:import"
	adminDuties := "fx.rate.administer"
	todayStr := time.Now().UTC().Format(time.DateOnly)
	future := time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
	ecbTypes := map[string]store.RateType{"CLOSING": {Code: "CLOSING", Provider: "ecb"}}
	oxrTypes := map[string]store.RateType{"CLOSING": {Code: "CLOSING", Provider: "openexchangerates"}}
	today := []string{"CLOSING@" + todayStr}
	tests := []struct {
		name, path, body, duties string
		types                    map[string]store.RateType
		imp                      *fakeImporter
		wantStatus               int
		wantBody                 string
		wantCalls                []string
		wantRetryAfter           string
	}{
		{"imports today", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{res: importer.Result{RunID: "r1", Attempts: 1, Imported: 30, Unchanged: 2}}, 200,
			`"runId":"r1","provider":"ecb","rateType":"CLOSING"`, today, ""},
		{"explicit today", ecbPath, fmt.Sprintf(`{"rateType":"CLOSING","date":%q}`, todayStr), adminDuties, ecbTypes,
			&fakeImporter{res: importer.Result{RunID: "r1"}}, 200, `"runId":"r1"`, today, ""},
		{"past date on a history provider", oxrPath, `{"rateType":"CLOSING","date":"2026-03-02"}`, adminDuties, oxrTypes,
			&fakeImporter{history: true, res: importer.Result{RunID: "r2", AlreadyDone: true, Imported: 30}}, 200,
			`"date":"2026-03-02","attempts":0,"imported":30,"unchanged":0,"alreadyDone":true`, []string{"CLOSING@2026-03-02"}, ""},
		{"past date without history support", ecbPath, `{"rateType":"CLOSING","date":"2026-03-02"}`, adminDuties, ecbTypes,
			&fakeImporter{}, 422, "FX_PROVIDER_HISTORY_UNSUPPORTED", nil, ""},
		{"stale cube reports its valid-from date and stays pending", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{res: importer.Result{RunID: "r5", Pending: true, ValidFrom: time.Date(2026, 2, 27, 0, 0, 0, 0, time.UTC)}}, 200,
			`"date":"2026-02-27"`, today, ""},
		{"degraded run", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{res: importer.Result{RunID: "r3", Degraded: true, LastSuccessAt: ptr(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))}}, 200,
			`"degraded":true,"lastSuccessAt":"2026-03-01T09:00:00Z"`, today, ""},
		{"run still in progress", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{res: importer.Result{RunID: "r4", InProgress: true}}, 409, "FX_IMPORT_IN_PROGRESS", today, "5"},
		{"run taken over by another replica", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{err: fmt.Errorf("finish run: %w", store.ErrLostOwnership)}, 409, "FX_IMPORT_IN_PROGRESS", today, "5"},
		{"client cancelled", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{err: fmt.Errorf("import cancelled: %w", context.Canceled)}, 503, "FX_IMPORT_CANCELLED", today, ""},
		{"unknown rate type", ecbPath, `{"rateType":"NOPE"}`, adminDuties, ecbTypes, &fakeImporter{}, 400, "FX_RATE_INVALID", nil, ""},
		{"rate type without provider", ecbPath, `{"rateType":"CLOSING"}`, adminDuties,
			map[string]store.RateType{"CLOSING": {Code: "CLOSING"}}, &fakeImporter{}, 422, "FX_PROVIDER_MISMATCH", nil, ""},
		{"rate type of another provider", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, oxrTypes, &fakeImporter{}, 422, "FX_PROVIDER_MISMATCH", nil, ""},
		{"maintain duty is not enough", ecbPath, `{"rateType":"CLOSING"}`, allDuties, ecbTypes, &fakeImporter{}, 403, "AUTH-0003", nil, ""},
		{"unknown provider", "/v1/fx/providers/nope:import", `{"rateType":"CLOSING"}`, adminDuties, ecbTypes, &fakeImporter{}, 404, "FX_PROVIDER_UNKNOWN", nil, ""},
		{"missing rate type", ecbPath, `{}`, adminDuties, ecbTypes, &fakeImporter{}, 400, "COM-0001", nil, ""},
		{"bad date", ecbPath, `{"rateType":"CLOSING","date":"yesterday"}`, adminDuties, ecbTypes, &fakeImporter{}, 400, "must be a date", nil, ""},
		{"unknown field", ecbPath, `{"rateType":"CLOSING","x":1}`, adminDuties, ecbTypes, &fakeImporter{}, 400, "COM-0001", nil, ""},
		{"future date", ecbPath, fmt.Sprintf(`{"rateType":"CLOSING","date":%q}`, future), adminDuties, ecbTypes, &fakeImporter{}, 422, "FX_RATE_DATE_FUTURE", nil, ""},
		{"provider without credentials", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{err: fmt.Errorf("oxr: %w", provider.ErrDisabled)}, 422, "FX_PROVIDER_UNAVAILABLE", today, ""},
		{"outage without last known rate", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{err: fmt.Errorf("%w: ecb", importer.ErrNoLastKnownRate)}, 502, "FX_PROVIDER_FAILED", today, ""},
		{"importer failure hides cause", ecbPath, `{"rateType":"CLOSING"}`, adminDuties, ecbTypes,
			&fakeImporter{err: errors.New("pg secret")}, 500, "COM-0500", today, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httpx.NewRouter()
			api.New(&fakeStore{types: tc.types}).WithImporters(map[string]api.Importer{"ecb": tc.imp, "openexchangerates": tc.imp}).Register(r)
			rec := do(r, http.MethodPost, tc.path, tc.body, tc.duties, tenantID)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if got := rec.Header().Get("Retry-After"); got != tc.wantRetryAfter {
				t.Fatalf("Retry-After = %q, want %q", got, tc.wantRetryAfter)
			}
			if strings.Join(tc.imp.calls, ",") != strings.Join(tc.wantCalls, ",") {
				t.Fatalf("importer calls %v, want %v", tc.imp.calls, tc.wantCalls)
			}
			if tc.wantStatus == 500 && strings.Contains(rec.Body.String(), "pg secret") {
				t.Fatalf("500 body leaks cause: %s", rec.Body.String())
			}
		})
	}
}

func TestImportNowHidesProviderErrorText(t *testing.T) {
	imp := &fakeImporter{err: fmt.Errorf("%w: ecb: request failed: dial tcp internal-gw.corp:443 boom", importer.ErrNoLastKnownRate)}
	r := httpx.NewRouter()
	types := map[string]store.RateType{"CLOSING": {Code: "CLOSING", Provider: "ecb"}}
	api.New(&fakeStore{types: types}).WithImporters(map[string]api.Importer{"ecb": imp}).Register(r)
	rec := do(r, http.MethodPost, "/v1/fx/providers/ecb:import", `{"rateType":"CLOSING"}`, "fx.rate.administer", testkit.TenantID(1))
	body := rec.Body.String()
	if rec.Code != 502 || !strings.Contains(body, "exchange-rate provider request failed") || strings.Contains(body, "internal-gw") || strings.Contains(body, "boom") {
		t.Fatalf("got %d %s", rec.Code, body)
	}
}

func TestImportNowRequiresTenant(t *testing.T) {
	r := httpx.NewRouter()
	api.New(&fakeStore{}).WithImporters(map[string]api.Importer{"ecb": &fakeImporter{}}).Register(r)
	if rec := do(r, http.MethodPost, "/v1/fx/providers/ecb:import", `{"rateType":"C"}`, "", ""); rec.Code != 401 {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func ptr[T any](v T) *T { return &v }

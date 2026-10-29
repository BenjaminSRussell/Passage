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
)

type fakeImporter struct {
	res   importer.Result
	err   error
	calls []string
}

func (f *fakeImporter) Run(_ context.Context, rateType string, date time.Time) (importer.Result, error) {
	f.calls = append(f.calls, rateType+"@"+date.Format(time.DateOnly))
	return f.res, f.err
}

func TestImportNow(t *testing.T) {
	tenantID := testkit.TenantID(1)
	const path = "/v1/fx/providers/ecb:import"
	adminDuties := "Administer exchange rates"
	future := time.Now().UTC().AddDate(0, 0, 2).Format(time.DateOnly)
	tests := []struct {
		name, path, body, duties string
		imp                      *fakeImporter
		wantStatus               int
		wantBody                 string
		wantCalls                []string
	}{
		{"imports today", path, `{"rateType":"CLOSING"}`, adminDuties,
			&fakeImporter{res: importer.Result{RunID: "r1", Attempts: 1, Imported: 30, Unchanged: 2}}, 200,
			`"runId":"r1","provider":"ecb","rateType":"CLOSING"`, []string{"CLOSING@" + time.Now().UTC().Format(time.DateOnly)}},
		{"explicit date", path, `{"rateType":"CLOSING","date":"2026-03-02"}`, adminDuties,
			&fakeImporter{res: importer.Result{RunID: "r2", AlreadyDone: true, Imported: 30}}, 200,
			`"date":"2026-03-02","attempts":0,"imported":30,"unchanged":0,"alreadyDone":true`, []string{"CLOSING@2026-03-02"}},
		{"degraded run", path, `{"rateType":"CLOSING"}`, adminDuties,
			&fakeImporter{res: importer.Result{RunID: "r3", Degraded: true, LastSuccessAt: ptr(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))}}, 200,
			`"degraded":true,"lastSuccessAt":"2026-03-01T09:00:00Z"`, []string{"CLOSING@" + time.Now().UTC().Format(time.DateOnly)}},
		{"maintain duty is not enough", path, `{"rateType":"CLOSING"}`, allDuties, &fakeImporter{}, 403, "AUTH-0003", nil},
		{"unknown provider", "/v1/fx/providers/nope:import", `{"rateType":"CLOSING"}`, adminDuties, &fakeImporter{}, 404, "FX_PROVIDER_UNKNOWN", nil},
		{"missing rate type", path, `{}`, adminDuties, &fakeImporter{}, 400, "COM-0001", nil},
		{"bad date", path, `{"rateType":"CLOSING","date":"yesterday"}`, adminDuties, &fakeImporter{}, 400, "must be a date", nil},
		{"unknown field", path, `{"rateType":"CLOSING","x":1}`, adminDuties, &fakeImporter{}, 400, "COM-0001", nil},
		{"future date", path, fmt.Sprintf(`{"rateType":"CLOSING","date":%q}`, future), adminDuties, &fakeImporter{}, 422, "FX_RATE_DATE_FUTURE", nil},
		{"provider without credentials", path, `{"rateType":"CLOSING"}`, adminDuties,
			&fakeImporter{err: fmt.Errorf("oxr: %w", provider.ErrDisabled)}, 422, "FX_PROVIDER_UNAVAILABLE", []string{"CLOSING@" + time.Now().UTC().Format(time.DateOnly)}},
		{"outage without last known rate", path, `{"rateType":"CLOSING"}`, adminDuties,
			&fakeImporter{err: fmt.Errorf("%w: ecb", importer.ErrNoLastKnownRate)}, 502, "FX_PROVIDER_FAILED", []string{"CLOSING@" + time.Now().UTC().Format(time.DateOnly)}},
		{"importer failure hides cause", path, `{"rateType":"CLOSING"}`, adminDuties,
			&fakeImporter{err: errors.New("pg secret")}, 500, "COM-0500", []string{"CLOSING@" + time.Now().UTC().Format(time.DateOnly)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httpx.NewRouter()
			api.New(&fakeStore{}).WithImporters(map[string]api.Importer{"ecb": tc.imp}).Register(r)
			rec := do(r, http.MethodPost, tc.path, tc.body, tc.duties, tenantID)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
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

func TestImportNowRequiresTenant(t *testing.T) {
	r := httpx.NewRouter()
	api.New(&fakeStore{}).WithImporters(map[string]api.Importer{"ecb": &fakeImporter{}}).Register(r)
	if rec := do(r, http.MethodPost, "/v1/fx/providers/ecb:import", `{"rateType":"C"}`, "", ""); rec.Code != 401 {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func ptr[T any](v T) *T { return &v }

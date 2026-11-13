package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

type andonFake struct {
	*fakeStore
	sweepAt  time.Time
	sweep    store.AndonSweepResult
	cleared  []string
	clearErr error
}

func (f *andonFake) SweepAndon(_ context.Context, now time.Time) (store.AndonSweepResult, error) {
	f.sweepAt = now
	return f.sweep, nil
}

func (f *andonFake) ClearAndon(_ context.Context, id, reason, by string, _ time.Time, _ store.Idem) (store.Andon, error) {
	if f.clearErr != nil {
		return store.Andon{}, f.clearErr
	}
	f.cleared = append(f.cleared, id+"|"+reason+"|"+by)
	return store.Andon{ID: id, Kind: "quality-fail"}, nil
}

func TestAndonSweep_RunsAtServerClockUnderItsOwnDuty(t *testing.T) {
	f := &andonFake{fakeStore: &fakeStore{}, sweep: store.AndonSweepResult{DowntimeRaised: 2, ScrapRateRaised: 1, Cleared: 3}}
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	h := newRouterAt(f, func() time.Time { return now })
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/andon:sweep", ``, floorDuty); rec.Code != http.StatusForbidden {
		t.Fatalf("without the sweep duty %d %s", rec.Code, rec.Body.String())
	}
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/andon:sweep", ``, DutyOEERollup)
	if rec.Code != http.StatusOK || !f.sweepAt.Equal(now) {
		t.Fatalf("sweep %d %s at %s", rec.Code, rec.Body.String(), f.sweepAt)
	}
	for _, want := range []string{`"downtimeRaised":2`, `"scrapRateRaised":1`, `"cleared":3`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("body %s lacks %s", rec.Body.String(), want)
		}
	}
}

func TestAndonClear_NeedsDutyReasonAndAlertId(t *testing.T) {
	f := &andonFake{fakeStore: &fakeStore{}}
	h := newRouter(f)
	path := "/v1/shopfloor/andon/" + regID + ":clear"
	if rec := call(t, h, http.MethodPost, path, `{"reason":"re-checked"}`, floorDuty); rec.Code != http.StatusForbidden {
		t.Fatalf("without the clear duty %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, path, `{"reason":"  "}`, DutyCheckOverride); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank reason %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/andon/not-a-uuid:clear", `{"reason":"x"}`, DutyCheckOverride); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeAndonNotFound {
		t.Fatalf("bad id %d %s", rec.Code, rec.Body.String())
	}
	rec := call(t, h, http.MethodPost, path, `{"reason":" re-checked "}`, DutyCheckOverride)
	if rec.Code != http.StatusOK || len(f.cleared) != 1 || f.cleared[0] != regID+"|re-checked|supervisor-1" {
		t.Fatalf("clear %d %s %v", rec.Code, rec.Body.String(), f.cleared)
	}
	f.clearErr = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeAndonNotClearable, "not clearable")
	rec = call(t, h, http.MethodPost, path, `{"reason":"x"}`, DutyCheckOverride)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeAndonNotClearable {
		t.Fatalf("not clearable %d %s", rec.Code, rec.Body.String())
	}
}

// Over HTTP and Postgres: a machine goes DOWN 5 minutes ago and falls silent; the sweep run 6
// minutes later raises the alert once, the board lists it, and a supervisor cannot clear it by hand.
func TestHTTP_AndonSweepRaisesForSilentMachine(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	h := newRouterAt(store.New(app), func() time.Time { return now.Add(6 * time.Minute) })
	duties := floorDuty + "," + DutyOEERollup + "," + DutyCheckOverride
	body := fmt.Sprintf(`{"machineId":"M-301","resourceId":%q,"idealCycleSeconds":"60"}`, resourceID)
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, duties, map[string]string{"Idempotency-Key": "m-301"}); rec.Code != http.StatusCreated {
		t.Fatalf("machine %d %s", rec.Code, rec.Body.String())
	}
	events := fmt.Sprintf(`{"events":[{"machineId":"M-301","seq":1,"kind":"state","ts":%q,"state":"DOWN"}]}`, now.Add(-5*time.Minute).Format(time.RFC3339))
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", events, duties); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"andonsRaised":0`) {
		t.Fatalf("ingest %d %s", rec.Code, rec.Body.String())
	}
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/andon:sweep", ``, duties)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"downtimeRaised":1`) {
		t.Fatalf("sweep %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/andon:sweep", ``, duties); !strings.Contains(rec.Body.String(), `"downtimeRaised":0`) {
		t.Fatalf("second sweep %d %s", rec.Code, rec.Body.String())
	}
	var board itemsResponse[store.Andon]
	rec = call(t, h, http.MethodGet, "/v1/shopfloor/andon", ``, duties)
	_ = json.Unmarshal(rec.Body.Bytes(), &board)
	if rec.Code != http.StatusOK || len(board.Items) != 1 || board.Items[0].Kind != "downtime" || board.Items[0].ResourceID != resourceID {
		t.Fatalf("board %d %s", rec.Code, rec.Body.String())
	}
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/andon/"+board.Items[0].ID+":clear", `{"reason":"it is fine"}`, duties)
	if rec.Code != http.StatusUnprocessableEntity || problemCode(t, rec) != store.CodeAndonNotClearable {
		t.Fatalf("clear a downtime alert %d %s", rec.Code, rec.Body.String())
	}
}

// A tenant's own andon thresholds replace the defaults: with three minutes down set, a machine down for five minutes
// raises its alert on ingest, where the default ten minutes would not.
func TestHTTP_AndonSettingsChangeTheThreshold(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	h := newRouterAt(store.New(app), func() time.Time { return now })
	duties := floorDuty + "," + DutySetup + "," + DutyView
	rec := call(t, h, http.MethodGet, "/v1/shopfloor/andon-settings", ``, duties)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"downtimeMinutes":10`) || !strings.Contains(rec.Body.String(), `"default":true`) {
		t.Fatalf("default settings %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPut, "/v1/shopfloor/andon-settings", `{"downtimeMinutes":0,"clearMinutes":5,"scrapPercent":5,"scrapMinCount":10}`, duties); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "downtimeMinutes") {
		t.Fatalf("zero minutes %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, h, http.MethodPut, "/v1/shopfloor/andon-settings", `{"downtimeMinutes":3,"clearMinutes":1,"scrapPercent":2,"scrapMinCount":20}`, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("without the setup duty %d", rec.Code)
	}
	rec = call(t, h, http.MethodPut, "/v1/shopfloor/andon-settings", `{"downtimeMinutes":3,"clearMinutes":1,"scrapPercent":2,"scrapMinCount":20}`, duties)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"downtimeMinutes":3`) || strings.Contains(rec.Body.String(), `"default":true`) {
		t.Fatalf("put settings %d %s", rec.Code, rec.Body.String())
	}
	body := fmt.Sprintf(`{"machineId":"M-302","resourceId":%q,"idealCycleSeconds":"60"}`, resourceID)
	if rec := callWith(t, h, http.MethodPost, "/v1/shopfloor/machines", body, duties, map[string]string{"Idempotency-Key": "m-302"}); rec.Code != http.StatusCreated {
		t.Fatalf("machine %d %s", rec.Code, rec.Body.String())
	}
	events := fmt.Sprintf(`{"events":[{"machineId":"M-302","seq":1,"kind":"state","ts":%q,"state":"DOWN"}]}`, now.Add(-5*time.Minute).Format(time.RFC3339))
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/machine-events", events, duties); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"andonsRaised":1`) {
		t.Fatalf("ingest with a three-minute threshold %d %s", rec.Code, rec.Body.String())
	}
}

func TestAndonSettings_FieldErrorsAndScrapRule(t *testing.T) {
	s := domain.AndonSettings{DowntimeMinutes: 3, ClearMinutes: 0, ScrapPercent: 2, ScrapMinCount: 20}
	if f := s.FieldErrors(); len(f) != 0 {
		t.Fatalf("valid settings: %v", f)
	}
	if !s.ScrapExceeded(100, 97) || s.ScrapExceeded(100, 98) || s.ScrapExceeded(19, 0) {
		t.Fatal("scrap rule with 2 % over 20 pieces")
	}
	if f := (domain.AndonSettings{}).FieldErrors(); len(f) != 3 {
		t.Fatalf("zero settings: %v", f)
	}
}

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/transport/internal/api"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

const holdID = "0190f0a0-0000-7000-8000-0000000000f1"

func (f *fakeStore) RecordScreeningHold(_ context.Context, loadID string, parties []domain.PartyScreening) (domain.ScreeningHold, error) {
	f.held = parties
	return domain.ScreeningHold{ID: holdID, LoadID: loadID, Status: domain.HoldOpen, Parties: parties, Version: 1}, f.holdErr
}

func (f *fakeStore) GetScreeningHold(_ context.Context, _ string) (domain.ScreeningHold, error) {
	return f.hold, f.holdErr
}

func (f *fakeStore) ListScreeningHolds(_ context.Context, filter store.HoldFilter) ([]domain.ScreeningHold, error) {
	f.holdFilter = filter
	return []domain.ScreeningHold{f.hold}, nil
}

func (f *fakeStore) ReleaseScreeningHold(_ context.Context, _ string, reason string, at time.Time) (domain.ScreeningHold, error) {
	f.released = reason
	if f.holdErr != nil {
		return domain.ScreeningHold{}, f.holdErr
	}
	h, _, err := f.hold.Release("user-test", reason, at)
	return h, asProblem(err)
}

// The hold half of AC-34.16 through the API: a POTENTIAL_MATCH stores a hold
// with the party results and names it in the response.
func TestScreenLoad_HoldIsStoredAndNamed(t *testing.T) {
	st := &fakeStore{load: domain.Load{ID: loadID}}
	h := screenRouter(st, stubProvider{"Northwind": domain.ScreenPotentialMatch})
	rec := do(t, h, http.MethodPost, base+"/loads/"+loadID+":screen", `{"parties":[{"role":"SHIP_TO","name":"Northwind"}]}`, api.DutyPlan)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"holdId":"`+holdID+`"`) {
		t.Fatalf("screen = %d %s", rec.Code, rec.Body.String())
	}
	if len(st.held) != 1 || st.held[0].Status != domain.ScreenPotentialMatch || st.held[0].Code != domain.CodeScreeningHold {
		t.Fatalf("stored parties = %+v", st.held)
	}
}

func TestScreenLoad_ClearAndWarnStoreNoHold(t *testing.T) {
	st := &fakeStore{load: domain.Load{ID: loadID}}
	h := screenRouter(st, stubProvider{"Acme": domain.ScreenClear})
	rec := do(t, h, http.MethodPost, base+"/loads/"+loadID+":screen", `{"parties":[{"role":"SHIP_TO","name":"Acme"}]}`, api.DutyPlan)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "holdId") || st.held != nil {
		t.Fatalf("clear screen = %d %s, held %v", rec.Code, rec.Body.String(), st.held)
	}
	rec = do(t, router(st), http.MethodPost, base+"/loads/"+loadID+":screen", `{"crossBorder":true,"parties":[{"role":"SHIP_TO","name":"Acme"}]}`, api.DutyPlan)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "holdId") || st.held != nil {
		t.Fatalf("warn screen = %d %s, held %v", rec.Code, rec.Body.String(), st.held)
	}
}

func TestScreenLoad_HoldStoreFailureIsNotASilentClear(t *testing.T) {
	st := &fakeStore{load: domain.Load{ID: loadID}, holdErr: store.NotFound("load")}
	h := screenRouter(st, stubProvider{"Northwind": domain.ScreenBlocked})
	rec := do(t, h, http.MethodPost, base+"/loads/"+loadID+":screen", `{"parties":[{"role":"SHIP_TO","name":"Northwind"}]}`, api.DutyPlan)
	if rec.Code != http.StatusNotFound || problem(t, rec).Code != domain.CodeNotFound {
		t.Fatalf("screen with failing hold store = %d %s", rec.Code, rec.Body.String())
	}
}

// The release half of AC-34.16: a TRADE_COMPLIANCE_RELEASE user with a reason
// releases the hold and the response shows user and reason.
func TestReleaseScreeningHold_AC3416(t *testing.T) {
	st := &fakeStore{hold: domain.ScreeningHold{ID: holdID, LoadID: loadID, Status: domain.HoldOpen, Version: 1,
		Parties: []domain.PartyScreening{{Role: "SHIP_TO", Name: "Northwind", Status: domain.ScreenPotentialMatch}}}}
	path := base + "/screening-holds/" + holdID + ":release"
	if rec := do(t, router(st), http.MethodPost, path, `{"reason":"cleared"}`, api.DutyPlan); rec.Code != http.StatusForbidden {
		t.Fatalf("release without TRADE_COMPLIANCE_RELEASE = %d", rec.Code)
	}
	rec := do(t, router(st), http.MethodPost, path, `{"reason":"  "}`, api.DutyTradeRelease)
	if rec.Code != http.StatusBadRequest || problem(t, rec).Errors["reason"] == "" {
		t.Fatalf("blank reason = %d %s", rec.Code, rec.Body.String())
	}
	rec = clockedDo(t, st, http.MethodPost, path, `{"reason":"  date of birth differs from the listed person "}`, api.DutyTradeRelease)
	if rec.Code != http.StatusOK {
		t.Fatalf("release = %d %s", rec.Code, rec.Body.String())
	}
	var got domain.ScreeningHold
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.HoldReleased || got.ReleasedBy != "user-test" || got.ReleaseReason != "date of birth differs from the listed person" ||
		got.ReleasedAt == nil || !got.ReleasedAt.Equal(gateNow) {
		t.Fatalf("released hold = %+v", got)
	}
}

func TestReleaseScreeningHold_BlockedAndUnknown(t *testing.T) {
	st := &fakeStore{hold: domain.ScreeningHold{ID: holdID, Status: domain.HoldOpen,
		Parties: []domain.PartyScreening{{Role: "CONSIGNEE", Name: "Listed Co", Status: domain.ScreenBlocked}}}}
	rec := do(t, router(st), http.MethodPost, base+"/screening-holds/"+holdID+":release", `{"reason":"looks fine"}`, api.DutyTradeRelease)
	if rec.Code != http.StatusUnprocessableEntity || problem(t, rec).Code != domain.CodeScreeningHold {
		t.Fatalf("release of a BLOCKED hold = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, router(st), http.MethodPost, base+"/screening-holds/not-a-uuid:release", `{"reason":"x"}`, api.DutyTradeRelease)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id = %d", rec.Code)
	}
}

func TestListScreeningHolds_Filters(t *testing.T) {
	st := &fakeStore{hold: domain.ScreeningHold{ID: holdID, Status: domain.HoldOpen}}
	rec := do(t, router(st), http.MethodGet, base+"/screening-holds?status=OPEN&loadId="+loadID, "", api.DutyView)
	if rec.Code != http.StatusOK || st.holdFilter.Status != domain.HoldOpen || st.holdFilter.LoadID != loadID {
		t.Fatalf("list = %d, filter %+v", rec.Code, st.holdFilter)
	}
	rec = do(t, router(st), http.MethodGet, base+"/screening-holds?status=CLOSED&loadId=x", "", api.DutyView)
	p := problem(t, rec)
	if rec.Code != http.StatusBadRequest || p.Errors["status"] == "" || p.Errors["loadId"] == "" {
		t.Fatalf("bad filters = %d %+v", rec.Code, p.Errors)
	}
}

func clockedDo(t *testing.T, st store.Store, method, path, body string, duties ...string) *httptest.ResponseRecorder {
	t.Helper()
	return doWith(t, clockedRouter(st), method, path, body, nil, duties...)
}

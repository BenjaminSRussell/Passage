package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

func (f *fakeStore) LoadPaySetup(context.Context, string) (domain.PaySetup, error) {
	return f.paySetup, nil
}

func (f *fakeStore) CreateTimeProfile(_ context.Context, in store.TimeProfileInput, by string, _ store.Idem) (store.TimeProfile, error) {
	f.profileIn = &in
	return store.TimeProfile{ID: jobID, Code: in.Code, Name: in.Name, Active: in.Active, Version: 1, Days: in.Days, CreatedBy: by}, nil
}

func (f *fakeStore) UpdateTimeProfile(_ context.Context, id string, version int, in store.TimeProfileInput, by string) (store.TimeProfile, error) {
	f.profileIn, f.updateVersion = &in, version
	return store.TimeProfile{ID: id, Code: "DAY", Name: in.Name, Active: in.Active, Version: version + 1, Days: in.Days, CreatedBy: by}, nil
}

func (f *fakeStore) GetTimeProfile(_ context.Context, id string) (store.TimeProfile, error) {
	return store.TimeProfile{ID: id, Code: "DAY", Name: "Day", Active: true, Version: 3, Days: []domain.ProfileDayInput{}}, nil
}

func (f *fakeStore) ListTimeProfiles(context.Context, bool, int) ([]store.TimeProfile, error) {
	return []store.TimeProfile{{ID: jobID, Code: "DAY", Name: "Day", Active: true, Version: 1, Days: []domain.ProfileDayInput{}}}, nil
}

func (f *fakeStore) CreatePayAgreement(_ context.Context, in store.PayAgreementInput, by string, _ store.Idem) (store.PayAgreement, error) {
	f.agreementIn = &in
	return store.PayAgreement{ID: jobID, Code: in.Code, Name: in.Name, DailyThreshold: in.Rules.DailyThreshold, Version: 1, Active: in.Active, CreatedBy: by}, nil
}

func (f *fakeStore) UpdatePayAgreement(_ context.Context, id string, version int, in store.PayAgreementInput, by string) (store.PayAgreement, error) {
	f.agreementIn, f.updateVersion = &in, version
	return store.PayAgreement{ID: id, Code: "US", Name: in.Name, Version: version + 1, Active: in.Active, CreatedBy: by}, nil
}

func (f *fakeStore) GetPayAgreement(_ context.Context, id string) (store.PayAgreement, error) {
	return store.PayAgreement{ID: id, Code: "US", Name: "US", Version: 2, Active: true}, nil
}

func (f *fakeStore) ListPayAgreements(context.Context, bool, int) ([]store.PayAgreement, error) {
	return []store.PayAgreement{{ID: jobID, Code: "US", Name: "US", Version: 1, Active: true}}, nil
}

func (f *fakeStore) AssignWorkerSetup(_ context.Context, id string, version int, profileID, agreementID *string, _ string, _ store.Idem) (store.Worker, error) {
	f.assigned, f.updateVersion = &assignRequest{ProfileID: profileID, PayAgreementID: agreementID}, version
	return store.Worker{ID: id, ProfileID: profileID, PayAgreementID: agreementID, Version: version + 1}, nil
}

func (f *fakeStore) AssignWorkerResource(_ context.Context, id string, version int, resourceID *string, _ string, _ store.Idem) (store.Worker, error) {
	f.resourceSet, f.updateVersion = resourceID, version
	return store.Worker{ID: id, DefaultResourceID: resourceID, Version: version + 1}, nil
}

const profileBody = `{"code":"DAY","name":"Day shift","days":[{"dayType":"weekday","shiftStart":"07:00","shiftEnd":"15:30","breaks":[{"from":"12:00","to":"12:30"}],"roundingMinutes":5,"toleranceBeforeMin":10,"toleranceAfterMin":5,"flexAllowed":false}]}`
const agreementBody = `{"code":"US","name":"US hourly","dailyThreshold":"8","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":1}`

func TestSetup_CreateProfileAndAgreement(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/time-profiles", profileBody, allDuty)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create profile: %d %s etag=%s", rec.Code, rec.Body.String(), rec.Header().Get("ETag"))
	}
	if f.profileIn == nil || f.profileIn.Code != "DAY" || !f.profileIn.Active || len(f.profileIn.Days) != 1 || f.profileIn.Days[0].Breaks[0].To != "12:30" {
		t.Fatalf("profile input = %+v", f.profileIn)
	}
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/pay-agreements", agreementBody, allDuty)
	if rec.Code != http.StatusCreated || f.agreementIn == nil || f.agreementIn.Rules.WeekStart != time.Monday || f.agreementIn.Rules.OvertimeMultiplier.String() != "1.5" {
		t.Fatalf("create agreement: %d %s %+v", rec.Code, rec.Body.String(), f.agreementIn)
	}
}

func TestSetup_ValidationRefusesBadInput(t *testing.T) {
	h := newRouter(&fakeStore{})
	cases := []struct{ path, body, field string }{
		{"/v1/shopfloor/time-profiles", `{"code":"","name":"x","days":[]}`, "code"},
		{"/v1/shopfloor/time-profiles", `{"code":"A","name":"","days":[]}`, "name"},
		{"/v1/shopfloor/time-profiles", `{"code":"A","name":"x","days":[{"dayType":"weekday","roundingMinutes":0}]}`, "days[0].roundingMinutes"},
		{"/v1/shopfloor/time-profiles", `{"code":"A","name":"x","days":[{"dayType":"weekday","roundingMinutes":1,"shiftStart":"09:00"}]}`, "days[0].shiftStart"},
		{"/v1/shopfloor/pay-agreements", `{"code":"A","name":"x","dailyThreshold":"30","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":1}`, "dailyThreshold"},
		{"/v1/shopfloor/pay-agreements", `{"code":"A","name":"x","dailyThreshold":"8","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":9}`, "weekStart"},
		{"/v1/shopfloor/pay-agreements", `{"code":"A","name":"x","dailyThreshold":"8","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":1,"shiftPremiums":[{"name":"N","from":"22:00","to":"22:00","premium":"0.1"}]}`, "shiftPremiums[0].to"},
		{"/v1/shopfloor/pay-agreements", `{"code":"A","name":"x","dailyThreshold":"8","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":1,"shiftPremiums":[{"name":"N","from":"22:00","to":"06:00","premium":"3"}]}`, "shiftPremiums[0].premium"},
	}
	for _, c := range cases {
		rec := call(t, h, http.MethodPost, c.path, c.body, allDuty)
		var p struct {
			Errors map[string]string `json:"errors"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s %s: %d errors=%v", c.path, c.field, rec.Code, p.Errors)
		}
	}
}

func TestSetup_UpdateNeedsIfMatchAndPassesVersion(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/time-profiles/" + jobID
	body := `{"name":"Renamed","active":false,"days":[]}`
	if rec := callWith(t, h, http.MethodPut, path, body, allDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPut, path, body, allDuty, map[string]string{"If-Match": "3"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unquoted If-Match: %d", rec.Code)
	}
	rec := callWith(t, h, http.MethodPut, path, body, allDuty, map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || f.updateVersion != 3 || f.profileIn.Active || rec.Header().Get("ETag") != `"4"` {
		t.Fatalf("update: %d %s version=%d etag=%s", rec.Code, rec.Body.String(), f.updateVersion, rec.Header().Get("ETag"))
	}
	rec = callWith(t, h, http.MethodPut, "/v1/shopfloor/pay-agreements/"+jobID, `{"name":"New","dailyThreshold":"9","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":1}`, allDuty, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || f.updateVersion != 2 || f.agreementIn.Rules.DailyThreshold.String() != "9" {
		t.Fatalf("update agreement: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWith(t, h, http.MethodPut, "/v1/shopfloor/time-profiles/not-a-uuid", body, allDuty, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeProfileNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestSetup_ReadsAndDuties(t *testing.T) {
	h := newRouter(&fakeStore{})
	for _, path := range []string{"/v1/shopfloor/time-profiles", "/v1/shopfloor/pay-agreements", "/v1/shopfloor/time-profiles/" + jobID, "/v1/shopfloor/pay-agreements/" + jobID} {
		if rec := call(t, h, http.MethodGet, path, "", "shopfloor.view"); rec.Code != http.StatusOK {
			t.Errorf("GET %s: %d", path, rec.Code)
		}
		if rec := call(t, h, http.MethodGet, path, "", "shopfloor.time.register"); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without the view duty: %d", path, rec.Code)
		}
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/time-profiles/"+jobID, "", "shopfloor.view"); rec.Header().Get("ETag") != `"3"` {
		t.Errorf("etag = %q", rec.Header().Get("ETag"))
	}
	for _, c := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/shopfloor/time-profiles", profileBody},
		{http.MethodPost, "/v1/shopfloor/pay-agreements", agreementBody},
		{http.MethodPost, "/v1/shopfloor/workers/" + workerID + ":assign-setup", `{"profileId":null,"payAgreementId":null}`},
	} {
		if rec := callWith(t, h, c.method, c.path, c.body, "shopfloor.view", map[string]string{"Idempotency-Key": "k", "If-Match": `"1"`}); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without the setup duty: %d", c.method, c.path, rec.Code)
		}
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/time-profiles?active=maybe", "", allDuty); rec.Code != http.StatusBadRequest {
		t.Errorf("active=maybe: %d", rec.Code)
	}
}

func TestSetup_AssignWorker(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/workers/" + workerID + ":assign-setup"
	hdr := map[string]string{"Idempotency-Key": "a-1", "If-Match": `"1"`}
	body := `{"profileId":"` + jobID + `","payAgreementId":null}`
	rec := callWith(t, h, http.MethodPost, path, body, allDuty, hdr)
	if rec.Code != http.StatusOK || f.assigned == nil || *f.assigned.ProfileID != jobID || f.assigned.PayAgreementID != nil || f.updateVersion != 1 {
		t.Fatalf("assign: %d %s %+v", rec.Code, rec.Body.String(), f.assigned)
	}
	if rec := callWith(t, h, http.MethodPost, path, `{"profileId":"nope"}`, allDuty, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad profile id: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, path, body, allDuty, map[string]string{"Idempotency-Key": "a-2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, path, body, allDuty, map[string]string{"If-Match": `"1"`}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key: %d", rec.Code)
	}
}

func TestSetup_AssignWorkerResource(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	path := "/v1/shopfloor/workers/" + workerID + ":assign-resource"
	hdr := map[string]string{"Idempotency-Key": "r-1", "If-Match": `"2"`}
	rec := callWith(t, h, http.MethodPost, path, `{"resourceId":"`+jobResource+`"}`, allDuty, hdr)
	if rec.Code != http.StatusOK || f.resourceSet == nil || *f.resourceSet != jobResource || f.updateVersion != 2 || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("assign: %d %s %v", rec.Code, rec.Body.String(), f.resourceSet)
	}
	if rec := callWith(t, h, http.MethodPost, path, `{"resourceId":null}`, allDuty, hdr); rec.Code != http.StatusOK || f.resourceSet != nil {
		t.Fatalf("clear: %d %v", rec.Code, f.resourceSet)
	}
	if rec := callWith(t, h, http.MethodPost, path, `{"resourceId":"r1"}`, allDuty, hdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad resource id: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, path, `{"resourceId":null}`, allDuty, map[string]string{"Idempotency-Key": "r-2"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing If-Match: %d", rec.Code)
	}
	if rec := callWith(t, h, http.MethodPost, path, `{"resourceId":null}`, "shopfloor.view", hdr); rec.Code != http.StatusForbidden {
		t.Fatalf("without the setup duty: %d", rec.Code)
	}
}

// The calculation reads the worker's pay setup: with a 10 h daily threshold from the
// agreement, a 10.5 h day has 10 base and 0.5 overtime hours, and the profile's weekday
// break comes off before the thresholds apply.
func TestSetup_CalculatePayUsesWorkerSetup(t *testing.T) {
	rules, _ := domain.AgreementInput{DailyThreshold: "10", WeeklyThreshold: "40", OvertimeMultiplier: "1.5", SundayMultiplier: "2", WeekStart: 1}.Rules()
	f := &fakeStore{
		paySetup: domain.PaySetup{Rules: &rules},
		punches: []domain.Punch{
			{ID: "in", Kind: domain.KindClockIn, At: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)},
			{ID: "out", Kind: domain.KindClockOut, At: time.Date(2026, 10, 20, 19, 30, 0, 0, time.UTC)},
		},
	}
	h := newRouter(f)
	body := `{"workerId":"` + workerID + `","from":"2026-10-20","to":"2026-10-20"}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/pay:calculate", body, allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("calculate: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Days []struct {
			PayLines []struct {
				Type  string
				Hours decimal.Decimal
			}
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Days) != 1 || len(resp.Days[0].PayLines) != 2 || resp.Days[0].PayLines[0].Hours.String() != "10" || !strings.HasPrefix(resp.Days[0].PayLines[1].Type, "overtime") {
		t.Fatalf("pay lines = %s", rec.Body.String())
	}

	// A profile break of 30 minutes on weekdays takes the day to 10.00 h: all base.
	f.paySetup.Profiles = domain.ProfileSet{domain.DayTypeWeekday: {RoundingMinutes: 1, Breaks: []domain.Window{{From: 12 * time.Hour, To: 12*time.Hour + 30*time.Minute}}}}
	f.replays = nil
	rec = callWith(t, h, http.MethodPost, "/v1/shopfloor/pay:calculate", body, allDuty, map[string]string{"Idempotency-Key": "key-2"})
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Days[0].PayLines) != 1 || resp.Days[0].PayLines[0].Type != "base" || resp.Days[0].PayLines[0].Hours.String() != "10" {
		t.Fatalf("with break: %s", rec.Body.String())
	}
}

func TestSetup_AgreementCarriesShiftPremiums(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	body := `{"code":"US","name":"US night","dailyThreshold":"8","weeklyThreshold":"40","overtimeMultiplier":"1.5","sundayMultiplier":"2","weekStart":1,"shiftPremiums":[{"name":"Night","from":"22:00","to":"06:00","premium":"0.15"}]}`
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/pay-agreements", body, allDuty)
	if rec.Code != http.StatusCreated || f.agreementIn == nil || len(f.agreementIn.Rules.ShiftPremiums) != 1 {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body.String(), f.agreementIn)
	}
	w := f.agreementIn.Rules.ShiftPremiums[0]
	if w.From != 22*time.Hour || w.To != 6*time.Hour || w.Premium.String() != "0.15" {
		t.Fatalf("window = %+v", w)
	}
}

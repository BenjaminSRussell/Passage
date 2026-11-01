package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const absenceCodeID = "0195f0a0-0000-7000-8000-000000000031"

func (f *fakeStore) CreateAbsenceCode(_ context.Context, in store.AbsenceCodeInput, by string, _ store.Idem) (store.AbsenceCode, error) {
	f.absenceIn = &in
	return store.AbsenceCode{ID: absenceCodeID, Code: in.Code, Name: in.Name, Paid: in.Paid, PayMultiplier: in.PayMultiplier, Active: in.Active, Version: 1, CreatedBy: by}, nil
}

func (f *fakeStore) UpdateAbsenceCode(_ context.Context, id string, version int, in store.AbsenceCodeInput, by string) (store.AbsenceCode, error) {
	f.absenceIn, f.updateVersion = &in, version
	return store.AbsenceCode{ID: id, Code: "SICK", Name: in.Name, Paid: in.Paid, PayMultiplier: in.PayMultiplier, Active: in.Active, Version: version + 1, CreatedBy: by}, nil
}

func (f *fakeStore) GetAbsenceCode(_ context.Context, id string) (store.AbsenceCode, error) {
	return store.AbsenceCode{ID: id, Code: "SICK", Name: "Sick leave", Paid: true, PayMultiplier: decimal.NewFromInt(1), Active: true, Version: 2}, nil
}

func (f *fakeStore) ListAbsenceCodes(context.Context, bool, int) ([]store.AbsenceCode, error) {
	return []store.AbsenceCode{{ID: absenceCodeID, Code: "SICK", Name: "Sick leave", Paid: true, PayMultiplier: decimal.NewFromInt(1), Active: true, Version: 1}}, nil
}

func TestAbsenceCode_CreateUpdateAndRules(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/absence-codes", `{"code":"HOL","name":"Holiday","paid":true,"payMultiplier":"1.25"}`, allDuty)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` || f.absenceIn.PayMultiplier.String() != "1.25" || !f.absenceIn.Active {
		t.Fatalf("create: %d %s %+v", rec.Code, rec.Body.String(), f.absenceIn)
	}
	// An unpaid code has no rate effect: its multiplier is 0, and another value is refused.
	rec = call(t, h, http.MethodPost, "/v1/shopfloor/absence-codes", `{"code":"UNP","name":"Unpaid leave","paid":false}`, allDuty)
	if rec.Code != http.StatusCreated || f.absenceIn.Paid || !f.absenceIn.PayMultiplier.IsZero() {
		t.Fatalf("unpaid: %d %s %+v", rec.Code, rec.Body.String(), f.absenceIn)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/absence-codes", `{"code":"UNP","name":"Unpaid leave","paid":false,"payMultiplier":"3"}`, allDuty); rec.Code != http.StatusBadRequest {
		t.Fatalf("unpaid with a multiplier: %d %s", rec.Code, rec.Body.String())
	}
	for name, body := range map[string]string{
		"no paid":       `{"code":"A","name":"A"}`,
		"zero":          `{"code":"A","name":"A","paid":true,"payMultiplier":"0"}`,
		"too high":      `{"code":"A","name":"A","paid":true,"payMultiplier":"5.5"}`,
		"decimals":      `{"code":"A","name":"A","paid":true,"payMultiplier":"1.00001"}`,
		"empty name":    `{"code":"A","name":" ","paid":true}`,
		"long code":     `{"code":"ABCDEFGHIJABCDEFGHIJABCDEFGHIJABCDEFGHIJX","name":"A","paid":true}`,
		"missing code":  `{"name":"A","paid":true}`,
		"unknown field": `{"code":"A","name":"A","paid":true,"extra":1}`,
	} {
		if rec := call(t, h, http.MethodPost, "/v1/shopfloor/absence-codes", body, allDuty); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want a validation error", name, rec.Code)
		}
	}
	path := "/v1/shopfloor/absence-codes/" + absenceCodeID
	if rec := callWith(t, h, http.MethodPut, path, `{"name":"Sick","paid":true}`, allDuty, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	rec = callWith(t, h, http.MethodPut, path, `{"name":"Sick","paid":true,"active":false}`, allDuty, map[string]string{"If-Match": `"2"`})
	if rec.Code != http.StatusOK || f.updateVersion != 2 || f.absenceIn.Active || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	if rec := callWith(t, h, http.MethodPut, "/v1/shopfloor/absence-codes/x", `{"name":"S","paid":true}`, allDuty, map[string]string{"If-Match": `"2"`}); rec.Code != http.StatusNotFound || problemCode(t, rec) != store.CodeAbsenceNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
}

func TestAbsenceCode_ReadsAndDuties(t *testing.T) {
	h := newRouter(&fakeStore{})
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/absence-codes", "", "shopfloor.view"); rec.Code != http.StatusOK {
		t.Fatalf("list: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/absence-codes/"+absenceCodeID, "", "shopfloor.view"); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/absence-codes?active=maybe", "", "shopfloor.view"); rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad filter: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/v1/shopfloor/absence-codes", `{"code":"A","name":"A","paid":true}`, "shopfloor.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without the setup duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/absence-codes", "", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("list without a duty: %d", rec.Code)
	}
}

func TestAbsenceRegistration_Validation(t *testing.T) {
	f := &fakeStore{}
	h := newRouter(f)
	item := func(extra string) string {
		return `{"items":[{"id":"0195f0a0-0000-7000-8000-0000000000a1","workerId":"` + workerID + `","startedAt":"2026-10-20T08:00:00Z",` + extra + `}]}`
	}
	ok := item(`"kind":"absence","absenceCodeId":"` + absenceCodeID + `","stoppedAt":"2026-10-20T16:00:00Z"`)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", ok, allDuty)
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated && rec.Code != http.StatusAccepted {
		t.Fatalf("absence: %d %s", rec.Code, rec.Body.String())
	}
	if len(f.registered) != 1 || f.registered[0].AbsenceCodeID == nil || *f.registered[0].AbsenceCodeID != absenceCodeID || f.registered[0].Hours.String() != "8" {
		t.Fatalf("stored = %+v", f.registered)
	}
	for name, body := range map[string]string{
		"no code":            item(`"kind":"absence","stoppedAt":"2026-10-20T16:00:00Z"`),
		"open absence":       item(`"kind":"absence","absenceCodeId":"` + absenceCodeID + `"`),
		"zero length":        item(`"kind":"absence","absenceCodeId":"` + absenceCodeID + `","stoppedAt":"2026-10-20T08:00:00Z"`),
		"bad code":           item(`"kind":"absence","absenceCodeId":"x","stoppedAt":"2026-10-20T16:00:00Z"`),
		"code on a clock-in": item(`"kind":"clock-in","absenceCodeId":"` + absenceCodeID + `"`),
		"over 24 hours":      item(`"kind":"absence","absenceCodeId":"` + absenceCodeID + `","stoppedAt":"2026-10-21T08:00:01Z"`),
	} {
		rec := call(t, h, http.MethodPost, "/v1/shopfloor/registrations", body, allDuty)
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want a validation error", name, rec.Code, rec.Body.String())
		}
	}
}

func TestAbsence_CalculatePayAddsAbsenceLines(t *testing.T) {
	f := &fakeStore{
		punches: []domain.Punch{
			{ID: "in", Kind: domain.KindClockIn, At: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)},
			{ID: "out", Kind: domain.KindClockOut, At: time.Date(2026, 10, 20, 13, 0, 0, 0, time.UTC)},
		},
		absences: []domain.Absence{
			{ID: "a1", Start: time.Date(2026, 10, 20, 14, 0, 0, 0, time.UTC), Stop: time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC), Hours: decimal.NewFromInt(4), Paid: true, Multiplier: decimal.NewFromInt(1)},
			{ID: "a2", Start: time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC), Stop: time.Date(2026, 10, 21, 16, 0, 0, 0, time.UTC), Hours: decimal.NewFromInt(8), Paid: false, Multiplier: decimal.Zero},
		},
	}
	h := newRouter(f)
	rec := call(t, h, http.MethodPost, "/v1/shopfloor/pay:calculate", `{"workerId":"`+workerID+`","from":"2026-10-20","to":"2026-10-21"}`, allDuty)
	if rec.Code != http.StatusOK {
		t.Fatalf("calculate: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Days []struct {
			WorkDate string
			Hours    decimal.Decimal
			PayLines []struct {
				Type   string
				Hours  decimal.Decimal
				Amount decimal.Decimal
			}
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Days) != 2 {
		t.Fatalf("days = %s", rec.Body.String())
	}
	d1, d2 := resp.Days[0], resp.Days[1]
	if d1.Hours.String() != "4" || len(d1.PayLines) != 2 || d1.PayLines[1].Type != "absence-paid" || d1.PayLines[1].Amount.String() != "80" {
		t.Fatalf("day 1 = %+v", d1)
	}
	if d2.Hours.String() != "0" || len(d2.PayLines) != 1 || d2.PayLines[0].Type != "absence-unpaid" || d2.PayLines[0].Hours.String() != "8" || !d2.PayLines[0].Amount.IsZero() {
		t.Fatalf("day 2 = %+v", d2)
	}
}

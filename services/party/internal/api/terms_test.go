package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/party/internal/api"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

type fakeTerms struct {
	term     domain.NewPaymentTerm
	calendar domain.NewPaymentCalendar
	missing  bool
}

func (f *fakeTerms) CreatePaymentTerm(_ context.Context, in domain.NewPaymentTerm) (domain.PaymentTerm, error) {
	f.term = in
	return domain.PaymentTerm{ID: knownID, Code: in.Code, Name: in.Name, NetDays: in.NetDays, Discounts: in.Discounts, Version: 1}, nil
}

func (f *fakeTerms) CreatePaymentCalendar(_ context.Context, in domain.NewPaymentCalendar) (domain.PaymentCalendar, error) {
	f.calendar = in
	return domain.PaymentCalendar{ID: knownID, Code: in.Code, Name: in.Name, Holidays: in.Holidays, WeekendDays: *in.WeekendDays, Shift: in.Shift, Version: 1}, nil
}

func (f *fakeTerms) TermForDates(_ context.Context, id string) (domain.PaymentTerm, error) {
	if f.missing {
		return domain.PaymentTerm{}, httpx.NewProblem(http.StatusNotFound, store.CodePaymentTermNotFound, "not found")
	}
	days := 30
	return domain.PaymentTerm{ID: id, NetDays: &days, Discounts: []domain.Discount{{Percent: 2, Days: 10}},
		Calendar: &domain.PaymentCalendar{Shift: domain.ShiftFollowing, WeekendDays: []int{0, 6}, Holidays: []string{"2027-03-31"}}}, nil
}

func TestTermRoutes_Create(t *testing.T) {
	f := &fakeTerms{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithTerms(f).Register(r)

	const terms, calendars = "/v1/party/payment-terms", "/v1/party/payment-calendars"
	body := `{"code":" 2-10-n30 ","name":"2/10 net 30","netDays":30,"discounts":[{"percent":2,"days":10}]}`
	if rec := do(r, http.MethodPost, terms, body, "party.write", true); rec.Code != http.StatusCreated || f.term.Code != "2-10-N30" || !strings.Contains(rec.Body.String(), `"percent":2`) {
		t.Fatalf("create term = %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodPost, calendars, `{"code":"us","name":"US","holidays":["2027-03-31"]}`, "party.import.write", true); rec.Code != http.StatusCreated || f.calendar.Code != "US" || f.calendar.Shift != domain.ShiftFollowing || len(*f.calendar.WeekendDays) != 2 {
		t.Fatalf("create calendar = %d %s %+v", rec.Code, rec.Body, f.calendar)
	}

	for _, bad := range []string{`{"code":"A","name":"x"}`, `{"code":"A","name":"x","netDays":1,"dayOfMonth":2}`, `{"code":"A","name":"x","netDays":1,"discounts":[{"percent":101,"days":1}]}`, `{"code":"A","name":"x","netDays":1,"calendarId":"nope"}`} {
		if rec := do(r, http.MethodPost, terms, bad, "party.write", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeTermInvalid) {
			t.Fatalf("term %s = %d %s; want 422", bad, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, terms, `{"code":"A","name":"x","netDays":"30"}`, "party.write", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("string netDays = %d, want 400", rec.Code)
	}
	for _, bad := range []string{`{"code":"US","name":"US","holidays":["2027-13-01"]}`, `{"code":"US","name":"US","shift":"sideways"}`, `{"code":"US","name":"US","weekendDays":[7]}`} {
		if rec := do(r, http.MethodPost, calendars, bad, "party.write", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), store.CodeCalendarInvalid) {
			t.Fatalf("calendar %s = %d %s; want 422", bad, rec.Code, rec.Body)
		}
	}
	for _, path := range []string{terms, calendars} {
		if rec := do(r, http.MethodPost, path, body, "party.read", true); rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without a write duty = %d, want 403", path, rec.Code)
		}
		if rec := do(r, http.MethodPost, path, body, "party.write", false); rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST %s without a tenant = %d, want 401", path, rec.Code)
		}
	}
}

func TestTermRoutes_DueDates(t *testing.T) {
	f := &fakeTerms{}
	r := httpx.NewRouter()
	api.New(&stubChecker{}, stubPinger{}, &fakeParties{}, &fakeSequencer{}).WithTerms(f).Register(r)
	path := "/v1/party/payment-terms/" + knownID + ":due-dates"

	for _, duty := range []string{"party.read", "party.vendor.lookup"} {
		rec := do(r, http.MethodPost, path, `{"documentDate":"2027-03-01"}`, duty, true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"dueDate":"2027-04-01"`) || !strings.Contains(rec.Body.String(), `"date":"2027-03-11"`) {
			t.Fatalf("due dates with %s = %d %s", duty, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, path, `{"documentDate":"2027-03-01"}`, "party.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("without party.read or party.vendor.lookup = %d, want 403", rec.Code)
	}
	for _, bad := range []string{`{}`, `{"documentDate":""}`, `{"documentDate":"2027-02-30"}`} {
		if rec := do(r, http.MethodPost, path, bad, "party.read", true); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"documentDate"`) {
			t.Fatalf("%s = %d %s; want 422", bad, rec.Code, rec.Body)
		}
	}
	if rec := do(r, http.MethodPost, path, `{"documentDate":{}}`, "party.read", true); rec.Code != http.StatusBadRequest {
		t.Fatalf("object documentDate = %d, want 400", rec.Code)
	}
	f.missing = true
	if rec := do(r, http.MethodPost, path, `{"documentDate":"2027-03-01"}`, "party.read", true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodePaymentTermNotFound) {
		t.Fatalf("unknown term = %d %s", rec.Code, rec.Body)
	}
}

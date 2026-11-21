package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

func TestTerms_CreateReadAndComputeWithCalendar(t *testing.T) {
	s := store.New(migratedApp(t))
	ctx := asUser(1, legalEntityA, "clerk")

	calendar, err := s.CreatePaymentCalendar(ctx, domain.NewPaymentCalendar{Code: "US", Name: "US bank", Holidays: []string{"2027-03-31"}}.Clean())
	if err != nil || calendar.Shift != domain.ShiftFollowing || len(calendar.WeekendDays) != 2 || len(calendar.Holidays) != 1 {
		t.Fatalf("calendar = %+v, %v", calendar, err)
	}
	days := 30
	term, err := s.CreatePaymentTerm(ctx, domain.NewPaymentTerm{Code: "2-10-N30", Name: "2/10 net 30", NetDays: &days,
		Discounts: []domain.Discount{{Percent: 2, Days: 10}}, CalendarID: calendar.ID}.Clean())
	if err != nil || term.CalendarID != calendar.ID || len(term.Discounts) != 1 || term.Discounts[0].Percent != 2 {
		t.Fatalf("term = %+v, %v", term, err)
	}

	got, err := s.TermForDates(ctx, term.ID)
	if err != nil || got.Calendar == nil {
		t.Fatalf("term for dates = %+v, %v", got, err)
	}
	dates := got.DueDates(time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC))
	if dates.DueDate != "2027-04-01" || len(dates.Discounts) != 1 || dates.Discounts[0].Date != "2027-03-11" {
		t.Fatalf("a due date on the 2027-03-31 holiday shifts to the next business day: %+v", dates)
	}
}

func TestTerms_WithoutCalendarOrDiscount(t *testing.T) {
	s := store.New(migratedApp(t))
	ctx := asUser(1, legalEntityA, "clerk")
	dom := 15
	term, err := s.CreatePaymentTerm(ctx, domain.NewPaymentTerm{Code: "EOM15", Name: "15th next month", DayOfMonth: &dom, OffsetMonths: 1}.Clean())
	if err != nil || len(term.Discounts) != 0 || term.CalendarID != "" {
		t.Fatalf("term = %+v, %v", term, err)
	}
	got, err := s.TermForDates(ctx, term.ID)
	if err != nil || got.Calendar != nil || got.DueDates(time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)).DueDate != "2027-02-15" {
		t.Fatalf("term for dates = %+v, %v", got, err)
	}
}

func TestTerms_ConflictsAndUnknowns(t *testing.T) {
	s := store.New(migratedApp(t))
	ctx := asUser(1, legalEntityA, "clerk")
	days := 30
	in := domain.NewPaymentTerm{Code: "NET30", Name: "Net 30", NetDays: &days}.Clean()
	if _, err := s.CreatePaymentTerm(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePaymentTerm(ctx, in); problemCode(t, err) != store.CodeTermExists {
		t.Fatalf("duplicate term code = %v", err)
	}
	cal := domain.NewPaymentCalendar{Code: "US", Name: "US"}.Clean()
	if _, err := s.CreatePaymentCalendar(ctx, cal); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePaymentCalendar(ctx, cal); problemCode(t, err) != store.CodeCalendarExists {
		t.Fatalf("duplicate calendar code = %v", err)
	}
	in.Code, in.CalendarID = "OTHER", ids.New()
	if _, err := s.CreatePaymentTerm(ctx, in); problemCode(t, err) != store.CodeTermInvalid {
		t.Fatalf("unknown calendar = %v", err)
	}
	if _, err := s.TermForDates(ctx, ids.New()); problemCode(t, err) != store.CodePaymentTermNotFound {
		t.Fatalf("unknown term = %v", err)
	}
}

func TestTerms_AnotherTenantSeesNeitherTermNorCalendar(t *testing.T) {
	s := store.New(migratedApp(t))
	owner, other := asUser(1, legalEntityA, "clerk"), asUser(2, legalEntityA, "clerk")
	cal, err := s.CreatePaymentCalendar(owner, domain.NewPaymentCalendar{Code: "US", Name: "US"}.Clean())
	if err != nil {
		t.Fatal(err)
	}
	days := 30
	term, err := s.CreatePaymentTerm(owner, domain.NewPaymentTerm{Code: "NET30", Name: "Net 30", NetDays: &days, CalendarID: cal.ID}.Clean())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TermForDates(other, term.ID); problemCode(t, err) != store.CodePaymentTermNotFound {
		t.Fatalf("another tenant's term = %v", err)
	}
	if _, err := s.CreatePaymentTerm(other, domain.NewPaymentTerm{Code: "NET30", Name: "Net 30", NetDays: &days, CalendarID: cal.ID}.Clean()); problemCode(t, err) != store.CodeTermInvalid {
		t.Fatalf("another tenant's calendar on a new term = %v", err)
	}
}

package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
)

func intp(n int) *int { return &n }

func day(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestDueDates_TwoTenNetThirty(t *testing.T) {
	term := domain.PaymentTerm{NetDays: intp(30), Discounts: []domain.Discount{{Percent: 2, Days: 10}}}
	got := term.DueDates(day("2027-03-01"))
	if got.DueDate != "2027-03-31" || len(got.Discounts) != 1 || got.Discounts[0].Date != "2027-03-11" || got.Discounts[0].Percent != 2 {
		t.Fatalf("dates = %+v", got)
	}
}

func TestDueDates_NoCalendar(t *testing.T) {
	term := domain.PaymentTerm{NetDays: intp(5)}
	if got := term.DueDates(day("2027-03-01")); got.DueDate != "2027-03-06" || len(got.Discounts) != 0 {
		t.Fatalf("a Saturday without a calendar stays put: %+v", got)
	}
}

func TestDueDates_DayOfMonth(t *testing.T) {
	cases := []struct {
		name         string
		dom, offset  int
		document, wt string
	}{
		{"next month 15th", 15, 1, "2027-01-20", "2027-02-15"},
		{"clamped to month end", 31, 1, "2027-01-20", "2027-02-28"},
		{"leap year clamp", 30, 1, "2028-01-05", "2028-02-29"},
		{"across year end", 10, 2, "2027-11-30", "2028-01-10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term := domain.PaymentTerm{DayOfMonth: intp(tc.dom), OffsetMonths: tc.offset}
			if got := term.DueDates(day(tc.document)); got.DueDate != tc.wt {
				t.Fatalf("due = %s, want %s", got.DueDate, tc.wt)
			}
		})
	}
}

func TestDueDates_CalendarShift(t *testing.T) {
	cases := []struct {
		name, shift, document, want string
		net                         int
	}{
		{"following skips Saturday", domain.ShiftFollowing, "2027-03-01", "2027-03-08", 5},
		{"holiday on Wednesday shifts to Thursday", domain.ShiftFollowing, "2027-03-01", "2027-03-04", 2},
		{"preceding on Saturday goes to Friday", domain.ShiftPreceding, "2027-03-01", "2027-03-05", 5},
		{"modified following stays in month", domain.ShiftModifiedFollowing, "2027-03-01", "2027-04-01", 31},
		{"modified following flips back at month end", domain.ShiftModifiedFollowing, "2027-03-01", "2027-03-31", 30},
		{"modified following Saturday at month end", domain.ShiftModifiedFollowing, "2027-05-01", "2027-05-28", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cal := &domain.PaymentCalendar{Shift: tc.shift, WeekendDays: []int{0, 6}, Holidays: []string{"2027-03-03", "2027-05-31"}}
			term := domain.PaymentTerm{NetDays: intp(tc.net), Calendar: cal}
			if got := term.DueDates(day(tc.document)); got.DueDate != tc.want {
				t.Fatalf("due = %s, want %s", got.DueDate, tc.want)
			}
		})
	}
}

func TestDueDates_DiscountDateIsNotShifted(t *testing.T) {
	cal := &domain.PaymentCalendar{Shift: domain.ShiftFollowing, WeekendDays: []int{0, 6}}
	term := domain.PaymentTerm{NetDays: intp(30), Discounts: []domain.Discount{{Percent: 2, Days: 4}}, Calendar: cal}
	if got := term.DueDates(day("2027-03-01")); got.Discounts[0].Date != "2027-03-05" {
		t.Fatalf("discount = %+v", got.Discounts)
	}
	term.Discounts[0].Days = 5
	if got := term.DueDates(day("2027-03-01")); got.Discounts[0].Date != "2027-03-06" {
		t.Fatalf("a discount date on a Saturday is not shifted: %+v", got.Discounts)
	}
}

func TestPaymentTermValidate(t *testing.T) {
	valid := domain.NewPaymentTerm{Code: "2-10-N30", Name: "2/10 net 30", NetDays: intp(30), Discounts: []domain.Discount{{Percent: 2, Days: 10}}}
	if errs := valid.Clean().Validate(); len(errs) != 0 {
		t.Fatalf("valid term rejected: %v", errs)
	}
	cases := map[string]struct {
		mutate func(*domain.NewPaymentTerm)
		field  string
	}{
		"empty code":          {func(n *domain.NewPaymentTerm) { n.Code = " " }, "code"},
		"both bases":          {func(n *domain.NewPaymentTerm) { n.DayOfMonth = intp(15) }, "netDays"},
		"neither base":        {func(n *domain.NewPaymentTerm) { n.NetDays = nil }, "netDays"},
		"day 32":              {func(n *domain.NewPaymentTerm) { n.NetDays, n.DayOfMonth = nil, intp(32) }, "dayOfMonth"},
		"negative net days":   {func(n *domain.NewPaymentTerm) { n.NetDays = intp(-1) }, "netDays"},
		"percent over 100":    {func(n *domain.NewPaymentTerm) { n.Discounts = []domain.Discount{{Percent: 101, Days: 1}} }, "discounts"},
		"negative days":       {func(n *domain.NewPaymentTerm) { n.Discounts = []domain.Discount{{Percent: 1, Days: -1}} }, "discounts"},
		"four tiers":          {func(n *domain.NewPaymentTerm) { n.Discounts = make([]domain.Discount, 4) }, "discounts"},
		"calendar not uuid":   {func(n *domain.NewPaymentTerm) { n.CalendarID = "nope" }, "calendarId"},
		"negative offset":     {func(n *domain.NewPaymentTerm) { n.OffsetMonths = -1 }, "offsetMonths"},
		"name over 100 runes": {func(n *domain.NewPaymentTerm) { n.Name = string(make([]rune, 101)) }, "name"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			n := valid
			n.Discounts = append([]domain.Discount(nil), valid.Discounts...)
			tc.mutate(&n)
			if _, ok := n.Clean().Validate()[tc.field]; !ok {
				t.Fatalf("no error on %s: %v", tc.field, n.Clean().Validate())
			}
		})
	}
}

func TestPaymentCalendarValidate_WithoutCleanDoesNotPanicAndBoundsHolidays(t *testing.T) {
	// Validate before Clean has no weekend list yet; it must report, not dereference nil.
	if errs := (domain.NewPaymentCalendar{Code: "US", Name: "US", Shift: domain.ShiftFollowing}).Validate(); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	many := make([]string, domain.MaxCalendarHolidays+1)
	for i := range many {
		many[i] = "2027-01-01"
	}
	if _, ok := (domain.NewPaymentCalendar{Code: "US", Name: "US", Holidays: many}).Clean().Validate()["holidays"]; !ok {
		t.Fatal("an over-long holiday list was accepted")
	}
}

func TestPaymentCalendarValidate(t *testing.T) {
	if errs := (domain.NewPaymentCalendar{Code: "us", Name: "US"}).Clean().Validate(); len(errs) != 0 {
		t.Fatalf("defaults rejected: %v", errs)
	}
	all := []int{0, 1, 2, 3, 4, 5, 6}
	cases := map[string]struct {
		in    domain.NewPaymentCalendar
		field string
	}{
		"bad holiday":     {domain.NewPaymentCalendar{Code: "US", Name: "US", Holidays: []string{"2027-13-01"}}, "holidays"},
		"unknown shift":   {domain.NewPaymentCalendar{Code: "US", Name: "US", Shift: "sideways"}, "shift"},
		"weekend day 7":   {domain.NewPaymentCalendar{Code: "US", Name: "US", WeekendDays: &[]int{7}}, "weekendDays"},
		"every day off":   {domain.NewPaymentCalendar{Code: "US", Name: "US", WeekendDays: &all}, "weekendDays"},
		"empty code":      {domain.NewPaymentCalendar{Name: "US"}, "code"},
		"empty name":      {domain.NewPaymentCalendar{Code: "US"}, "name"},
		"feb 30 holidays": {domain.NewPaymentCalendar{Code: "US", Name: "US", Holidays: []string{"2027-02-30"}}, "holidays"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := tc.in.Clean().Validate()[tc.field]; !ok {
				t.Fatalf("no error on %s: %v", tc.field, tc.in.Clean().Validate())
			}
		})
	}
}

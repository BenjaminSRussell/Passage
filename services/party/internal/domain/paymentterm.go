package domain

import (
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	ShiftFollowing         = "following"
	ShiftPreceding         = "preceding"
	ShiftModifiedFollowing = "modified_following"

	maxDiscountTiers = 3
	maxTermDays      = 3650
	maxOffsetMonths  = 120
)

var termCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{1,20}$`)

type Discount struct {
	Percent float64 `json:"percent"`
	Days    int     `json:"days"`
}

type PaymentTerm struct {
	ID           string           `json:"id"`
	Code         string           `json:"code"`
	Name         string           `json:"name"`
	NetDays      *int             `json:"netDays,omitempty"`
	DayOfMonth   *int             `json:"dayOfMonth,omitempty"`
	OffsetMonths int              `json:"offsetMonths"`
	Discounts    []Discount       `json:"discounts"`
	CalendarID   string           `json:"calendarId,omitempty"`
	Calendar     *PaymentCalendar `json:"-"`
	Version      int              `json:"version"`
}

type NewPaymentTerm struct {
	Code         string     `json:"code"`
	Name         string     `json:"name"`
	NetDays      *int       `json:"netDays"`
	DayOfMonth   *int       `json:"dayOfMonth"`
	OffsetMonths int        `json:"offsetMonths"`
	Discounts    []Discount `json:"discounts"`
	CalendarID   string     `json:"calendarId"`
}

func (t NewPaymentTerm) Clean() NewPaymentTerm {
	t.Code = strings.ToUpper(strings.TrimSpace(t.Code))
	t.Name = strings.TrimSpace(t.Name)
	t.CalendarID = strings.TrimSpace(t.CalendarID)
	if id, err := uuid.Parse(t.CalendarID); err == nil {
		t.CalendarID = id.String()
	}
	return t
}

func (t NewPaymentTerm) Validate() map[string]string {
	errs := map[string]string{}
	if !termCodePattern.MatchString(t.Code) {
		errs["code"] = "is required: 1 to 20 characters of A-Z, 0-9, dash or underscore"
	}
	if n := len([]rune(t.Name)); n < 1 || n > 100 {
		errs["name"] = "is required, at most 100 characters"
	}
	if (t.NetDays == nil) == (t.DayOfMonth == nil) {
		errs["netDays"] = "exactly one of netDays and dayOfMonth is required"
	}
	if t.NetDays != nil && (*t.NetDays < 0 || *t.NetDays > maxTermDays) {
		errs["netDays"] = "must be between 0 and 3650"
	}
	if t.DayOfMonth != nil && (*t.DayOfMonth < 1 || *t.DayOfMonth > 31) {
		errs["dayOfMonth"] = "must be between 1 and 31"
	}
	if t.OffsetMonths < 0 || t.OffsetMonths > maxOffsetMonths {
		errs["offsetMonths"] = "must be between 0 and 120"
	}
	if len(t.Discounts) > maxDiscountTiers {
		errs["discounts"] = "at most 3 discount tiers"
	}
	for _, d := range t.Discounts {
		if math.IsNaN(d.Percent) || d.Percent <= 0 || d.Percent > 100 || d.Days < 0 || d.Days > maxTermDays {
			errs["discounts"] = "each tier needs a percent above 0 and at most 100, and days between 0 and 3650"
		}
	}
	if _, err := uuid.Parse(t.CalendarID); t.CalendarID != "" && err != nil {
		errs["calendarId"] = "must be a UUID when given"
	}
	return errs
}

// MaxCalendarHolidays bounds one calendar's holiday list so a create cannot store an unbounded array.
const MaxCalendarHolidays = 400

type PaymentCalendar struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Holidays    []string `json:"holidays"`
	WeekendDays []int    `json:"weekendDays"`
	Shift       string   `json:"shift"`
	Version     int      `json:"version"`
}

type NewPaymentCalendar struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Holidays    []string `json:"holidays"`
	WeekendDays *[]int   `json:"weekendDays"`
	Shift       string   `json:"shift"`
}

func (c NewPaymentCalendar) Clean() NewPaymentCalendar {
	c.Code = strings.ToUpper(strings.TrimSpace(c.Code))
	c.Name = strings.TrimSpace(c.Name)
	c.Shift = strings.TrimSpace(c.Shift)
	if c.Shift == "" {
		c.Shift = ShiftFollowing
	}
	if c.WeekendDays == nil {
		c.WeekendDays = &[]int{int(time.Saturday), int(time.Sunday)}
	}
	if c.Holidays == nil {
		c.Holidays = []string{}
	}
	return c
}

func (c NewPaymentCalendar) Validate() map[string]string {
	errs := map[string]string{}
	if !termCodePattern.MatchString(c.Code) {
		errs["code"] = "is required: 1 to 20 characters of A-Z, 0-9, dash or underscore"
	}
	if n := len([]rune(c.Name)); n < 1 || n > 100 {
		errs["name"] = "is required, at most 100 characters"
	}
	switch c.Shift {
	case ShiftFollowing, ShiftPreceding, ShiftModifiedFollowing:
	default:
		errs["shift"] = "must be following, preceding or modified_following"
	}
	var weekend []int
	if c.WeekendDays != nil {
		weekend = *c.WeekendDays
	}
	if len(weekend) > 6 {
		errs["weekendDays"] = "at most 6 weekend days"
	}
	for _, d := range weekend {
		if d < 0 || d > 6 {
			errs["weekendDays"] = "each day must be 0 (Sunday) to 6 (Saturday)"
		}
	}
	if len(c.Holidays) > MaxCalendarHolidays {
		errs["holidays"] = "at most 400 holidays; keep one or two years at a time"
	}
	for _, h := range c.Holidays {
		if _, err := time.Parse(time.DateOnly, h); err != nil {
			errs["holidays"] = "each holiday must be a YYYY-MM-DD date"
		}
	}
	return errs
}

func (c PaymentCalendar) isBusinessDay(d time.Time) bool {
	for _, w := range c.WeekendDays {
		if int(d.Weekday()) == w {
			return false
		}
	}
	return !slices.Contains(c.Holidays, d.Format(time.DateOnly))
}

func (c PaymentCalendar) step(d time.Time, dir int) time.Time {
	for range 366 {
		if c.isBusinessDay(d) {
			return d
		}
		d = d.AddDate(0, 0, dir)
	}
	return d
}

func (c PaymentCalendar) Shifted(d time.Time) time.Time {
	switch c.Shift {
	case ShiftPreceding:
		return c.step(d, -1)
	case ShiftModifiedFollowing:
		if next := c.step(d, 1); next.Month() == d.Month() {
			return next
		}
		return c.step(d, -1)
	default:
		return c.step(d, 1)
	}
}

type DiscountDate struct {
	Percent float64 `json:"percent"`
	Date    string  `json:"date"`
}

type Dates struct {
	DueDate   string         `json:"dueDate"`
	Discounts []DiscountDate `json:"discounts"`
}

func (t PaymentTerm) DueDates(documentDate time.Time) Dates {
	due := documentDate
	if t.NetDays != nil {
		due = documentDate.AddDate(0, 0, *t.NetDays)
	} else if t.DayOfMonth != nil {
		first := time.Date(documentDate.Year(), documentDate.Month()+time.Month(t.OffsetMonths), 1, 0, 0, 0, 0, time.UTC)
		lastDay := first.AddDate(0, 1, -1).Day()
		due = first.AddDate(0, 0, min(*t.DayOfMonth, lastDay)-1)
	}
	if t.Calendar != nil {
		due = t.Calendar.Shifted(due)
	}
	out := Dates{DueDate: due.Format(time.DateOnly), Discounts: []DiscountDate{}}
	for _, d := range t.Discounts {
		out.Discounts = append(out.Discounts, DiscountDate{Percent: d.Percent, Date: documentDate.AddDate(0, 0, d.Days).Format(time.DateOnly)})
	}
	return out
}

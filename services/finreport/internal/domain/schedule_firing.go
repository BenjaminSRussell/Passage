package domain

import (
	"fmt"
	"time"
)

// FiringLookback bounds how far back a firing is still made up when the job was
// not running at the cron time. Older cron times are skipped, never run late.
const FiringLookback = 31 * 24 * time.Hour

// maxFiringScan bounds the cron times examined in one look-back window, so a
// per-minute cron cannot make the job scan for long.
const maxFiringScan = 50000

// DueFire returns the latest cron time of the schedule in (after, now], or false
// when none is due. Only the latest one is returned: when the job missed
// several firings, the statement of the newest period is what is wanted, and
// running every missed one would send a burst of stale statements. A cron time
// older than FiringLookback is not made up.
func (s Schedule) DueFire(after, now time.Time) (time.Time, bool, error) {
	spec, err := ParseCron(s.Cron)
	if err != nil {
		return time.Time{}, false, err
	}
	loc, err := loadZone(s.Timezone)
	if err != nil {
		return time.Time{}, false, err
	}
	if floor := now.Add(-FiringLookback); after.Before(floor) {
		after = floor
	}
	var last time.Time
	at := after.In(loc)
	for i := 0; i < maxFiringScan; i++ {
		next, err := spec.Next(at)
		if err != nil {
			return time.Time{}, false, err
		}
		if next.After(now) {
			break
		}
		last, at = next, next
	}
	return last.UTC(), !last.IsZero(), nil
}

// FiringPeriod is the period a firing runs for and the fiscal calendar that
// holds it and the two fiscal years before it (prior-year columns need them).
type FiringPeriod struct {
	Year     int
	No       int
	Calendar []Period
}

// Label is the period as "YYYY-PP", the form used in file names and events.
func (p FiringPeriod) Label() string { return fmt.Sprintf("%04d-%02d", p.Year, p.No) }

// FiringPeriod places a firing at fire in the fiscal calendar: the period is the
// calendar month before the month of fire, in the schedule's time zone, which is
// how a month-end statement pack is run on the first days of the next month.
// Fiscal years are twelve monthly periods plus the opening period 0, numbered by
// the calendar year in which they end (with FiscalYearStartMonth 1, the calendar
// year). The ledger's own calendar is wave 14's (NOT-IMPLEMENTED.md handoff).
func (s Schedule) FiringPeriod(fire time.Time) (FiringPeriod, error) {
	loc, err := loadZone(s.Timezone)
	if err != nil {
		return FiringPeriod{}, err
	}
	start := s.FiscalYearStartMonth
	if start == 0 {
		start = 1
	}
	if start < 1 || start > 12 {
		return FiringPeriod{}, fmt.Errorf("fiscalYearStartMonth %d is not a month", start)
	}
	t := fire.In(loc)
	month := time.Date(t.Year(), t.Month()-1, 1, 0, 0, 0, 0, loc)
	year, no := fiscalPlace(month, start)
	cal := make([]Period, 0, 3*13)
	for fy := year - 2; fy <= year; fy++ {
		cal = append(cal, Period{Year: fy, No: 0, Quarter: 1})
		for n := 1; n <= 12; n++ {
			cal = append(cal, Period{Year: fy, No: n, Quarter: (n-1)/3 + 1})
		}
	}
	return FiringPeriod{Year: year, No: no, Calendar: cal}, nil
}

// fiscalPlace is the fiscal year and period number of a calendar month.
func fiscalPlace(month time.Time, start int) (year, no int) {
	m := int(month.Month())
	no = (m-start+12)%12 + 1
	year = month.Year()
	if start > 1 && m >= start {
		year++
	}
	return year, no
}

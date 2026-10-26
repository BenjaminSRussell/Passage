package domain

import "time"

func TeamInForce(from time.Time, to *time.Time, asOf time.Time) bool {
	day := dateOf(asOf)
	return !dateOf(from).After(day) && (to == nil || !day.After(dateOf(*to)))
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func MemberRangeViolation(teamFrom time.Time, teamTo *time.Time, validFrom time.Time, validTo *time.Time, today time.Time) string {
	if teamTo != nil && dateOf(*teamTo).Before(dateOf(today)) {
		return "expired"
	}
	if dateOf(validFrom).Before(dateOf(teamFrom)) {
		return "range"
	}
	if teamTo != nil && (dateOf(validFrom).After(dateOf(*teamTo)) || validTo == nil || dateOf(*validTo).After(dateOf(*teamTo))) {
		return "range"
	}
	return ""
}

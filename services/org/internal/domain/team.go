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

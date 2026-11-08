package domain

import (
	"errors"
	"testing"
	"time"
)

func TestResolvePostingDate(t *testing.T) {
	// 23:30 UTC on 1 March is already 2 March in Auckland (UTC+13) and still 1 March in New York.
	at := time.Date(2027, 3, 1, 23, 30, 0, 0, time.UTC)
	d := func(s string) *time.Time { v, _ := time.Parse("2006-01-02", s); return &v }
	cases := []struct {
		name, source, zone string
		req                *time.Time
		want               string
		err                error
	}{
		{"ship timestamp in the warehouse zone", PostingShipTimestamp, "Pacific/Auckland", nil, "2027-03-02", nil},
		{"ship timestamp west of UTC", PostingShipTimestamp, "America/New_York", nil, "2027-03-01", nil},
		{"request equal to the computed date", PostingShipTimestamp, "Pacific/Auckland", d("2027-03-02"), "2027-03-02", nil},
		{"request contradicting the computed date", PostingShipTimestamp, "Pacific/Auckland", d("2027-03-01"), "", ErrPostingDate},
		{"today is the day in the warehouse zone", PostingToday, "Pacific/Auckland", nil, "2027-03-02", nil},
		{"today west of UTC", PostingToday, "America/New_York", nil, "2027-03-01", nil},
		{"today is a default a requested date overrides", PostingToday, "Pacific/Auckland", d("2027-02-28"), "2027-02-28", nil},
		{"manual takes the requested date", PostingManual, "UTC", d("2027-02-20"), "2027-02-20", nil},
		{"manual needs a date", PostingManual, "UTC", nil, "", ErrPostingDateRequired},
	}
	for _, c := range cases {
		got, err := ResolvePostingDate(c.source, c.zone, at, c.req)
		if !errors.Is(err, c.err) {
			t.Errorf("%s: error %v, want %v", c.name, err, c.err)
			continue
		}
		if c.err == nil && got.Format("2006-01-02") != c.want {
			t.Errorf("%s: %s, want %s", c.name, got.Format("2006-01-02"), c.want)
		}
	}
	for _, source := range []string{PostingShipTimestamp, PostingToday} {
		if _, err := ResolvePostingDate(source, "Mars/Olympus", at, nil); err == nil {
			t.Errorf("%s: an unknown time zone resolved", source)
		}
	}
	if _, err := ResolvePostingDate("LATER", "UTC", at, nil); err == nil {
		t.Error("an unknown source resolved")
	}
}

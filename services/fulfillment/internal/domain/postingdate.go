package domain

import (
	"errors"
	"fmt"
	"time"
	// The warehouse time zones are loaded by name, also in an image without a zoneinfo database.
	_ "time/tzdata"
)

// Posting date sources of a warehouse (section 5.28.13 posting_date_source,
// BR-28.3).
const (
	PostingShipTimestamp = "SHIP_TIMESTAMP"
	PostingToday         = "TODAY"
	PostingManual        = "MANUAL"
)

// ErrPostingDate is a posting date that contradicts the warehouse's
// posting_date_source (FUL-0074).
var ErrPostingDate = errors.New("FUL-0074: the posting date contradicts the warehouse's posting_date_source")

// ErrPostingDateRequired is a MANUAL warehouse post without a posting date.
var ErrPostingDateRequired = errors.New("postingDate is required when the warehouse's posting_date_source is MANUAL")

// ValidPostingSource reports whether s is a posting date source.
func ValidPostingSource(s string) bool {
	return s == PostingShipTimestamp || s == PostingToday || s == PostingManual
}

// ResolvePostingDate gives the posting date of a packing slip posted at
// shippedAt. The day of the post is always taken in the warehouse time zone
// (section 5.28.20: the posting date derives from the warehouse time zone).
//   - SHIP_TIMESTAMP (BR-28.3) is that day and nothing else: a request that
//     names another date is refused (FUL-0074).
//   - TODAY defaults to that day; a request that names a date overrides it
//     (the parameter is the posting date default, like the SAP actual goods
//     issue date that defaults to today).
//   - MANUAL takes the requested date and has no default.
//
// The result is a date at 00:00 UTC.
func ResolvePostingDate(source, zone string, shippedAt time.Time, requested *time.Time) (time.Time, error) {
	switch source {
	case PostingManual:
		if requested == nil {
			return time.Time{}, ErrPostingDateRequired
		}
		return utcDay(*requested), nil
	case PostingShipTimestamp, PostingToday:
	default:
		return time.Time{}, fmt.Errorf("unknown posting date source %q", source)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("warehouse time zone %q: %w", zone, err)
	}
	want := utcDay(shippedAt.In(loc))
	if requested == nil {
		return want, nil
	}
	if source == PostingToday {
		return utcDay(*requested), nil
	}
	if got := utcDay(*requested); !got.Equal(want) {
		return time.Time{}, fmt.Errorf("%w: %s computes %s, the request names %s", ErrPostingDate, source, want.Format("2006-01-02"), got.Format("2006-01-02"))
	}
	return want, nil
}

func utcDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

package domain

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Dashboard limits (FR-46.8, §9.46.13 analytics.dashboard.refresh_s). A
// dashboard load is budgeted for 8 tiles (§9.46.19); 12 is the hard cap.
const (
	MaxDashboardTiles  = 12
	DefaultRefreshSecs = 60
	MinRefreshSecs     = 15
	MaxRefreshSecs     = 600
	maxDashboardTitle  = 120
)

// Tile periods a dashboard tile may show. They map to fiscal-calendar windows in Cube.
var tilePeriods = []string{"MTD", "QTD", "YTD", "TRAILING_12M"}

var dashboardID = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,62}$`)

// IsDashboardID reports whether id is a well-formed dashboard id.
func IsDashboardID(id string) bool { return dashboardID.MatchString(id) }

// Tile is one KPI tile bound to a published governed metric.
type Tile struct {
	Title  string `json:"title"`
	Metric string `json:"metric"`
	Period string `json:"period"`
}

// Dashboard is an embedded dashboard definition. Tiles are rendered in order.
// Version is the ETag source: PUT replaces a dashboard only with If-Match.
type Dashboard struct {
	ID             string     `json:"dashboardId"`
	Title          string     `json:"title"`
	Tiles          []Tile     `json:"tiles"`
	RefreshSeconds int        `json:"refreshSeconds"`
	Version        int        `json:"version"`
	UpdatedAt      *time.Time `json:"updatedAt,omitempty"`
}

// PrepareDashboard trims titles, defaults the refresh interval and checks the
// dashboard against the shape rules and the published metric catalog. Every
// tile must reference a published metric, so a dashboard never shows a measure
// that is not governed (FR-46.6). Every invalid field is reported at once.
func PrepareDashboard(d Dashboard, catalog []Metric) (Dashboard, *Error) {
	fields := map[string]string{}
	d.Title = strings.TrimSpace(d.Title)
	if d.RefreshSeconds == 0 {
		d.RefreshSeconds = DefaultRefreshSecs
	}
	if !IsDashboardID(d.ID) {
		fields["dashboardId"] = "2 to 63 lowercase letters, digits, _ or -, starting with a letter"
	}
	if d.Title == "" || utf8.RuneCountInString(d.Title) > maxDashboardTitle {
		fields["title"] = "required, at most 120 characters"
	}
	if len(d.Tiles) == 0 || len(d.Tiles) > MaxDashboardTiles {
		fields["tiles"] = "between 1 and 12 tiles"
	}
	if d.RefreshSeconds < MinRefreshSecs || d.RefreshSeconds > MaxRefreshSecs {
		fields["refreshSeconds"] = "15 to 600 seconds"
	}
	tiles := make([]Tile, len(d.Tiles))
	for i, t := range d.Tiles {
		key := "tiles[" + strconv.Itoa(i) + "]"
		t.Title = strings.TrimSpace(t.Title)
		if t.Title == "" || utf8.RuneCountInString(t.Title) > maxDashboardTitle {
			fields[key+".title"] = "required, at most 120 characters"
		}
		if !slices.Contains(tilePeriods, t.Period) {
			fields[key+".period"] = "MTD, QTD, YTD or TRAILING_12M"
		}
		if _, ok := CatalogMetric(catalog, t.Metric); !ok {
			fields[key+".metric"] = "a published governed metric"
		}
		tiles[i] = t
	}
	if len(fields) > 0 {
		return Dashboard{}, validation(fields)
	}
	d.Tiles = tiles
	return d, nil
}

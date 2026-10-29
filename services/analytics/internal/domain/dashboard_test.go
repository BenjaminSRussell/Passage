package domain

import "testing"

func validDashboard() Dashboard {
	return Dashboard{
		ID: "controller_kpis", Title: " Controller KPIs ",
		Tiles: []Tile{
			{Title: "Revenue", Metric: "revenue", Period: "MTD"},
			{Title: " OTIF ", Metric: "otif_pct", Period: "QTD"},
		},
	}
}

func TestPrepareDashboard_DefaultsAndTrims(t *testing.T) {
	got, err := PrepareDashboard(validDashboard(), GovernedMetrics())
	if err != nil {
		t.Fatal(err)
	}
	if got.RefreshSeconds != DefaultRefreshSecs || got.Title != "Controller KPIs" || got.Tiles[1].Title != "OTIF" {
		t.Fatalf("prepared = %+v", got)
	}
}

func TestPrepareDashboard_Rejects(t *testing.T) {
	catalog := GovernedMetrics()
	cases := []struct {
		name   string
		mutate func(*Dashboard)
		field  string
	}{
		{"bad id", func(d *Dashboard) { d.ID = "Controller KPIs" }, "dashboardId"},
		{"one-letter id", func(d *Dashboard) { d.ID = "c" }, "dashboardId"},
		{"blank title", func(d *Dashboard) { d.Title = "   " }, "title"},
		{"no tiles", func(d *Dashboard) { d.Tiles = nil }, "tiles"},
		{"too many tiles", func(d *Dashboard) {
			for range MaxDashboardTiles {
				d.Tiles = append(d.Tiles, Tile{Title: "x", Metric: "revenue", Period: "YTD"})
			}
		}, "tiles"},
		{"refresh too fast", func(d *Dashboard) { d.RefreshSeconds = 5 }, "refreshSeconds"},
		{"refresh too slow", func(d *Dashboard) { d.RefreshSeconds = 601 }, "refreshSeconds"},
		{"unpublished metric", func(d *Dashboard) { d.Tiles[0].Metric = "scratch_metric" }, "tiles[0].metric"},
		{"bad period", func(d *Dashboard) { d.Tiles[1].Period = "WEEKLY" }, "tiles[1].period"},
		{"blank tile title", func(d *Dashboard) { d.Tiles[1].Title = " " }, "tiles[1].title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDashboard()
			tc.mutate(&d)
			_, err := PrepareDashboard(d, catalog)
			if err == nil || err.Code != CodeValidation || err.Fields[tc.field] == "" {
				t.Fatalf("want a validation error on %s, got %v", tc.field, err)
			}
		})
	}
	// Boundaries are inclusive.
	for _, secs := range []int{MinRefreshSecs, MaxRefreshSecs} {
		d := validDashboard()
		d.RefreshSeconds = secs
		if _, err := PrepareDashboard(d, catalog); err != nil {
			t.Errorf("refresh %d s: %v", secs, err)
		}
	}
}

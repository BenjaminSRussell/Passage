package domain

import (
	"testing"
	"time"
)

func TestBlockUtilization(t *testing.T) {
	day := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)
	h := func(hh int) time.Time { return day.Add(time.Duration(hh) * time.Hour) }
	iv := func(a, b int) Interval { return Interval{Start: h(a), End: h(b)} }
	white := []Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	open := []Interval{iv(8, 16)}
	job := func(a, b int, color string) BlockJob {
		return BlockJob{Interval: iv(a, b), Attributes: map[string]string{"color": color}}
	}

	tests := []struct {
		name     string
		open     []Interval
		blocks   []CapacityBlock
		jobs     []BlockJob
		from, to time.Time
		want     []BlockWindow
	}{
		{
			name:   "a matching job uses the block, a red job inside it is not counted",
			open:   open,
			blocks: []CapacityBlock{{ID: "w", Priority: 1, Condition: white, Windows: []Interval{iv(8, 12)}}},
			jobs:   []BlockJob{job(8, 10, "white"), job(10, 11, "red"), job(12, 14, "white")},
			from:   h(0), to: h(24),
			want: []BlockWindow{{BlockID: "w", Priority: 1, Window: iv(8, 12), BlockMinutes: 240, MatchedMinutes: 120, UnusedMinutes: 120}},
		},
		{
			name:   "closed calendar time inside the window holds nothing",
			open:   open,
			blocks: []CapacityBlock{{ID: "w", Condition: white, Windows: []Interval{iv(6, 10)}}},
			from:   h(0), to: h(24),
			want: []BlockWindow{{BlockID: "w", Window: iv(6, 10), BlockMinutes: 120, UnusedMinutes: 120}},
		},
		{
			name: "a higher-priority block takes the overlap from a lower one (BR-23.15)",
			open: open,
			blocks: []CapacityBlock{
				{ID: "low", Priority: 1, Windows: []Interval{iv(8, 12)}},
				{ID: "high", Priority: 5, Condition: white, Windows: []Interval{iv(10, 14)}},
			},
			jobs: []BlockJob{job(10, 12, "white")},
			from: h(0), to: h(24),
			want: []BlockWindow{
				{BlockID: "low", Priority: 1, Window: iv(8, 12), BlockMinutes: 120, UnusedMinutes: 120},
				{BlockID: "high", Priority: 5, Window: iv(10, 14), BlockMinutes: 240, MatchedMinutes: 120, UnusedMinutes: 120},
			},
		},
		{
			name:   "windows are clipped to the range and windows outside it are left out",
			open:   []Interval{iv(8, 16), iv(32, 40)},
			blocks: []CapacityBlock{{ID: "w", Windows: []Interval{iv(8, 12), iv(32, 36)}}},
			jobs:   []BlockJob{job(9, 11, "red")},
			from:   h(10), to: h(24),
			want: []BlockWindow{{BlockID: "w", Window: iv(10, 12), BlockMinutes: 120, MatchedMinutes: 60, UnusedMinutes: 60}},
		},
		{
			name:   "overlapping windows of one block count each minute once",
			open:   open,
			blocks: []CapacityBlock{{ID: "w", Windows: []Interval{iv(10, 14), iv(8, 12)}}},
			from:   h(0), to: h(24),
			want: []BlockWindow{
				{BlockID: "w", Window: iv(8, 12), BlockMinutes: 240, UnusedMinutes: 240},
				{BlockID: "w", Window: iv(10, 14), BlockMinutes: 120, UnusedMinutes: 120},
			},
		},
		{
			name: "no blocks give an empty list, not nil",
			open: open,
			from: h(0), to: h(24),
			want: []BlockWindow{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BlockUtilization(tc.open, tc.blocks, tc.jobs, tc.from, tc.to)
			if got == nil {
				t.Fatal("got nil, want a list")
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d windows %+v, want %d", len(got), got, len(tc.want))
			}
			for i := range got {
				g, w := got[i], tc.want[i]
				if g.BlockID != w.BlockID || g.Priority != w.Priority || !g.Window.Start.Equal(w.Window.Start) || !g.Window.End.Equal(w.Window.End) ||
					g.BlockMinutes != w.BlockMinutes || g.MatchedMinutes != w.MatchedMinutes || g.UnusedMinutes != w.UnusedMinutes {
					t.Errorf("window %d = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

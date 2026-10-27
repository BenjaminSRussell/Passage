package domain

import (
	"slices"
	"sort"
	"time"
)

// BlockJob is a booked segment on a resource with the attributes of its
// operation, as the block utilization inquiry reads it.
type BlockJob struct {
	Interval
	Attributes map[string]string
}

// BlockWindow is one occurrence of a capacity block window inside an inquiry
// range with what it held and how much of that was used (FR-23.17, PRD
// 4.23.14 Block utilization). Window is the occurrence clipped to the range.
// BlockMinutes counts the open calendar minutes the block holds there once
// higher-priority blocks have taken theirs (BR-23.15); MatchedMinutes counts
// the booked minutes of jobs matching the block condition inside them, and
// UnusedMinutes the rest.
type BlockWindow struct {
	BlockID        string
	Priority       int
	Window         Interval
	BlockMinutes   int
	MatchedMinutes int
	UnusedMinutes  int
}

// BlockUtilization expands each block's windows over [from, to) on one
// resource. Blocks are resolved as Placeable resolves them: in priority order
// (highest first, then by ID), each window holds only the open time no
// higher-priority block, nor an earlier window of the same block, has already
// claimed, so no minute is counted twice. Windows that hold no minute of the
// range are still listed, with zero minutes, when they overlap it. The result
// is in start order, then priority (highest first) and block ID.
func BlockUtilization(open []Interval, blocks []CapacityBlock, jobs []BlockJob, from, to time.Time) []BlockWindow {
	ordered := slices.Clone(blocks)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority > ordered[j].Priority
		}
		return ordered[i].ID < ordered[j].ID
	})
	span := Interval{Start: from, End: to}
	var claimed []Interval
	out := []BlockWindow{}
	for _, b := range ordered {
		var matching []Interval
		for _, j := range jobs {
			if b.Matches(j.Attributes) {
				matching = append(matching, j.Interval)
			}
		}
		windows := slices.Clone(b.Windows)
		sort.SliceStable(windows, func(i, j int) bool { return windows[i].Start.Before(windows[j].Start) })
		for _, w := range windows {
			clipped, ok := ivIntersect(w, span)
			if !ok {
				continue
			}
			bw := BlockWindow{BlockID: b.ID, Priority: b.Priority, Window: clipped}
			for _, o := range open {
				piece, ok := ivIntersect(clipped, o)
				if !ok {
					continue
				}
				for _, held := range ivSubtractAll(piece, claimed) {
					bw.BlockMinutes += held.Minutes()
					for _, m := range matching {
						if used, ok := ivIntersect(held, m); ok {
							bw.MatchedMinutes += used.Minutes()
						}
					}
				}
				claimed = append(claimed, piece)
			}
			bw.UnusedMinutes = bw.BlockMinutes - bw.MatchedMinutes
			out = append(out, bw)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Window.Start.Equal(out[j].Window.Start) {
			return out[i].Window.Start.Before(out[j].Window.Start)
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].BlockID < out[j].BlockID
	})
	return out
}

package domain

import (
	"context"
	"sort"

	"github.com/shopspring/decimal"
)

const BatchRule = "batch"

type RecLine struct {
	No int
	Facts
}

type Item struct {
	ID    string
	Batch string
	Facts
}

type AutoMatch struct {
	LineNos    []int
	ItemIDs    []string
	Score      int
	Rule       string
	Difference decimal.Decimal
}

type Suggestion struct {
	LineNo int
	ItemID string
	Score  int
}

type Outcome struct {
	Matches     []AutoMatch
	Suggestions []Suggestion
}

// ReconcileStatement matches lines to open items. N:1 batch receipts come first:
// a line whose reference names a batch of two or more items matches them when
// their amounts add up to the line exactly and every item is inside the widest
// date window of the rules. The remaining lines go through MatchLine one to one.
// An item claimed by more than one match is released from all of them, so a
// duplicate or ambiguous claim is never auto-matched (BR-36.2).
func ReconcileStatement(ctx context.Context, lines []RecLine, items []Item, rules []Rule, t Thresholds) (Outcome, error) {
	ordered := append([]Rule(nil), rules...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Priority != ordered[j].Priority {
			return ordered[i].Priority < ordered[j].Priority
		}
		return ordered[i].Name < ordered[j].Name
	})
	window := 0
	for _, r := range ordered {
		window = max(window, r.Tolerance.DateWindowDays)
	}
	var out Outcome
	used := map[string]bool{}
	done := map[int]bool{}
	for _, m := range matchBatches(lines, items, window) {
		out.Matches = append(out.Matches, m)
		done[m.LineNos[0]] = true
		for _, id := range m.ItemIDs {
			used[id] = true
		}
	}
	open := map[string]Facts{}
	for _, it := range items {
		if !used[it.ID] {
			open[it.ID] = it.Facts
		}
	}
	index := newMatchIndex(open)
	decisions := map[int]Decision{}
	claims := map[string]int{}
	for _, l := range lines {
		if done[l.No] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		line := prepare("", l.Facts)
		d := index.matchLine(line, ordered, t, true)
		decisions[l.No] = d
		if d.Status == StatusMatched {
			claims[d.Best.ID]++
		}
	}
	for _, l := range lines {
		d, ok := decisions[l.No]
		switch {
		case !ok || d.Best == nil:
		case d.Status == StatusMatched && claims[d.Best.ID] == 1:
			out.Matches = append(out.Matches, AutoMatch{
				LineNos: []int{l.No}, ItemIDs: []string{d.Best.ID}, Score: d.Best.Score, Rule: d.Rule,
				Difference: l.Amount.Sub(open[d.Best.ID].Amount),
			})
		default:
			out.Suggestions = append(out.Suggestions, Suggestion{LineNo: l.No, ItemID: d.Best.ID, Score: d.Best.Score})
		}
	}
	return out, nil
}

func matchBatches(lines []RecLine, items []Item, window int) []AutoMatch {
	groups := map[string][]Item{}
	for _, it := range items {
		if k := normalizedReference(it.Batch); k != "" {
			groups[k] = append(groups[k], it)
		}
	}
	var found []AutoMatch
	claims := map[string]int{}
	for _, l := range lines {
		key, group := "", []Item(nil)
		for _, ref := range lineReferences(l.Facts) {
			if len(ref) >= MinStrictReferenceLen && !placeholderReference(ref) && len(groups[ref]) >= 2 {
				key, group = ref, groups[ref]
				break
			}
		}
		if key == "" {
			continue
		}
		sum, inWindow := decimal.Zero, true
		ids := make([]string, 0, len(group))
		for _, it := range group {
			sum = sum.Add(it.Amount)
			inWindow = inWindow && withinWindow(l.ValueDate, it.ValueDate, window)
			ids = append(ids, it.ID)
		}
		if !inWindow || !sum.Equal(l.Amount) {
			continue
		}
		sort.Strings(ids)
		found = append(found, AutoMatch{LineNos: []int{l.No}, ItemIDs: ids, Score: 100, Rule: BatchRule})
		for _, id := range ids {
			claims[id]++
		}
	}
	kept := found[:0]
	for _, m := range found {
		contested := false
		for _, id := range m.ItemIDs {
			contested = contested || claims[id] > 1
		}
		if !contested {
			kept = append(kept, m)
		}
	}
	return kept
}

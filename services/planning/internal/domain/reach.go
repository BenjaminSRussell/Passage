package domain

// ReachSet returns the items a dynamic net-change run replans (BR-22.6): the
// changed items plus every item reachable from them through BOM components,
// that is every item below them with a greater low-level code. Parents of a
// changed item are not in the set. Each item is visited once, so the walk
// ends on any input.
//
// A dynamic run built on it must keep, as fixed dependent demand, the
// bom-line demand that planned orders outside the set put on items inside
// it; planning the set alone would drop that demand.
func ReachSet(items []Item, changed []string) map[string]bool {
	children := make(map[string][]string, len(items))
	for _, it := range items {
		for _, c := range it.Components {
			children[it.ID] = append(children[it.ID], c.ItemID)
		}
	}
	reach := make(map[string]bool)
	stack := append([]string(nil), changed...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if reach[id] {
			continue
		}
		reach[id] = true
		stack = append(stack, children[id]...)
	}
	return reach
}

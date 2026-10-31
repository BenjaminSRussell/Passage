package domain

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Direction is the work direction a location directive or line applies to.
type Direction string

const (
	DirectionPut  Direction = "PUT"
	DirectionPick Direction = "PICK"
)

// ValidDirection reports whether d is PUT or PICK.
func ValidDirection(d Direction) bool { return d == DirectionPut || d == DirectionPick }

// LocationStatus is the operational state of a location (FR-31.1).
type LocationStatus string

const (
	StatusActive     LocationStatus = "ACTIVE"
	StatusBlockedIn  LocationStatus = "BLOCKED_IN"
	StatusBlockedOut LocationStatus = "BLOCKED_OUT"
	StatusBlocked    LocationStatus = "BLOCKED"
	StatusInactive   LocationStatus = "INACTIVE"
)

// Allows reports whether a location in this status may take part in work in
// direction d. BLOCKED_IN refuses put-away, BLOCKED_OUT refuses picks.
func (s LocationStatus) Allows(d Direction) bool {
	switch s {
	case StatusActive:
		return true
	case StatusBlockedIn:
		return d == DirectionPick
	case StatusBlockedOut:
		return d == DirectionPut
	}
	return false
}

// Usage is an amount of weight (kg), volume (m3), license plates and distinct
// items. Items is never read from a request: a location's item count is derived
// from its stock (see load), so it is not part of the JSON shape.
type Usage struct {
	Weight decimal.Decimal `json:"weight"`
	Volume decimal.Decimal `json:"volume"`
	LPs    int             `json:"lps"`
	Items  int             `json:"-"`
}

// Capacity is the profile limit set; a nil limit is unlimited (FR-31.1).
type Capacity struct {
	MaxWeight       *decimal.Decimal `json:"maxWeight,omitempty"`
	MaxVolume       *decimal.Decimal `json:"maxVolume,omitempty"`
	MaxLPs          *int             `json:"maxLps,omitempty"`
	MaxItems        *int             `json:"maxItems,omitempty"`
	AllowMixedItems bool             `json:"allowMixedItems"`
}

// Fits reports whether used+add stays within every limit (BR-31.2). Weight and
// volume are compared at 6 decimals. When a limit is exceeded the reason names
// the dimension as "lps 1/1" (used over maximum), the form the trace prints.
func (c Capacity) Fits(used, add Usage) (string, bool) {
	if c.MaxWeight != nil && round6(used.Weight).Add(round6(add.Weight)).GreaterThan(*c.MaxWeight) {
		return fmt.Sprintf("weight %s/%s", round6(used.Weight), c.MaxWeight), false
	}
	if c.MaxVolume != nil && round6(used.Volume).Add(round6(add.Volume)).GreaterThan(*c.MaxVolume) {
		return fmt.Sprintf("volume %s/%s", round6(used.Volume), c.MaxVolume), false
	}
	if c.MaxLPs != nil && used.LPs+add.LPs > *c.MaxLPs {
		return fmt.Sprintf("lps %d/%d", used.LPs, *c.MaxLPs), false
	}
	if c.MaxItems != nil && used.Items+add.Items > *c.MaxItems {
		return fmt.Sprintf("items %d/%d", used.Items, *c.MaxItems), false
	}
	return "", true
}

func round6(d decimal.Decimal) decimal.Decimal { return d.Round(6) }

// Location is a bin in a warehouse zone. Stock is item quantity by item id;
// Used holds the weight, volume and LP count (Used.Items is derived, see load).
type Location struct {
	ID           string                     `json:"id"`
	Name         string                     `json:"name"`
	Zone         string                     `json:"zone"`
	PickSequence int                        `json:"pickSequence"`
	Status       LocationStatus             `json:"status"`
	Capacity     Capacity                   `json:"capacity"`
	Used         Usage                      `json:"used"`
	Stock        map[string]decimal.Decimal `json:"stock,omitempty"`
	// FireSection is the fire-containment section (FR-31.19); empty means none.
	FireSection string `json:"fireSection,omitempty"`
	// RackGroup is the location group used by "separate" segregation.
	RackGroup string `json:"rackGroup,omitempty"`
	// AllowedStorageClasses restricts hazmat storage; empty allows any class.
	AllowedStorageClasses []string `json:"allowedStorageClasses,omitempty"`
	// Version counts stored changes of a persisted location.
	Version int `json:"version,omitempty"`
	// inbound holds items that open PUT lines will bring (Reserve, planning
	// only): they count as items for mixing, the item limit and emptiness.
	inbound map[string]bool
	// incoming is the quantity of each item that open PUT lines will bring.
	incoming map[string]decimal.Decimal
}

// load returns the current usage with Items derived from the stock map.
func (l Location) load() Usage {
	u := l.Used
	u.Items = l.itemCount()
	return u
}

func (l Location) itemCount() int {
	n := 0
	for _, q := range l.Stock {
		if q.IsPositive() {
			n++
		}
	}
	for item := range l.inbound {
		if !l.holds(item) {
			n++
		}
	}
	return n
}

func (l Location) holds(item string) bool { return l.Stock[item].IsPositive() }

// covers reports whether the location holds at least qty of item.
func (l Location) covers(item string, qty decimal.Decimal) bool {
	return l.holds(item) && l.Stock[item].GreaterThanOrEqual(qty)
}

func (l Location) isEmpty() bool { return l.Used.LPs == 0 && l.itemCount() == 0 }

// mixing refuses a second item when the profile forbids mixed items (WMS-0103).
func (l Location) mixing(item string) (string, bool) {
	if l.Capacity.AllowMixedItems {
		return "", true
	}
	others := make([]string, 0, len(l.Stock)+len(l.inbound))
	for other, q := range l.Stock {
		if q.IsPositive() && other != item {
			others = append(others, other)
		}
	}
	for other := range l.inbound {
		if other != item && !l.holds(other) {
			others = append(others, other)
		}
	}
	if len(others) == 0 {
		return "", true
	}
	slices.Sort(others)
	return "mixing: holds " + others[0], false
}

// admitPut is the non-mutating put-away check: mixing rules, then capacity.
func (l Location) admitPut(item string, add Usage) (string, bool) {
	if reason, ok := l.mixing(item); !ok {
		return reason, false
	}
	if !l.holds(item) && !l.inbound[item] {
		add.Items = 1
	}
	if reason, ok := l.Capacity.Fits(l.load(), add); !ok {
		return "capacity: " + reason, false
	}
	return "", true
}

// Receive puts qty of item (with add's weight, volume and LPs) into the location.
// It is the only mutation path for stock, so capacity is an invariant: when a
// rule refuses, the location is left unchanged and the reason is returned.
func (l *Location) Receive(item string, qty decimal.Decimal, add Usage) (string, bool) {
	if reason, ok := l.admitPut(item, add); !ok {
		return reason, false
	}
	l.Used.Weight = round6(l.Used.Weight.Add(round6(add.Weight)))
	l.Used.Volume = round6(l.Used.Volume.Add(round6(add.Volume)))
	l.Used.LPs += add.LPs
	if l.Stock == nil {
		l.Stock = map[string]decimal.Decimal{}
	}
	l.Stock[item] = l.Stock[item].Add(qty)
	return "", true
}

// LocationName builds a name from a prefix and components, zero-padded to two
// digits: ("A", 1, 2, 3) gives "A-01-02-03" (FR-31.1 LocationFormat).
func LocationName(prefix string, parts ...int) (string, error) {
	if prefix == "" || strings.Contains(prefix, "-") || len(parts) == 0 {
		return "", fmt.Errorf("location name needs a prefix without '-' and at least one component")
	}
	var b strings.Builder
	b.WriteString(prefix)
	for _, p := range parts {
		if p < 0 || p > 99 {
			return "", fmt.Errorf("location component %d must be 0 to 99", p)
		}
		fmt.Fprintf(&b, "-%02d", p)
	}
	return b.String(), nil
}

// ParseLocationName reverses LocationName. Every component must be exactly two digits.
func ParseLocationName(name string) (string, []int, error) {
	fields := strings.Split(name, "-")
	if len(fields) < 2 || fields[0] == "" {
		return "", nil, fmt.Errorf("location name %q is not prefix-nn-nn", name)
	}
	parts := make([]int, 0, len(fields)-1)
	for _, f := range fields[1:] {
		if len(f) != 2 || !allDigits(f) {
			return "", nil, fmt.Errorf("location name %q has component %q, not two digits", name, f)
		}
		n, err := strconv.Atoi(f)
		if err != nil {
			return "", nil, err
		}
		parts = append(parts, n)
	}
	return fields[0], parts, nil
}

// Action is one step of a directive line: the strategy orders candidates in
// Zone (empty = any zone). See BR-31.1 step 3.
type Action struct {
	Seq      int      `json:"seq"`
	Strategy Strategy `json:"strategy"`
	Zone     string   `json:"zone,omitempty"`
}

// ValidStrategy reports whether s is one of the strategies of FR-31.3.
func ValidStrategy(s Strategy) bool {
	switch s {
	case StrategyConsolidate, StrategyFIFO, StrategyLIFO, StrategyFEFO, StrategyEmptyLocation,
		StrategyMatchPackingQuantity, StrategyRoundUpToLP, StrategyOutboundStaging:
		return true
	}
	return false
}

// sortCandidates orders admitted locations best first for the strategy. For a
// PUT with consolidate, locations already holding the item come first; ties go
// by pick sequence, then name. Pick strategies order by pick sequence in this
// slice; receipt-date ordering for FIFO/FEFO needs quant dates (later slice).
func sortCandidates(c []Location, s Strategy, item string) {
	slices.SortStableFunc(c, func(a, b Location) int {
		if s == StrategyConsolidate {
			ha, hb := a.holds(item), b.holds(item)
			if ha != hb {
				if ha {
					return -1
				}
				return 1
			}
		}
		if a.PickSequence != b.PickSequence {
			return cmp.Compare(a.PickSequence, b.PickSequence)
		}
		return strings.Compare(a.Name, b.Name)
	})
}

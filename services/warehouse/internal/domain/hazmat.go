package domain

import (
	"fmt"
	"maps"
	"regexp"
	"slices"

	"github.com/shopspring/decimal"
)

// HazmatRel is the segregation relation between two storage classes (BR-31.15).
type HazmatRel string

const (
	RelAllowed    HazmatRel = "allowed"
	RelSeparate   HazmatRel = "separate"
	RelProhibited HazmatRel = "prohibited"
)

// ValidRel reports whether r is one of the three segregation relations.
func ValidRel(r HazmatRel) bool {
	return r == RelAllowed || r == RelSeparate || r == RelProhibited
}

var unNumberPattern = regexp.MustCompile(`^UN[0-9]{4}$`)

// HazmatProfile is the dangerous-goods data of one item (FR-31.19). StorageClass
// is the TRGS 510 or NFPA 30 class that storage checks compare; the other fields
// are carried for wave 34 transport checks and wave 33 SDS links.
type HazmatProfile struct {
	ItemID             string           `json:"itemId"`
	UNNumber           string           `json:"unNumber,omitempty"`
	ShippingName       string           `json:"shippingName,omitempty"`
	Class              string           `json:"class,omitempty"`
	Division           string           `json:"division,omitempty"`
	PackingGroup       string           `json:"packingGroup,omitempty"`
	StorageClass       string           `json:"storageClass"`
	WaterHazardClass   string           `json:"waterHazardClass,omitempty"`
	FlashPointC        *decimal.Decimal `json:"flashPointC,omitempty"`
	NetExplosiveMassKg *decimal.Decimal `json:"netExplosiveMassKg,omitempty"`
}

// Validate checks the fields a storage check depends on.
func (p HazmatProfile) Validate() error {
	switch {
	case p.ItemID == "":
		return fmt.Errorf("item id is required")
	case p.StorageClass == "":
		return fmt.Errorf("storage class is required")
	case p.UNNumber != "" && !unNumberPattern.MatchString(p.UNNumber):
		return fmt.Errorf("UN number %q must be UN followed by four digits", p.UNNumber)
	case p.NetExplosiveMassKg != nil && p.NetExplosiveMassKg.IsNegative():
		return fmt.Errorf("net explosive mass must not be negative")
	}
	return nil
}

// SegregationPair is one entry of the reference-data matrix. The relation is
// symmetric: (3, 5.1) and (5.1, 3) are the same entry.
type SegregationPair struct {
	A   string    `json:"a"`
	B   string    `json:"b"`
	Rel HazmatRel `json:"rel"`
}

// SegregationMatrix holds the explicit relations keyed by sorted class pair.
// A pair with no entry is allowed.
type SegregationMatrix map[[2]string]HazmatRel

func pairKey(a, b string) [2]string {
	if b < a {
		a, b = b, a
	}
	return [2]string{a, b}
}

// Rel returns the relation between two storage classes.
func (m SegregationMatrix) Rel(a, b string) HazmatRel {
	if r, ok := m[pairKey(a, b)]; ok {
		return r
	}
	return RelAllowed
}

// Compliance is the hazmat reference data an evaluation uses: profiles by item,
// the segregation matrix, and section quantity limits in kg (BR-31.15). The
// zero value disables every hazmat check.
type Compliance struct {
	Profiles       map[string]HazmatProfile
	Matrix         SegregationMatrix
	SectionLimitKg map[string]decimal.Decimal
}

// NewCompliance validates the reference data and builds a Compliance.
func NewCompliance(profiles []HazmatProfile, pairs []SegregationPair, limits map[string]decimal.Decimal) (Compliance, error) {
	c := Compliance{
		Profiles:       map[string]HazmatProfile{},
		Matrix:         SegregationMatrix{},
		SectionLimitKg: limits,
	}
	for _, p := range profiles {
		if err := p.Validate(); err != nil {
			return Compliance{}, fmt.Errorf("hazmat profile %q: %w", p.ItemID, err)
		}
		if _, dup := c.Profiles[p.ItemID]; dup {
			return Compliance{}, fmt.Errorf("hazmat profile %q is listed twice", p.ItemID)
		}
		c.Profiles[p.ItemID] = p
	}
	for _, p := range pairs {
		if p.A == "" || p.B == "" {
			return Compliance{}, fmt.Errorf("segregation pair needs two storage classes")
		}
		if !ValidRel(p.Rel) {
			return Compliance{}, fmt.Errorf("segregation %s/%s has unknown relation %q", p.A, p.B, p.Rel)
		}
		k := pairKey(p.A, p.B)
		if old, dup := c.Matrix[k]; dup && old != p.Rel {
			return Compliance{}, fmt.Errorf("segregation %s/%s is given two relations", p.A, p.B)
		}
		c.Matrix[k] = p.Rel
	}
	for sec, lim := range limits {
		if lim.IsNegative() {
			return Compliance{}, fmt.Errorf("section %s limit must not be negative", sec)
		}
	}
	return c, nil
}

// sectionState summarises a fire-containment section: the weight on its
// locations and the storage classes present, with the racks holding each class.
type sectionState struct {
	kg      decimal.Decimal
	classes map[string]map[string]bool
}

type sections map[string]*sectionState

// sectionsOf builds the section view once per evaluation. The hazmat quantity
// of a section (BR-31.15) is the weight on its locations that hold at least one
// item with a hazmat profile; locations holding only non-hazmat stock do not
// count. Stock carries quantities per item, not weights, so a location mixing
// hazmat and other items counts its whole weight (the conservative reading).
func (c Compliance) sectionsOf(locations []Location) sections {
	if len(c.Profiles) == 0 {
		return nil
	}
	out := sections{}
	for _, l := range locations {
		if l.FireSection == "" {
			continue
		}
		s := out[l.FireSection]
		if s == nil {
			s = &sectionState{kg: decimal.Zero, classes: map[string]map[string]bool{}}
			out[l.FireSection] = s
		}
		hazmat := false
		for item, q := range l.Stock {
			p, ok := c.Profiles[item]
			if !ok || !q.IsPositive() {
				continue
			}
			hazmat = true
			racks := s.classes[p.StorageClass]
			if racks == nil {
				racks = map[string]bool{}
				s.classes[p.StorageClass] = racks
			}
			racks[l.RackGroup] = true
		}
		if hazmat {
			s.kg = s.kg.Add(l.Used.Weight)
		}
	}
	return out
}

// admitHazmat is the storage-compatibility test for a PUT of a hazmat item into
// l (BR-31.15): allowed storage classes of the location, prohibited and separate
// pairs within the section, and the section quantity limit. Non-hazmat lines and
// PICK lines pass. Locations without a rack group share the empty group, so a
// "separate" pair is refused between them rather than assumed apart.
func (c Compliance) admitHazmat(line Line, l Location, secs sections) (string, bool) {
	p, ok := c.Profiles[line.ItemID]
	if !ok || line.Direction != DirectionPut {
		return "", true
	}
	cls := p.StorageClass
	if len(l.AllowedStorageClasses) > 0 && !slices.Contains(l.AllowedStorageClasses, cls) {
		return fmt.Sprintf("hazmat: class %s not allowed at %s", cls, l.Name), false
	}
	if l.FireSection == "" {
		return "", true
	}
	s := secs[l.FireSection]
	if s == nil {
		return "", true
	}
	for _, held := range slices.Sorted(maps.Keys(s.classes)) {
		switch c.Matrix.Rel(cls, held) {
		case RelProhibited:
			return fmt.Sprintf("hazmat: class %s vs %s prohibited in %s", cls, held, l.FireSection), false
		case RelSeparate:
			if s.classes[held][l.RackGroup] {
				return fmt.Sprintf("hazmat: class %s vs %s separate in rack %s", cls, held, l.RackGroup), false
			}
		}
	}
	if lim, ok := c.SectionLimitKg[l.FireSection]; ok {
		if after := s.kg.Add(line.Weight); after.GreaterThan(lim) {
			return fmt.Sprintf("hazmat: section %s limit %s kg exceeded (%s/%s)", l.FireSection, lim, after, lim), false
		}
	}
	return "", true
}

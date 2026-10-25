package domain

import (
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/shopspring/decimal"
)

const (
	StatusDraft      = "draft"
	StatusActive     = "active"
	StatusExpired    = "expired"
	StatusSuperseded = "superseded"

	LineItem      = "item"
	LinePhantom   = "phantom"
	LineCoProduct = "co_product"
	LineByProduct = "by_product"

	RoundUpQuantity = "quantity"

	MaxLevels = 25
)

var hundred = decimal.NewFromInt(100)

type BomLine struct {
	LineNo             int
	ComponentProductID string
	Quantity           decimal.Decimal
	Series             decimal.Decimal
	LineType           string
	ScrapConst         decimal.Decimal
	ScrapVarPct        decimal.Decimal
	RoundUp            string
	EffectiveFrom      time.Time
	EffectiveTo        time.Time
}

type BomVersion struct {
	ID            string
	ProductID     string
	SiteID        string
	EffectiveFrom time.Time
	EffectiveTo   time.Time
	FromQty       *decimal.Decimal
	ToQty         *decimal.Decimal
	Status        string
	Lines         []BomLine
}

type Catalog interface {
	VersionsFor(product string) []BomVersion
	VersionsUsing(component string) []BomVersion
}

type ExplodeRequest struct {
	ProductID string
	SiteID    string
	AsOf      time.Time
	Quantity  decimal.Decimal
	Levels    int
}

type Node struct {
	ProductID string
	LineNo    int
	Level     int
	Quantity  decimal.Decimal
	Children  []*Node
}

type FlatLine struct {
	ProductID string
	Quantity  decimal.Decimal
}

type Explosion struct {
	Root *Node
	Flat []FlatLine
}

type UsedBy struct {
	ProductID string
	VersionID string
	SiteID    string
	Level     int
	Parents   []*UsedBy
}

func covers(v BomVersion, asOf time.Time) bool {
	return (v.Status == StatusActive || v.Status == StatusExpired) &&
		!asOf.Before(v.EffectiveFrom) && (v.EffectiveTo.IsZero() || !asOf.After(v.EffectiveTo))
}

func inQty(v BomVersion, qty decimal.Decimal) bool {
	return (v.FromQty == nil || qty.GreaterThanOrEqual(*v.FromQty)) && (v.ToQty == nil || qty.LessThanOrEqual(*v.ToQty))
}

func LineActive(l BomLine, asOf time.Time) bool {
	return (l.EffectiveFrom.IsZero() || !asOf.Before(l.EffectiveFrom)) && (l.EffectiveTo.IsZero() || !asOf.After(l.EffectiveTo))
}

func ResolveVersion(versions []BomVersion, product, site string, asOf time.Time, qty decimal.Decimal) (BomVersion, error) {
	best, ties := -1, 0
	var picked BomVersion
	for _, v := range versions {
		if v.ProductID != product || !covers(v, asOf) || !inQty(v, qty) || (v.SiteID != "" && v.SiteID != site) {
			continue
		}
		score := 0
		if v.SiteID != "" {
			score += 2
		}
		if v.FromQty != nil || v.ToQty != nil {
			score++
		}
		switch {
		case score > best:
			best, ties, picked = score, 1, v
		case score == best:
			ties++
		}
	}
	switch {
	case best < 0:
		return BomVersion{}, errf(CodeBomNoVersion, "no active BOM version for %s on %s", product, asOf.Format(dateLayout))
	case ties > 1:
		return BomVersion{}, errf(CodeBomOverlap, "%d versions of %s match site %q on %s", ties, product, site, asOf.Format(dateLayout))
	}
	return picked, nil
}

func ValidateLine(l BomLine) error {
	if !l.Quantity.IsPositive() || !l.Series.IsPositive() || l.ScrapConst.IsNegative() ||
		l.ScrapVarPct.IsNegative() || l.ScrapVarPct.GreaterThan(hundred) {
		return errf(CodeBomLine, "line %d of component %s has invalid quantity, series or scrap", l.LineNo, l.ComponentProductID)
	}
	return nil
}

func Consumption(l BomLine, parentQty decimal.Decimal) decimal.Decimal {
	q := parentQty.Mul(l.Quantity).Mul(hundred.Add(l.ScrapVarPct)).Div(l.Series.Mul(hundred)).Add(l.ScrapConst)
	if l.RoundUp == RoundUpQuantity {
		return q.Ceil()
	}
	// ponytail: measurement and consumption rounding need unit precision and order totals, not available in the pure domain; upgrade when unit precision is supplied
	return q
}

type walker struct {
	cat    Catalog
	req    ExplodeRequest
	flat   map[string]decimal.Decimal
	onPath map[string]bool
	path   []string
}

func Explode(cat Catalog, req ExplodeRequest) (Explosion, error) {
	if !req.Quantity.IsPositive() {
		return Explosion{}, errf(CodeBomQty, "quantity must be positive")
	}
	if req.Levels < 1 || req.Levels > MaxLevels {
		return Explosion{}, errf(CodeBomLevels, "levels must be between 1 and %d", MaxLevels)
	}
	w := &walker{cat: cat, req: req, flat: map[string]decimal.Decimal{}, onPath: map[string]bool{}}
	rootVersion, err := w.resolve(req.ProductID, req.Quantity)
	if err != nil {
		return Explosion{}, err
	}
	children, err := w.descend(req.ProductID, rootVersion, req.Quantity, 0)
	if err != nil {
		return Explosion{}, err
	}
	flat := make([]FlatLine, 0, len(w.flat))
	for p, q := range w.flat {
		flat = append(flat, FlatLine{ProductID: p, Quantity: q})
	}
	sort.Slice(flat, func(i, j int) bool { return flat[i].ProductID < flat[j].ProductID })
	return Explosion{Root: &Node{ProductID: req.ProductID, Quantity: req.Quantity, Children: children}, Flat: flat}, nil
}

func (w *walker) resolve(product string, qty decimal.Decimal) (BomVersion, error) {
	return ResolveVersion(w.cat.VersionsFor(product), product, w.req.SiteID, w.req.AsOf, qty)
}

func (w *walker) descend(product string, v BomVersion, qty decimal.Decimal, level int) ([]*Node, error) {
	w.onPath[product] = true
	w.path = append(w.path, product)
	defer func() {
		delete(w.onPath, product)
		w.path = w.path[:len(w.path)-1]
	}()
	var out []*Node
	for _, line := range v.Lines {
		if !LineActive(line, w.req.AsOf) || line.LineType == LineCoProduct || line.LineType == LineByProduct {
			continue
		}
		if err := ValidateLine(line); err != nil {
			return nil, err
		}
		comp := line.ComponentProductID
		if w.onPath[comp] {
			return nil, errf(CodeBomCycle, "Adding %s creates a cycle: %s", comp, strings.Join(append(w.path, comp), "→"))
		}
		compQty := Consumption(line, qty)
		cv, err := w.resolve(comp, compQty)
		missing := HasCode(err, CodeBomNoVersion)
		if err != nil && !missing {
			return nil, err
		}
		if line.LineType == LinePhantom {
			if missing {
				return nil, errf(CodeBomPhantomNoBom, "Phantom %s has no active BOM on %s.", comp, w.req.AsOf.Format(dateLayout))
			}
			inline, err := w.descend(comp, cv, compQty, level)
			if err != nil {
				return nil, err
			}
			out = append(out, inline...)
			continue
		}
		node := &Node{ProductID: comp, LineNo: line.LineNo, Level: level + 1, Quantity: compQty}
		if !missing && level+1 < w.req.Levels {
			if node.Children, err = w.descend(comp, cv, compQty, level+1); err != nil {
				return nil, err
			}
		}
		if len(node.Children) == 0 {
			w.flat[comp] = w.flat[comp].Add(compQty)
		}
		out = append(out, node)
	}
	return out, nil
}

func CheckCycle(cat Catalog, parent, component string, asOf time.Time) error {
	seen := map[string]bool{}
	var walk func(product string, path []string) []string
	walk = func(product string, path []string) []string {
		path = append(path, product)
		if product == parent {
			return path
		}
		if seen[product] {
			return nil
		}
		seen[product] = true
		for _, v := range cat.VersionsFor(product) {
			if !covers(v, asOf) {
				continue
			}
			for _, l := range v.Lines {
				if LineActive(l, asOf) {
					if found := walk(l.ComponentProductID, path); found != nil {
						return found
					}
				}
			}
		}
		return nil
	}
	if found := walk(component, []string{parent}); found != nil {
		return errf(CodeBomCycle, "Adding %s creates a cycle: %s", component, strings.Join(found, "→"))
	}
	return nil
}

func rangesOverlap(aFrom, aTo, bFrom, bTo time.Time) bool {
	return (aTo.IsZero() || !bFrom.After(aTo)) && (bTo.IsZero() || !aFrom.After(bTo))
}

func qtyOverlap(a, b BomVersion) bool {
	return (a.ToQty == nil || b.FromQty == nil || b.FromQty.LessThanOrEqual(*a.ToQty)) &&
		(b.ToQty == nil || a.FromQty == nil || a.FromQty.LessThanOrEqual(*b.ToQty))
}

func CheckNoOverlap(existing []BomVersion, candidate BomVersion) error {
	for _, other := range existing {
		if other.ID != candidate.ID && other.Status == StatusActive && other.ProductID == candidate.ProductID &&
			other.SiteID == candidate.SiteID && qtyOverlap(other, candidate) &&
			rangesOverlap(other.EffectiveFrom, other.EffectiveTo, candidate.EffectiveFrom, candidate.EffectiveTo) {
			return errf(CodeBomOverlap, "Version overlaps active version %s for this site, date and quantity.", other.ID)
		}
	}
	return nil
}

func WhereUsed(cat Catalog, component string, asOf time.Time, levels int) ([]*UsedBy, error) {
	if levels < 1 || levels > MaxLevels {
		return nil, errf(CodeBomLevels, "levels must be between 1 and %d", MaxLevels)
	}
	onPath := map[string]bool{component: true}
	var walk func(comp string, level int) ([]*UsedBy, error)
	walk = func(comp string, level int) ([]*UsedBy, error) {
		var out []*UsedBy
		for _, v := range cat.VersionsUsing(comp) {
			if !covers(v, asOf) || !usesComponent(v, comp, asOf) {
				continue
			}
			if onPath[v.ProductID] {
				return nil, errf(CodeBomCycle, "%s uses itself through %s", v.ProductID, comp)
			}
			node := &UsedBy{ProductID: v.ProductID, VersionID: v.ID, SiteID: v.SiteID, Level: level}
			if level < levels {
				onPath[v.ProductID] = true
				parents, err := walk(v.ProductID, level+1)
				delete(onPath, v.ProductID)
				if err != nil {
					return nil, err
				}
				node.Parents = parents
			}
			out = append(out, node)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].ProductID+"/"+out[i].VersionID < out[j].ProductID+"/"+out[j].VersionID
		})
		return out, nil
	}
	return walk(component, 1)
}

func usesComponent(v BomVersion, component string, asOf time.Time) bool {
	for _, l := range v.Lines {
		if l.ComponentProductID == component && LineActive(l, asOf) {
			return true
		}
	}
	return false
}

func RollupCost(flat []FlatLine, unitCost map[string]money.Amount, currency string) (money.Amount, []string, error) {
	var priced []money.Amount
	var missing []string
	for _, f := range flat {
		uc, ok := unitCost[f.ProductID]
		if !ok {
			missing = append(missing, f.ProductID)
			continue
		}
		priced = append(priced, uc.Mul(f.Quantity))
	}
	total, err := money.Sum(currency, priced...)
	return total, missing, err
}

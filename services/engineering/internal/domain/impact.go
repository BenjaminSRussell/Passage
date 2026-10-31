package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

const (
	ReasonReplaced = "replaced"
	ReasonParent   = "parent"

	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	ChangeChanged = "changed"
)

// LineChange is one component whose summed quantity differs between the old
// and the new version of a replacement.
type LineChange struct {
	ComponentProductID string
	Change             string
	OldQty             decimal.Decimal
	NewQty             decimal.Decimal
}

type ImpactReplacement struct {
	OldVersionID string
	NewVersionID string
	ProductID    string
	Changes      []LineChange
}

// ImpactProduct is a product an ECO touches: the product whose version is
// replaced, or a product that uses it through the bill of material. TopLevel
// marks a product no active version uses as of the date, the distinct
// top-level products a where-used of the replaced products returns (AC-9.6).
type ImpactProduct struct {
	ProductID  string
	Reason     string
	Level      int
	TopLevel   bool
	SiteIDs    []string
	VersionIDs []string
}

// Impact is the ECO impact report of FR-9.8.
type Impact struct {
	AsOf                 time.Time
	Replacements         []ImpactReplacement
	Products             []ImpactProduct
	SiteIDs              []string
	OpenProductionOrders int
	OpenPlannedOrders    int
}

// OrderCounts supplies the open order counts per product. Production and
// planned orders arrive in waves 21 and 22; until then every count is zero.
type OrderCounts interface {
	OpenOrders(product string) (production, planned int)
}

type NoOrders struct{}

func (NoOrders) OpenOrders(string) (int, int) { return 0, 0 }

func lineTotals(v BomVersion) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	for _, l := range v.Lines {
		out[l.ComponentProductID] = out[l.ComponentProductID].Add(l.Quantity)
	}
	return out
}

// DiffLines compares the summed component quantities of two versions.
func DiffLines(oldV, newV BomVersion) []LineChange {
	was, now := lineTotals(oldV), lineTotals(newV)
	var out []LineChange
	for c, q := range was {
		switch n, ok := now[c]; {
		case !ok:
			out = append(out, LineChange{ComponentProductID: c, Change: ChangeRemoved, OldQty: q})
		case !n.Equal(q):
			out = append(out, LineChange{ComponentProductID: c, Change: ChangeChanged, OldQty: q, NewQty: n})
		}
	}
	for c, n := range now {
		if _, ok := was[c]; !ok {
			out = append(out, LineChange{ComponentProductID: c, Change: ChangeAdded, NewQty: n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComponentProductID < out[j].ComponentProductID })
	return out
}

type impactAcc struct {
	p     ImpactProduct
	sites map[string]bool
	vers  map[string]bool
}

// BuildImpact lists the products an ECO affects as of a date: each replaced
// product with its active versions, and every product that uses it at any
// level (multi-level where-used, across sites). versions holds the replaced
// and replacing versions; cat must reach the replaced products upwards.
func BuildImpact(cat Catalog, e Eco, versions []BomVersion, asOf time.Time, orders OrderCounts) (Impact, error) {
	byID := map[string]BomVersion{}
	for _, v := range versions {
		byID[v.ID] = v
	}
	if orders == nil {
		orders = NoOrders{}
	}
	report := Impact{AsOf: asOf, Replacements: []ImpactReplacement{}, Products: []ImpactProduct{}, SiteIDs: []string{}}
	accs := map[string]*impactAcc{}
	touch := func(product, reason string, level int, site, version string) {
		a := accs[product]
		if a == nil {
			a = &impactAcc{p: ImpactProduct{ProductID: product, Reason: reason, Level: level}, sites: map[string]bool{}, vers: map[string]bool{}}
			accs[product] = a
		}
		if reason == ReasonReplaced {
			a.p.Reason, a.p.Level = ReasonReplaced, 0
		} else if a.p.Reason != ReasonReplaced && level < a.p.Level {
			a.p.Level = level
		}
		if site != "" {
			a.sites[site] = true
		}
		if version != "" {
			a.vers[version] = true
		}
	}
	used := map[string]bool{}
	var walk func(nodes []*UsedBy)
	walk = func(nodes []*UsedBy) {
		for _, n := range nodes {
			touch(n.ProductID, ReasonParent, n.Level, n.SiteID, n.VersionID)
			if len(n.Parents) > 0 {
				used[n.ProductID] = true
			}
			walk(n.Parents)
		}
	}
	var replaced []string
	for _, r := range e.Replaces {
		oldV, newV := byID[r.OldVersionID], byID[r.NewVersionID]
		product := oldV.ProductID
		if product == "" {
			product = newV.ProductID
		}
		report.Replacements = append(report.Replacements, ImpactReplacement{
			OldVersionID: r.OldVersionID, NewVersionID: r.NewVersionID, ProductID: product, Changes: DiffLines(oldV, newV),
		})
		if product == "" {
			continue
		}
		// Only the replacement's own site is affected: a version of the same
		// product at another site is resolved there instead of the replaced one.
		site := oldV.SiteID
		if oldV.ID == "" {
			site = newV.SiteID
		}
		touch(product, ReasonReplaced, 0, site, r.OldVersionID)
		for _, v := range cat.VersionsFor(product) {
			if v.SiteID == site && covers(v, asOf) {
				touch(product, ReasonReplaced, 0, v.SiteID, v.ID)
			}
		}
		if !contains(replaced, product) {
			replaced = append(replaced, product)
		}
	}
	for _, product := range replaced {
		parents, err := WhereUsed(cat, product, asOf, MaxLevels)
		if err != nil {
			return Impact{}, err
		}
		if len(parents) > 0 {
			used[product] = true
		}
		walk(parents)
	}
	sites := map[string]bool{}
	for _, a := range accs {
		a.p.SiteIDs, a.p.VersionIDs, a.p.TopLevel = keys(a.sites), keys(a.vers), !used[a.p.ProductID]
		for s := range a.sites {
			sites[s] = true
		}
		prod, plan := orders.OpenOrders(a.p.ProductID)
		report.OpenProductionOrders += prod
		report.OpenPlannedOrders += plan
		report.Products = append(report.Products, a.p)
	}
	sort.Slice(report.Products, func(i, j int) bool {
		a, b := report.Products[i], report.Products[j]
		if a.Level != b.Level {
			return a.Level < b.Level
		}
		return a.ProductID < b.ProductID
	})
	report.SiteIDs = keys(sites)
	return report, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// ItemKind says how an item's standard is obtained.
type ItemKind string

const (
	KindPurchased    ItemKind = "Purchased"
	KindManufactured ItemKind = "Manufactured"
)

// ExplosionMode says how child items contribute to a parent (FR-39.4 step 3).
// Multilevel rolls every child in this run. SingleLevel takes every child at
// its active standard. MakeToOrder rolls purchased children at their version
// price and explodes the manufactured children on make-to-order lines;
// ByLineType does the same but explodes no make-to-order line, so those
// sub-assemblies are costed at their active standard. Phantom lines are
// exploded in place in every mode (BR-39.3); see explodes.
type ExplosionMode string

const (
	ModeMultilevel  ExplosionMode = "Multilevel"
	ModeSingleLevel ExplosionMode = "SingleLevel"
	ModeMakeToOrder ExplosionMode = "MakeToOrder"
	ModeByLineType  ExplosionMode = "ByLineType"
)

// Defaults from the parameter table (costing.maxLevels, costing.rollWorkers).
const (
	DefaultMaxLevels = 20
	DefaultWorkers   = 4
	maxWorkers       = 32
	maxLevelsCeiling = 40
)

// Diagnostic codes from the wave-39 message catalog.
const (
	CodeCycle            = "CST-0701"
	CodeNoPrice          = "CST-0702"
	CodeFallback         = "CST-0703"
	CodeDepth            = "CST-0704"
	CodeMissingComponent = "CST-0706"
	CodeBlocked          = "CST-0707"
)

// Diagnostic statuses for Result.Status.
const (
	StatusPending = "Pending"
	StatusError   = "Error"
)

// BOMLine is one component of a manufactured item. ScrapPct is in percent.
// A Phantom line's child is exploded in place (BR-39.3): its own-level costs
// and routing hours join the parent's own level. MakeToOrder marks the
// make-to-order sub-assemblies that the MakeToOrder mode explodes.
type BOMLine struct {
	ChildID     string          `json:"childId"`
	Qty         decimal.Decimal `json:"qty"`
	ScrapPct    decimal.Decimal `json:"scrapPct"`
	Phantom     bool            `json:"phantom,omitempty"`
	MakeToOrder bool            `json:"makeToOrder,omitempty"`
}

// Operation is one routing step. Setup is spread over the costing lot size
// (BR-39.5); run time is charged per unit. Component defaults to DL. ScrapPct
// is the operation's scrap in percent; it compounds backward through the route
// (BR-39.6).
type Operation struct {
	Component  string          `json:"component,omitempty"`
	SetupHours decimal.Decimal `json:"setupHours"`
	RunHours   decimal.Decimal `json:"runHours"`
	Rate       decimal.Decimal `json:"rate"`
	LotSize    decimal.Decimal `json:"lotSize"`
	ScrapPct   decimal.Decimal `json:"scrapPct,omitempty"`
}

// NodeType is a costing sheet node kind (FR-39.2).
type NodeType string

const (
	NodeSurcharge NodeType = "Surcharge"
	NodeRate      NodeType = "Rate"
)

// SheetNode is an indirect cost node evaluated after the direct costs of its
// level. A surcharge takes Percentage of the basis components; a rate takes
// Rate per routing hour (RateUnit "Hour": run time plus setup spread over the
// costing lot, per unit) or per unit ("Unit", the default).
type SheetNode struct {
	ID            string          `json:"id"`
	Type          NodeType        `json:"type"`
	Component     string          `json:"component"`
	Basis         []string        `json:"basis,omitempty"`
	Percentage    decimal.Decimal `json:"percentage"`
	Rate          decimal.Decimal `json:"rate"`
	RateUnit      string          `json:"rateUnit,omitempty"`
	IncludeRolled bool            `json:"includeRolled"`
}

// Item is the costing input for one item at one site and version.
type Item struct {
	ID          string           `json:"id"`
	Kind        ItemKind         `json:"kind"`
	Price       *decimal.Decimal `json:"price,omitempty"`
	ActivePrice *decimal.Decimal `json:"activePrice,omitempty"`
	ActiveSplit Split            `json:"activeSplit,omitempty"`
	CostingLot  decimal.Decimal  `json:"costingLot"`
	BOM         []BOMLine        `json:"bom,omitempty"`
	Operations  []Operation      `json:"operations,omitempty"`
	Sheet       []SheetNode      `json:"sheet,omitempty"`
}

// Catalog is the set of items being rolled, keyed by item id.
type Catalog map[string]Item

// RollOptions controls one roll. Zero values take the defaults.
type RollOptions struct {
	Mode             ExplosionMode `json:"mode"`
	MaxLevels        int           `json:"maxLevels"`
	FallbackToActive bool          `json:"fallbackToActive"`
	Workers          int           `json:"workers"`
}

func (o RollOptions) normalized() RollOptions {
	if o.Mode == "" {
		o.Mode = ModeMultilevel
	}
	if o.MaxLevels == 0 {
		o.MaxLevels = DefaultMaxLevels
	}
	if o.Workers == 0 {
		o.Workers = DefaultWorkers
	}
	return o
}

// Validate rejects option values outside the parameter ranges.
func (o RollOptions) Validate() error {
	n := o.normalized()
	switch n.Mode {
	case ModeMultilevel, ModeSingleLevel, ModeMakeToOrder, ModeByLineType:
	default:
		return fmt.Errorf("mode must be Multilevel, SingleLevel, MakeToOrder or ByLineType")
	}
	if n.MaxLevels < 1 || n.MaxLevels > maxLevelsCeiling {
		return fmt.Errorf("maxLevels must be 1 to %d", maxLevelsCeiling)
	}
	if n.Workers < 1 || n.Workers > maxWorkers {
		return fmt.Errorf("workers must be 1 to %d", maxWorkers)
	}
	return nil
}

// Diagnostic is one explained problem or fallback. Severity is error or warning.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Ref      string `json:"ref,omitempty"`
}

// Result is the rolled standard of one item per unit. Own holds the costs that
// arise at this level (purchases, operations, sheet nodes, phantoms exploded in
// place); Rolled holds the split carried up from manufactured children
// (FR-39.4 step 4).
type Result struct {
	ItemID      string          `json:"itemId"`
	Status      string          `json:"status"`
	Level       int             `json:"level"`
	Own         Split           `json:"own"`
	Rolled      Split           `json:"rolled"`
	Unit        decimal.Decimal `json:"unitCost"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`

	// direct and hours are the own-level costs before the costing sheet and
	// the routing hours per unit. A parent exploding this item as a phantom
	// takes them into its own level and applies its own sheet (BR-39.3).
	direct Split
	hours  decimal.Decimal
}

// Combined returns Own plus Rolled, rounded to the stored unit scale.
func (r Result) Combined() Split {
	out := r.Own.Clone()
	for c, v := range r.Rolled {
		out.Add(c, v)
	}
	return out.Rounded(UnitScale)
}

func errDiag(code, msg, ref string) Diagnostic {
	return Diagnostic{Code: code, Severity: "error", Message: msg, Ref: ref}
}

func warnDiag(code, msg, ref string) Diagnostic {
	return Diagnostic{Code: code, Severity: "warning", Message: msg, Ref: ref}
}

// planState tracks the depth-first walk used to find levels and cycles.
type planState uint8

const (
	unvisited planState = iota
	visiting
	visited
)

type planNode struct {
	state planState
	level int
	ok    bool
	diags []Diagnostic
}

// planner assigns each item its roll level (leaves are 0; a parent is one
// above its deepest child) and marks items that cannot roll: members of a
// cycle, items over the depth limit, and items with a blocked component.
type planner struct {
	cat   Catalog
	max   int
	nodes map[string]*planNode
	stack []string
}

func (p *planner) node(id string) *planNode {
	n, ok := p.nodes[id]
	if !ok {
		n = &planNode{}
		p.nodes[id] = n
	}
	return n
}

func (p *planner) cyclePath(id string) string {
	start := 0
	for i, s := range p.stack {
		if s == id {
			start = i
			break
		}
	}
	path := append(append([]string{}, p.stack[start:]...), id)
	return strings.Join(path, " -> ")
}

// visit returns the level of id and whether it can roll.
func (p *planner) visit(id string) (int, bool) {
	n := p.node(id)
	switch n.state {
	case visited:
		return n.level, n.ok
	case visiting:
		return 0, false
	}
	n.state = visiting
	p.stack = append(p.stack, id)
	item := p.cat[id]
	level, ok := 0, true
	if item.Kind == KindManufactured {
		for _, line := range item.BOM {
			if _, exists := p.cat[line.ChildID]; !exists {
				n.diags = append(n.diags, errDiag(CodeMissingComponent, fmt.Sprintf("component %s is not in the catalog", line.ChildID), id))
				ok = false
				continue
			}
			if p.node(line.ChildID).state == visiting {
				n.diags = append(n.diags, errDiag(CodeCycle, "cycle detected: "+p.cyclePath(line.ChildID), id))
				ok = false
				continue
			}
			childLevel, childOK := p.visit(line.ChildID)
			if !childOK {
				n.diags = append(n.diags, errDiag(CodeBlocked, "component "+line.ChildID+" is in error", id))
				ok = false
			}
			if childLevel+1 > level {
				level = childLevel + 1
			}
		}
	}
	p.stack = p.stack[:len(p.stack)-1]
	if level > p.max {
		n.diags = append(n.diags, errDiag(CodeDepth, fmt.Sprintf("BOM depth %d exceeds limit %d", level, p.max), id))
		ok = false
	}
	n.state, n.level, n.ok = visited, level, ok
	return level, ok
}

// planRoll plans every item in cat, in sorted id order for determinism.
func planRoll(cat Catalog, maxLevels int) map[string]*planNode {
	p := &planner{cat: cat, max: maxLevels, nodes: make(map[string]*planNode, len(cat))}
	for _, id := range sortedKeys(cat) {
		p.visit(id)
	}
	return p.nodes
}

// purchasePrice resolves a purchased item's unit price (BR-39.4).
func purchasePrice(it Item, fallback bool) (decimal.Decimal, *Diagnostic) {
	if it.Price != nil {
		return *it.Price, nil
	}
	if fallback && it.ActivePrice != nil {
		d := warnDiag(CodeFallback, "fallback to the active standard price for "+it.ID, it.ID)
		return *it.ActivePrice, &d
	}
	d := errDiag(CodeNoPrice, "no price for "+it.ID+" in this version", it.ID)
	return decimal.Zero, &d
}

// explodes reports whether a BOM line takes the child's result from this run
// rather than the child's active standard (FR-39.4 step 3). A phantom
// manufactured child is always exploded (BR-39.3). A purchased child has no
// structure to explode, so rolling it means taking its price in this version
// (with fallback, BR-39.4); only SingleLevel takes it at its active standard.
func explodes(mode ExplosionMode, line BOMLine, child Item) bool {
	if line.Phantom && child.Kind == KindManufactured {
		return true
	}
	switch mode {
	case ModeMultilevel:
		return true
	case ModeMakeToOrder:
		return child.Kind == KindPurchased || line.MakeToOrder
	case ModeByLineType:
		return child.Kind == KindPurchased
	}
	return false
}

// activeStandard is the split a child contributes when it is not exploded: its
// active split, or for a purchased child its active price as material. A child
// with neither fails the parent with CST-0702 (BR-39.4).
func activeStandard(child Item, parentID string) (Split, *Diagnostic) {
	if child.ActiveSplit != nil {
		return child.ActiveSplit, nil
	}
	if child.Kind == KindPurchased && child.ActivePrice != nil {
		return Split{CompDM: *child.ActivePrice}, nil
	}
	d := errDiag(CodeNoPrice, "no active standard for "+child.ID, parentID)
	return nil, &d
}

// scrapFactors returns, per operation, the quantity processed there for one
// unit of finished output (BR-39.6): 1 + scrap% of that operation times the
// factors of every later operation, so scrap compounds backward.
func scrapFactors(ops []Operation) []decimal.Decimal {
	out := make([]decimal.Decimal, len(ops))
	f := decimal.NewFromInt(1)
	for k := len(ops) - 1; k >= 0; k-- {
		f = f.Mul(decimal.NewFromInt(100).Add(ops[k].ScrapPct).Div(decimal.NewFromInt(100)))
		out[k] = f
	}
	return out
}

// rollItem computes one item from the already-rolled results of its children.
// It reads done but never writes it, so items of one level can run in parallel.
func rollItem(cat Catalog, it Item, done map[string]Result, opts RollOptions) Result {
	res := Result{ItemID: it.ID, Status: StatusPending, Own: Split{}, Rolled: Split{}}
	fail := func(d Diagnostic) Result {
		res.Status = StatusError
		res.Diagnostics = append(res.Diagnostics, d)
		return res
	}
	if it.Kind == KindPurchased {
		price, diag := purchasePrice(it, opts.FallbackToActive)
		if diag != nil {
			if diag.Severity == "error" {
				return fail(*diag)
			}
			res.Diagnostics = append(res.Diagnostics, *diag)
		}
		res.Own.Add(CompDM, price)
		return finish(res)
	}

	hours := decimal.Zero
	// Route scrap compounds backward to the start of the route (BR-39.6), so
	// the material issued before the first operation is scaled by its factor.
	factors := scrapFactors(it.Operations)
	routeFactor := decimal.NewFromInt(1)
	if len(factors) > 0 {
		routeFactor = factors[0]
	}
	for _, line := range it.BOM {
		child, known := cat[line.ChildID]
		if !known {
			return fail(errDiag(CodeMissingComponent, "component "+line.ChildID+" is not in the catalog", it.ID))
		}
		qty := line.Qty.Mul(decimal.NewFromInt(100).Add(line.ScrapPct).Div(decimal.NewFromInt(100))).Mul(routeFactor)
		if !explodes(opts.Mode, line, child) {
			split, diag := activeStandard(child, it.ID)
			if diag != nil {
				return fail(*diag)
			}
			if child.Kind == KindPurchased {
				res.Own.AddScaled(split, qty)
			} else {
				res.Rolled.AddScaled(split, qty)
			}
			continue
		}
		r, ok := done[child.ID]
		if !ok || r.Status != StatusPending {
			return fail(errDiag(CodeBlocked, "component "+child.ID+" did not roll", it.ID))
		}
		switch {
		case child.Kind == KindPurchased:
			res.Own.AddScaled(r.Combined(), qty)
		case line.Phantom:
			// In place (BR-39.3): the phantom's direct costs and hours join this
			// level, and this level's sheet (not the phantom's) applies to them.
			res.Own.AddScaled(r.direct, qty)
			res.Rolled.AddScaled(r.Rolled, qty)
			hours = hours.Add(r.hours.Mul(qty))
		default:
			res.Rolled.AddScaled(r.Combined(), qty)
		}
	}

	lot := it.CostingLot
	for k, op := range it.Operations {
		comp := op.Component
		if comp == "" {
			comp = CompDL
		}
		l := op.LotSize
		if l.IsZero() {
			l = lot
		}
		if l.IsZero() {
			l = decimal.NewFromInt(1)
		}
		// Run time is charged on the quantity the operation processes, which
		// route scrap compounds backward (BR-39.6); setup is per lot and is not
		// scaled by scrap (BR-39.5). Routing hours follow the same rule.
		setupPerUnit := op.SetupHours.Mul(op.Rate).Div(l)
		res.Own.Add(comp, op.RunHours.Mul(op.Rate).Mul(factors[k]).Add(setupPerUnit))
		hours = hours.Add(op.RunHours.Mul(factors[k])).Add(op.SetupHours.Div(l))
	}

	res.direct = res.Own.Clone()
	for _, node := range it.Sheet {
		switch node.Type {
		case NodeSurcharge:
			basis := decimal.Zero
			for _, c := range node.Basis {
				basis = basis.Add(res.Own[c])
				if node.IncludeRolled {
					basis = basis.Add(res.Rolled[c])
				}
			}
			res.Own.Add(node.Component, basis.Mul(node.Percentage).Div(decimal.NewFromInt(100)))
		case NodeRate:
			driver := decimal.NewFromInt(1)
			if node.RateUnit == "Hour" {
				driver = hours
			}
			res.Own.Add(node.Component, node.Rate.Mul(driver))
		}
	}
	res.hours = hours
	return finish(res)
}

// finish rounds components to the unit scale and sets the unit cost to their sum,
// so the split always conserves the unit cost exactly (BR-39.1).
func finish(res Result) Result {
	res.Own = res.Own.Rounded(UnitScale)
	res.Rolled = res.Rolled.Rounded(UnitScale)
	res.Unit = res.Combined().Total()
	return res
}

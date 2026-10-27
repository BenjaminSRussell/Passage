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
	// CodeSheetNode extends the catalog for this slice: a costing sheet node of
	// an unknown type, or one that refers to a node that is not an earlier Basis
	// or Total node (see README).
	CodeSheetNode        = "CST-0708"
	CodeGroupCostUnknown = "CST-0761"
	// CodeNoTransferPrice extends the catalog: in the ProfitCenter view a
	// profit-center-sourced item has no transfer price and no cost-plus basis.
	CodeNoTransferPrice = "CST-0762"
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
	// NodeBasis reports the sum of its Basis components as a subtotal. It adds
	// nothing to the split; a later surcharge names it in BasisRef.
	NodeBasis NodeType = "Basis"
	// NodeTotal reports the sum of the subtotals of the earlier Basis or Total
	// nodes named in Refs. It adds nothing to the split.
	NodeTotal NodeType = "Total"
)

// SheetNode is a costing sheet node evaluated after the direct costs of its
// level, in sheet order. A surcharge takes Percentage of the basis components,
// or of the earlier Basis or Total node named in BasisRef; a rate takes Rate
// per routing hour (RateUnit "Hour": run time plus setup spread over the
// costing lot, per unit) or per unit ("Unit", the default). A Basis or Total
// node is a subtotal taken at its position, so it includes the surcharges and
// rates of the nodes before it when they charge one of its components.
type SheetNode struct {
	ID            string          `json:"id"`
	Type          NodeType        `json:"type"`
	Component     string          `json:"component,omitempty"`
	Basis         []string        `json:"basis,omitempty"`
	BasisRef      string          `json:"basisRef,omitempty"`
	Refs          []string        `json:"refs,omitempty"`
	Percentage    decimal.Decimal `json:"percentage"`
	Rate          decimal.Decimal `json:"rate"`
	RateUnit      string          `json:"rateUnit,omitempty"`
	IncludeRolled bool            `json:"includeRolled"`
}

// Item is the costing input for one item at one site and version.
// Intercompany marks a purchased item bought from an intercompany partner
// (FR-39.17); LegalCost is the supplying entity's Legal-view unit cost, already
// converted at the receipt's rate, which the Group view rolls instead of the
// intercompany price (BR-39.15).
//
// ProfitCenterSource marks a purchased item supplied by another profit center
// of the same legal entity. The ProfitCenter view prices it at TransferPrice
// (the transfer-price table entry for the profit-center pair) or, without one,
// at the supplying profit center's cost (LegalCost) plus CostPlusPct percent
// (FR-39.17). The Legal and Group views ignore these fields.
type Item struct {
	ID           string           `json:"id"`
	Kind         ItemKind         `json:"kind"`
	Price        *decimal.Decimal `json:"price,omitempty"`
	ActivePrice  *decimal.Decimal `json:"activePrice,omitempty"`
	ActiveSplit  Split            `json:"activeSplit,omitempty"`
	CostingLot   decimal.Decimal  `json:"costingLot"`
	BOM          []BOMLine        `json:"bom,omitempty"`
	Operations   []Operation      `json:"operations,omitempty"`
	Sheet        []SheetNode      `json:"sheet,omitempty"`
	Intercompany bool             `json:"intercompany,omitempty"`
	LegalCost    *decimal.Decimal `json:"legalCost,omitempty"`

	ProfitCenterSource bool             `json:"profitCenterSource,omitempty"`
	TransferPrice      *decimal.Decimal `json:"transferPrice,omitempty"`
	CostPlusPct        *decimal.Decimal `json:"costPlusPct,omitempty"`
}

// Catalog is the set of items being rolled, keyed by item id.
type Catalog map[string]Item

// Valuation views a roll can run under (FR-39.17, costing.valuationViews).
const (
	ViewLegal        = "Legal"
	ViewGroup        = "Group"
	ViewProfitCenter = "ProfitCenter"
)

// RollOptions controls one roll. Zero values take the defaults.
type RollOptions struct {
	Mode             ExplosionMode `json:"mode"`
	MaxLevels        int           `json:"maxLevels"`
	FallbackToActive bool          `json:"fallbackToActive"`
	Workers          int           `json:"workers"`
	View             string        `json:"view,omitempty"`
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
	if o.View == "" {
		o.View = ViewLegal
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
	switch n.View {
	case ViewLegal, ViewGroup, ViewProfitCenter:
	default:
		return fmt.Errorf("view must be Legal, Group or ProfitCenter")
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
// (FR-39.4 step 4). Subtotals holds the value of each Basis and Total sheet
// node by node id, per unit.
type Result struct {
	ItemID      string                     `json:"itemId"`
	Status      string                     `json:"status"`
	Level       int                        `json:"level"`
	Own         Split                      `json:"own"`
	Rolled      Split                      `json:"rolled"`
	Unit        decimal.Decimal            `json:"unitCost"`
	Subtotals   map[string]decimal.Decimal `json:"subtotals,omitempty"`
	Diagnostics []Diagnostic               `json:"diagnostics,omitempty"`

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

// viewCost returns the cost a valuation view puts on a purchased item in place
// of its price (FR-39.17). ok is false when the view does not change the item.
// diag is a CST-0761 warning (Group view, intercompany item without a supplier
// Legal-view cost: the intercompany price applies, BR-39.15) or a CST-0762
// error (ProfitCenter view, profit-center-sourced item with neither a transfer
// price nor a cost-plus basis). diag.Ref is the item; activeStandard points
// it at the parent instead.
func viewCost(it Item, opts RollOptions) (cost decimal.Decimal, ok bool, diag *Diagnostic) {
	switch {
	case opts.View == ViewGroup && it.Intercompany:
		if it.LegalCost != nil {
			return *it.LegalCost, true, nil
		}
		d := warnDiag(CodeGroupCostUnknown, "group cost unknown for intercompany item "+it.ID+"; intercompany price used", it.ID)
		return decimal.Zero, false, &d
	case opts.View == ViewProfitCenter && it.ProfitCenterSource:
		return profitCenterCost(it)
	}
	return decimal.Zero, false, nil
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
//
// In the Group view an intercompany purchased child takes its supplier's
// Legal-view cost; without one its active price applies and CST-0761 warns
// (BR-39.15). In the ProfitCenter view a profit-center-sourced child takes its
// transfer price, or fails the parent with CST-0762 (see viewCost).
func activeStandard(child Item, parentID string, opts RollOptions) (Split, []Diagnostic, *Diagnostic) {
	var warns []Diagnostic
	if child.Kind == KindPurchased {
		cost, ok, diag := viewCost(child, opts)
		if ok {
			return Split{CompDM: cost}, nil, nil
		}
		if diag != nil {
			diag.Ref = parentID
			if diag.Severity == "error" {
				return nil, nil, diag
			}
			warns = append(warns, *diag)
		}
	}
	if child.ActiveSplit != nil {
		return child.ActiveSplit, warns, nil
	}
	if child.Kind == KindPurchased && child.ActivePrice != nil {
		return Split{CompDM: *child.ActivePrice}, warns, nil
	}
	d := errDiag(CodeNoPrice, "no active standard for "+child.ID, parentID)
	return nil, nil, &d
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
		cost, ok, diag := viewCost(it, opts)
		switch {
		case ok:
			res.Own.Add(CompDM, cost)
			return finish(res)
		case diag != nil && diag.Severity == "error":
			return fail(*diag)
		case diag != nil:
			res.Diagnostics = append(res.Diagnostics, *diag)
		}
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
			split, warns, diag := activeStandard(child, it.ID, opts)
			if diag != nil {
				return fail(*diag)
			}
			res.Diagnostics = append(res.Diagnostics, warns...)
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
	subtotals, diag := applySheet(&res, it, hours)
	if diag != nil {
		return fail(*diag)
	}
	if len(subtotals) > 0 {
		res.Subtotals = make(map[string]decimal.Decimal, len(subtotals))
		for id, v := range subtotals {
			res.Subtotals[id] = v.Round(UnitScale)
		}
	}
	res.hours = hours
	return finish(res)
}

// basisOf sums the named components of the result so far; rolled components
// count only when includeRolled is set (FR-39.2 include-rolled-subassemblies).
func basisOf(res *Result, components []string, includeRolled bool) decimal.Decimal {
	basis := decimal.Zero
	for _, c := range components {
		basis = basis.Add(res.Own[c])
		if includeRolled {
			basis = basis.Add(res.Rolled[c])
		}
	}
	return basis
}

// applySheet evaluates the costing sheet in order (FR-39.2) and returns the
// Basis and Total subtotals. A reference to a node that is not an earlier
// Basis or Total node, or an unknown node type, is CST-0708.
func applySheet(res *Result, it Item, hours decimal.Decimal) (map[string]decimal.Decimal, *Diagnostic) {
	subtotals := map[string]decimal.Decimal{}
	sheetErr := func(msg string) (map[string]decimal.Decimal, *Diagnostic) {
		d := errDiag(CodeSheetNode, msg, it.ID)
		return nil, &d
	}
	for _, node := range it.Sheet {
		switch node.Type {
		case NodeBasis:
			subtotals[node.ID] = basisOf(res, node.Basis, node.IncludeRolled)
		case NodeTotal:
			sum := decimal.Zero
			for _, ref := range node.Refs {
				v, ok := subtotals[ref]
				if !ok {
					return sheetErr("total node " + node.ID + " refers to " + ref + ", which is not an earlier Basis or Total node")
				}
				sum = sum.Add(v)
			}
			subtotals[node.ID] = sum
		case NodeSurcharge:
			basis := basisOf(res, node.Basis, node.IncludeRolled)
			if node.BasisRef != "" {
				v, ok := subtotals[node.BasisRef]
				if !ok {
					return sheetErr("surcharge node " + node.ID + " refers to " + node.BasisRef + ", which is not an earlier Basis or Total node")
				}
				basis = v
			}
			res.Own.Add(node.Component, basis.Mul(node.Percentage).Div(decimal.NewFromInt(100)))
		case NodeRate:
			driver := decimal.NewFromInt(1)
			if node.RateUnit == "Hour" {
				driver = hours
			}
			res.Own.Add(node.Component, node.Rate.Mul(driver))
		default:
			return sheetErr("sheet node " + node.ID + " has unknown type " + string(node.Type))
		}
	}
	return subtotals, nil
}

// finish rounds components to the unit scale and sets the unit cost to their sum,
// so the split always conserves the unit cost exactly (BR-39.1).
func finish(res Result) Result {
	res.Own = res.Own.Rounded(UnitScale)
	res.Rolled = res.Rolled.Rounded(UnitScale)
	res.Unit = res.Combined().Total()
	return res
}

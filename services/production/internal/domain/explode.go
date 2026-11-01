package domain

import (
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// BOMComponent is one line of the BOM version snapshot taken at order creation.
// Phantom lines have no stock of their own: they are flattened into their children.
type BOMComponent struct {
	LineNo     string          `json:"lineNo"`
	ItemID     string          `json:"itemId"`
	QtyPer     decimal.Decimal `json:"qtyPer"`
	SeriesSize decimal.Decimal `json:"seriesSize"`
	ScrapPct   decimal.Decimal `json:"scrapPct"`
	RoundUp    bool            `json:"roundUp"`
	UnitCost   decimal.Decimal `json:"unitCost"`
	// Flushing is the line's flushing principle (FR-21.5); empty means manual,
	// or, under a phantom, the phantom's own principle.
	Flushing Flushing       `json:"flushing,omitempty"`
	Phantom  bool           `json:"phantom"`
	Children []BOMComponent `json:"children,omitempty"`
}

// OrderLine is an exploded component line of a production order (FR-21.1).
// QtyPer is the effective quantity per unit of the order's product.
// QtyConsumed is what posted journals have issued so far (FR-21.5).
type OrderLine struct {
	LineNo      string          `json:"lineNo"`
	ItemID      string          `json:"itemId"`
	QtyPer      decimal.Decimal `json:"qtyPer"`
	SeriesSize  decimal.Decimal `json:"seriesSize"`
	ScrapPct    decimal.Decimal `json:"scrapPct"`
	RoundUp     bool            `json:"roundUp"`
	QtyRequired decimal.Decimal `json:"qtyRequired"`
	UnitCost    decimal.Decimal `json:"unitCost"`
	Flushing    Flushing        `json:"flushing"`
	QtyConsumed decimal.Decimal `json:"qtyConsumed"`
}

// Explode flattens a BOM snapshot for orderQty units (FR-21.1). Phantom lines
// are exploded in place; their children keep the parent's line number with a
// dotted suffix (30 → 30.1, 30.2).
func Explode(bom []BOMComponent, orderQty decimal.Decimal) ([]OrderLine, error) {
	if !orderQty.IsPositive() {
		return nil, &Error{Code: CodeInvalidInput, Detail: "order quantity must be positive"}
	}
	out := []OrderLine{}
	if err := explode(&out, bom, "", decimal.NewFromInt(1), orderQty, FlushManual); err != nil {
		return nil, err
	}
	return out, nil
}

func explode(out *[]OrderLine, comps []BOMComponent, prefix string, mult, orderQty decimal.Decimal, inherited Flushing) error {
	for i, c := range comps {
		no := c.LineNo
		if prefix != "" {
			no = prefix + "." + strconv.Itoa(i+1)
		}
		if c.QtyPer.IsNegative() || c.ScrapPct.IsNegative() || !c.ScrapPct.LessThan(hundred) {
			return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("line %s: quantity must not be negative and scrap must be below 100", no)}
		}
		if c.Flushing != "" && !c.Flushing.Valid() {
			return &Error{Code: CodeInvalidInput, Detail: fmt.Sprintf("line %s: flushing must be manual, start, finish or available-at-location", no)}
		}
		flushing := c.Flushing
		if flushing == "" {
			flushing = inherited
		}
		qtyPer := mult.Mul(c.QtyPer)
		if c.Phantom {
			// A phantom's series size is ignored: its quantity scales the children directly.
			if err := explode(out, c.Children, no, qtyPer, orderQty, flushing); err != nil {
				return err
			}
			continue
		}
		series := c.SeriesSize
		if series.IsZero() {
			series = one
		}
		req, err := RequiredQty(qtyPer, orderQty, series, c.ScrapPct, c.RoundUp)
		if err != nil {
			return err
		}
		*out = append(*out, OrderLine{LineNo: no, ItemID: c.ItemID, QtyPer: qtyPer, SeriesSize: series, ScrapPct: c.ScrapPct,
			RoundUp: c.RoundUp, QtyRequired: req, UnitCost: c.UnitCost, Flushing: flushing, QtyConsumed: decimal.Zero})
	}
	return nil
}

// Requantify recomputes the required quantity of already exploded lines for a
// new order quantity (a quantity change before Estimated is reported). The
// effective quantity per unit, series, scrap and round-up flag of each line are kept.
func Requantify(lines []OrderLine, orderQty decimal.Decimal) ([]OrderLine, error) {
	if !orderQty.IsPositive() {
		return nil, &Error{Code: CodeInvalidInput, Detail: "order quantity must be positive"}
	}
	out := make([]OrderLine, len(lines))
	for i, l := range lines {
		req, err := RequiredQty(l.QtyPer, orderQty, l.SeriesSize, l.ScrapPct, l.RoundUp)
		if err != nil {
			return nil, err
		}
		l.QtyRequired = req
		out[i] = l
	}
	return out, nil
}

// Operation is a route operation of the order, planned on infinite capacity (wave 21).
// SubcontractPrice is the vendor price per unit of the operation's process quantity.
// The feedback fields (hours, good and error quantity, status) are booked by
// route-card journals (FR-21.6) and read-only on create. PORequestedAt is when
// release asked purchasing for the vendor PO of a subcontract operation
// (FR-21.10); it is read-only too, as is POCancelledAt (when that request was withdrawn).
type Operation struct {
	OperNumber       int             `json:"operNumber"`
	ResourceID       string          `json:"resourceId"`
	SetupH           decimal.Decimal `json:"setupTimeH"`
	RunHPer          decimal.Decimal `json:"runTimeHPer"`
	ScrapPct         decimal.Decimal `json:"scrapPct"`
	LaborRate        decimal.Decimal `json:"laborRate"`
	OverheadRate     decimal.Decimal `json:"overheadRate"`
	Subcontract      bool            `json:"subcontract"`
	SubcontractPrice decimal.Decimal `json:"subcontractPrice"`
	ProcessQty       decimal.Decimal `json:"processQty"`
	HoursReported    decimal.Decimal `json:"hoursReported"`
	QtyGood          decimal.Decimal `json:"qtyGood"`
	QtyError         decimal.Decimal `json:"qtyError"`
	Status           OperStatus      `json:"status,omitempty"`
	PORequestedAt    *time.Time      `json:"poRequestedAt,omitempty"`
	POCancelledAt    *time.Time      `json:"poCancelledAt,omitempty"`
	// QueueBeforeH, QueueAfterH and TransitH are the waiting and transport hours the
	// schedule places around the operation's setup and run. omitzero keeps the request
	// hash and the JSON of a route without them as they were.
	QueueBeforeH decimal.Decimal `json:"queueBeforeH,omitzero"`
	QueueAfterH  decimal.Decimal `json:"queueAfterH,omitzero"`
	TransitH     decimal.Decimal `json:"transitH,omitzero"`
	// PlannedStart and PlannedEnd are the window `:schedule` gave the operation,
	// queue and transit included; absent until the order is scheduled.
	PlannedStart *time.Time `json:"plannedStart,omitempty"`
	PlannedEnd   *time.Time `json:"plannedEnd,omitempty"`
}

// OperStatus is the feedback status of an operation: open until the first
// route card, started, and finished when a route card closes it (FR-21.6).
type OperStatus string

const (
	OperOpen     OperStatus = "open"
	OperStarted  OperStatus = "started"
	OperFinished OperStatus = "finished"
)

// PlanOperations sets each operation's process quantity by BR-21.8 and returns
// the operations in route order. Operation numbers must strictly increase.
func PlanOperations(ops []Operation, orderQty decimal.Decimal) ([]Operation, error) {
	scraps := make([]decimal.Decimal, len(ops))
	for i, op := range ops {
		if i > 0 && op.OperNumber <= ops[i-1].OperNumber {
			return nil, &Error{Code: CodeInvalidInput, Detail: "operation numbers must strictly increase"}
		}
		scraps[i] = op.ScrapPct
	}
	qtys, err := StartQuantities(orderQty, scraps)
	if err != nil {
		return nil, err
	}
	out := make([]Operation, len(ops))
	copy(out, ops)
	for i := range out {
		out[i].ProcessQty = qtys[i]
	}
	return out, nil
}

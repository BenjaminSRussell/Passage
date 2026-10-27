package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Goods-in-transit order statuses (FR-34.12). A PENDING order moves ownership
// of its receipt lines to the in-transit warehouse when it ships at the
// transfer event, and is IN_TRANSIT from then on. Receipt at the destination
// warehouse comes with the receipt integration (NOT-IMPLEMENTED.md).
const (
	GITPending   = "PENDING"
	GITInTransit = "IN_TRANSIT"
)

// Goods-in-transit codes (slice-local; the catalog leaves TMS-0111 to TMS-0120
// unassigned). TMS-0119: the order or its voyage is in the wrong state, or a
// receipt line is already on another order. TMS-0120: the Incoterm transfers
// ownership at destination receipt, so no order is opened (BR-34.11).
const (
	CodeGITState         = "TMS-0119"
	CodeNoGoodsInTransit = "TMS-0120"
)

// InTransitWarehousePrefix builds the default in_transit_warehouse name,
// TRANSIT-{site} (§6.34.13).
const InTransitWarehousePrefix = "TRANSIT-"

// FieldErrors is a request that fails validation against stored data, such as
// a receipt line that is not on the voyage. The API returns it as COM-0001.
type FieldErrors map[string]string

func (f FieldErrors) Error() string { return fmt.Sprintf("transport: %d field errors", len(f)) }

// GITCost is one cost type's estimated share of a receipt line.
type GITCost struct {
	CostType string          `json:"costType"`
	Amount   decimal.Decimal `json:"amount"`
}

// GITLine is one receipt line moved into transit: its goods value and quantity,
// and its estimated landed cost per cost type and in total.
type GITLine struct {
	Line      int             `json:"line"`
	Value     decimal.Decimal `json:"value"`
	Quantity  decimal.Decimal `json:"quantity"`
	Costs     []GITCost       `json:"costs"`
	Estimated decimal.Decimal `json:"estimated"`
}

// GoodsInTransit is the order that moves receipt lines of a voyage into the
// in-transit warehouse at the Incoterm transfer event (FR-34.12, BR-34.11).
// Code is TMS-0121 once ownership has moved.
type GoodsInTransit struct {
	ID                 string          `json:"id"`
	VoyageID           string          `json:"voyageId"`
	VoyageReference    string          `json:"voyageReference"`
	SiteID             string          `json:"siteId"`
	Incoterm           string          `json:"incoterm"`
	TransferEvent      string          `json:"transferEvent"`
	InTransitWarehouse string          `json:"inTransitWarehouse"`
	Currency           string          `json:"currency"`
	Status             string          `json:"status"`
	Lines              []GITLine       `json:"lines"`
	GoodsValue         decimal.Decimal `json:"goodsValue"`
	EstimatedTotal     decimal.Decimal `json:"estimatedTotal"`
	Version            int             `json:"version"`
	CreatedAt          time.Time       `json:"createdAt"`
	ShippedAt          *time.Time      `json:"shippedAt,omitempty"`
	Code               string          `json:"code,omitempty"`
}

// ValidateReceiptLineList checks the receipt line numbers of a goods-in-transit
// request: each 1 or more and listed once. An empty list means every line.
func ValidateReceiptLineList(lines []int) map[string]string {
	fields := map[string]string{}
	seen := map[int]bool{}
	for _, l := range lines {
		switch {
		case l < 1:
			fields["receiptLines"] = "receipt line numbers are 1 or more"
		case seen[l]:
			fields["receiptLines"] = "list each receipt line once (line " + strconv.Itoa(l) + " is repeated)"
		}
		seen[l] = true
	}
	if len(lines) > maxVoyageReceipts {
		fields["receiptLines"] = "at most 3000 receipt lines"
	}
	return fields
}

// NewGoodsInTransit opens a PENDING order for receipt lines of an ESTIMATED
// voyage under an origin Incoterm (BR-34.11). An empty line list takes every
// receipt line of the voyage. The estimate per line is the share it receives
// when the voyage's cost lines are allocated (BR-34.6), so the order carries
// exactly the landed cost the voyage would post for those lines.
func NewGoodsInTransit(v Voyage, incoterm, siteID string, lines []int) (GoodsInTransit, error) {
	rule, err := TransferFor(incoterm)
	if err != nil {
		return GoodsInTransit{}, err
	}
	if !rule.GoodsInTransit {
		return GoodsInTransit{}, &Error{Code: CodeNoGoodsInTransit,
			Detail: rule.Incoterm + " transfers ownership at destination receipt; no goods-in-transit order is opened"}
	}
	if v.Status != VoyageEstimated {
		return GoodsInTransit{}, &Error{Code: CodeGITState,
			Detail: "voyage " + v.Reference + " is " + v.Status + "; goods in transit is opened on an ESTIMATED voyage"}
	}
	est, aerr := v.Allocate()
	if aerr != nil {
		return GoodsInTransit{}, aerr
	}
	picked := lines
	if len(picked) == 0 {
		for _, r := range v.ReceiptLines {
			picked = append(picked, r.Line)
		}
	}
	index := map[int]int{}
	for i, r := range v.ReceiptLines {
		index[r.Line] = i
	}
	g := GoodsInTransit{
		VoyageID:           v.ID,
		VoyageReference:    v.Reference,
		SiteID:             siteID,
		Incoterm:           rule.Incoterm,
		TransferEvent:      rule.Event,
		InTransitWarehouse: InTransitWarehousePrefix + siteID,
		Currency:           v.Currency,
		Status:             GITPending,
		Lines:              make([]GITLine, 0, len(picked)),
		GoodsValue:         decimal.Zero,
		EstimatedTotal:     decimal.Zero,
	}
	var missing []string
	for _, line := range picked {
		i, ok := index[line]
		if !ok {
			missing = append(missing, strconv.Itoa(line))
			continue
		}
		r := v.ReceiptLines[i]
		gl := GITLine{Line: line, Value: r.Value, Quantity: r.Quantity, Costs: make([]GITCost, 0, len(est.CostAllocations)),
			Estimated: est.Allocations[i].Amount}
		for _, c := range est.CostAllocations {
			gl.Costs = append(gl.Costs, GITCost{CostType: c.CostType, Amount: c.Shares[i].Amount})
		}
		g.Lines = append(g.Lines, gl)
		g.GoodsValue = g.GoodsValue.Add(r.Value)
		g.EstimatedTotal = g.EstimatedTotal.Add(gl.Estimated)
	}
	if len(missing) > 0 {
		return GoodsInTransit{}, FieldErrors{"receiptLines": "not on voyage " + v.Reference + ": line " + strings.Join(missing, ", ")}
	}
	slices.SortFunc(g.Lines, func(a, b GITLine) int { return a.Line - b.Line })
	return g, nil
}

// Ship moves a PENDING order to IN_TRANSIT at the transfer event: ownership of
// its lines is now in the in-transit warehouse (TMS-0121).
func (g GoodsInTransit) Ship(at time.Time) (GoodsInTransit, error) {
	if g.Status != GITPending {
		return g, &Error{Code: CodeGITState, Detail: "goods-in-transit order is already " + g.Status}
	}
	shipped := at.UTC()
	g.Status = GITInTransit
	g.ShippedAt = &shipped
	g.Code = CodeOwnershipMoved
	return g, nil
}

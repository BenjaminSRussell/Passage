package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Voyage statuses (PRD §6.34.4 LandedCostVoyage). A voyage is ESTIMATED until its
// cost lines are allocated to the receipt lines, then POSTED. The FR-34.12
// tracking stages (IN_TRANSIT, ARRIVED, RECEIVED, COSTED) need goods-in-transit
// orders and receipts, which are not built yet (NOT-IMPLEMENTED.md).
const (
	VoyageEstimated = "ESTIMATED"
	VoyagePosted    = "POSTED"
)

// ContainerTypes are the shipping container types accepted on a voyage (FR-34.12).
var ContainerTypes = []string{"20GP", "40GP", "40HC", "45HC", "REEFER"}

// LegModes are the transport modes of a voyage leg.
var LegModes = []string{"OCEAN", "AIR", "ROAD", "RAIL"}

// Voyage size limits. 3,000 receipt lines is the PRD's capacity assumption (§6.34.19).
const (
	maxVoyageLegs       = 10
	maxVoyageContainers = 200
	maxVoyageFolios     = 100
	maxVoyageCostLines  = 50
	maxVoyageReceipts   = 3000
	maxVoyageText       = 40
	maxCostTypeLength   = 20
)

// Leg is one stretch of a voyage: origin port to destination port, or port to warehouse.
type Leg struct {
	Seq  int    `json:"seq"`
	From string `json:"from"`
	To   string `json:"to"`
	Mode string `json:"mode"`
}

// Container is a shipping container on a voyage. Number is an ISO 6346 number (BR-34.12).
type Container struct {
	Number string `json:"number"`
	Type   string `json:"type"`
	Seal   string `json:"seal,omitempty"`
}

// Folio groups containers per vendor or forwarder.
type Folio struct {
	FolioNo    string   `json:"folioNo"`
	Forwarder  string   `json:"forwarder"`
	Containers []string `json:"containers"`
}

// VoyageCostLine is one landed cost on the voyage, such as freight or duty, and
// how it is spread over the receipt lines (FR-34.9).
type VoyageCostLine struct {
	CostType         string          `json:"costType"`
	Amount           decimal.Decimal `json:"amount"`
	AllocationMethod string          `json:"allocationMethod"`
}

// VoyageReceiptLine is a purchase receipt line that absorbs landed cost. Each
// allocation method reads one of the bases; Value also decides the residual line.
type VoyageReceiptLine struct {
	Line     int             `json:"line"`
	Value    decimal.Decimal `json:"value"`
	Weight   decimal.Decimal `json:"weight"`
	Volume   decimal.Decimal `json:"volume"`
	Quantity decimal.Decimal `json:"quantity"`
}

// CostAllocation is one cost line spread over the receipt lines. Shares sum to
// Amount to the cent; Residual went to ResidualLine (TMS-0110).
type CostAllocation struct {
	CostType         string          `json:"costType"`
	AllocationMethod string          `json:"allocationMethod"`
	Amount           decimal.Decimal `json:"amount"`
	Shares           []Allocation    `json:"shares"`
	Residual         decimal.Decimal `json:"residual"`
	ResidualLine     int             `json:"residualLine"`
}

// Voyage groups legs, containers, folios, landed cost lines and the receipt
// lines that receive the cost. CostAllocations and Allocations (the total per
// receipt line, which costing consumes) are filled when the voyage is posted.
type Voyage struct {
	ID              string              `json:"id"`
	LegalEntityID   string              `json:"legalEntityId,omitempty"`
	Reference       string              `json:"reference"`
	Currency        string              `json:"currency"`
	Status          string              `json:"status"`
	Legs            []Leg               `json:"legs"`
	Containers      []Container         `json:"containers"`
	Folios          []Folio             `json:"folios"`
	CostLines       []VoyageCostLine    `json:"costLines"`
	ReceiptLines    []VoyageReceiptLine `json:"receiptLines"`
	CostAllocations []CostAllocation    `json:"costAllocations"`
	Allocations     []Allocation        `json:"allocations"`
	Version         int                 `json:"version"`
	CreatedAt       time.Time           `json:"createdAt"`
	PostedAt        *time.Time          `json:"postedAt,omitempty"`
}

// NormalizeVoyage trims and upper-cases the codes a client may send in any case,
// numbers the legs in the order given, and clears the server-owned fields.
func NormalizeVoyage(v Voyage) Voyage {
	v.ID, v.Status, v.Version, v.CreatedAt, v.PostedAt = "", "", 0, time.Time{}, nil
	v.CostAllocations, v.Allocations = nil, nil
	v.Reference = strings.TrimSpace(v.Reference)
	v.Currency = strings.ToUpper(strings.TrimSpace(v.Currency))
	for i := range v.Legs {
		v.Legs[i].Seq = i + 1
		v.Legs[i].From = strings.ToUpper(strings.TrimSpace(v.Legs[i].From))
		v.Legs[i].To = strings.ToUpper(strings.TrimSpace(v.Legs[i].To))
		v.Legs[i].Mode = strings.ToUpper(strings.TrimSpace(v.Legs[i].Mode))
	}
	for i := range v.Containers {
		v.Containers[i].Number = strings.ToUpper(strings.TrimSpace(v.Containers[i].Number))
		v.Containers[i].Type = strings.ToUpper(strings.TrimSpace(v.Containers[i].Type))
		v.Containers[i].Seal = strings.TrimSpace(v.Containers[i].Seal)
	}
	for i := range v.Folios {
		v.Folios[i].FolioNo = strings.TrimSpace(v.Folios[i].FolioNo)
		v.Folios[i].Forwarder = strings.TrimSpace(v.Folios[i].Forwarder)
		for j := range v.Folios[i].Containers {
			v.Folios[i].Containers[j] = strings.ToUpper(strings.TrimSpace(v.Folios[i].Containers[j]))
		}
	}
	for i := range v.CostLines {
		v.CostLines[i].CostType = strings.ToUpper(strings.TrimSpace(v.CostLines[i].CostType))
		v.CostLines[i].AllocationMethod = strings.ToUpper(strings.TrimSpace(v.CostLines[i].AllocationMethod))
	}
	return v
}

// ValidateVoyage reports structural field errors for a new voyage, keyed by
// JSON path. Container check digits are checked by ContainerErrors so that the
// API can answer with TMS-0124.
func ValidateVoyage(v Voyage) map[string]string {
	f := map[string]string{}
	if n := len(v.Reference); n == 0 || n > maxVoyageText {
		f["reference"] = "must be 1 to 40 characters, such as the vessel voyage number"
	}
	if !currencyRe.MatchString(v.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if len(v.Legs) == 0 || len(v.Legs) > maxVoyageLegs {
		f["legs"] = "between 1 and 10 legs are required"
	}
	for i, l := range v.Legs {
		if !textOK(l.From) || !textOK(l.To) {
			f[fmt.Sprintf("legs[%d]", i)] = "every leg needs an origin and a destination of up to 40 characters"
		} else if !slices.Contains(LegModes, l.Mode) {
			f[fmt.Sprintf("legs[%d]", i)] = "mode must be OCEAN, AIR, ROAD or RAIL"
		}
	}
	if len(v.Containers) > maxVoyageContainers {
		f["containers"] = "at most 200 containers"
	}
	containers := map[string]bool{}
	for i, c := range v.Containers {
		key := fmt.Sprintf("containers[%d]", i)
		switch {
		case containers[c.Number]:
			f[key] = "container numbers must be unique on a voyage"
		case !slices.Contains(ContainerTypes, c.Type):
			f[key] = "type must be one of 20GP, 40GP, 40HC, 45HC, REEFER"
		case len(c.Seal) > maxVoyageText:
			f[key] = "seal number must be at most 40 characters"
		}
		containers[c.Number] = true
	}
	if len(v.Folios) > maxVoyageFolios {
		f["folios"] = "at most 100 folios"
	}
	folios := map[string]bool{}
	for i, fo := range v.Folios {
		key := fmt.Sprintf("folios[%d]", i)
		switch {
		case !textOK(fo.FolioNo) || !textOK(fo.Forwarder):
			f[key] = "every folio needs a folio number and a vendor or forwarder of up to 40 characters"
		case folios[fo.FolioNo]:
			f[key] = "folio numbers must be unique on a voyage"
		}
		folios[fo.FolioNo] = true
		for _, c := range fo.Containers {
			if !containers[c] {
				f[key] = "folio refers to container " + c + ", which is not on the voyage"
			}
		}
	}
	if len(v.ReceiptLines) == 0 || len(v.ReceiptLines) > maxVoyageReceipts {
		f["receiptLines"] = "between 1 and 3000 receipt lines are required"
	}
	lines := map[int]bool{}
	for i, r := range v.ReceiptLines {
		key := fmt.Sprintf("receiptLines[%d]", i)
		switch {
		case r.Line < 1:
			f[key] = "line must be 1 or more"
		case lines[r.Line]:
			f[key] = "receipt line numbers must be unique"
		case r.Value.IsNegative() || r.Weight.IsNegative() || r.Volume.IsNegative() || r.Quantity.IsNegative():
			f[key] = "value, weight, volume and quantity must not be negative"
		}
		lines[r.Line] = true
	}
	if len(v.CostLines) == 0 || len(v.CostLines) > maxVoyageCostLines {
		f["costLines"] = "between 1 and 50 cost lines are required"
	}
	for i, c := range v.CostLines {
		key := fmt.Sprintf("costLines[%d]", i)
		switch {
		case c.CostType == "" || len(c.CostType) > maxCostTypeLength:
			f[key] = "cost type must be 1 to 20 characters, such as FREIGHT, DUTY, BROKERAGE or INSURANCE"
		case c.Amount.IsNegative() || !c.Amount.Equal(c.Amount.Round(2)):
			f[key] = "amount must be zero or more with at most two decimals"
		case !slices.Contains(AllocationMethods, c.AllocationMethod):
			f[key] = "allocation method must be WEIGHT, VOLUME, VALUE, QUANTITY or EQUAL"
		case len(v.ReceiptLines) > 0 && !basisTotal(c.AllocationMethod, v.ReceiptLines).IsPositive():
			f[key] = "the receipt lines' total " + strings.ToLower(c.AllocationMethod) + " must be above zero to allocate by it"
		}
	}
	return f
}

// ContainerErrors checks every container number's ISO 6346 check digit
// (BR-34.12) and returns the failures keyed by JSON path.
func ContainerErrors(v Voyage) map[string]string {
	f := map[string]string{}
	for i, c := range v.Containers {
		if err := ValidContainerNumber(c.Number); err != nil {
			f[fmt.Sprintf("containers[%d].number", i)] = "container number " + c.Number + " fails the ISO 6346 check digit"
		}
	}
	return f
}

// Allocate spreads every cost line over the receipt lines by its method
// (BR-34.6), keeps the per-cost-line shares, and sums them per receipt line.
// Each cost line's shares equal its amount to the cent, so the receipt-line
// totals equal the voyage cost total to the cent. The caller checks the status.
func (v Voyage) Allocate() (Voyage, *Error) {
	receipts := make([]ReceiptLine, len(v.ReceiptLines))
	totals := make([]decimal.Decimal, len(v.ReceiptLines))
	for i, r := range v.ReceiptLines {
		receipts[i] = ReceiptLine{Line: r.Line, Value: r.Value, Weight: r.Weight, Volume: r.Volume, Quantity: r.Quantity}
		totals[i] = decimal.Zero
	}
	out := v
	out.CostAllocations = make([]CostAllocation, 0, len(v.CostLines))
	for _, c := range v.CostLines {
		res, err := Allocate(c.Amount, c.AllocationMethod, receipts)
		if err != nil {
			return v, err
		}
		for i, s := range res.Shares {
			totals[i] = totals[i].Add(s.Amount)
		}
		out.CostAllocations = append(out.CostAllocations, CostAllocation{
			CostType: c.CostType, AllocationMethod: c.AllocationMethod, Amount: c.Amount,
			Shares: res.Shares, Residual: res.Residual, ResidualLine: res.ResidualLine,
		})
	}
	out.Allocations = make([]Allocation, len(receipts))
	for i, r := range receipts {
		out.Allocations[i] = Allocation{Line: r.Line, Amount: totals[i]}
	}
	out.Status = VoyagePosted
	return out, nil
}

// CostTotal is the sum of the voyage's cost lines.
func (v Voyage) CostTotal() decimal.Decimal {
	total := decimal.Zero
	for _, c := range v.CostLines {
		total = total.Add(c.Amount)
	}
	return total
}

func basisTotal(method string, lines []VoyageReceiptLine) decimal.Decimal {
	total := decimal.Zero
	for _, r := range lines {
		total = total.Add(basis(method, ReceiptLine{Value: r.Value, Weight: r.Weight, Volume: r.Volume, Quantity: r.Quantity}))
	}
	return total
}

func textOK(s string) bool { return s != "" && len(s) <= maxVoyageText }

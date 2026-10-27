package domain

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

// Punchout codes (BR-35.13 and §6.35.18).
const (
	CodePunchoutLine  = "EDI-0133" // a returned line is dropped
	CodePunchoutTotal = "EDI-0134" // the cart total does not match the line sum
)

// cartTotalTolerance is the BR-35.13 total difference that rejects a whole cart.
var cartTotalTolerance = decimal.RequireFromString("0.01")

// DefaultUoM maps common supplier and SAP units to UN/ECE Recommendation 20 codes.
// A supplier's own table overrides it key by key.
var DefaultUoM = map[string]string{
	"EA": "C62", "ST": "C62", "PC": "C62", "C62": "C62",
	"KG": "KGM", "KGM": "KGM", "LB": "LBR", "LBR": "LBR",
	"M": "MTR", "MTR": "MTR", "L": "LTR", "LTR": "LTR",
	"CA": "CS", "CS": "CS", "BOX": "BX", "BX": "BX", "PAL": "PF",
}

// SupplierPunchout is the supplier configuration that validation needs (§6.35.13).
type SupplierPunchout struct {
	Currencies      []string                   // currencies allowed for the supplier
	UoMMap          map[string]string          // supplier unit to UN/ECE code; overrides DefaultUoM
	TolerancePct    decimal.Decimal            // punchout_price_tolerance_pct, 0..20
	ReferencePrices map[string]decimal.Decimal // catalog price by supplier part id
}

// RawCartItem is one returned line before validation. Numbers stay as text so
// that malformed values become line issues instead of parse failures.
type RawCartItem struct {
	Line           int
	SupplierPartID string
	Description    string
	Unit           string
	Quantity       string
	Price          string
	PriceUnit      string // OCI NEW_ITEM-PRICEUNIT: Price is for this many units; empty means 1
	Currency       string
	UNSPSC         string
	LeadTimeDays   string
}

// RawCart is a parsed cart from either transport.
type RawCart struct {
	Items         []RawCartItem
	StatedTotal   string // empty when the transport carries no total (OCI)
	TotalCurrency string
}

// CartLine is a requisition line ready for wave 17. Every line is catalog-sourced.
type CartLine struct {
	Line           int             `json:"line"`
	SupplierPartID string          `json:"supplierPartId"`
	Description    string          `json:"description"`
	Quantity       decimal.Decimal `json:"quantity"`
	UnitCode       string          `json:"unitCode"`
	UnitPrice      decimal.Decimal `json:"unitPrice"`
	Currency       string          `json:"currency"`
	UNSPSC         string          `json:"unspsc"`
	LeadTimeDays   int             `json:"leadTimeDays"`
	Catalog        bool            `json:"catalog"`
	NeedsApproval  bool            `json:"needsApproval"`
}

// CartResult is the outcome of validating a returned cart.
type CartResult struct {
	Lines         []CartLine `json:"lines"`
	Issues        []Issue    `json:"issues"`
	CartRejected  bool       `json:"cartRejected"`
	ComputedTotal string     `json:"computedTotal"`
	StatedTotal   string     `json:"statedTotal,omitempty"`
}

type cxmlMoney struct {
	Currency string `xml:"currency,attr"`
	Value    string `xml:",chardata"`
}

type cxmlClassification struct {
	Domain string `xml:"domain,attr"`
	Value  string `xml:",chardata"`
}

type cxmlPunchoutDoc struct {
	XMLName xml.Name `xml:"cXML"`
	Message struct {
		POOM *struct {
			Header *struct {
				Total *struct {
					Money cxmlMoney `xml:"Money"`
				} `xml:"Total"`
			} `xml:"PunchOutOrderMessageHeader"`
			Items []struct {
				Quantity string `xml:"quantity,attr"`
				ItemID   struct {
					SupplierPartID string `xml:"SupplierPartID"`
				} `xml:"ItemID"`
				Detail struct {
					UnitPrice struct {
						Money cxmlMoney `xml:"Money"`
					} `xml:"UnitPrice"`
					Description     string               `xml:"Description"`
					UnitOfMeasure   string               `xml:"UnitOfMeasure"`
					Classifications []cxmlClassification `xml:"Classification"`
					LeadTime        string               `xml:"LeadTime"`
				} `xml:"ItemDetail"`
			} `xml:"ItemIn"`
		} `xml:"PunchOutOrderMessage"`
	} `xml:"Message"`
}

// ParsePunchoutCXML reads a cXML PunchOutOrderMessage (PunchOutSetupResponse
// cart return). Item-level data comes from ItemIn; the stated total from the
// message header.
func ParsePunchoutCXML(doc []byte) (RawCart, error) {
	var w cxmlPunchoutDoc
	if err := xml.NewDecoder(bytes.NewReader(doc)).Decode(&w); err != nil {
		return RawCart{}, fmt.Errorf("punchout: not well-formed cXML: %w", err)
	}
	if w.Message.POOM == nil {
		return RawCart{}, errors.New("punchout: payload is not a PunchOutOrderMessage")
	}
	cart := RawCart{}
	if h := w.Message.POOM.Header; h != nil && h.Total != nil {
		cart.StatedTotal = strings.TrimSpace(h.Total.Money.Value)
		cart.TotalCurrency = h.Total.Money.Currency
	}
	for i, it := range w.Message.POOM.Items {
		item := RawCartItem{
			Line:           i + 1,
			SupplierPartID: strings.TrimSpace(it.ItemID.SupplierPartID),
			Description:    strings.TrimSpace(it.Detail.Description),
			Unit:           strings.TrimSpace(it.Detail.UnitOfMeasure),
			Quantity:       strings.TrimSpace(it.Quantity),
			Price:          strings.TrimSpace(it.Detail.UnitPrice.Money.Value),
			Currency:       it.Detail.UnitPrice.Money.Currency,
			LeadTimeDays:   strings.TrimSpace(it.Detail.LeadTime),
		}
		for _, c := range it.Detail.Classifications {
			if c.Domain == "UNSPSC" {
				item.UNSPSC = strings.TrimSpace(c.Value)
			}
		}
		cart.Items = append(cart.Items, item)
	}
	return cart, nil
}

var ociItemField = regexp.MustCompile(`^NEW_ITEM-([A-Z_]+)\[(\d+)\]$`)

// ociFieldNames maps SAP OCI 4.0 and 5.0 NEW_ITEM fields to cart items. The
// UNSPSC code arrives as EXT_CATEGORY_ID when EXT_SCHEMA_TYPE is UNSPSC; the
// SAP material group (MATGROUP) is a buyer-side code and is not taken as one.
var ociFieldNames = map[string]string{
	"VENDORMAT":       "SupplierPartID",
	"DESCRIPTION":     "Description",
	"QUANTITY":        "Quantity",
	"PRICE":           "Price",
	"PRICEUNIT":       "PriceUnit",
	"CURRENCY":        "Currency",
	"UNIT":            "Unit",
	"EXT_CATEGORY_ID": "CategoryID",
	"EXT_SCHEMA_TYPE": "SchemaType",
	"LEADTIME":        "LeadTimeDays",
}

// ParsePunchoutOCI reads the HOOK_URL form post of an SAP OCI catalog. Items are
// the indexed NEW_ITEM-<FIELD>[n] fields, ordered by index. OCI carries no total.
func ParsePunchoutOCI(fields map[string]string) (RawCart, error) {
	byIndex := map[int]*RawCartItem{}
	category := map[int]string{}
	schema := map[int]string{}
	for name, value := range fields {
		m := ociItemField.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		target, known := ociFieldNames[m[1]]
		if !known {
			continue
		}
		idx, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		item := byIndex[idx]
		if item == nil {
			item = &RawCartItem{Line: idx}
			byIndex[idx] = item
		}
		v := strings.TrimSpace(value)
		switch target {
		case "SupplierPartID":
			item.SupplierPartID = v
		case "Description":
			item.Description = v
		case "Quantity":
			item.Quantity = v
		case "Price":
			item.Price = v
		case "PriceUnit":
			item.PriceUnit = v
		case "CategoryID":
			category[idx] = v
		case "SchemaType":
			schema[idx] = strings.ToUpper(v)
		case "Currency":
			item.Currency = v
		case "Unit":
			item.Unit = v
		case "LeadTimeDays":
			item.LeadTimeDays = v
		}
	}
	if len(byIndex) == 0 {
		return RawCart{}, errors.New("punchout: no NEW_ITEM fields in the OCI form post")
	}
	indexes := make([]int, 0, len(byIndex))
	for idx := range byIndex {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	cart := RawCart{}
	for _, idx := range indexes {
		item := *byIndex[idx]
		if schema[idx] == "UNSPSC" {
			item.UNSPSC = category[idx]
		}
		cart.Items = append(cart.Items, item)
	}
	return cart, nil
}

// ValidatePunchout applies BR-35.13 to a returned cart. Invalid lines are dropped
// with EDI-0133 and the rest are kept. The total is the sum of quantity times
// price over every returned line, compared with the stated total; a difference
// above 0.01 rejects the whole cart with EDI-0134. A price above the reference
// catalog price plus tolerance flags the line for approval. An OCI price for a
// price unit above 1 is converted to a price per unit (six decimals).
func ValidatePunchout(cart RawCart, sup SupplierPunchout) CartResult {
	res := CartResult{}
	computed := decimal.Zero
	allowed := map[string]bool{}
	for _, c := range sup.Currencies {
		allowed[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	uom := map[string]string{}
	for k, v := range sup.UoMMap {
		uom[strings.ToUpper(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for _, item := range cart.Items {
		qty, qtyErr := decimal.NewFromString(item.Quantity)
		price, priceErr := decimal.NewFromString(item.Price)
		per, perErr := decimal.NewFromInt(1), error(nil)
		if item.PriceUnit != "" {
			per, perErr = decimal.NewFromString(item.PriceUnit)
		}
		if priceErr == nil && perErr == nil && per.IsPositive() && !per.Equal(decimal.NewFromInt(1)) {
			price = price.DivRound(per, 6)
		}
		if qtyErr == nil && priceErr == nil {
			computed = computed.Add(qty.Mul(price))
		}
		var reasons []string
		if item.SupplierPartID == "" {
			reasons = append(reasons, "missing supplier part id")
		}
		if qtyErr != nil || !qty.IsPositive() {
			reasons = append(reasons, "quantity must be greater than zero")
		}
		if priceErr != nil || price.IsNegative() {
			reasons = append(reasons, "unit price must be zero or more")
		}
		if perErr != nil || !per.IsPositive() {
			reasons = append(reasons, "price unit must be greater than zero")
		}
		if !allowed[strings.ToUpper(item.Currency)] {
			reasons = append(reasons, fmt.Sprintf("currency %q is not allowed for this supplier", item.Currency))
		}
		unit := unitCode(item.Unit, uom)
		if unit == "" {
			reasons = append(reasons, fmt.Sprintf("unit %q has no UN/ECE Recommendation 20 mapping", item.Unit))
		}
		if len(reasons) > 0 {
			res.Issues = append(res.Issues, Issue{Code: CodePunchoutLine,
				Detail: fmt.Sprintf("Punchout line %d dropped: %s.", item.Line, strings.Join(reasons, "; "))})
			continue
		}
		lead, _ := strconv.Atoi(item.LeadTimeDays)
		line := CartLine{Line: item.Line, SupplierPartID: item.SupplierPartID, Description: item.Description,
			Quantity: qty, UnitCode: unit, UnitPrice: price, Currency: strings.ToUpper(item.Currency),
			UNSPSC: item.UNSPSC, LeadTimeDays: lead, Catalog: true}
		if ref, ok := sup.ReferencePrices[item.SupplierPartID]; ok {
			limit := ref.Mul(decimal.NewFromInt(1).Add(sup.TolerancePct.Div(hundred)))
			line.NeedsApproval = price.GreaterThan(limit)
		}
		res.Lines = append(res.Lines, line)
	}
	res.ComputedTotal = computed.StringFixed(2)
	if cart.StatedTotal != "" {
		stated, err := decimal.NewFromString(cart.StatedTotal)
		res.StatedTotal = cart.StatedTotal
		if err != nil || stated.Sub(computed).Abs().GreaterThan(cartTotalTolerance) {
			res.CartRejected = true
			res.Lines = nil
			res.Issues = append(res.Issues, Issue{Code: CodePunchoutTotal, Reject: true,
				Detail: fmt.Sprintf("Punchout cart total %s differs from line sum %s; cart rejected.",
					cart.StatedTotal, res.ComputedTotal)})
		}
	}
	return res
}

// unitCode resolves a supplier unit through the supplier table (keys already
// upper-cased), then the default.
func unitCode(unit string, supplier map[string]string) string {
	key := strings.ToUpper(strings.TrimSpace(unit))
	if key == "" {
		return ""
	}
	if code, ok := supplier[key]; ok && code != "" {
		return code
	}
	return DefaultUoM[key]
}

package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// Object types an ad-hoc estimate can be costed for (FR-39.13).
const (
	ObjectQuote         = "Quote"
	ObjectInternalOrder = "InternalOrder"
	ObjectProjectTask   = "ProjectTask"
)

// Model line types (FR-39.13): item, activity, subcontract and overhead lines.
const (
	LineItem        = "Item"
	LineActivity    = "Activity"
	LineSubcontract = "Subcontract"
	LineOverhead    = "Overhead"
)

// CodeEstimateInvalid extends the catalog: a costing model or an ad-hoc
// estimate request is not usable (bad structure or formula, a characteristic
// without a value, a negative quantity or price).
const CodeEstimateInvalid = "CST-0764"

// CostingModel is an easy-cost-planning model (FR-39.13). Characteristics are
// the inputs (cavities, machine hours, material weight); each line's formulas
// compute its quantity and, optionally, its unit price.
type CostingModel struct {
	Code            string           `json:"code"`
	Characteristics []Characteristic `json:"characteristics"`
	Lines           []ModelLine      `json:"lines"`
}

// Characteristic is a named model input with an optional default value.
type Characteristic struct {
	Name    string           `json:"name"`
	Unit    string           `json:"unit,omitempty"`
	Default *decimal.Decimal `json:"default,omitempty"`
}

// ModelLine prices one cost element. Ref is the item, activity, subcontract
// service or overhead node. QuantityFormula gives the quantity. PriceFormula,
// when set, gives the unit price (an overhead rate, or a price computed from
// the characteristics); otherwise the price of Ref is taken from the prices
// resolved from the selected costing version.
type ModelLine struct {
	Type            string `json:"type"`
	Ref             string `json:"ref"`
	Component       string `json:"component"`
	QuantityFormula string `json:"quantityFormula"`
	PriceFormula    string `json:"priceFormula,omitempty"`
}

// Price is a unit price resolved from the selected costing version. Source is
// the price source; a Fallback source raises CST-0703 (BR-39.4).
type Price struct {
	Amount decimal.Decimal `json:"amount"`
	Source string          `json:"source,omitempty"`
}

// Price sources of an estimate line besides the caller's own.
const (
	// PriceSourceFallback marks a price taken from the active standard because
	// the selected version had none (BR-39.4).
	PriceSourceFallback = "Fallback"
	// PriceSourceFormula marks a price computed by the line's PriceFormula.
	PriceSourceFormula = "Formula"
)

// Bounds of one costing model.
const (
	MaxModelLines           = 500
	MaxModelCharacteristics = 100
)

// EstimateLine is one priced line of an ad-hoc estimate, at the unit scale.
type EstimateLine struct {
	Type      string          `json:"type"`
	Ref       string          `json:"ref"`
	Component string          `json:"component"`
	Quantity  decimal.Decimal `json:"quantity"`
	Price     decimal.Decimal `json:"price"`
	Amount    decimal.Decimal `json:"amount"`
	Source    string          `json:"source,omitempty"`
}

// AdHocEstimate is the cost of a non-material object (FR-39.13). Split is the
// cost by component after the costing sheet, rounded to the posting scale;
// Total is the rounded sum of the unrounded split and equals the sum of Split,
// the rounding residual going to DM (BR-39.1, as in the sales order estimate).
type AdHocEstimate struct {
	ModelCode       string                     `json:"modelCode"`
	ObjectType      string                     `json:"objectType"`
	ObjectID        string                     `json:"objectId"`
	Currency        string                     `json:"currency,omitempty"`
	Characteristics map[string]decimal.Decimal `json:"characteristics"`
	Lines           []EstimateLine             `json:"lines"`
	Total           decimal.Decimal            `json:"total"`
	Split           Split                      `json:"split"`
	Subtotals       map[string]decimal.Decimal `json:"subtotals,omitempty"`
	Diagnostics     []Diagnostic               `json:"diagnostics"`
}

// ParsedLine holds a model line's compiled formulas; Price is nil when the
// line takes its price from the costing version.
type ParsedLine struct {
	Quantity Formula
	Price    *Formula
}

// Validate checks the model's structure: a code, unique characteristic names
// that formulas can refer to, known line types, parseable formulas, and
// formulas that read only the model's characteristics. It returns the parsed
// formulas by line.
func (m CostingModel) Validate() ([]ParsedLine, error) {
	if strings.TrimSpace(m.Code) == "" {
		return nil, invalidModel("model code is required")
	}
	if len(m.Characteristics) > MaxModelCharacteristics {
		return nil, invalidModel(fmt.Sprintf("model has more than %d characteristics", MaxModelCharacteristics))
	}
	names := map[string]bool{}
	for i, c := range m.Characteristics {
		if !isName(c.Name) {
			return nil, invalidModel(fmt.Sprintf("characteristic %d needs a name of letters, digits and underscores that starts with a letter or underscore", i))
		}
		if names[c.Name] {
			return nil, invalidModel("duplicate characteristic " + c.Name)
		}
		names[c.Name] = true
	}
	if len(m.Lines) == 0 {
		return nil, invalidModel("model needs at least one line")
	}
	if len(m.Lines) > MaxModelLines {
		return nil, invalidModel(fmt.Sprintf("model has more than %d lines", MaxModelLines))
	}
	parsed := make([]ParsedLine, 0, len(m.Lines))
	for i, ln := range m.Lines {
		switch ln.Type {
		case LineItem, LineActivity, LineSubcontract, LineOverhead:
		default:
			return nil, invalidModel(fmt.Sprintf("line %d has unknown type %q", i, ln.Type))
		}
		if ln.Ref == "" || ln.Component == "" {
			return nil, invalidModel(fmt.Sprintf("line %d needs ref and component", i))
		}
		qty, err := ParseFormula(ln.QuantityFormula)
		if err != nil {
			return nil, invalidModel(fmt.Sprintf("line %d quantity: %v", i, err))
		}
		pl := ParsedLine{Quantity: qty}
		reads := qty.Idents()
		if strings.TrimSpace(ln.PriceFormula) != "" {
			price, err := ParseFormula(ln.PriceFormula)
			if err != nil {
				return nil, invalidModel(fmt.Sprintf("line %d price: %v", i, err))
			}
			pl.Price = &price
			reads = append(reads, price.Idents()...)
		}
		for _, f := range reads {
			if !names[f] {
				return nil, invalidModel(fmt.Sprintf("line %d uses unknown characteristic %q", i, f))
			}
		}
		parsed = append(parsed, pl)
	}
	return parsed, nil
}

func invalidModel(detail string) error {
	return &CodedError{Code: CodeEstimateInvalid, Detail: detail}
}

// EstimateInput is what an ad-hoc estimate is costed for and with. Values are
// the characteristic values; a missing value takes the characteristic's
// default. Prices resolve the lines without a price formula. Sheet, when
// given, is applied to the priced split, with RoutingHours driving Hour rates.
// Scale is the posting scale of the currency (PostingScale).
type EstimateInput struct {
	ObjectType   string
	ObjectID     string
	Currency     string
	Values       map[string]decimal.Decimal
	Prices       map[string]Price
	Sheet        []SheetNode
	RoutingHours decimal.Decimal
	Scale        int32
}

// EstimateAdHoc prices a costing model for one object (FR-39.13). Each line is
// quantity × price at the unit scale. A missing price is CST-0702 and a
// fallback price warns CST-0703 (BR-39.4); a negative quantity or price, an
// unknown characteristic value, or a characteristic with neither a value nor a
// default is CST-0764.
func EstimateAdHoc(m CostingModel, in EstimateInput) (AdHocEstimate, error) {
	switch in.ObjectType {
	case ObjectQuote, ObjectInternalOrder, ObjectProjectTask:
	default:
		return AdHocEstimate{}, invalidModel("unknown object type " + in.ObjectType)
	}
	if in.ObjectID == "" {
		return AdHocEstimate{}, invalidModel("object id is required")
	}
	parsed, err := m.Validate()
	if err != nil {
		return AdHocEstimate{}, err
	}
	vars := map[string]decimal.Decimal{}
	for _, c := range m.Characteristics {
		v, ok := in.Values[c.Name]
		if !ok {
			if c.Default == nil {
				return AdHocEstimate{}, invalidModel("characteristic " + c.Name + " has no value and no default")
			}
			v = *c.Default
		}
		vars[c.Name] = v
	}
	for _, name := range sortedKeys(in.Values) {
		if _, ok := vars[name]; !ok {
			return AdHocEstimate{}, invalidModel("characteristic " + name + " is not in model " + m.Code)
		}
	}
	out := AdHocEstimate{ModelCode: m.Code, ObjectType: in.ObjectType, ObjectID: in.ObjectID, Currency: in.Currency,
		Characteristics: vars, Lines: make([]EstimateLine, 0, len(m.Lines)), Diagnostics: []Diagnostic{}}
	res := Result{ItemID: m.Code, Own: Split{}, Rolled: Split{}}
	for i, ln := range m.Lines {
		qty, err := parsed[i].Quantity.Eval(vars)
		if err != nil {
			return AdHocEstimate{}, invalidModel(fmt.Sprintf("line %d quantity: %v", i, err))
		}
		if qty.IsNegative() {
			return AdHocEstimate{}, invalidModel(fmt.Sprintf("line %d quantity %s is negative", i, qty.Round(UnitScale)))
		}
		el := EstimateLine{Type: ln.Type, Ref: ln.Ref, Component: ln.Component, Quantity: qty.Round(UnitScale)}
		if parsed[i].Price != nil {
			price, err := parsed[i].Price.Eval(vars)
			if err != nil {
				return AdHocEstimate{}, invalidModel(fmt.Sprintf("line %d price: %v", i, err))
			}
			el.Price, el.Source = price.Round(UnitScale), PriceSourceFormula
		} else {
			p, ok := in.Prices[ln.Ref]
			if !ok {
				return AdHocEstimate{}, &CodedError{Code: CodeNoPrice,
					Detail: fmt.Sprintf("no price for %s in the costing version", ln.Ref)}
			}
			el.Price, el.Source = p.Amount.Round(UnitScale), p.Source
			if p.Source == PriceSourceFallback {
				out.Diagnostics = append(out.Diagnostics, warnDiag(CodeFallback,
					"fallback to the active standard price for "+ln.Ref, ln.Ref))
			}
		}
		if el.Price.IsNegative() {
			return AdHocEstimate{}, invalidModel(fmt.Sprintf("line %d price %s is negative", i, el.Price))
		}
		el.Amount = el.Quantity.Mul(el.Price).Round(UnitScale)
		res.Own.Add(el.Component, el.Amount)
		out.Lines = append(out.Lines, el)
	}
	subtotals, bad := applySheet(&res, Item{ID: m.Code, Sheet: in.Sheet}, in.RoutingHours)
	if bad != nil {
		return AdHocEstimate{}, &CodedError{Code: bad.Code, Detail: bad.Message}
	}
	if len(subtotals) > 0 {
		out.Subtotals = make(map[string]decimal.Decimal, len(subtotals))
		for id, v := range subtotals {
			out.Subtotals[id] = v.Round(in.Scale)
		}
	}
	out.Split, out.Total = IssueCOGS(decimal.NewFromInt(1), res.Own.Rounded(UnitScale), in.Scale)
	return out, nil
}

// isName reports whether s can be read by a formula as a characteristic name.
func isName(s string) bool {
	if s == "" || !isNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isNamePart(s[i]) {
			return false
		}
	}
	return true
}

package domain

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// MaxActiveScenarios is the number of active scenarios one plan may have
// (FR-22.15, tenant parameter "Simulation scenarios per plan", default 10).
const MaxActiveScenarios = 10

const (
	scenarioMaxLines    = 500
	scenarioMaxFactor   = 100
	scenarioMaxLeadDays = 3650
	scenarioMaxDemand   = 3650
	scenarioDateLayout  = "2006-01-02"
)

var scenarioCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)

// ItemFactor multiplies the independent demand of one item; it replaces the
// scenario's overall factor for that item.
type ItemFactor struct {
	ItemID string          `json:"itemId"`
	Factor decimal.Decimal `json:"factor"`
}

// AddedDemand is a sales demand the scenario adds on a date.
type AddedDemand struct {
	ItemID string          `json:"itemId"`
	Date   string          `json:"date"`
	Qty    decimal.Decimal `json:"qty"`
}

// LeadTimeOverride replaces the lead time of an item in days.
type LeadTimeOverride struct {
	ItemID       string `json:"itemId"`
	LeadTimeDays int    `json:"leadTimeDays"`
}

// ScenarioSpec is the setup of one planning scenario (data model
// ScenarioOverride, FR-22.15). DemandFactor 1 leaves demand as it is.
type ScenarioSpec struct {
	Code              string
	PlanCode          string
	Name              string
	DemandFactor      decimal.Decimal
	ItemFactors       []ItemFactor
	AddedDemand       []AddedDemand
	LeadTimeOverrides []LeadTimeOverride
	Active            bool
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func validItemID(id string) bool { return uuidPattern.MatchString(id) }

func factorOK(f decimal.Decimal) bool {
	return !f.IsNegative() && f.LessThanOrEqual(decimal.NewFromInt(scenarioMaxFactor)) && f.Exponent() >= -4
}

// Validate returns one message per invalid field, keyed by wire name; nil
// means the spec is valid. Line errors are keyed like "addedDemand[2].qty".
func (s ScenarioSpec) Validate() map[string]string {
	errs := map[string]string{}
	if !scenarioCode.MatchString(s.Code) {
		errs["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if !scenarioCode.MatchString(s.PlanCode) {
		errs["planCode"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if n := len([]rune(s.Name)); n < 1 || n > 120 {
		errs["name"] = "must be 1 to 120 characters"
	}
	if !factorOK(s.DemandFactor) {
		errs["demandFactor"] = "must be 0 to 100 with at most 4 decimals"
	}
	if len(s.ItemFactors) > scenarioMaxLines {
		errs["itemFactors"] = "at most 500 lines"
	}
	if len(s.AddedDemand) > scenarioMaxLines {
		errs["addedDemand"] = "at most 500 lines"
	}
	if len(s.LeadTimeOverrides) > scenarioMaxLines {
		errs["leadTimeOverrides"] = "at most 500 lines"
	}
	seen := map[string]bool{}
	for i, f := range s.ItemFactors {
		key := "itemFactors[" + strconv.Itoa(i) + "]"
		switch {
		case !validItemID(f.ItemID):
			errs[key+".itemId"] = "must be a UUID"
		case seen[f.ItemID]:
			errs[key+".itemId"] = "appears twice"
		}
		seen[f.ItemID] = true
		if !factorOK(f.Factor) {
			errs[key+".factor"] = "must be 0 to 100 with at most 4 decimals"
		}
	}
	for i, a := range s.AddedDemand {
		key := "addedDemand[" + strconv.Itoa(i) + "]"
		if !validItemID(a.ItemID) {
			errs[key+".itemId"] = "must be a UUID"
		}
		if _, err := time.Parse(scenarioDateLayout, a.Date); err != nil {
			errs[key+".date"] = "must be a date YYYY-MM-DD"
		}
		if !a.Qty.IsPositive() || a.Qty.Exponent() < -6 {
			errs[key+".qty"] = "must be greater than 0 with at most 6 decimals"
		}
	}
	seen = map[string]bool{}
	for i, l := range s.LeadTimeOverrides {
		key := "leadTimeOverrides[" + strconv.Itoa(i) + "]"
		switch {
		case !validItemID(l.ItemID):
			errs[key+".itemId"] = "must be a UUID"
		case seen[l.ItemID]:
			errs[key+".itemId"] = "appears twice"
		}
		seen[l.ItemID] = true
		if l.LeadTimeDays < 0 || l.LeadTimeDays > scenarioMaxLeadDays {
			errs[key+".leadTimeDays"] = "must be 0 to 3650"
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Factors returns the demand factor of an item under the scenario: its own
// item factor, else the scenario's demand factor (BR-22.14).
func (s ScenarioSpec) Factors() func(itemID string) decimal.Decimal {
	itemFactor := make(map[string]decimal.Decimal, len(s.ItemFactors))
	for _, f := range s.ItemFactors {
		itemFactor[f.ItemID] = f.Factor
	}
	return func(itemID string) decimal.Decimal {
		if f, ok := itemFactor[itemID]; ok {
			return f
		}
		return s.DemandFactor
	}
}

// ApplyScenario returns the items as a simulation sees them (BR-22.14): the
// independent demand of each item (sales lines and forecast) multiplied by its
// factor (the item's own factor, else the scenario's); dependent, transfer and
// safety-stock demand is left as sent, and dependent demand of lower levels
// follows from the explosion, the added demand lines appended as sales
// demand, the lead-time overrides applied, and auto-firming switched off since
// a simulation never firms. The input slice and its items are not changed.
// An added line dated before asOf is due today; one more than 3650 days out is
// an error. A line or override for an item outside the snapshot is ignored.
//
// A run applies the scenario before the forecast model is planned, scaling
// the model's cells by Factors itself, so that under assemble-to-order-final
// the added lines (which no factor scales) consume the scaled forecast like
// the scaled sales do (BR-22.13): factor 2, forecast 100 and an added sale of
// 40 plan 160 + 40, never 120 + 40.
func ApplyScenario(items []Item, asOf time.Time, s ScenarioSpec) ([]Item, error) {
	factorOf := s.Factors()
	lead := map[string]int{}
	for _, l := range s.LeadTimeOverrides {
		lead[l.ItemID] = l.LeadTimeDays
	}
	out := make([]Item, len(items))
	index := make(map[string]int, len(items))
	for i, it := range items {
		index[it.ID] = i
		factor := factorOf(it.ID)
		demand := make([]Requirement, 0, len(it.Demand))
		for _, d := range it.Demand {
			if (d.RefType == RefSales || d.RefType == RefForecast) && !factor.Equal(decimal.NewFromInt(1)) {
				d.Qty = d.Qty.Mul(factor)
			}
			if d.Qty.IsPositive() {
				demand = append(demand, d)
			}
		}
		it.Demand = demand
		if days, ok := lead[it.ID]; ok {
			it.LeadTimeDays = days
		}
		it.AutoFirm = false
		out[i] = it
	}
	for n, a := range s.AddedDemand {
		i, ok := index[a.ItemID]
		if !ok {
			continue
		}
		date, err := time.Parse(scenarioDateLayout, a.Date)
		if err != nil {
			return nil, fmt.Errorf("%w: addedDemand[%d].date", ErrInvalidItem, n)
		}
		day := int(date.Sub(asOf) / (24 * time.Hour))
		if day > scenarioMaxDemand {
			return nil, fmt.Errorf("%w: addedDemand[%d].date is more than 3650 days out", ErrInvalidItem, n)
		}
		if day < 0 {
			day = 0
		}
		out[i].Demand = append(out[i].Demand, Requirement{Ref: fmt.Sprintf("SC:%s:%d", s.Code, n+1), RefType: RefSales, Date: day, Qty: a.Qty, Priority: PrioritySales})
	}
	return out, nil
}

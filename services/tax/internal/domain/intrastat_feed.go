package domain

import (
	"fmt"
	"regexp"
	"sort"
	"time"
)

var productPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// IntrastatProduct is what the journal needs to know of a product to report it: the commodity code, the country it originates in and
// its net mass (and, where the commodity has a supplementary unit, the quantity in it) per unit.
type IntrastatProduct struct {
	ProductID            string `json:"product_id"`
	CommodityCode        string `json:"commodity_code"`
	OriginCountry        string `json:"origin_country"`
	NetMassPerUnit       Dec    `json:"net_mass_per_unit"`
	SupplementaryPerUnit Dec    `json:"supplementary_per_unit"`
}

func ValidateIntrastatProduct(p IntrastatProduct) error {
	switch {
	case !productPattern.MatchString(p.ProductID):
		return invalid("product_id", "product_id must be a UUID")
	case !masterCodePatterns[MasterCommodity].MatchString(p.CommodityCode):
		return invalid("commodity_code", "a commodity code is %s", masterCodeHints[MasterCommodity])
	case !ValidCountry(p.OriginCountry):
		return invalid("origin_country", "origin_country must be a 2-letter ISO 3166 code in capitals")
	case p.NetMassPerUnit.IsNegative() || p.NetMassPerUnit.Exponent() < -maxScale:
		return invalid("net_mass_per_unit", "net_mass_per_unit must not be negative, with at most %d decimal places", maxScale)
	case p.SupplementaryPerUnit.IsNegative() || p.SupplementaryPerUnit.Exponent() < -maxScale:
		return invalid("supplementary_per_unit", "supplementary_per_unit must not be negative, with at most %d decimal places", maxScale)
	}
	return nil
}

// MovementLine is one product line of a goods movement: how many units and what they were worth, as invoiced.
type MovementLine struct {
	LineNo    int    `json:"line_no"`
	ProductID string `json:"product_id"`
	Qty       Dec    `json:"qty"`
	Value     Dec    `json:"value"`
}

// Movement is a shipment (flow dispatch) or a receipt (flow arrival) staged for the Intrastat journal. The partner country, the
// nature of the transaction, the transport and the delivery terms are what the document knows; whatever it does not know stays empty
// and is filled by the defaults of the transfer, or flagged by the journal's validation.
type Movement struct {
	ID              string         `json:"id"`
	Flow            string         `json:"flow"`
	SourceRef       string         `json:"source_ref"`
	MovementDate    Date           `json:"movement_date"`
	PartnerCountry  string         `json:"partner_country"`
	TransactionCode string         `json:"transaction_code"`
	TransportMode   string         `json:"transport_mode"`
	DeliveryTerms   string         `json:"delivery_terms"`
	Port            string         `json:"port"`
	Lines           []MovementLine `json:"lines"`
	Status          string         `json:"status"`
}

const maxMovementLines = 1000

func ValidateMovement(m Movement) error {
	switch {
	case !oneOf(m.Flow, FlowArrival, FlowDispatch):
		return invalid("flow", "flow must be %q or %q", FlowArrival, FlowDispatch)
	case m.SourceRef == "" || len(m.SourceRef) > 100:
		return invalid("source_ref", "source_ref is required and at most 100 characters")
	case m.MovementDate.IsZero():
		return invalid("movement_date", "movement_date is required")
	case !ValidCountry(m.PartnerCountry):
		return invalid("partner_country", "partner_country must be a 2-letter ISO 3166 code in capitals")
	case len(m.Lines) == 0 || len(m.Lines) > maxMovementLines:
		return invalid("lines", "lines must hold between 1 and %d entries", maxMovementLines)
	}
	seen := map[int]bool{}
	for i, l := range m.Lines {
		field := fmt.Sprintf("lines[%d]", i)
		switch {
		case l.LineNo < 1 || seen[l.LineNo]:
			return invalid(field+".line_no", "line_no is required, positive and unique")
		case !productPattern.MatchString(l.ProductID):
			return invalid(field+".product_id", "product_id must be a UUID")
		case !l.Qty.IsPositive():
			return invalid(field+".qty", "qty must be positive")
		case l.Value.IsNegative():
			return invalid(field+".value", "value must not be negative")
		}
		seen[l.LineNo] = true
	}
	return nil
}

// MovementDefaults fill what a movement does not carry when it is transferred.
type MovementDefaults struct {
	TransactionCode string `json:"transaction_code"`
	TransportMode   string `json:"transport_mode"`
	DeliveryTerms   string `json:"delivery_terms"`
	Port            string `json:"port"`
}

// UnmappedLine is a movement line the journal could not take because its product has no Intrastat mapping.
type UnmappedLine struct {
	SourceRef string `json:"source_ref"`
	LineNo    int    `json:"line_no"`
	ProductID string `json:"product_id"`
}

// MovementLines turns the staged movements of a journal's flow and month into Intrastat lines. Movements to or from the entity's own
// country or a country outside the EU are not Intrastat business and come back in ignored. A line whose product has no mapping is
// returned in unmapped and its movement is not complete (complete lists the movements whose every line was mapped). Each line's
// source_ref is "<movement ref>:<line no>", so transferring a line twice adds it once.
func MovementLines(flow, homeCountry string, month time.Time, movements []Movement, products map[string]IntrastatProduct, d MovementDefaults) (lines []IntrastatLine, unmapped []UnmappedLine, complete, ignored []string) {
	movements = append([]Movement{}, movements...)
	sort.SliceStable(movements, func(i, j int) bool {
		if !movements[i].MovementDate.Equal(movements[j].MovementDate.Time) {
			return movements[i].MovementDate.Before(movements[j].MovementDate.Time)
		}
		return movements[i].SourceRef < movements[j].SourceRef
	})
	for _, m := range movements {
		if m.Flow != flow || m.MovementDate.Year() != month.Year() || m.MovementDate.Month() != month.Month() {
			continue
		}
		if m.PartnerCountry == homeCountry || !IsEUMember(m.PartnerCountry) {
			ignored = append(ignored, m.ID)
			continue
		}
		whole := true
		for _, l := range m.Lines {
			p, ok := products[l.ProductID]
			if !ok {
				unmapped = append(unmapped, UnmappedLine{SourceRef: m.SourceRef, LineNo: l.LineNo, ProductID: l.ProductID})
				whole = false
				continue
			}
			value := l.Value
			lines = append(lines, IntrastatLine{
				SourceRef: fmt.Sprintf("%s:%d", m.SourceRef, l.LineNo), PartnerCountry: m.PartnerCountry, CommodityCode: p.CommodityCode,
				TransactionCode: firstOf(m.TransactionCode, d.TransactionCode), TransportMode: firstOf(m.TransportMode, d.TransportMode),
				DeliveryTerms: firstOf(m.DeliveryTerms, d.DeliveryTerms), Port: firstOf(m.Port, d.Port), OriginCountry: p.OriginCountry,
				NetMass: Dec{l.Qty.Mul(p.NetMassPerUnit.Decimal).Round(3)}, SupplementaryQty: Dec{l.Qty.Mul(p.SupplementaryPerUnit.Decimal).Round(3)},
				InvoiceValue: Dec{value.Decimal.Round(2)}, StatisticalValue: Dec{value.Decimal.Round(2)},
			})
		}
		if whole {
			complete = append(complete, m.ID)
		}
	}
	return lines, unmapped, complete, ignored
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

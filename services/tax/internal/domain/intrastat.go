package domain

import (
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	FlowArrival  = "arrival"
	FlowDispatch = "dispatch"

	JournalDraft     = "draft"
	JournalValidated = "validated"
	JournalExported  = "exported"

	MasterCommodity       = "commodity"
	MasterTransactionCode = "transaction_code"
	MasterTransportMode   = "transport_mode"
	MasterPort            = "port"
	MasterCountry         = "country"
	MasterDeliveryTerms   = "delivery_terms"

	maxIntrastatLines = 5000
)

var (
	MasterKinds = []string{MasterCommodity, MasterTransactionCode, MasterTransportMode, MasterPort, MasterCountry, MasterDeliveryTerms}

	masterCodePatterns = map[string]*regexp.Regexp{
		MasterCommodity:       regexp.MustCompile(`^[0-9]{8}$`),
		MasterTransactionCode: regexp.MustCompile(`^[0-9]{1,2}$`),
		MasterTransportMode:   regexp.MustCompile(`^[0-9]$`),
		MasterPort:            regexp.MustCompile(`^[A-Za-z0-9]{1,10}$`),
		MasterCountry:         regexp.MustCompile(`^[A-Z]{2}$`),
		MasterDeliveryTerms:   regexp.MustCompile(`^[A-Z]{3}$`),
	}
	masterCodeHints = map[string]string{
		MasterCommodity:       "8 digits (CN8)",
		MasterTransactionCode: "1 or 2 digits",
		MasterTransportMode:   "1 digit",
		MasterPort:            "up to 10 letters or digits",
		MasterCountry:         "2 capital letters (ISO 3166)",
		MasterDeliveryTerms:   "3 capital letters (Incoterms)",
	}
)

// MasterEntry is one row of the Intrastat master data. Attrs carries what the kind needs:
// supplementary_unit for a commodity code, eu_member and intrastat_code for a country,
// country for a port.
type MasterEntry struct {
	Kind        string            `json:"kind"`
	Code        string            `json:"code"`
	Description string            `json:"description"`
	Attrs       map[string]string `json:"attrs"`
}

func ValidMasterKind(kind string) bool { return oneOf(kind, MasterKinds...) }

func ValidateMasterEntry(e MasterEntry) error {
	if !ValidMasterKind(e.Kind) {
		return invalid("kind", "unknown master data kind %q", e.Kind)
	}
	if !masterCodePatterns[e.Kind].MatchString(e.Code) {
		return invalid("code", "a %s code is %s", strings.ReplaceAll(e.Kind, "_", " "), masterCodeHints[e.Kind])
	}
	if strings.TrimSpace(e.Description) == "" || len(e.Description) > 200 {
		return invalid("description", "description is required and at most 200 characters")
	}
	allowed := map[string][]string{
		MasterCommodity: {"supplementary_unit"}, MasterCountry: {"eu_member", "intrastat_code"}, MasterPort: {"country"},
	}[e.Kind]
	for k, v := range e.Attrs {
		if !oneOf(k, allowed...) {
			return invalid("attrs."+k, "%s entries take no %q attribute", strings.ReplaceAll(e.Kind, "_", " "), k)
		}
		if len(v) > 20 {
			return invalid("attrs."+k, "at most 20 characters")
		}
	}
	if v, ok := e.Attrs["eu_member"]; ok && v != "true" && v != "false" {
		return invalid("attrs.eu_member", "eu_member must be true or false")
	}
	if c, ok := e.Attrs["country"]; ok && !ValidCountry(c) {
		return invalid("attrs.country", "country must be a 2-letter ISO 3166 code in capitals")
	}
	return nil
}

// Master is the master data a journal is validated against.
type Master struct {
	Commodities      map[string]string // code to supplementary unit ("" when none)
	TransactionCodes map[string]bool
	TransportModes   map[string]bool
	Ports            map[string]bool
	DeliveryTerms    map[string]bool
	Countries        map[string]CountryProps
}

type CountryProps struct {
	EUMember      bool
	IntrastatCode string
}

func NewMaster(entries []MasterEntry) Master {
	m := Master{
		Commodities: map[string]string{}, TransactionCodes: map[string]bool{}, TransportModes: map[string]bool{},
		Ports: map[string]bool{}, DeliveryTerms: map[string]bool{}, Countries: map[string]CountryProps{},
	}
	for _, e := range entries {
		switch e.Kind {
		case MasterCommodity:
			m.Commodities[e.Code] = e.Attrs["supplementary_unit"]
		case MasterTransactionCode:
			m.TransactionCodes[e.Code] = true
		case MasterTransportMode:
			m.TransportModes[e.Code] = true
		case MasterPort:
			m.Ports[e.Code] = true
		case MasterDeliveryTerms:
			m.DeliveryTerms[e.Code] = true
		case MasterCountry:
			m.Countries[e.Code] = CountryProps{EUMember: e.Attrs["eu_member"] == "true", IntrastatCode: e.Attrs["intrastat_code"]}
		}
	}
	return m
}

// IntrastatLine is one line of an Intrastat declaration.
type IntrastatLine struct {
	LineNo           int      `json:"line_no"`
	SourceRef        string   `json:"source_ref"`
	PartnerCountry   string   `json:"partner_country"`
	CommodityCode    string   `json:"commodity_code"`
	TransactionCode  string   `json:"transaction_code"`
	TransportMode    string   `json:"transport_mode"`
	DeliveryTerms    string   `json:"delivery_terms"`
	Port             string   `json:"port"`
	OriginCountry    string   `json:"origin_country"`
	NetMass          Dec      `json:"net_mass"`
	SupplementaryQty Dec      `json:"supplementary_qty"`
	InvoiceValue     Dec      `json:"invoice_value"`
	StatisticalValue Dec      `json:"statistical_value"`
	CompressedFrom   int      `json:"compressed_from"`
	Errors           []string `json:"errors"`
}

// ValidateIntrastatLine checks one line against the flow, the entity's home country and the
// master data and returns every problem it finds, in plain words.
func ValidateIntrastatLine(flow, homeCountry string, l IntrastatLine, m Master) []string {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	switch p, known := m.Countries[l.PartnerCountry]; {
	case l.PartnerCountry == "":
		add("The partner country is missing.")
	case !known:
		add("Partner country %s is not in the country master data.", l.PartnerCountry)
	case !p.EUMember:
		add("Partner country %s is not an EU member state, so the trade is not declared in Intrastat.", l.PartnerCountry)
	case l.PartnerCountry == homeCountry:
		add("The partner country is the company's own country; Intrastat covers trade with other member states.")
	}
	unit, known := m.Commodities[l.CommodityCode]
	switch {
	case l.CommodityCode == "":
		add("The commodity code is missing.")
	case !known:
		add("Commodity code %s is not in the commodity master data.", l.CommodityCode)
	case unit != "" && !l.SupplementaryQty.GreaterThan(decimal.Zero):
		add("Commodity %s needs a quantity in %s.", l.CommodityCode, unit)
	}
	if !m.TransactionCodes[l.TransactionCode] {
		add("Transaction code %q is missing or not in the master data.", l.TransactionCode)
	}
	if !m.TransportModes[l.TransportMode] {
		add("Transport mode %q is missing or not in the master data.", l.TransportMode)
	}
	if !m.DeliveryTerms[l.DeliveryTerms] {
		add("Delivery terms %q are missing or not in the master data.", l.DeliveryTerms)
	}
	if l.Port != "" && !m.Ports[l.Port] {
		add("Port %s is not in the port master data.", l.Port)
	}
	if l.OriginCountry == "" && flow == FlowArrival {
		add("The country of origin is required on arrivals.")
	} else if _, ok := m.Countries[l.OriginCountry]; l.OriginCountry != "" && !ok {
		add("Country of origin %s is not in the country master data.", l.OriginCountry)
	}
	if !l.NetMass.GreaterThan(decimal.Zero) {
		add("The net mass must be above zero.")
	}
	if !l.InvoiceValue.GreaterThan(decimal.Zero) {
		add("The invoice value must be above zero.")
	}
	if !l.StatisticalValue.GreaterThan(decimal.Zero) {
		add("The statistical value must be above zero.")
	}
	return errs
}

// ValidateIntrastatLines validates every line and records the problems on the line itself.
// It returns how many lines have a problem.
func ValidateIntrastatLines(flow, homeCountry string, lines []IntrastatLine, m Master) int {
	bad := 0
	for i := range lines {
		lines[i].Errors = ValidateIntrastatLine(flow, homeCountry, lines[i], m)
		if len(lines[i].Errors) > 0 {
			bad++
		}
	}
	return bad
}

// CompressIntrastatLines merges lines that agree on every classifying field into one line and
// sums mass, quantity and values. Order follows the first line of each group and the lines are
// renumbered from 1. Compressed lines have no single source, so source_ref is cleared; the
// transfer ledger keeps the references that were already taken.
func CompressIntrastatLines(lines []IntrastatLine) []IntrastatLine {
	type key [7]string
	index := map[key]int{}
	var out []IntrastatLine
	for _, l := range lines {
		k := key{l.PartnerCountry, l.CommodityCode, l.TransactionCode, l.TransportMode, l.DeliveryTerms, l.Port, l.OriginCountry}
		if l.CompressedFrom < 1 {
			l.CompressedFrom = 1
		}
		i, seen := index[k]
		if !seen {
			index[k] = len(out)
			l.Errors = nil
			out = append(out, l)
			continue
		}
		g := &out[i]
		g.NetMass = Dec{g.NetMass.Add(l.NetMass.Decimal)}
		g.SupplementaryQty = Dec{g.SupplementaryQty.Add(l.SupplementaryQty.Decimal)}
		g.InvoiceValue = Dec{g.InvoiceValue.Add(l.InvoiceValue.Decimal)}
		g.StatisticalValue = Dec{g.StatisticalValue.Add(l.StatisticalValue.Decimal)}
		g.CompressedFrom += l.CompressedFrom
		g.SourceRef = ""
	}
	for i := range out {
		out[i].LineNo = i + 1
	}
	return out
}

// ValidateTransferLines checks the shape of lines handed over for transfer. Their content is
// checked later by validation, so a draft can hold incomplete lines for the user to fix.
func ValidateTransferLines(lines []IntrastatLine) error {
	if len(lines) == 0 || len(lines) > maxIntrastatLines {
		return invalid("lines", "lines must hold between 1 and %d entries", maxIntrastatLines)
	}
	seen := map[string]bool{}
	for i, l := range lines {
		field := fmt.Sprintf("lines[%d]", i)
		switch {
		case l.SourceRef == "" || len(l.SourceRef) > 64:
			return invalid(field+".source_ref", "source_ref identifies the shipment or receipt line and is at most 64 characters")
		case seen[l.SourceRef]:
			return invalid(field+".source_ref", "source_ref %q appears twice in the transfer", l.SourceRef)
		}
		if err := checkLineShape(field, l); err != nil {
			return err
		}
		seen[l.SourceRef] = true
	}
	return nil
}

// CheckLineShape rejects values no master data could ever accept (too long, negative).
func CheckLineShape(l IntrastatLine) error { return checkLineShape("line", l) }

func checkLineShape(field string, l IntrastatLine) error {
	for name, v := range map[string]string{
		"partner_country": l.PartnerCountry, "commodity_code": l.CommodityCode, "transaction_code": l.TransactionCode,
		"transport_mode": l.TransportMode, "delivery_terms": l.DeliveryTerms, "port": l.Port, "origin_country": l.OriginCountry,
	} {
		if len(v) > 10 {
			return invalid(field+"."+name, "%s is at most 10 characters", name)
		}
	}
	for name, v := range map[string]Dec{
		"net_mass": l.NetMass, "supplementary_qty": l.SupplementaryQty, "invoice_value": l.InvoiceValue, "statistical_value": l.StatisticalValue,
	} {
		if v.IsNegative() {
			return invalid(field+"."+name, "%s must not be negative", name)
		}
	}
	return nil
}

// IntrastatCSV writes the declaration as CSV: one row per line, values in whole currency
// units, mass in kilograms to three places. A correction journal marks every row "A".
func IntrastatCSV(w io.Writer, flow, homeCountry string, correction bool, lines []IntrastatLine, m Master) error {
	out := csv.NewWriter(w)
	if err := out.Write([]string{
		"line", "flow", "indicator", "member_state", "partner_member_state", "commodity_code", "country_of_origin", "transaction_code",
		"transport_mode", "delivery_terms", "port", "net_mass_kg", "supplementary_qty", "invoice_value", "statistical_value",
	}); err != nil {
		return err
	}
	indicator := "N"
	if correction {
		indicator = "A"
	}
	// Intrastat uses its own country code where it differs from ISO (EL for Greece, XI for Northern Ireland).
	code := func(c string) string {
		if p := m.Countries[c]; p.IntrastatCode != "" {
			return p.IntrastatCode
		}
		return c
	}
	sorted := append([]IntrastatLine{}, lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].LineNo < sorted[j].LineNo })
	for _, l := range sorted {
		origin := ""
		if l.OriginCountry != "" {
			origin = code(l.OriginCountry)
		}
		if err := out.Write([]string{
			fmt.Sprint(l.LineNo), flow, indicator, code(homeCountry), code(l.PartnerCountry), l.CommodityCode, origin, l.TransactionCode,
			l.TransportMode, l.DeliveryTerms, l.Port, l.NetMass.StringFixed(3), l.SupplementaryQty.StringFixed(3),
			l.InvoiceValue.Round(0).String(), l.StatisticalValue.Round(0).String(),
		}); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

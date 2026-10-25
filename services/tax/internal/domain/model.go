package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	CodeRateNotFound            = "TAX-0001"
	CodeRateInvalid             = "TAX-0002"
	CodeGroupUnresolved         = "TAX-0003"
	CodeNoIntersection          = "TAX-0004"
	CodeExemptionExpired        = "TAX-0005"
	CodeReverseChargeIneligible = "TAX-0006"
	CodeProviderFallback        = "TAX-0007"
	CodeSettlementMismatch      = "TAX-0008"
	CodeValidation              = "COM-0001"

	MechStandard      = "standard"
	MechReverseCharge = "reverse_charge"
	MechUseTax        = "use_tax"

	DirOutput = "output"
	DirInput  = "input"
	DirUse    = "use"

	RoundPerLine     = "per_line"
	RoundPerDocument = "per_document"

	BasisLine     = "line"
	BasisDocument = "document"

	EngineInternal = "internal"

	maxLines     = 1000
	maxScale     = 6
	maxRefLength = 64
	dateLayout   = "2006-01-02"
)

var maxMagnitude = decimal.New(1, 15)

type Error struct{ Code, Detail, Field string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func invalid(field, format string, a ...any) *Error {
	return &Error{Code: CodeValidation, Field: field, Detail: fmt.Sprintf(format, a...)}
}

type Dec struct{ decimal.Decimal }

func D(s string) Dec { return Dec{decimal.RequireFromString(s)} }

func (d *Dec) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("decimal must be a JSON string")
	}
	v, err := decimal.NewFromString(s)
	if err != nil {
		return fmt.Errorf("%q is not a decimal", s)
	}
	if v.Exponent() < -maxScale || !v.Abs().LessThan(maxMagnitude) {
		return fmt.Errorf("%q exceeds %d decimal places or 15 integer digits", s, maxScale)
	}
	d.Decimal = v
	return nil
}

type Date struct{ time.Time }

func Day(s string) Date {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return Date{t}
}

func (d *Date) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("date must be a JSON string")
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return fmt.Errorf("%q is not a YYYY-MM-DD date", s)
	}
	d.Time = t
	return nil
}

func (d Date) MarshalJSON() ([]byte, error) { return json.Marshal(d.Format(dateLayout)) }

type Rate struct {
	ValidFrom  Date `json:"valid_from"`
	ValidTo    Date `json:"valid_to"`
	FromAmount *Dec `json:"from_amount"`
	ToAmount   *Dec `json:"to_amount"`
	RatePct    Dec  `json:"rate_pct"`
	MinTax     *Dec `json:"min_tax"`
	MaxTax     *Dec `json:"max_tax"`
}

type Code struct {
	Code             string `json:"code"`
	Jurisdiction     string `json:"jurisdiction"`
	Mechanism        string `json:"mechanism"`
	CalcBasis        string `json:"calc_basis"`
	Rounding         string `json:"rounding"`
	CompoundSequence int    `json:"compound_sequence"`
	RecoverablePct   *Dec   `json:"recoverable_pct"`
	Rates            []Rate `json:"rates"`
}

type Exemption struct {
	Number    string   `json:"number"`
	Codes     []string `json:"codes"`
	ValidFrom Date     `json:"valid_from"`
	ValidTo   Date     `json:"valid_to"`
}

type Config struct {
	DefaultRounding string              `json:"default_rounding"`
	Groups          map[string][]string `json:"groups"`
	ItemGroups      map[string][]string `json:"item_groups"`
	Codes           []Code              `json:"codes"`
	Exemptions      []Exemption         `json:"exemptions"`
}

type Party struct {
	TaxGroup    string `json:"tax_group"`
	TaxID       string `json:"tax_id"`
	Country     string `json:"country"`
	Certificate string `json:"certificate"`
}

type Line struct {
	LineRef          string `json:"line_ref"`
	Amount           Dec    `json:"amount"`
	ItemTaxGroup     string `json:"item_tax_group"`
	PriceIncludesTax bool   `json:"price_includes_tax"`
}

type Document struct {
	Currency        string `json:"currency"`
	Direction       string `json:"direction"`
	DocumentDate    Date   `json:"document_date"`
	TaxDate         Date   `json:"tax_date"`
	ShipFromCountry string `json:"ship_from_country"`
	Party           Party  `json:"party"`
	Lines           []Line `json:"lines"`
}

type TaxLine struct {
	Code           string `json:"code"`
	Jurisdiction   string `json:"jurisdiction"`
	RatePct        Dec    `json:"rate_pct"`
	TaxAmount      Dec    `json:"tax_amount"`
	Recoverable    Dec    `json:"recoverable"`
	Nonrecoverable Dec    `json:"nonrecoverable"`
	Direction      string `json:"direction"`
}

type LineResult struct {
	LineRef string    `json:"line_ref"`
	Base    Dec       `json:"base"`
	Taxes   []TaxLine `json:"taxes"`
}

type CodeTotal struct {
	Code      string `json:"code"`
	Direction string `json:"direction"`
	Base      Dec    `json:"base"`
	TaxAmount Dec    `json:"tax_amount"`
}

type Warning struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type Result struct {
	Engine              string       `json:"engine"`
	Fallback            bool         `json:"fallback"`
	Lines               []LineResult `json:"lines"`
	DocumentTaxesByCode []CodeTotal  `json:"document_taxes_by_code"`
	Warnings            []Warning    `json:"warnings"`
	DeterminationTrace  []string     `json:"determination_trace"`
}

func validate(doc Document, cfg Config) error {
	if len(doc.Currency) != 3 || doc.Currency != strings.ToUpper(doc.Currency) {
		return invalid("currency", "currency must be a 3-letter ISO 4217 code")
	}
	if doc.Direction != DirOutput && doc.Direction != DirInput {
		return invalid("direction", "direction must be %q or %q", DirOutput, DirInput)
	}
	if doc.DocumentDate.IsZero() {
		return invalid("document_date", "document_date is required")
	}
	if len(doc.Lines) == 0 || len(doc.Lines) > maxLines {
		return invalid("lines", "lines must hold between 1 and %d entries", maxLines)
	}
	for i, l := range doc.Lines {
		if l.LineRef == "" || len(l.LineRef) > maxRefLength {
			return invalid(fmt.Sprintf("lines[%d].line_ref", i), "line_ref is required and at most %d characters", maxRefLength)
		}
	}
	if !oneOf(cfg.DefaultRounding, "", RoundPerLine, RoundPerDocument) {
		return invalid("config.default_rounding", "unknown rounding %q", cfg.DefaultRounding)
	}
	seen := map[string]bool{}
	for i, c := range cfg.Codes {
		field := fmt.Sprintf("config.codes[%d]", i)
		switch {
		case c.Code == "" || seen[c.Code]:
			return invalid(field+".code", "code is required and unique")
		case !oneOf(c.Mechanism, "", MechStandard, MechReverseCharge, MechUseTax):
			return invalid(field+".mechanism", "unknown mechanism %q", c.Mechanism)
		case !oneOf(c.CalcBasis, "", BasisLine, BasisDocument):
			return invalid(field+".calc_basis", "unknown calc_basis %q", c.CalcBasis)
		case !oneOf(c.Rounding, "", RoundPerLine, RoundPerDocument):
			return invalid(field+".rounding", "unknown rounding %q", c.Rounding)
		case c.CompoundSequence < 0:
			return invalid(field+".compound_sequence", "compound_sequence must not be negative")
		}
		seen[c.Code] = true
	}
	return nil
}

func oneOf(v string, options ...string) bool {
	for _, o := range options {
		if v == o {
			return true
		}
	}
	return false
}

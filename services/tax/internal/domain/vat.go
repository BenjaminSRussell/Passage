package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

const (
	BoxBase    = "base"
	BoxTax     = "tax"
	BoxPayable = "payable"

	SupplyGoods      = "goods"
	SupplyServices   = "services"
	SupplyTriangular = "triangular"
)

var boxPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,16}$`)

func IsEUMember(country string) bool { return euMembers[country] }

// BoxCode ties a return box to the tax code and direction whose amounts it sums.
type BoxCode struct {
	Code      string `json:"code"`
	Direction string `json:"direction"`
}

// Box defines one box of the VAT return.
type Box struct {
	Box   string    `json:"box"`
	Label string    `json:"label"`
	Kind  string    `json:"kind"`
	Sort  int       `json:"sort"`
	Codes []BoxCode `json:"codes"`
}

// ValidateBoxes checks a whole box set: unique boxes, known kinds, a mapping for every base and
// tax box, and no code and direction feeding the same kind of box twice (a double count).
func ValidateBoxes(boxes []Box) error {
	if len(boxes) > 100 {
		return invalid("boxes", "at most 100 boxes")
	}
	seen := map[string]bool{}
	feeds := map[string]string{}
	for i, b := range boxes {
		field := fmt.Sprintf("boxes[%d]", i)
		switch {
		case !boxPattern.MatchString(b.Box):
			return invalid(field+".box", "box is required, at most 16 letters, digits, dots, dashes or underscores")
		case seen[b.Box]:
			return invalid(field+".box", "box %q is listed twice", b.Box)
		case strings.TrimSpace(b.Label) == "" || len(b.Label) > 160:
			return invalid(field+".label", "label is required and at most 160 characters")
		case !oneOf(b.Kind, BoxBase, BoxTax, BoxPayable):
			return invalid(field+".kind", "kind must be %q, %q or %q", BoxBase, BoxTax, BoxPayable)
		case b.Kind != BoxPayable && len(b.Codes) == 0:
			return invalid(field+".codes", "a %s box needs at least one tax code", b.Kind)
		case b.Kind == BoxPayable && len(b.Codes) > 0:
			return invalid(field+".codes", "a payable box sums the whole period and takes no codes")
		}
		seen[b.Box] = true
		for j, c := range b.Codes {
			cf := fmt.Sprintf("%s.codes[%d]", field, j)
			if !ValidCode(c.Code) || !oneOf(c.Direction, DirOutput, DirInput, DirUse) {
				return invalid(cf, "a code and a direction of output, input or use are required")
			}
			key := b.Kind + "|" + c.Code + "|" + c.Direction
			if other, dup := feeds[key]; dup {
				return invalid(cf, "%s %s is already counted in box %s", c.Code, c.Direction, other)
			}
			feeds[key] = b.Box
		}
	}
	return nil
}

// VATTxn is one recorded tax transaction with the figures a return needs.
type VATTxn struct {
	Code             string
	Direction        string
	TaxDate          time.Time
	Amount           decimal.Decimal
	Base             *decimal.Decimal
	CounterpartyCtry string
	CounterpartyVAT  string
	SupplyKind       string
}

type BoxResult struct {
	Box    string          `json:"box"`
	Label  string          `json:"label"`
	Kind   string          `json:"kind"`
	Amount decimal.Decimal `json:"amount"`
}

type VATReturn struct {
	PeriodFrom   Date        `json:"period_from"`
	PeriodTo     Date        `json:"period_to"`
	Boxes        []BoxResult `json:"boxes"`
	Transactions int         `json:"transactions"`
	// MissingBase counts transactions that feed a base box but carry no base amount; the box
	// total is then understated and the return should not be filed until they are corrected.
	MissingBase int `json:"missing_base"`
	// Unmapped counts transactions whose code and direction feed no box at all.
	Unmapped int `json:"unmapped"`
}

func sign(direction string) decimal.Decimal {
	if direction == DirInput {
		return decimal.NewFromInt(-1)
	}
	return decimal.NewFromInt(1)
}

// ComputeVATReturn sums the boxes over the transactions of the period. Payable is output and
// use tax less input tax, so a negative payable is a refund claim.
func ComputeVATReturn(period Period, boxes []Box, txns []VATTxn) VATReturn {
	out := VATReturn{PeriodFrom: period.From, PeriodTo: period.To, Boxes: []BoxResult{}}
	sorted := append([]Box{}, boxes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Sort < sorted[j].Sort })
	totals := map[string]decimal.Decimal{}
	baseBoxes := map[BoxCode]bool{}
	anyBox := map[BoxCode]bool{}
	for _, b := range sorted {
		totals[b.Box] = decimal.Zero
		for _, c := range b.Codes {
			anyBox[c] = true
			if b.Kind == BoxBase {
				baseBoxes[c] = true
			}
		}
	}
	payable := decimal.Zero
	for _, t := range txns {
		if t.TaxDate.Before(period.From.Time) || t.TaxDate.After(period.To.Time) {
			continue
		}
		out.Transactions++
		key := BoxCode{Code: t.Code, Direction: t.Direction}
		if !anyBox[key] {
			out.Unmapped++
		}
		if baseBoxes[key] && t.Base == nil {
			out.MissingBase++
		}
		payable = payable.Add(t.Amount.Mul(sign(t.Direction)))
		for _, b := range sorted {
			for _, c := range b.Codes {
				if c != key {
					continue
				}
				switch {
				case b.Kind == BoxTax:
					totals[b.Box] = totals[b.Box].Add(t.Amount)
				case b.Kind == BoxBase && t.Base != nil:
					totals[b.Box] = totals[b.Box].Add(*t.Base)
				}
			}
		}
	}
	for _, b := range sorted {
		amount := totals[b.Box]
		if b.Kind == BoxPayable {
			amount = payable
		}
		out.Boxes = append(out.Boxes, BoxResult{Box: b.Box, Label: b.Label, Kind: b.Kind, Amount: amount})
	}
	return out
}

// ESLRow is one line of the EU sales list: the supplies to one VAT number of one kind.
type ESLRow struct {
	Country      string          `json:"country"`
	VATID        string          `json:"vat_id"`
	Indicator    string          `json:"indicator"`
	SupplyKind   string          `json:"supply_kind"`
	Base         decimal.Decimal `json:"base"`
	Transactions int             `json:"transactions"`
}

type ESL struct {
	PeriodFrom   Date     `json:"period_from"`
	PeriodTo     Date     `json:"period_to"`
	HomeCountry  string   `json:"home_country"`
	VATID        string   `json:"vat_id"`
	Rows         []ESLRow `json:"rows"`
	Total        string   `json:"total"`
	MissingVATID int      `json:"missing_vat_id"`
	MissingBase  int      `json:"missing_base"`
}

// ESLIndicator is the EU sales list indicator: L goods, S services, T triangulation.
func ESLIndicator(kind string) string {
	switch kind {
	case SupplyServices:
		return "S"
	case SupplyTriangular:
		return "T"
	}
	return "L"
}

// ComputeESL lists the output transactions of the period sold to another EU member state,
// grouped by country, VAT number and kind of supply. Transactions without a counterparty VAT
// number or a base amount are counted, not listed: such a line cannot be filed.
func ComputeESL(period Period, homeCountry, vatID string, txns []VATTxn) ESL {
	out := ESL{PeriodFrom: period.From, PeriodTo: period.To, HomeCountry: homeCountry, VATID: vatID, Rows: []ESLRow{}}
	type key struct{ country, vat, kind string }
	grouped := map[key]*ESLRow{}
	total := decimal.Zero
	for _, t := range txns {
		if t.Direction != DirOutput || t.TaxDate.Before(period.From.Time) || t.TaxDate.After(period.To.Time) ||
			!IsEUMember(t.CounterpartyCtry) || t.CounterpartyCtry == homeCountry {
			continue
		}
		if t.CounterpartyVAT == "" {
			out.MissingVATID++
			continue
		}
		if t.Base == nil {
			out.MissingBase++
			continue
		}
		kind := t.SupplyKind
		if kind == "" {
			kind = SupplyGoods
		}
		k := key{t.CounterpartyCtry, t.CounterpartyVAT, kind}
		row := grouped[k]
		if row == nil {
			row = &ESLRow{Country: k.country, VATID: k.vat, Indicator: ESLIndicator(kind), SupplyKind: kind}
			grouped[k] = row
		}
		row.Base = row.Base.Add(*t.Base)
		row.Transactions++
		total = total.Add(*t.Base)
	}
	for _, r := range grouped {
		out.Rows = append(out.Rows, *r)
	}
	sort.Slice(out.Rows, func(i, j int) bool {
		a, b := out.Rows[i], out.Rows[j]
		if a.Country != b.Country {
			return a.Country < b.Country
		}
		if a.VATID != b.VATID {
			return a.VATID < b.VATID
		}
		return a.SupplyKind < b.SupplyKind
	})
	out.Total = total.StringFixed(2)
	return out
}

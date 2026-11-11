package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Filing formats and national box layouts (13 deliverable 5). A layout names the boxes of one country's return so a tenant maps its
// tax codes to them once; a filing format turns the computed return into the file or figures that country's authority takes. They
// are written from the authorities' published layouts for the boxes that a sales and purchase ledger can fill; every figure is
// still reviewed by an accountant before filing (#512).

const (
	FormatGBMTD   = "gb-mtd-json"
	FormatDEUStVA = "de-ustva-csv"
	FormatDEZM    = "de-zm-csv"
)

// LayoutBox is one box of a national return. Role says which transactions belong in it, to guide the mapping.
type LayoutBox struct {
	Box   string `json:"box"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
	Role  string `json:"role"`
}

// Layout is one country's return layout and the filing format that goes with it.
type Layout struct {
	Country string      `json:"country"`
	Name    string      `json:"name"`
	Format  string      `json:"format"`
	Boxes   []LayoutBox `json:"boxes"`
}

var layouts = []Layout{
	{Country: "DE", Name: "Umsatzsteuer-Voranmeldung (UStVA)", Format: FormatDEUStVA, Boxes: []LayoutBox{
		{"81", "Kz 81 Taxable supplies at 19 percent (net)", BoxBase, "output, standard rate"},
		{"86", "Kz 86 Taxable supplies at 7 percent (net)", BoxBase, "output, reduced rate"},
		{"41", "Kz 41 Tax-free intra-Community supplies (net)", BoxBase, "output, zero-rated EU supplies"},
		{"61", "Kz 61 Input tax on intra-Community acquisitions", BoxTax, "use or reverse-charge input tax"},
		{"66", "Kz 66 Input tax on invoices from other businesses", BoxTax, "input"},
		{"83", "Kz 83 Remaining advance payment (or refund)", BoxPayable, "whole period"},
	}},
	{Country: "GB", Name: "VAT return (VAT100)", Format: FormatGBMTD, Boxes: []LayoutBox{
		{"1", "Box 1 VAT due on sales and other outputs", BoxTax, "output"},
		{"2", "Box 2 VAT due on acquisitions from other EU countries", BoxTax, "use or reverse-charge output"},
		{"4", "Box 4 VAT reclaimed on purchases and other inputs", BoxTax, "input"},
		{"5", "Box 5 Net VAT to pay to or reclaim from HMRC", BoxPayable, "whole period"},
		{"6", "Box 6 Total value of sales and other outputs, excluding VAT", BoxBase, "output"},
		{"7", "Box 7 Total value of purchases and other inputs, excluding VAT", BoxBase, "input"},
		{"8", "Box 8 Total value of supplies of goods to EU countries, excluding VAT", BoxBase, "output, EU goods"},
		{"9", "Box 9 Total value of acquisitions of goods from EU countries, excluding VAT", BoxBase, "input, EU goods"},
	}},
}

// Layouts lists the national layouts that ship with the service.
func Layouts() []Layout { return append([]Layout{}, layouts...) }

// LayoutFor returns the layout of a country.
func LayoutFor(country string) (Layout, bool) {
	for _, l := range layouts {
		if l.Country == country {
			return l, true
		}
	}
	return Layout{}, false
}

// BoxesFromLayout builds the box set of a layout from the tenant's mapping of each non-payable box to the tax codes that feed it. A
// box with no mapping is refused by name, so a half-mapped layout never replaces a working one.
func BoxesFromLayout(l Layout, mapping map[string][]BoxCode) ([]Box, error) {
	known := map[string]bool{}
	out := make([]Box, 0, len(l.Boxes))
	var missing []string
	for i, b := range l.Boxes {
		known[b.Box] = true
		box := Box{Box: b.Box, Label: b.Label, Kind: b.Kind, Sort: (i + 1) * 10, Codes: []BoxCode{}}
		if b.Kind != BoxPayable {
			box.Codes = mapping[b.Box]
			if len(box.Codes) == 0 {
				missing = append(missing, b.Box)
			}
		}
		out = append(out, box)
	}
	for k := range mapping {
		if !known[k] {
			return nil, invalid("mapping."+k, "%s has no box %s", l.Name, k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, invalid("mapping", "map tax codes to box %s of the %s layout", strings.Join(missing, ", "), l.Name)
	}
	if err := ValidateBoxes(out); err != nil {
		return nil, err
	}
	return out, nil
}

// FilingFile is a formatted return or list, ready to download.
type FilingFile struct {
	ContentType string
	Filename    string
	Body        []byte
}

func boxAmounts(r VATReturn, need ...string) (map[string]decimal.Decimal, error) {
	got := map[string]decimal.Decimal{}
	for _, b := range r.Boxes {
		got[b.Box] = b.Amount
	}
	var missing []string
	for _, n := range need {
		if _, ok := got[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, invalid("format", "the return has no box %s; apply the layout for this format first", strings.Join(missing, ", "))
	}
	return got, nil
}

func whole(d decimal.Decimal) decimal.Decimal { return d.Truncate(0) }

// HMRC takes numbers, not strings: amounts to the penny, values in whole pounds.
func pence(d decimal.Decimal) json.Number  { return json.Number(d.Round(2).StringFixed(2)) }
func pounds(d decimal.Decimal) json.Number { return json.Number(whole(d).String()) }

// FormatVATReturn renders the computed return in a filing format. periodKey is the authority's own period identifier (HMRC's
// obligation period key) where the format needs one. A return with transactions missing their base amount is refused: its base
// boxes are understated and filing it would be wrong.
func FormatVATReturn(format string, r VATReturn, periodKey string) (FilingFile, error) {
	if r.MissingBase > 0 {
		return FilingFile{}, invalid("format", "%d transactions have no base amount, so the return's base boxes are understated; correct them before filing", r.MissingBase)
	}
	period := r.PeriodFrom.Format(dateLayout) + "_" + r.PeriodTo.Format(dateLayout)
	switch format {
	case FormatGBMTD:
		if periodKey == "" || len(periodKey) > 8 {
			return FilingFile{}, invalid("period_key", "period_key is required: the obligation period key HMRC gave for this return")
		}
		b, err := boxAmounts(r, "1", "2", "4", "5", "6", "7", "8", "9")
		if err != nil {
			return FilingFile{}, err
		}
		totalDue := b["1"].Add(b["2"])
		body := struct {
			PeriodKey                    string      `json:"periodKey"`
			VATDueSales                  json.Number `json:"vatDueSales"`
			VATDueAcquisitions           json.Number `json:"vatDueAcquisitions"`
			TotalVATDue                  json.Number `json:"totalVatDue"`
			VATReclaimedCurrPeriod       json.Number `json:"vatReclaimedCurrPeriod"`
			NetVATDue                    json.Number `json:"netVatDue"`
			TotalValueSalesExVAT         json.Number `json:"totalValueSalesExVAT"`
			TotalValuePurchasesExVAT     json.Number `json:"totalValuePurchasesExVAT"`
			TotalValueGoodsSuppliedExVAT json.Number `json:"totalValueGoodsSuppliedExVAT"`
			TotalAcquisitionsExVAT       json.Number `json:"totalAcquisitionsExVAT"`
			Finalised                    bool        `json:"finalised"`
		}{periodKey, pence(b["1"]), pence(b["2"]), pence(totalDue), pence(b["4"]), pence(b["5"].Abs()),
			pounds(b["6"]), pounds(b["7"]), pounds(b["8"]), pounds(b["9"]), true}
		raw, err := json.MarshalIndent(body, "", "  ")
		if err != nil {
			return FilingFile{}, err
		}
		return FilingFile{ContentType: "application/json", Filename: "vat100-" + periodKey + ".json", Body: raw}, nil
	case FormatDEUStVA:
		b, err := boxAmounts(r, "81", "86", "41", "61", "66", "83")
		if err != nil {
			return FilingFile{}, err
		}
		var buf bytes.Buffer
		buf.WriteString("Kz;Betrag\n")
		for _, kz := range []string{"41", "81", "86"} {
			fmt.Fprintf(&buf, "%s;%s\n", kz, whole(b[kz]).String())
		}
		for _, kz := range []string{"61", "66", "83"} {
			fmt.Fprintf(&buf, "%s;%s\n", kz, b[kz].StringFixed(2))
		}
		return FilingFile{ContentType: "text/csv; charset=utf-8", Filename: "ustva-" + period + ".csv", Body: buf.Bytes()}, nil
	}
	return FilingFile{}, invalid("format", "unknown return format %q; use %s or %s", format, FormatGBMTD, FormatDEUStVA)
}

// FormatESL renders the EU sales list in a national file format. The German Zusammenfassende Meldung file lists the country, the
// VAT number without its country prefix, the amount in whole euros and the kind (empty for goods, S for services, D for triangular).
func FormatESL(format string, l ESL) (FilingFile, error) {
	if format != FormatDEZM {
		return FilingFile{}, invalid("format", "unknown sales list format %q; use %s", format, FormatDEZM)
	}
	if l.MissingVATID > 0 || l.MissingBase > 0 {
		return FilingFile{}, invalid("format", "%d sales lack a VAT number and %d lack a base amount, so the list is incomplete; correct them before filing", l.MissingVATID, l.MissingBase)
	}
	var buf bytes.Buffer
	buf.WriteString("Laenderkennzeichen;USt-IdNr;Betrag;Art\n")
	for _, r := range l.Rows {
		art := map[string]string{SupplyServices: "S", SupplyTriangular: "D"}[r.SupplyKind]
		fmt.Fprintf(&buf, "%s;%s;%s;%s\n", r.Country, strings.TrimPrefix(r.VATID, r.Country), whole(r.Base).String(), art)
	}
	return FilingFile{ContentType: "text/csv; charset=utf-8", Filename: "zm-" + l.PeriodFrom.Format(dateLayout) + "_" + l.PeriodTo.Format(dateLayout) + ".csv", Body: buf.Bytes()}, nil
}

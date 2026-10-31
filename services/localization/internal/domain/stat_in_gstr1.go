package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// IN_GSTR1: the GSTR-1 return JSON of the GSTN offline tool, sections b2b
// (table 4A) and hsn (table 12). The rules: supplier and recipient GSTINs with
// their check character, fp as MMYYYY, invoice numbers of at most 16
// characters (letters, digits, '/' and '-') unique in the return, invoice dates
// dd-mm-yyyy not after the period, place of supply a state code, the GST rate
// slabs, IGST alone on an inter-state supply (place of supply outside the
// supplier's state, or a SEZ or deemed export) and CGST = SGST otherwise, each
// within one rupee of taxable value x rate, and the HSN summary reconciling
// with the invoices. Sections other than b2b and hsn are reported as not
// covered rather than passed unchecked.

var (
	gstrPeriod  = regexp.MustCompile(`^(0[1-9]|1[0-2])20\d{2}$`)
	gstrInvNum  = regexp.MustCompile(`^[A-Za-z0-9/-]{1,16}$`)
	gstrHSN     = regexp.MustCompile(`^\d{4}(\d{2}(\d{2})?)?$`)
	gstrUQC     = regexp.MustCompile(`^[A-Z]{3}$`)
	gstrPOS     = regexp.MustCompile(`^(0[1-9]|[12]\d|3[0-8]|97)$`)
	gstrTaxKeys = []string{"txval", "iamt", "camt", "samt", "csamt"}
)

// GST rate slabs in percent, including the 40% slab of September 2025.
var gstrRates = []string{"0", "0.1", "0.25", "1", "1.5", "3", "5", "6", "7.5", "12", "18", "28", "40"}

// Rounding tolerance between a tax amount and taxable value x rate. ASSUMED:
// one rupee, pending GSTN confirmation of the portal's limit.
var gstrTolerance = decimal.NewFromInt(1)

var gstrKnownTop = map[string]bool{"gstin": true, "fp": true, "b2b": true, "hsn": true, "gt": true, "cur_gt": true, "version": true, "hash": true}

type gstrItemDetail struct {
	Txval decimal.Decimal `json:"txval"`
	Rt    decimal.Decimal `json:"rt"`
	Iamt  decimal.Decimal `json:"iamt"`
	Camt  decimal.Decimal `json:"camt"`
	Samt  decimal.Decimal `json:"samt"`
	Csamt decimal.Decimal `json:"csamt"`
}

func (d gstrItemDetail) amounts() map[string]decimal.Decimal {
	return map[string]decimal.Decimal{"txval": d.Txval, "iamt": d.Iamt, "camt": d.Camt, "samt": d.Samt, "csamt": d.Csamt}
}

type gstrInvoice struct {
	Inum   string          `json:"inum"`
	Idt    string          `json:"idt"`
	Val    decimal.Decimal `json:"val"`
	Pos    string          `json:"pos"`
	Rchrg  string          `json:"rchrg"`
	InvTyp string          `json:"inv_typ"`
	Itms   []struct {
		Num    int            `json:"num"`
		ItmDet gstrItemDetail `json:"itm_det"`
	} `json:"itms"`
}

type gstrHSNRow struct {
	Num   int             `json:"num"`
	HsnSc string          `json:"hsn_sc"`
	Desc  string          `json:"desc"`
	Uqc   string          `json:"uqc"`
	Qty   decimal.Decimal `json:"qty"`
	Rt    decimal.Decimal `json:"rt"`
	gstrItemDetail
}

type gstrReturn struct {
	Gstin string `json:"gstin"`
	Fp    string `json:"fp"`
	B2B   []struct {
		Ctin string        `json:"ctin"`
		Inv  []gstrInvoice `json:"inv"`
	} `json:"b2b"`
	HSN *struct {
		Data []gstrHSNRow `json:"data"`
	} `json:"hsn"`
}

// ValidateGSTR1 checks a GSTR-1 JSON against the rules above; it returns every failure.
func ValidateGSTR1(raw []byte) []FieldError {
	var errs fieldErrs
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		errs.add("json", "%s", err)
		return errs
	}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !gstrKnownTop[k] {
			errs.add(k, "section not covered by this validator (b2b and hsn only)")
		}
	}
	var r gstrReturn
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&r); err != nil {
		errs.add("json", "%s", err)
		return errs
	}
	if ValidateTaxID("IN_GSTIN", r.Gstin) != nil {
		errs.add("gstin", "%q is not a valid GSTIN", r.Gstin)
	}
	var periodEnd time.Time
	if !gstrPeriod.MatchString(r.Fp) {
		errs.add("fp", "%q must be the return period MMYYYY", r.Fp)
	} else {
		start, _ := time.Parse("012006", r.Fp)
		periodEnd = start.AddDate(0, 1, -1)
	}
	home := ""
	if len(r.Gstin) >= 2 {
		home = r.Gstin[:2]
	}
	if len(r.B2B) == 0 {
		errs.add("b2b", "is required")
	}
	sums := map[string]decimal.Decimal{}
	seen := map[string]bool{}
	for pi, party := range r.B2B {
		pf := fmt.Sprintf("b2b[%d]", pi)
		if ValidateTaxID("IN_GSTIN", party.Ctin) != nil {
			errs.add(pf+".ctin", "%q is not a valid GSTIN", party.Ctin)
		} else if party.Ctin == r.Gstin {
			errs.add(pf+".ctin", "the recipient cannot be the supplier")
		}
		for ii, inv := range party.Inv {
			f := fmt.Sprintf("%s.inv[%d]", pf, ii)
			gstrInvoiceChecks(&errs, f, inv, home, periodEnd, seen, sums)
		}
	}
	if r.HSN == nil || len(r.HSN.Data) == 0 {
		errs.add("hsn.data", "the HSN summary (table 12) is required")
		return errs
	}
	hsnSums := map[string]decimal.Decimal{}
	for i, row := range r.HSN.Data {
		f := fmt.Sprintf("hsn.data[%d]", i)
		if row.Num != i+1 {
			errs.add(f+".num", "rows are numbered 1, 2, 3 ...; found %d", row.Num)
		}
		if !gstrHSN.MatchString(row.HsnSc) {
			errs.add(f+".hsn_sc", "%q must be a 4, 6 or 8 digit HSN code", row.HsnSc)
		}
		if !gstrUQC.MatchString(row.Uqc) {
			errs.add(f+".uqc", "%q must be a GSTN unit quantity code", row.Uqc)
		}
		if row.Qty.IsNegative() {
			errs.add(f+".qty", "must not be negative")
		}
		if !gstrRateValid(row.Rt) {
			errs.add(f+".rt", "%s is not a GST rate", row.Rt)
		}
		for k, v := range row.amounts() {
			hsnSums[k] = hsnSums[k].Add(v)
		}
	}
	for _, k := range gstrTaxKeys {
		if !hsnSums[k].Equal(sums[k]) {
			errs.add("hsn.data", "%s totals %s, the invoices total %s", k, hsnSums[k].StringFixed(2), sums[k].StringFixed(2))
		}
	}
	return errs
}

func gstrInvoiceChecks(errs *fieldErrs, f string, inv gstrInvoice, home string, periodEnd time.Time, seen map[string]bool, sums map[string]decimal.Decimal) {
	if !gstrInvNum.MatchString(inv.Inum) {
		errs.add(f+".inum", "%q must be 1 to 16 letters, digits, '/' or '-'", inv.Inum)
	} else if key := strings.ToUpper(inv.Inum); seen[key] {
		errs.add(f+".inum", "%q appears twice in the return", inv.Inum)
	} else {
		seen[key] = true
	}
	if d, ok := validDate("02-01-2006", inv.Idt); !ok {
		errs.add(f+".idt", "%q must be a date dd-mm-yyyy", inv.Idt)
	} else if !periodEnd.IsZero() && d.After(periodEnd) {
		errs.add(f+".idt", "%s is after the return period", inv.Idt)
	}
	if !gstrPOS.MatchString(inv.Pos) {
		errs.add(f+".pos", "%q must be a two-digit state code", inv.Pos)
	}
	if inv.Rchrg != "Y" && inv.Rchrg != "N" {
		errs.add(f+".rchrg", "must be Y or N")
	}
	interState := inv.Pos != home
	switch inv.InvTyp {
	case "R", "DE":
	case "SEZWP", "SEZWOP", "CBW":
		interState = true
	default:
		errs.add(f+".inv_typ", "%q must be R, SEZWP, SEZWOP, DE or CBW", inv.InvTyp)
	}
	if !inv.Val.IsPositive() || !inv.Val.Equal(inv.Val.Truncate(2)) {
		errs.add(f+".val", "must be positive with at most two decimals")
	}
	if len(inv.Itms) == 0 {
		errs.add(f+".itms", "an invoice needs at least one item")
	}
	taxable := decimal.Zero
	for ti, it := range inv.Itms {
		itf := fmt.Sprintf("%s.itms[%d]", f, ti)
		d := it.ItmDet
		if it.Num != ti+1 {
			errs.add(itf+".num", "items are numbered 1, 2, 3 ...; found %d", it.Num)
		}
		if !gstrRateValid(d.Rt) {
			errs.add(itf+".rt", "%s is not a GST rate", d.Rt)
		}
		for _, k := range gstrTaxKeys {
			v := d.amounts()[k]
			if v.IsNegative() || !v.Equal(v.Truncate(2)) {
				errs.add(itf+"."+k, "must be non-negative with at most two decimals")
			}
			sums[k] = sums[k].Add(v)
		}
		taxable = taxable.Add(d.Txval)
		tax := d.Txval.Mul(d.Rt).Div(decimal.NewFromInt(100))
		if inv.InvTyp == "SEZWOP" {
			tax = decimal.Zero
		}
		if interState {
			if !d.Camt.IsZero() || !d.Samt.IsZero() {
				errs.add(itf, "an inter-state supply carries IGST only")
			}
			if d.Iamt.Sub(tax).Abs().GreaterThan(gstrTolerance) {
				errs.add(itf+".iamt", "%s is not taxable value x rate (%s)", d.Iamt, tax.StringFixed(2))
			}
			continue
		}
		if !d.Iamt.IsZero() {
			errs.add(itf+".iamt", "an intra-state supply carries CGST and SGST, not IGST")
		}
		if !d.Camt.Equal(d.Samt) {
			errs.add(itf+".samt", "CGST %s and SGST %s must be equal", d.Camt, d.Samt)
		}
		if d.Camt.Add(d.Samt).Sub(tax).Abs().GreaterThan(gstrTolerance) {
			errs.add(itf+".camt", "CGST + SGST %s is not taxable value x rate (%s)", d.Camt.Add(d.Samt), tax.StringFixed(2))
		}
	}
	if inv.Val.LessThan(taxable) {
		errs.add(f+".val", "invoice value %s is below its taxable value %s", inv.Val, taxable)
	}
}

func gstrRateValid(rt decimal.Decimal) bool {
	for _, r := range gstrRates {
		if rt.Equal(decimal.RequireFromString(r)) {
			return true
		}
	}
	return false
}

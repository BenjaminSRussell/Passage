package domain

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// CodePeppolRule is the EINV code of a failed Peppol BIS Billing 3.0 rule found by CheckPeppolRules.
const CodePeppolRule = "EINV-0114"

// xnode is one element of a parsed document: local name, attributes, direct text and children.
type xnode struct {
	name     string
	attrs    map[string]string
	text     string
	children []*xnode
}

func parseXML(doc string) (*xnode, error) {
	dec := xml.NewDecoder(strings.NewReader(doc))
	var root *xnode
	var stack []*xnode
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xnode{name: t.Name.Local, attrs: map[string]string{}}
			for _, a := range t.Attr {
				n.attrs[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.children = append(p.children, n)
			} else if root == nil {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text += string(t)
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("no document element")
	}
	return root, nil
}

// all returns the nodes found by following the names from n (each step one level of children).
func (n *xnode) all(path ...string) []*xnode {
	cur := []*xnode{n}
	for _, name := range path {
		var next []*xnode
		for _, c := range cur {
			for _, ch := range c.children {
				if ch.name == name {
					next = append(next, ch)
				}
			}
		}
		cur = next
	}
	return cur
}

func (n *xnode) first(path ...string) *xnode {
	if all := n.all(path...); len(all) > 0 {
		return all[0]
	}
	return nil
}

func (n *xnode) val(path ...string) string {
	if f := n.first(path...); f != nil {
		return strings.TrimSpace(f.text)
	}
	return ""
}

func (n *xnode) amountAt(path ...string) (decimal.Decimal, bool) {
	s := n.val(path...)
	if s == "" {
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(s)
	return d, err == nil
}

// CheckPeppolRules runs a Go-native subset of the Peppol BIS Billing 3.0 rules (the EN 16931 BR-*, BR-CO-*, BR-S-* and the
// PEPPOL-EN16931-R* and PEPPOL-COMMON rules) on a built UBL 2.1 invoice or credit note, as text. It is not the schematron: the official
// CEN and Peppol schematron needs an XSLT 2 processor, which the service does not carry, and it was run offline over the builder's output
// (testdata/peppol, TestPeppol50_MatchesSchematronReport). This subset runs on every stored document and catches what a later edit, a
// country pack or a hand-fed document could break: the identifiers, the mandatory header elements, both parties' name, address,
// country and endpoint, the lines, and the totals arithmetic including each VAT breakdown. The result is sorted by rule id.
func CheckPeppolRules(doc string) []Issue {
	root, err := parseXML(doc)
	if err != nil {
		return []Issue{{Code: CodePeppolRule, Reject: true, Detail: "the document is not well-formed XML: " + err.Error()}}
	}
	var issues []Issue
	fail := func(rule, format string, args ...any) {
		issues = append(issues, Issue{Code: CodePeppolRule, Reject: true, Detail: rule + ": " + fmt.Sprintf(format, args...)})
	}
	credit := root.name == "CreditNote"
	if root.name != "Invoice" && !credit {
		fail("UBL-SR", "the document element must be Invoice or CreditNote, not %s", root.name)
		return issues
	}
	lineName, qtyName := "InvoiceLine", "InvoicedQuantity"
	if credit {
		lineName, qtyName = "CreditNoteLine", "CreditedQuantity"
	}
	if root.val("CustomizationID") == "" {
		fail("BR-01", "an invoice shall have a specification identifier")
	} else if root.val("CustomizationID") != peppolCustomization {
		fail("PEPPOL-EN16931-R004", "the specification identifier must be %s", peppolCustomization)
	}
	if root.val("ProfileID") != peppolProfile {
		fail("PEPPOL-EN16931-R001", "the business process must be %s", peppolProfile)
	}
	if root.val("ID") == "" {
		fail("BR-02", "an invoice shall have an invoice number")
	}
	if root.val("IssueDate") == "" {
		fail("BR-03", "an invoice shall have an issue date")
	}
	typeCode := root.val("InvoiceTypeCode")
	if credit {
		typeCode = root.val("CreditNoteTypeCode")
	}
	if typeCode == "" {
		fail("BR-04", "an invoice shall have an invoice type code")
	}
	cur := root.val("DocumentCurrencyCode")
	if cur == "" {
		fail("BR-05", "an invoice shall have a document currency code")
	}
	if root.val("BuyerReference") == "" && root.first("OrderReference") == nil {
		fail("PEPPOL-EN16931-R003", "a buyer reference or an order reference shall be provided")
	}
	for _, p := range []struct{ tag, role, name, addr, country, endpoint string }{
		{"AccountingSupplierParty", "seller", "BR-06", "BR-08", "BR-09", "PEPPOL-EN16931-R020"},
		{"AccountingCustomerParty", "buyer", "BR-07", "BR-10", "BR-11", "PEPPOL-EN16931-R010"},
	} {
		party := root.first(p.tag, "Party")
		if party == nil {
			fail(p.name, "an invoice shall have the %s", p.tag)
			continue
		}
		if party.val("PartyLegalEntity", "RegistrationName") == "" {
			fail(p.name, "the %s shall have a name", p.role)
		}
		if party.first("PostalAddress") == nil {
			fail(p.addr, "the %s shall have a postal address", p.role)
		}
		if party.val("PostalAddress", "Country", "IdentificationCode") == "" {
			fail(p.country, "the %s postal address shall have a country code", p.role)
		}
		ep := party.first("EndpointID")
		switch {
		case ep == nil || strings.TrimSpace(ep.text) == "":
			fail(p.endpoint, "the %s electronic address shall be provided", p.role)
		case ep.attrs["schemeID"] == "":
			fail(p.endpoint, "the %s electronic address needs its scheme identifier", p.role)
		case !validEndpoint(ep.attrs["schemeID"], strings.TrimSpace(ep.text)):
			fail("PEPPOL-COMMON-R040", "the %s endpoint %s fails the check digit of scheme %s", p.role, strings.TrimSpace(ep.text), ep.attrs["schemeID"])
		}
	}
	seller := root.first("AccountingSupplierParty", "Party")
	if seller != nil && seller.val("PartyTaxScheme", "CompanyID") == "" && seller.val("PartyLegalEntity", "CompanyID") == "" {
		fail("BR-CO-26", "the seller shall have a VAT identifier or a legal registration identifier")
	}

	tot := root.first("LegalMonetaryTotal")
	if tot == nil {
		fail("BR-12", "an invoice shall have the document totals")
		return sortedIssues(issues)
	}
	lineExt, okLE := tot.amountAt("LineExtensionAmount")
	excl, okEx := tot.amountAt("TaxExclusiveAmount")
	incl, okIn := tot.amountAt("TaxInclusiveAmount")
	pay, okPay := tot.amountAt("PayableAmount")
	for _, c := range []struct {
		ok   bool
		rule string
		name string
	}{{okLE, "BR-12", "sum of invoice line net amount"}, {okEx, "BR-13", "invoice total amount without VAT"}, {okIn, "BR-14", "invoice total amount with VAT"}, {okPay, "BR-15", "amount due for payment"}} {
		if !c.ok {
			fail(c.rule, "an invoice shall have the %s", c.name)
		}
	}
	allow, _ := tot.amountAt("AllowanceTotalAmount")
	charge, _ := tot.amountAt("ChargeTotalAmount")
	prepaid, _ := tot.amountAt("PrepaidAmount")
	rounding, _ := tot.amountAt("PayableRoundingAmount")

	lines := root.all(lineName)
	if len(lines) == 0 {
		fail("BR-16", "an invoice shall have at least one invoice line")
	}
	type rk struct{ cat, pct string }
	taxable := map[rk]decimal.Decimal{}
	lineSum := decimal.Zero
	for _, l := range lines {
		id := l.val("ID")
		if id == "" {
			fail("BR-21", "each invoice line shall have an identifier")
		}
		if l.val(qtyName) == "" {
			fail("BR-22", "line %s shall have a quantity", id)
		}
		if q := l.first(qtyName); q != nil && q.attrs["unitCode"] == "" {
			fail("BR-23", "line %s quantity shall have a unit of measure", id)
		}
		amt, ok := l.amountAt("LineExtensionAmount")
		if !ok {
			fail("BR-24", "line %s shall have a net amount", id)
		}
		if l.val("Item", "Name") == "" {
			fail("BR-25", "line %s shall have an item name", id)
		}
		if _, ok := l.amountAt("Price", "PriceAmount"); !ok {
			fail("BR-26", "line %s shall have an item net price", id)
		}
		cat := l.first("Item", "ClassifiedTaxCategory")
		if cat == nil || cat.val("ID") == "" {
			fail("BR-CO-04", "line %s shall be categorised with a VAT category code", id)
			continue
		}
		pct := cat.val("Percent")
		if d, err := decimal.NewFromString(pct); err == nil {
			pct = d.String()
		}
		lineSum = lineSum.Add(amt)
		k := rk{cat.val("ID"), pct}
		taxable[k] = taxable[k].Add(amt)
	}
	if okLE && !lineSum.Equal(lineExt) {
		fail("BR-CO-10", "the sum of line net amounts %s differs from LineExtensionAmount %s", lineSum, lineExt)
	}
	sumDoc := func(tag string) decimal.Decimal {
		total := decimal.Zero
		for _, ac := range root.all("AllowanceCharge") {
			if (ac.val("ChargeIndicator") == "true") != (tag == "charge") {
				continue
			}
			a, _ := ac.amountAt("Amount")
			total = total.Add(a)
			cat := ac.first("TaxCategory")
			if cat == nil {
				fail("BR-CO-04", "a document %s shall be categorised with a VAT category code", tag)
				continue
			}
			pct := cat.val("Percent")
			if d, err := decimal.NewFromString(pct); err == nil {
				pct = d.String()
			}
			k := rk{cat.val("ID"), pct}
			if tag == "charge" {
				taxable[k] = taxable[k].Add(a)
			} else {
				taxable[k] = taxable[k].Sub(a)
			}
		}
		return total
	}
	if got := sumDoc("allowance"); !got.Equal(allow) {
		fail("BR-CO-11", "the document allowances %s differ from AllowanceTotalAmount %s", got, allow)
	}
	if got := sumDoc("charge"); !got.Equal(charge) {
		fail("BR-CO-12", "the document charges %s differ from ChargeTotalAmount %s", got, charge)
	}
	if okEx && okLE && !excl.Equal(lineExt.Sub(allow).Add(charge)) {
		fail("BR-CO-13", "TaxExclusiveAmount %s differs from lines less allowances plus charges %s", excl, lineExt.Sub(allow).Add(charge))
	}
	seen := map[rk]bool{}
	taxTotals := root.all("TaxTotal")
	if len(taxTotals) == 0 {
		fail("BR-CO-14", "an invoice shall have the VAT total")
	}
	// The first TaxTotal is in the document currency and carries the breakdowns; a second one (accounting currency) has none.
	var vat, sub decimal.Decimal
	for _, tt := range taxTotals[:min(1, len(taxTotals))] {
		vat, _ = tt.amountAt("TaxAmount")
		for _, st := range tt.all("TaxSubtotal") {
			cat := st.first("TaxCategory")
			if cat == nil {
				fail("BR-48", "each VAT breakdown shall have a category")
				continue
			}
			pct := cat.val("Percent")
			if d, err := decimal.NewFromString(pct); err == nil {
				pct = d.String()
			}
			k := rk{cat.val("ID"), pct}
			seen[k] = true
			base, _ := st.amountAt("TaxableAmount")
			tax, _ := st.amountAt("TaxAmount")
			sub = sub.Add(tax)
			if !base.Equal(taxable[k]) {
				fail("BR-"+strings.ToUpper(k.cat)+"-08", "the taxable amount %s of category %s at %s%% differs from its lines, allowances and charges %s", base, k.cat, k.pct, taxable[k])
			}
			rate, _ := decimal.NewFromString(pct)
			if want := base.Mul(rate).Div(hundred).Round(2); !tax.Equal(want) {
				fail("BR-CO-17", "the VAT of category %s at %s%% is %s, not %s", k.cat, k.pct, tax, want)
			}
		}
	}
	for k := range taxable {
		if !seen[k] {
			fail("BR-CO-18", "category %s at %s%% is used by a line but has no VAT breakdown", k.cat, k.pct)
		}
	}
	if len(taxTotals) > 0 && !sub.Equal(vat) {
		fail("BR-CO-14", "the VAT total %s differs from the sum of the VAT breakdowns %s", vat, sub)
	}
	if okIn && okEx && len(taxTotals) > 0 && !incl.Equal(excl.Add(vat)) {
		fail("BR-CO-15", "TaxInclusiveAmount %s differs from TaxExclusiveAmount plus VAT %s", incl, excl.Add(vat))
	}
	if okPay && okIn && !pay.Equal(incl.Sub(prepaid).Add(rounding)) {
		fail("BR-CO-16", "PayableAmount %s differs from the inclusive amount less prepaid plus rounding %s", pay, incl.Sub(prepaid).Add(rounding))
	}
	return sortedIssues(issues)
}

func sortedIssues(in []Issue) []Issue {
	sort.SliceStable(in, func(i, j int) bool { return in[i].Detail < in[j].Detail })
	return in
}

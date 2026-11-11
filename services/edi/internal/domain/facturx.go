package domain

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Factur-X / ZUGFeRD (#35, 35-2): the EN 16931 profile of the UN/CEFACT Cross Industry Invoice (CII D16B) and a PDF that carries it as
// the embedded file factur-x.xml. BuildCII writes the XML from the canonical invoice the Peppol builder takes, so both documents state
// the same figures; FacturXPDF puts the XML into a PDF whose page shows the invoice in words.
const (
	MandateFacturX = "FACTURX"

	facturxGuideline = "urn:cen.eu:en16931:2017"
	nsRSM            = "urn:un:unece:uncefact:data:standard:CrossIndustryInvoice:100"
	nsRAM            = "urn:un:unece:uncefact:data:standard:ReusableAggregateBusinessInformationEntity:100"
	nsUDT            = "urn:un:unece:uncefact:data:standard:UnqualifiedDataType:100"
)

type facturxMandate struct{}

func (facturxMandate) Code() string                           { return MandateFacturX }
func (facturxMandate) Format() string                         { return "FACTURX_EN16931" }
func (facturxMandate) Build(inv UBLInvoice) (string, []Issue) { return BuildCII(inv) }

func ram(name, body string) string { return "<ram:" + name + ">" + body + "</ram:" + name + ">" }

func ciiDate(iso string) string {
	if t, err := time.Parse("2006-01-02", iso); err == nil {
		return t.Format("20060102")
	}
	return iso
}

func ciiDateTime(name, iso string) string {
	return ram(name, `<udt:DateTimeString format="102">`+ciiDate(iso)+`</udt:DateTimeString>`)
}

func ciiAmount(v decimal.Decimal) string { return v.StringFixed(2) }

func ciiTax(category string, percent decimal.Decimal, withAmounts bool, calc, basis decimal.Decimal) string {
	var b strings.Builder
	if withAmounts {
		b.WriteString(ram("CalculatedAmount", ciiAmount(calc)))
	}
	b.WriteString(ram("TypeCode", "VAT"))
	if withAmounts {
		b.WriteString(ram("BasisAmount", ciiAmount(basis)))
	}
	b.WriteString(ram("CategoryCode", category))
	b.WriteString(ram("RateApplicablePercent", rate(category, percent)))
	return b.String()
}

func ciiParty(p UBLParty) string {
	var b strings.Builder
	b.WriteString(ram("Name", esc(p.Name)))
	if p.LegalID != "" {
		b.WriteString(ram("SpecifiedLegalOrganization", ram("ID", esc(p.LegalID))))
	}
	if p.ContactName != "" || p.ContactPhone != "" || p.ContactEmail != "" {
		var c strings.Builder
		if p.ContactName != "" {
			c.WriteString(ram("PersonName", esc(p.ContactName)))
		}
		if p.ContactPhone != "" {
			c.WriteString(ram("TelephoneUniversalCommunication", ram("CompleteNumber", esc(p.ContactPhone))))
		}
		if p.ContactEmail != "" {
			c.WriteString(ram("EmailURIUniversalCommunication", ram("URIID", esc(p.ContactEmail))))
		}
		b.WriteString(ram("DefinedTradeContact", c.String()))
	}
	var addr strings.Builder
	if p.PostalZone != "" {
		addr.WriteString(ram("PostcodeCode", esc(p.PostalZone)))
	}
	if p.StreetName != "" {
		addr.WriteString(ram("LineOne", esc(p.StreetName)))
	}
	if p.CityName != "" {
		addr.WriteString(ram("CityName", esc(p.CityName)))
	}
	addr.WriteString(ram("CountryID", esc(p.CountryCode)))
	b.WriteString(ram("PostalTradeAddress", addr.String()))
	if p.EndpointID != "" {
		b.WriteString(ram("URIUniversalCommunication", `<ram:URIID schemeID="`+esc(p.EndpointScheme)+`">`+esc(p.EndpointID)+`</ram:URIID>`))
	}
	if p.VATID != "" {
		b.WriteString(ram("SpecifiedTaxRegistration", `<ram:ID schemeID="VA">`+esc(p.VATID)+`</ram:ID>`))
	}
	return b.String()
}

// BuildCII returns the Factur-X EN 16931 XML (CII D16B) of an invoice or credit note, or the blocking issues. The invoice is checked
// with ValidateUBL first: the amounts and VAT rules are the EN 16931 ones both syntaxes share.
func BuildCII(inv UBLInvoice) (string, []Issue) {
	if issues := ValidateUBL(inv); len(issues) > 0 {
		return "", issues
	}
	t := ComputeUBLTotals(inv)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<rsm:CrossIndustryInvoice xmlns:rsm="` + nsRSM + `" xmlns:ram="` + nsRAM + `" xmlns:udt="` + nsUDT + `">`)
	b.WriteString("<rsm:ExchangedDocumentContext>" + ram("GuidelineSpecifiedDocumentContextParameter", ram("ID", facturxGuideline)) + "</rsm:ExchangedDocumentContext>")
	typeCode := ublCodeInvoice
	if inv.CreditNote {
		typeCode = ublCodeCreditNote
	}
	b.WriteString("<rsm:ExchangedDocument>" + ram("ID", esc(inv.ID)) + ram("TypeCode", typeCode) + ciiDateTime("IssueDateTime", inv.IssueDate) + "</rsm:ExchangedDocument>")
	b.WriteString("<rsm:SupplyChainTradeTransaction>")
	for i, l := range inv.Lines {
		lineTotal := t.Lines[i].Amount
		b.WriteString(ram("IncludedSupplyChainTradeLineItem",
			ram("AssociatedDocumentLineDocument", ram("LineID", esc(l.ID)))+
				ram("SpecifiedTradeProduct", ram("Name", esc(l.Name)))+
				ram("SpecifiedLineTradeAgreement", ram("NetPriceProductTradePrice", ram("ChargeAmount", l.UnitPrice.String())))+
				ram("SpecifiedLineTradeDelivery", `<ram:BilledQuantity unitCode="`+esc(l.UnitCode)+`">`+l.Quantity.String()+`</ram:BilledQuantity>`)+
				ram("SpecifiedLineTradeSettlement",
					ram("ApplicableTradeTax", ciiTax(l.Category, l.Percent, false, decimal.Zero, decimal.Zero))+
						ram("SpecifiedTradeSettlementLineMonetarySummation", ram("LineTotalAmount", ciiAmount(lineTotal))))))
	}
	agreement := ""
	if inv.BuyerReference != "" {
		agreement += ram("BuyerReference", esc(inv.BuyerReference))
	}
	agreement += ram("SellerTradeParty", ciiParty(inv.Supplier)) + ram("BuyerTradeParty", ciiParty(inv.Customer))
	if inv.OrderReference != "" {
		agreement += ram("BuyerOrderReferencedDocument", ram("IssuerAssignedID", esc(inv.OrderReference)))
	}
	b.WriteString(ram("ApplicableHeaderTradeAgreement", agreement))
	delivery := ""
	if inv.DeliveryDate != "" {
		delivery = ram("ActualDeliverySupplyChainEvent", ciiDateTime("OccurrenceDateTime", inv.DeliveryDate))
	}
	b.WriteString(ram("ApplicableHeaderTradeDelivery", delivery))

	var s strings.Builder
	s.WriteString(ram("InvoiceCurrencyCode", esc(inv.Currency)))
	s.WriteString(ram("SpecifiedTradeSettlementPaymentMeans", ram("TypeCode", paymentCode(inv))+ram("PayeePartyCreditorFinancialAccount", ram("IBANID", esc(inv.IBAN)))))
	for _, st := range t.TaxSubtotals {
		extra := ""
		if r := inv.ExemptionReasons[st.Category]; r != "" {
			extra = ram("ExemptionReason", esc(r))
		}
		s.WriteString(ram("ApplicableTradeTax", ciiTaxWithReason(st, extra)))
	}
	for _, ac := range inv.AllowanceCharges {
		ind := "false"
		if ac.Charge {
			ind = "true"
		}
		s.WriteString(ram("SpecifiedTradeAllowanceCharge",
			ram("ChargeIndicator", "<udt:Indicator>"+ind+"</udt:Indicator>")+ram("ActualAmount", ciiAmount(ac.Amount.Round(2)))+ram("Reason", esc(ac.Reason))+
				ram("CategoryTradeTax", ram("TypeCode", "VAT")+ram("CategoryCode", ac.Category)+ram("RateApplicablePercent", rate(ac.Category, ac.Percent)))))
	}
	s.WriteString(ram("SpecifiedTradePaymentTerms", ciiDateTime("DueDateDateTime", inv.DueDate)))
	sum := ram("LineTotalAmount", ciiAmount(t.LineExtension))
	if t.Charges.IsPositive() {
		sum += ram("ChargeTotalAmount", ciiAmount(t.Charges))
	}
	if t.Allowances.IsPositive() {
		sum += ram("AllowanceTotalAmount", ciiAmount(t.Allowances))
	}
	sum += ram("TaxBasisTotalAmount", ciiAmount(t.TaxExclusive))
	sum += `<ram:TaxTotalAmount currencyID="` + esc(inv.Currency) + `">` + ciiAmount(t.TaxAmount) + `</ram:TaxTotalAmount>`
	sum += ram("GrandTotalAmount", ciiAmount(t.TaxInclusive))
	if t.Prepaid.IsPositive() {
		sum += ram("TotalPrepaidAmount", ciiAmount(t.Prepaid))
	}
	sum += ram("DuePayableAmount", ciiAmount(t.Payable))
	s.WriteString(ram("SpecifiedTradeSettlementHeaderMonetarySummation", sum))
	b.WriteString(ram("ApplicableHeaderTradeSettlement", s.String()))
	b.WriteString("</rsm:SupplyChainTradeTransaction></rsm:CrossIndustryInvoice>")
	return b.String(), nil
}

func ciiTaxWithReason(st TaxSubtotal, reason string) string {
	return ram("CalculatedAmount", ciiAmount(st.Tax)) + ram("TypeCode", "VAT") + reason + ram("BasisAmount", ciiAmount(st.Taxable)) +
		ram("CategoryCode", st.Category) + ram("RateApplicablePercent", rate(st.Category, st.Percent))
}

// pdfText escapes a string for a PDF literal string in WinAnsi: characters outside Latin-1 are shown as a question mark.
func pdfText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 32:
			b.WriteByte(' ')
		case r > 255:
			b.WriteByte('?')
		case r > 126:
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// FacturXPDF builds the Factur-X PDF of a stored CII document: a one-page A4 invoice summary (number, date, the parties, the lines
// and the totals, read from the XML) with the XML embedded as factur-x.xml (AFRelationship Data) and the XMP metadata that names
// the PDF/A-3 part and the Factur-X profile. The PDF carries the structure of PDF/A-3b; it is not certified: the fonts are the
// standard 14 (not embedded) and there is no ICC output intent, so a strict validator (veraPDF) will say so. The legal document is
// the embedded XML.
func FacturXPDF(cii string, created time.Time) ([]byte, error) {
	root, err := parseXML(cii)
	if err != nil || root.name != "CrossIndustryInvoice" {
		return nil, fmt.Errorf("not a Factur-X CII document")
	}
	tx := root.first("SupplyChainTradeTransaction")
	if tx == nil {
		return nil, fmt.Errorf("the CII document has no SupplyChainTradeTransaction")
	}
	doc := root.first("ExchangedDocument")
	number := doc.val("ID")
	title := "Invoice"
	if doc.val("TypeCode") == ublCodeCreditNote {
		title = "Credit note"
	}
	agreement := tx.first("ApplicableHeaderTradeAgreement")
	settle := tx.first("ApplicableHeaderTradeSettlement")
	cur := settle.val("InvoiceCurrencyCode")
	sum := settle.first("SpecifiedTradeSettlementHeaderMonetarySummation")

	var lines []string
	y := 800
	put := func(size int, s string) {
		lines = append(lines, fmt.Sprintf("BT /F1 %d Tf 50 %d Td (%s) Tj ET", size, y, pdfText(s)))
		y -= size + 6
	}
	put(18, title+" "+number)
	d := doc.first("IssueDateTime", "DateTimeString")
	if d != nil && len(strings.TrimSpace(d.text)) == 8 {
		v := strings.TrimSpace(d.text)
		put(10, "Issue date: "+v[:4]+"-"+v[4:6]+"-"+v[6:])
	}
	if due := settle.first("SpecifiedTradePaymentTerms", "DueDateDateTime", "DateTimeString"); due != nil && len(strings.TrimSpace(due.text)) == 8 {
		v := strings.TrimSpace(due.text)
		put(10, "Due date: "+v[:4]+"-"+v[4:6]+"-"+v[6:])
	}
	for _, p := range []struct{ label, tag string }{{"From: ", "SellerTradeParty"}, {"To: ", "BuyerTradeParty"}} {
		party := agreement.first(p.tag)
		if party == nil {
			continue
		}
		addr := party.first("PostalTradeAddress")
		put(10, p.label+party.val("Name")+", "+addr.val("LineOne")+", "+addr.val("PostcodeCode")+" "+addr.val("CityName")+" "+addr.val("CountryID"))
	}
	if ref := agreement.val("BuyerOrderReferencedDocument", "IssuerAssignedID"); ref != "" {
		put(10, "Your order: "+ref)
	}
	if ref := agreement.val("BuyerReference"); ref != "" {
		put(10, "Your reference: "+ref)
	}
	y -= 8
	put(10, "No.  Description                                  Quantity   Unit price   VAT %   Amount ("+cur+")")
	for _, l := range tx.all("IncludedSupplyChainTradeLineItem") {
		q := l.first("SpecifiedLineTradeDelivery", "BilledQuantity")
		qty, unit := "", ""
		if q != nil {
			qty, unit = strings.TrimSpace(q.text), q.attrs["unitCode"]
		}
		put(10, fmt.Sprintf("%-4s %-40s %s %s   %s   %s   %s", l.val("AssociatedDocumentLineDocument", "LineID"), l.val("SpecifiedTradeProduct", "Name"), qty, unit,
			l.val("SpecifiedLineTradeAgreement", "NetPriceProductTradePrice", "ChargeAmount"), l.val("SpecifiedLineTradeSettlement", "ApplicableTradeTax", "RateApplicablePercent"),
			l.val("SpecifiedLineTradeSettlement", "SpecifiedTradeSettlementLineMonetarySummation", "LineTotalAmount")))
	}
	y -= 8
	for _, row := range []struct{ label, tag string }{{"Net total", "TaxBasisTotalAmount"}, {"VAT", "TaxTotalAmount"}, {"Total", "GrandTotalAmount"}, {"Amount due", "DuePayableAmount"}} {
		put(10, fmt.Sprintf("%s: %s %s", row.label, sum.val(row.tag), cur))
	}
	if iban := settle.val("SpecifiedTradeSettlementPaymentMeans", "PayeePartyCreditorFinancialAccount", "IBANID"); iban != "" {
		put(10, "Pay to IBAN "+iban)
	}
	put(8, "This PDF carries the invoice as structured data (Factur-X, EN 16931) in the attachment factur-x.xml.")

	stamp := created.UTC().Format("20060102150405")
	iso := created.UTC().Format("2006-01-02T15:04:05Z")
	xmp := `<?xpacket begin="` + "\xef\xbb\xbf" + `" id="W5M0MpCehiHzreSzNTczkc9d"?>
<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about="" xmlns:pdfaid="http://www.aiim.org/pdfa/ns/id/"><pdfaid:part>3</pdfaid:part><pdfaid:conformance>B</pdfaid:conformance></rdf:Description>
<rdf:Description rdf:about="" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title><rdf:Alt><rdf:li xml:lang="x-default">` + esc(title+" "+number) + `</rdf:li></rdf:Alt></dc:title></rdf:Description>
<rdf:Description rdf:about="" xmlns:pdf="http://ns.adobe.com/pdf/1.3/"><pdf:Producer>Passage EDI</pdf:Producer></rdf:Description>
<rdf:Description rdf:about="" xmlns:xmp="http://ns.adobe.com/xap/1.0/"><xmp:CreateDate>` + iso + `</xmp:CreateDate><xmp:ModifyDate>` + iso + `</xmp:ModifyDate></rdf:Description>
<rdf:Description rdf:about="" xmlns:fx="urn:factur-x:pdfa:CrossIndustryDocument:invoice:1p0#"><fx:DocumentType>INVOICE</fx:DocumentType><fx:DocumentFileName>factur-x.xml</fx:DocumentFileName><fx:Version>1.0</fx:Version><fx:ConformanceLevel>EN 16931</fx:ConformanceLevel></rdf:Description>
<rdf:Description rdf:about="" xmlns:pdfaExtension="http://www.aiim.org/pdfa/ns/extension/" xmlns:pdfaSchema="http://www.aiim.org/pdfa/ns/schema#" xmlns:pdfaProperty="http://www.aiim.org/pdfa/ns/property#"><pdfaExtension:schemas><rdf:Bag><rdf:li rdf:parseType="Resource"><pdfaSchema:schema>Factur-X PDFA Extension Schema</pdfaSchema:schema><pdfaSchema:namespaceURI>urn:factur-x:pdfa:CrossIndustryDocument:invoice:1p0#</pdfaSchema:namespaceURI><pdfaSchema:prefix>fx</pdfaSchema:prefix><pdfaSchema:property><rdf:Seq>` +
		fxProp("DocumentFileName", "name of the embedded XML invoice file") + fxProp("DocumentType", "INVOICE") + fxProp("Version", "The actual version of the Factur-X XML schema") + fxProp("ConformanceLevel", "The conformance level of the embedded Factur-X data") +
		`</rdf:Seq></pdfaSchema:property></rdf:li></rdf:Bag></pdfaExtension:schemas></rdf:Description>
</rdf:RDF></x:xmpmeta><?xpacket end="w"?>`

	var out bytes.Buffer
	var offsets []int
	obj := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	stream := func(dict, data string) string {
		return fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dict, len(data), data)
	}
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	obj("<< /Type /Catalog /Pages 2 0 R /Metadata 5 0 R /AF [7 0 R] /Names << /EmbeddedFiles << /Names [(factur-x.xml) 7 0 R] >> >> /MarkInfo << /Marked true >> >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Contents 4 0 R /Resources << /Font << /F1 6 0 R >> >> >>")
	obj(stream("", strings.Join(lines, "\n")))
	obj(stream("/Type /Metadata /Subtype /XML", xmp))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	obj("<< /Type /Filespec /F (factur-x.xml) /UF (factur-x.xml) /Desc (Factur-X invoice) /AFRelationship /Data /EF << /F 8 0 R /UF 8 0 R >> >>")
	obj(stream(fmt.Sprintf("/Type /EmbeddedFile /Subtype /text#2Fxml /Params << /Size %d /ModDate (D:%s) >>", len(cii), stamp), cii))
	obj(fmt.Sprintf("<< /Producer (Passage EDI) /CreationDate (D:%s) /ModDate (D:%s) >>", stamp, stamp))
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", o)
	}
	id := fmt.Sprintf("%x", fnv64(cii))
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 9 0 R /ID [<%s%s> <%s%s>] >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, id, id, id, id, xref)
	return out.Bytes(), nil
}

func fxProp(name, desc string) string {
	return `<rdf:li rdf:parseType="Resource"><pdfaProperty:name>` + name + `</pdfaProperty:name><pdfaProperty:valueType>Text</pdfaProperty:valueType><pdfaProperty:category>external</pdfaProperty:category><pdfaProperty:description>` + desc + `</pdfaProperty:description></rdf:li>`
}

func fnv64(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

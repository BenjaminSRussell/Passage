package domain

import (
	"encoding/json"
	"fmt"
)

// MaskedValue replaces a personal-data value the caller may not see (BR-35.12).
const MaskedValue = "***"

// Element is one data element in the segment tree. Ref is the X12 reference
// (segment id plus two-digit position, e.g. N102); Name is the dictionary name
// the viewer shows on hover, empty when the element is not in the dictionary.
type Element struct {
	Ref    string `json:"ref"`
	Name   string `json:"name,omitempty"`
	Value  string `json:"value"`
	Masked bool   `json:"masked,omitempty"`
}

// Node is one level of the tree: interchange (ISA), group (GS), transaction
// (ST) or segment.
type Node struct {
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	Elements []Element `json:"elements,omitempty"`
	Children []Node    `json:"children,omitempty"`
}

// Tree renders a parsed interchange as the segment tree of the raw payload
// viewer (FR-35.10, §6.35.6 interaction rules). With maskPII set, address and
// contact elements are replaced by MaskedValue and flagged (BR-35.12). The
// trailers (SE, GE, IEA) carry only counts and control numbers already shown
// on their headers, so they are left out.
func Tree(ic Interchange, maskPII bool) Node {
	root := Node{Kind: "interchange", Name: "ISA", Elements: pairs(
		"ISA05", ic.Sender.Qualifier, "ISA06", ic.Sender.ID,
		"ISA07", ic.Receiver.Qualifier, "ISA08", ic.Receiver.ID,
		"ISA09", ic.Date, "ISA10", ic.Time, "ISA12", ic.Version, "ISA13", ic.Control, "ISA15", ic.Usage)}
	for _, g := range ic.Groups {
		gn := Node{Kind: "group", Name: "GS", Elements: pairs(
			"GS01", g.FunctionalID, "GS02", g.Sender, "GS03", g.Receiver,
			"GS04", g.Date, "GS05", g.Time, "GS06", g.Control, "GS08", g.Version)}
		for _, t := range g.Transactions {
			tn := Node{Kind: "transaction", Name: "ST", Elements: pairs("ST01", t.SetID, "ST02", t.Control)}
			for _, s := range t.Body {
				tn.Children = append(tn.Children, segmentNode(s, maskPII))
			}
			gn.Children = append(gn.Children, tn)
		}
		root.Children = append(root.Children, gn)
	}
	return root
}

// pairs builds elements from alternating ref, value arguments.
func pairs(kv ...string) []Element {
	out := make([]Element, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, Element{Ref: kv[i], Name: dictionary[kv[i]], Value: kv[i+1]})
	}
	return out
}

func segmentNode(s Segment, maskPII bool) Node {
	n := Node{Kind: "segment", Name: s.ID, Elements: make([]Element, 0, len(s.Elements))}
	for i, v := range s.Elements {
		ref := fmt.Sprintf("%s%02d", s.ID, i+1)
		e := Element{Ref: ref, Name: dictionary[ref], Value: v}
		if maskPII && v != "" && PIIElement(s.ID, i+1) {
			e.Value, e.Masked = MaskedValue, true
		}
		n.Elements = append(n.Elements, e)
	}
	return n
}

// PIIElement reports whether element pos (one based) of segment id holds
// personal data under BR-35.12: the party name (N102), the additional name
// lines (N2), every address line (N3, N4) and the contact name and numbers of
// PER (PER02, PER04, PER06, PER08). Entity and qualifier codes, and the GLN in
// N104, stay visible because routing and support depend on them.
func PIIElement(id string, pos int) bool {
	switch id {
	case "N2", "N3", "N4":
		return true
	case "N1":
		return pos == 2
	case "PER":
		return pos == 2 || pos == 4 || pos == 6 || pos == 8
	}
	return false
}

// piiCanonicalKeys are the canonical document fields that carry the N102
// party names (Order.ShipToName and BillToName, Location.Name).
var piiCanonicalKeys = map[string]bool{"shipToName": true, "billToName": true, "name": true}

// MaskCanonical returns the canonical document with every party name replaced
// by MaskedValue (BR-35.12 applied to the canonical view beside the raw one).
// A document that is not JSON is returned as it is.
func MaskCanonical(doc []byte) []byte {
	if len(doc) == 0 {
		return doc
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return doc
	}
	out, err := json.Marshal(maskValue(v))
	if err != nil {
		return doc
	}
	return out
}

func maskValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if s, ok := val.(string); ok && piiCanonicalKeys[k] && s != "" {
				t[k] = MaskedValue
				continue
			}
			t[k] = maskValue(val)
		}
	case []any:
		for i := range t {
			t[i] = maskValue(t[i])
		}
	}
	return v
}

// dictionary gives the X12 004010 dictionary name for the elements of the
// envelopes and of the sets this service reads and writes (850, 855, 856, 810,
// 820, 214, 997). The viewer shows the ref alone for anything not listed.
var dictionary = map[string]string{
	"ISA05": "Interchange ID Qualifier", "ISA06": "Interchange Sender ID",
	"ISA07": "Interchange ID Qualifier", "ISA08": "Interchange Receiver ID",
	"ISA09": "Interchange Date", "ISA10": "Interchange Time",
	"ISA12": "Interchange Control Version Number", "ISA13": "Interchange Control Number",
	"ISA15": "Usage Indicator",
	"GS01":  "Functional Identifier Code", "GS02": "Application Sender's Code",
	"GS03": "Application Receiver's Code", "GS04": "Date", "GS05": "Time",
	"GS06": "Group Control Number", "GS08": "Version / Release / Industry Identifier Code",
	"ST01": "Transaction Set Identifier Code", "ST02": "Transaction Set Control Number",
	"BEG01": "Transaction Set Purpose Code", "BEG02": "Purchase Order Type Code",
	"BEG03": "Purchase Order Number", "BEG05": "Date",
	"CUR01": "Entity Identifier Code", "CUR02": "Currency Code",
	"REF01": "Reference Identification Qualifier", "REF02": "Reference Identification",
	"DTM01": "Date/Time Qualifier", "DTM02": "Date",
	"PER01": "Contact Function Code", "PER02": "Name",
	"PER03": "Communication Number Qualifier", "PER04": "Communication Number",
	"PER05": "Communication Number Qualifier", "PER06": "Communication Number",
	"N101": "Entity Identifier Code", "N102": "Name",
	"N103": "Identification Code Qualifier", "N104": "Identification Code",
	"N201": "Name", "N202": "Name",
	"N301": "Address Information", "N302": "Address Information",
	"N401": "City Name", "N402": "State or Province Code", "N403": "Postal Code", "N404": "Country Code",
	"PO101": "Assigned Identification", "PO102": "Quantity Ordered",
	"PO103": "Unit or Basis for Measurement Code", "PO104": "Unit Price",
	"PO105": "Basis of Unit Price Code", "PO106": "Product/Service ID Qualifier", "PO107": "Product/Service ID",
	"PID01": "Item Description Type", "PID05": "Description",
	"SAC01": "Allowance or Charge Indicator", "SAC02": "Service, Promotion, Allowance, or Charge Code",
	"SAC05": "Amount",
	"CTT01": "Number of Line Items",
	"BSN01": "Transaction Set Purpose Code", "BSN02": "Shipment Identification",
	"BSN03": "Date", "BSN04": "Time",
	"HL01": "Hierarchical ID Number", "HL02": "Hierarchical Parent ID Number", "HL03": "Hierarchical Level Code",
	"TD101": "Packaging Code", "TD102": "Lading Quantity", "TD106": "Weight Qualifier",
	"TD107": "Weight", "TD108": "Unit or Basis for Measurement Code",
	"TD501": "Routing Sequence Code", "TD502": "Identification Code Qualifier", "TD503": "Identification Code",
	"PRF01": "Purchase Order Number",
	"MAN01": "Marks and Numbers Qualifier", "MAN02": "Marks and Numbers",
	"LIN02": "Product/Service ID Qualifier", "LIN03": "Product/Service ID",
	"SN102": "Number of Units Shipped", "SN103": "Unit or Basis for Measurement Code",
	"BIG01": "Date", "BIG02": "Invoice Number", "BIG04": "Purchase Order Number", "BIG07": "Transaction Type Code",
	"IT101": "Assigned Identification", "IT102": "Quantity Invoiced",
	"IT103": "Unit or Basis for Measurement Code", "IT104": "Unit Price",
	"TDS01": "Amount", "TXI01": "Tax Type Code", "TXI02": "Monetary Amount",
	"ITD01": "Terms Type Code", "ITD07": "Terms Net Days",
	"BAK01": "Transaction Set Purpose Code", "BAK02": "Acknowledgment Type",
	"BAK03": "Purchase Order Number", "BAK04": "Date", "BAK09": "Acknowledgment Date",
	"ACK01": "Line Item Status Code", "ACK02": "Quantity", "ACK03": "Unit or Basis for Measurement Code",
	"AK101": "Functional Identifier Code", "AK102": "Group Control Number",
	"AK201": "Transaction Set Identifier Code", "AK202": "Transaction Set Control Number",
	"AK501": "Transaction Set Acknowledgment Code",
	"AK901": "Functional Group Acknowledge Code", "AK902": "Number of Transaction Sets Included",
	"AK903": "Number of Received Transaction Sets", "AK904": "Number of Accepted Transaction Sets",
	"BPR01": "Transaction Handling Code", "BPR02": "Monetary Amount", "BPR03": "Credit/Debit Flag Code",
	"BPR04": "Payment Method Code", "BPR16": "Date",
	"TRN01": "Trace Type Code", "TRN02": "Reference Identification", "ENT01": "Assigned Number",
	"RMR01": "Reference Identification Qualifier", "RMR02": "Reference Identification",
	"RMR04": "Monetary Amount", "RMR05": "Monetary Amount", "RMR06": "Monetary Amount",
	"B1001": "Reference Identification", "B1002": "Shipment Identification Number", "B1003": "Standard Carrier Alpha Code",
	"L1101": "Reference Identification", "L1102": "Reference Identification Qualifier", "LX01": "Assigned Number",
	"AT701": "Shipment Status Code", "AT702": "Shipment Status or Appointment Reason Code",
	"AT705": "Date", "AT706": "Time", "AT707": "Time Code",
	"MS101": "City Name", "MS102": "State or Province Code", "MS103": "Country Code",
}

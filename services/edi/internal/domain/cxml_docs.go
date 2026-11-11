package domain

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// CXMLLine is one line of a canonical cXML document. Status is the confirmation status of the line (accept, reject, detail).
type CXMLLine struct {
	Line      string `json:"line"`
	Quantity  string `json:"quantity,omitempty"`
	Unit      string `json:"unit,omitempty"`
	Item      string `json:"item,omitempty"`
	UnitPrice string `json:"unitPrice,omitempty"`
	Status    string `json:"status,omitempty"`
}

// CXMLCanonical is the canonical form the message monitor and the consumers read for a document from a cXML network (#35, 35-4).
// Number is the shipmentID of a ship notice, the invoiceID of an invoice, the confirmation type of a confirmation; OrderIDs are the
// purchase orders it refers to.
type CXMLCanonical struct {
	DocType   string     `json:"docType"`
	PayloadID string     `json:"payloadId"`
	Number    string     `json:"number,omitempty"`
	Date      string     `json:"date,omitempty"`
	Purpose   string     `json:"purpose,omitempty"`
	Currency  string     `json:"currency,omitempty"`
	Gross     string     `json:"gross,omitempty"`
	OrderIDs  []string   `json:"orderIds"`
	Lines     []CXMLLine `json:"lines"`
}

// ErrCXMLDocument marks a cXML document whose required elements are missing.
var ErrCXMLDocument = errors.New("cxml: document")

type xmlNode struct {
	Name     string
	Attr     map[string]string
	Text     string
	Children []*xmlNode
}

func readXMLTree(raw []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	dec.Strict = true
	var stack []*xmlNode
	var root *xmlNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("cxml: not well-formed: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{Name: t.Name.Local, Attr: map[string]string{}}
			for _, a := range t.Attr {
				n.Attr[a.Name.Local] = a.Value
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
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
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("cxml: empty document")
	}
	return root, nil
}

// find returns the first descendant (depth first) with the name.
func (n *xmlNode) find(name string) *xmlNode {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
		if f := c.find(name); f != nil {
			return f
		}
	}
	return nil
}

func (n *xmlNode) all(name string) []*xmlNode {
	var out []*xmlNode
	for _, c := range n.Children {
		if c.Name == name {
			out = append(out, c)
		}
		out = append(out, c.all(name)...)
	}
	return out
}

func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (n *xmlNode) text(name string) string {
	if c := n.find(name); c != nil {
		return strings.TrimSpace(c.Text)
	}
	return ""
}

func moneyOf(n *xmlNode) (value, currency string) {
	if n == nil {
		return "", ""
	}
	m := n.find("Money")
	if m == nil {
		return "", ""
	}
	return strings.TrimSpace(m.Text), m.Attr["currency"]
}

func ordersOf(doc *xmlNode) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, o := range doc.all("OrderReference") {
		if id := o.Attr["orderID"]; id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// MapCXMLDocument maps a ConfirmationRequest, ShipNoticeRequest or InvoiceDetailRequest to its canonical form. Required: the order
// reference of a confirmation, the shipmentID and an order of a ship notice, the invoiceID, date and a line of an invoice. A missing
// element is ErrCXMLDocument.
func MapCXMLDocument(raw []byte, env CXMLEnvelope) (CXMLCanonical, error) {
	root, err := readXMLTree(raw)
	if err != nil {
		return CXMLCanonical{}, err
	}
	out := CXMLCanonical{DocType: env.DocType, PayloadID: env.PayloadID, OrderIDs: []string{}, Lines: []CXMLLine{}}
	req := root.child("Request")
	if req == nil {
		return out, fmt.Errorf("%w: no Request", ErrCXMLDocument)
	}
	doc := req.child(env.DocType)
	if doc == nil {
		return out, fmt.Errorf("%w: no %s", ErrCXMLDocument, env.DocType)
	}
	out.OrderIDs = ordersOf(doc)
	switch env.DocType {
	case "ConfirmationRequest":
		h := doc.child("ConfirmationHeader")
		if h == nil || len(out.OrderIDs) == 0 {
			return out, fmt.Errorf("%w: a ConfirmationRequest needs its ConfirmationHeader and an OrderReference", ErrCXMLDocument)
		}
		out.Number, out.Purpose, out.Date = h.Attr["confirmID"], h.Attr["type"], h.Attr["noticeDate"]
		for _, it := range doc.all("ConfirmationItem") {
			l := CXMLLine{Line: it.Attr["lineNumber"], Quantity: it.Attr["quantity"], Unit: it.text("UnitOfMeasure")}
			if st := it.child("ConfirmationStatus"); st != nil {
				l.Status = st.Attr["type"]
			}
			out.Lines = append(out.Lines, l)
		}
	case "ShipNoticeRequest":
		h := doc.child("ShipNoticeHeader")
		if h == nil || h.Attr["shipmentID"] == "" || len(out.OrderIDs) == 0 {
			return out, fmt.Errorf("%w: a ShipNoticeRequest needs its shipmentID and an OrderReference", ErrCXMLDocument)
		}
		out.Number, out.Date = h.Attr["shipmentID"], h.Attr["shipmentDate"]
		if out.Date == "" {
			out.Date = h.Attr["noticeDate"]
		}
		for _, it := range doc.all("ShipNoticeItem") {
			l := CXMLLine{Line: it.Attr["lineNumber"], Quantity: it.Attr["quantity"], Unit: it.text("UnitOfMeasure"), Item: it.text("SupplierPartID")}
			out.Lines = append(out.Lines, l)
		}
	case "InvoiceDetailRequest":
		h := doc.child("InvoiceDetailRequestHeader")
		if h == nil || h.Attr["invoiceID"] == "" || h.Attr["invoiceDate"] == "" {
			return out, fmt.Errorf("%w: an InvoiceDetailRequest needs its invoiceID and invoiceDate", ErrCXMLDocument)
		}
		out.Number, out.Date, out.Purpose = h.Attr["invoiceID"], h.Attr["invoiceDate"], h.Attr["purpose"]
		for _, it := range doc.all("InvoiceDetailItem") {
			price, cur := moneyOf(it.child("UnitPrice"))
			if out.Currency == "" {
				out.Currency = cur
			}
			out.Lines = append(out.Lines, CXMLLine{Line: it.Attr["invoiceLineNumber"], Quantity: it.Attr["quantity"], Unit: it.text("UnitOfMeasure"),
				Item: it.text("SupplierPartID"), UnitPrice: price})
		}
		if len(out.Lines) == 0 {
			return out, fmt.Errorf("%w: an InvoiceDetailRequest needs at least one InvoiceDetailItem", ErrCXMLDocument)
		}
		if sum := doc.child("InvoiceDetailSummary"); sum != nil {
			out.Gross, _ = moneyOf(sum.child("GrossAmount"))
			if out.Gross == "" {
				out.Gross, _ = moneyOf(sum.child("NetAmount"))
			}
		}
	default:
		return out, fmt.Errorf("%w: %s is not an inbound document", ErrCXMLDocument, env.DocType)
	}
	return out, nil
}

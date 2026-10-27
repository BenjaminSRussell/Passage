package domain

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

func fixtureText(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../testdata/x12/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return string(b)
}

// findSegment returns the first segment node with the given id, depth first.
func findSegment(n Node, id string) (Node, bool) {
	if n.Kind == "segment" && n.Name == id {
		return n, true
	}
	for _, c := range n.Children {
		if s, ok := findSegment(c, id); ok {
			return s, true
		}
	}
	return Node{}, false
}

func elementOf(t *testing.T, seg Node, ref string) Element {
	t.Helper()
	for _, e := range seg.Elements {
		if e.Ref == ref {
			return e
		}
	}
	t.Fatalf("segment %s has no element %s", seg.Name, ref)
	return Element{}
}

func TestFixture_850BasicMapsWithoutIssues(t *testing.T) {
	ic, err := Parse(fixtureText(t, "850-basic.x12"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ic.Groups) != 1 || len(ic.Groups[0].Transactions) != 1 {
		t.Fatalf("want 1 group with 1 transaction, got %+v", ic.Groups)
	}
	order, issues := Map850(ic.Groups[0].Transactions[0])
	if len(issues) != 0 {
		t.Fatalf("clean fixture produced issues: %+v", issues)
	}
	if order.PONumber != "PO-88001" || order.ShipToName != "ACME DC" || order.ShipToGLN != "0012345000007" || len(order.Lines) != 1 {
		t.Fatalf("order = %+v", order)
	}
}

func TestFixture_CTTMismatchIsRejected(t *testing.T) {
	ic, err := Parse(fixtureText(t, "850-ctt-mismatch.x12"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, issues := Map850(ic.Groups[0].Transactions[0])
	if len(issues) != 1 || issues[0].Code != CodeCountMismatch || !issues[0].Reject {
		t.Fatalf("CTT01 2 with one PO1 must reject with %s (BR-35.1), got %+v", CodeCountMismatch, issues)
	}
}

func TestTree_EnvelopeShapeAndDictionary(t *testing.T) {
	ic, err := Parse(fixtureText(t, "850-basic.x12"))
	if err != nil {
		t.Fatal(err)
	}
	root := Tree(ic, false)
	if root.Kind != "interchange" || root.Name != "ISA" {
		t.Fatalf("root = %s/%s", root.Kind, root.Name)
	}
	if e := elementOf(t, root, "ISA13"); e.Value != "000000001" || e.Name != "Interchange Control Number" {
		t.Fatalf("ISA13 = %+v", e)
	}
	if e := elementOf(t, root, "ISA06"); e.Value != "DISTRIBUTOR" {
		t.Fatalf("ISA06 must be trimmed, got %q", e.Value)
	}
	if len(root.Children) != 1 || root.Children[0].Kind != "group" || elementOf(t, root.Children[0], "GS01").Value != "PO" {
		t.Fatalf("want one PO group, got %+v", root.Children)
	}
	st := root.Children[0].Children[0]
	// BEG CUR N1 N3 N4 PER PO1 CTT: the body between ST and SE, no trailers.
	var names []string
	for _, s := range st.Children {
		names = append(names, s.Name)
	}
	if st.Kind != "transaction" || strings.Join(names, " ") != "BEG CUR N1 N3 N4 PER PO1 CTT" {
		t.Fatalf("transaction %s has segments %v", st.Name, names)
	}
	if e := elementOf(t, st.Children[0], "BEG03"); e.Name != "Purchase Order Number" || e.Value != "PO-88001" {
		t.Fatalf("BEG03 = %+v", e)
	}
	// An element outside the dictionary still shows its ref and value.
	if e := elementOf(t, st.Children[0], "BEG04"); e.Name != "" || e.Value != "" {
		t.Fatalf("BEG04 = %+v", e)
	}
}

func TestTree_MasksAddressAndContactElements(t *testing.T) {
	ic, err := Parse(fixtureText(t, "850-basic.x12"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		maskPII    bool
		segment    string
		ref        string
		wantValue  string
		wantMasked bool
	}{
		{"N1 name masked", true, "N1", "N102", MaskedValue, true},
		{"N1 entity code stays visible", true, "N1", "N101", "ST", false},
		{"N1 GLN stays visible for routing", true, "N1", "N104", "0012345000007", false},
		{"N3 address line masked", true, "N3", "N301", MaskedValue, true},
		{"N4 city masked", true, "N4", "N401", MaskedValue, true},
		{"N4 postal code masked", true, "N4", "N403", MaskedValue, true},
		{"PER contact name masked", true, "PER", "PER02", MaskedValue, true},
		{"PER phone number masked", true, "PER", "PER04", MaskedValue, true},
		{"PER qualifier stays visible", true, "PER", "PER03", "TE", false},
		{"PO1 quantity never masked", true, "PO1", "PO102", "10", false},
		{"unmasked view shows N1 name", false, "N1", "N102", "ACME DC", false},
		{"unmasked view shows N3 line", false, "N3", "N301", "100 MAIN ST", false},
		{"unmasked view shows the phone", false, "PER", "PER04", "5551234", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seg, ok := findSegment(Tree(ic, c.maskPII), c.segment)
			if !ok {
				t.Fatalf("segment %s not in tree", c.segment)
			}
			e := elementOf(t, seg, c.ref)
			if e.Value != c.wantValue || e.Masked != c.wantMasked {
				t.Fatalf("%s = (%q, masked=%v), want (%q, masked=%v)", c.ref, e.Value, e.Masked, c.wantValue, c.wantMasked)
			}
		})
	}
}

// The masked tree never carries a value of an address or contact element,
// whatever the partner sent. Values are drawn in lower case so none can equal
// one of the codes that rightly stay visible (ST, 92, TE, EM).
func TestTree_MaskedTreeNeverLeaksPII(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		value := rapid.StringMatching(`[a-z][a-z0-9 ]{0,30}`)
		name, street, city, phone := value.Draw(t, "name"), value.Draw(t, "street"), value.Draw(t, "city"), value.Draw(t, "phone")
		ic := Interchange{Groups: []Group{{Transactions: []Transaction{{SetID: "850", Control: "0001",
			Body: []Segment{
				{ID: "N1", Elements: []string{"ST", name, "92", "0012345000007"}},
				{ID: "N2", Elements: []string{name}},
				{ID: "N3", Elements: []string{street, street}},
				{ID: "N4", Elements: []string{city, "NY", "10001", "US"}},
				{ID: "PER", Elements: []string{"BD", name, "TE", phone, "EM", phone}},
			}}}}}}
		b, err := json.Marshal(Tree(ic, true))
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{name, street, city, phone} {
			if strings.Contains(string(b), `"value":"`+secret+`"`) {
				t.Fatalf("masked tree carries %q: %s", secret, b)
			}
		}
	})
}

func TestMaskCanonical(t *testing.T) {
	order := `{"poNumber":"PO-1","shipToName":"ACME DC","shipToGln":"0012345000007","billToName":"","lines":[{"lineNo":"1"}]}`
	var got map[string]any
	if err := json.Unmarshal(MaskCanonical([]byte(order)), &got); err != nil {
		t.Fatal(err)
	}
	if got["shipToName"] != MaskedValue || got["shipToGln"] != "0012345000007" || got["poNumber"] != "PO-1" || got["billToName"] != "" {
		t.Fatalf("masked order = %v", got)
	}
	asn := `{"shipFrom":{"name":"Plant","gln":"0614141000012"},"orders":[{"shipTo":{"name":"Dock","gln":"1"}}]}`
	masked := string(MaskCanonical([]byte(asn)))
	if strings.Contains(masked, "Plant") || strings.Contains(masked, "Dock") || !strings.Contains(masked, "0614141000012") {
		t.Fatalf("masked ASN = %s", masked)
	}
	if got := MaskCanonical(nil); got != nil {
		t.Fatalf("empty document = %q", got)
	}
	if got := string(MaskCanonical([]byte("not json"))); got != "not json" {
		t.Fatalf("non-JSON document = %q", got)
	}
}

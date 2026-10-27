package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// cxmlCart returns a PunchOutOrderMessage with three items. The third uses unit
// "DOZ", which has no mapping in the default table or the supplier table.
func cxmlCart(total string) string {
	return `<?xml version="1.0"?>
<cXML payloadID="po-1" timestamp="2026-10-01T10:00:00Z">
 <Header/>
 <Message><PunchOutOrderMessage>
  <BuyerCookie>cookie-1</BuyerCookie>
  <PunchOutOrderMessageHeader operationAllowed="create">
   <Total><Money currency="USD">` + total + `</Money></Total>
  </PunchOutOrderMessageHeader>
  <ItemIn quantity="2">
   <ItemID><SupplierPartID>SUP-100</SupplierPartID></ItemID>
   <ItemDetail><UnitPrice><Money currency="USD">10.00</Money></UnitPrice>
    <Description xml:lang="en">Bolt M8</Description><UnitOfMeasure>EA</UnitOfMeasure>
    <Classification domain="UNSPSC">31161501</Classification><LeadTime>3</LeadTime></ItemDetail>
  </ItemIn>
  <ItemIn quantity="5">
   <ItemID><SupplierPartID>SUP-200</SupplierPartID></ItemID>
   <ItemDetail><UnitPrice><Money currency="USD">4.00</Money></UnitPrice>
    <Description xml:lang="en">Washer</Description><UnitOfMeasure>KG</UnitOfMeasure></ItemDetail>
  </ItemIn>
  <ItemIn quantity="3">
   <ItemID><SupplierPartID>SUP-300</SupplierPartID></ItemID>
   <ItemDetail><UnitPrice><Money currency="USD">6.00</Money></UnitPrice>
    <Description xml:lang="en">Gasket</Description><UnitOfMeasure>DOZ</UnitOfMeasure></ItemDetail>
  </ItemIn>
 </PunchOutOrderMessage></Message>
</cXML>`
}

func usdSupplier() SupplierPunchout {
	return SupplierPunchout{Currencies: []string{"USD"}, TolerancePct: decimal.RequireFromString("5")}
}

func TestPunchoutCXML_DropsUnmappedUoMAndKeepsRest(t *testing.T) {
	// 2*10 + 5*4 + 3*6 = 58.00
	cart, err := ParsePunchoutCXML([]byte(cxmlCart("58.00")))
	if err != nil {
		t.Fatal(err)
	}
	res := ValidatePunchout(cart, usdSupplier())
	if res.CartRejected {
		t.Fatalf("cart rejected: %+v", res.Issues)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(res.Lines))
	}
	for _, l := range res.Lines {
		if !l.Catalog {
			t.Error("punchout lines must carry the catalog flag")
		}
	}
	if res.Lines[0].UnitCode != "C62" || res.Lines[1].UnitCode != "KGM" {
		t.Errorf("units = %s, %s", res.Lines[0].UnitCode, res.Lines[1].UnitCode)
	}
	if len(res.Issues) != 1 || res.Issues[0].Code != CodePunchoutLine {
		t.Fatalf("issues = %+v", res.Issues)
	}
	if !strings.Contains(res.Issues[0].Detail, "line 3") {
		t.Errorf("detail = %q", res.Issues[0].Detail)
	}
	if res.Lines[0].UNSPSC != "31161501" || res.Lines[0].LeadTimeDays != 3 {
		t.Errorf("classification/lead time not carried: %+v", res.Lines[0])
	}
}

func TestPunchoutCXML_TotalMismatchRejectsCart(t *testing.T) {
	cart, err := ParsePunchoutCXML([]byte(cxmlCart("60.00")))
	if err != nil {
		t.Fatal(err)
	}
	res := ValidatePunchout(cart, usdSupplier())
	if !res.CartRejected || len(res.Lines) != 0 {
		t.Fatalf("cart must be rejected whole: %+v", res)
	}
	// The DOZ line is still dropped with EDI-0133 and the cart is rejected with EDI-0134.
	if !hasCode(res.Issues, CodePunchoutTotal) || !hasCode(res.Issues, CodePunchoutLine) {
		t.Fatalf("issues = %+v", res.Issues)
	}
}

func TestPunchoutCXML_RejectsNonPunchoutDocument(t *testing.T) {
	if _, err := ParsePunchoutCXML([]byte(`<cXML payloadID="p"><Request><OrderRequest/></Request></cXML>`)); err == nil {
		t.Fatal("expected error for a non-PunchOutOrderMessage")
	}
}

func TestPunchoutOCI_TwoItemsFromFormPost(t *testing.T) {
	fields := map[string]string{
		"NEW_ITEM-VENDORMAT[1]":       "OCI-1",
		"NEW_ITEM-DESCRIPTION[1]":     "Hex nut",
		"NEW_ITEM-QUANTITY[1]":        "4",
		"NEW_ITEM-PRICE[1]":           "1.25",
		"NEW_ITEM-CURRENCY[1]":        "USD",
		"NEW_ITEM-UNIT[1]":            "EA",
		"NEW_ITEM-VENDORMAT[2]":       "OCI-2",
		"NEW_ITEM-DESCRIPTION[2]":     "Flange",
		"NEW_ITEM-QUANTITY[2]":        "2",
		"NEW_ITEM-PRICE[2]":           "18.50",
		"NEW_ITEM-CURRENCY[2]":        "USD",
		"NEW_ITEM-UNIT[2]":            "KG",
		"NEW_ITEM-MATGROUP[2]":        "BUYER-GRP",
		"NEW_ITEM-EXT_SCHEMA_TYPE[2]": "UNSPSC",
		"NEW_ITEM-EXT_CATEGORY_ID[2]": "31171500",
		"HOOK_URL":                    "https://x",
	}
	cart, err := ParsePunchoutOCI(fields)
	if err != nil {
		t.Fatal(err)
	}
	if len(cart.Items) != 2 || cart.Items[0].Line != 1 || cart.Items[1].Line != 2 {
		t.Fatalf("items = %+v", cart.Items)
	}
	res := ValidatePunchout(cart, usdSupplier())
	if len(res.Lines) != 2 || len(res.Issues) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.Lines[0].Description != "Hex nut" || res.Lines[1].UnitPrice.String() != "18.5" || res.Lines[1].UnitCode != "KGM" {
		t.Errorf("lines = %+v", res.Lines)
	}
	if res.Lines[1].UNSPSC != "31171500" || res.Lines[0].UNSPSC != "" {
		t.Errorf("UNSPSC must come from EXT_CATEGORY_ID with schema UNSPSC, not MATGROUP: %q, %q", res.Lines[0].UNSPSC, res.Lines[1].UNSPSC)
	}
	if cart.StatedTotal != "" {
		t.Error("OCI carries no total, so no total check may run")
	}
}

func TestPunchoutOCI_PriceUnitAndSupplierUoM(t *testing.T) {
	cart, err := ParsePunchoutOCI(map[string]string{
		"NEW_ITEM-VENDORMAT[1]": "OCI-9", "NEW_ITEM-QUANTITY[1]": "3", "NEW_ITEM-PRICE[1]": "25.00",
		"NEW_ITEM-PRICEUNIT[1]": "100", "NEW_ITEM-CURRENCY[1]": "USD", "NEW_ITEM-UNIT[1]": "pk",
	})
	if err != nil {
		t.Fatal(err)
	}
	sup := usdSupplier()
	sup.UoMMap = map[string]string{"Pk": "PK"} // supplier keys are matched case-insensitively
	res := ValidatePunchout(cart, sup)
	if len(res.Lines) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !res.Lines[0].UnitPrice.Equal(decimal.RequireFromString("0.25")) || res.Lines[0].UnitCode != "PK" {
		t.Fatalf("line = %+v, want 0.25 per PK", res.Lines[0])
	}
	cart.Items[0].PriceUnit = "0"
	if res := ValidatePunchout(cart, sup); len(res.Lines) != 0 || !hasCode(res.Issues, CodePunchoutLine) {
		t.Fatalf("price unit 0 must drop the line: %+v", res)
	}
}

func TestPunchoutOCI_NoItemsIsError(t *testing.T) {
	if _, err := ParsePunchoutOCI(map[string]string{"HOOK_URL": "x"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestValidatePunchout_LineRules(t *testing.T) {
	tests := []struct {
		name     string
		item     RawCartItem
		wantDrop bool
	}{
		{"valid", RawCartItem{SupplierPartID: "A", Quantity: "1", Price: "0", Currency: "USD", Unit: "EA"}, false},
		{"missing part id", RawCartItem{Quantity: "1", Price: "1", Currency: "USD", Unit: "EA"}, true},
		{"zero quantity", RawCartItem{SupplierPartID: "A", Quantity: "0", Price: "1", Currency: "USD", Unit: "EA"}, true},
		{"negative price", RawCartItem{SupplierPartID: "A", Quantity: "1", Price: "-1", Currency: "USD", Unit: "EA"}, true},
		{"price not a number", RawCartItem{SupplierPartID: "A", Quantity: "1", Price: "abc", Currency: "USD", Unit: "EA"}, true},
		{"currency not allowed", RawCartItem{SupplierPartID: "A", Quantity: "1", Price: "1", Currency: "EUR", Unit: "EA"}, true},
		{"unit unmapped", RawCartItem{SupplierPartID: "A", Quantity: "1", Price: "1", Currency: "USD", Unit: "XYZ"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.item.Line = 1
			res := ValidatePunchout(RawCart{Items: []RawCartItem{tc.item}}, usdSupplier())
			dropped := len(res.Lines) == 0
			if dropped != tc.wantDrop {
				t.Fatalf("dropped=%v want %v; issues %+v", dropped, tc.wantDrop, res.Issues)
			}
		})
	}
}

func TestValidatePunchout_ApprovalFlagAboveTolerance(t *testing.T) {
	sup := SupplierPunchout{
		Currencies:      []string{"USD"},
		TolerancePct:    decimal.RequireFromString("5"),
		ReferencePrices: map[string]decimal.Decimal{"A": decimal.RequireFromString("10")},
	}
	items := []RawCartItem{
		{Line: 1, SupplierPartID: "A", Quantity: "1", Price: "10.50", Currency: "USD", Unit: "EA"}, // at tolerance limit
		{Line: 2, SupplierPartID: "A", Quantity: "1", Price: "10.51", Currency: "USD", Unit: "EA"}, // above
	}
	res := ValidatePunchout(RawCart{Items: items}, sup)
	if res.Lines[0].NeedsApproval || !res.Lines[1].NeedsApproval {
		t.Fatalf("approval flags = %v, %v", res.Lines[0].NeedsApproval, res.Lines[1].NeedsApproval)
	}
}

// Property: a cart whose stated total equals the line sum is never rejected, and
// a total off by more than 0.01 always is.
func TestValidatePunchout_TotalProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 8).Draw(t, "lines")
		var items []RawCartItem
		sum := decimal.Zero
		for i := 0; i < n; i++ {
			q := rapid.Int64Range(1, 50).Draw(t, "qty")
			p := rapid.Int64Range(0, 99999).Draw(t, "price")
			price := decimal.New(p, -2)
			sum = sum.Add(decimal.NewFromInt(q).Mul(price))
			items = append(items, RawCartItem{Line: i + 1, SupplierPartID: "P", Quantity: decimal.NewFromInt(q).String(),
				Price: price.String(), Currency: "USD", Unit: "EA"})
		}
		cart := RawCart{Items: items, StatedTotal: sum.StringFixed(2)}
		if res := ValidatePunchout(cart, usdSupplier()); res.CartRejected {
			t.Fatalf("exact total rejected: %+v", res.Issues)
		}
		cart.StatedTotal = sum.Add(decimal.RequireFromString("0.02")).StringFixed(2)
		if res := ValidatePunchout(cart, usdSupplier()); !res.CartRejected {
			t.Fatalf("off-by-0.02 total accepted")
		}
	})
}

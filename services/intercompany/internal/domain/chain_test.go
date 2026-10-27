package domain

import (
	"errors"
	"testing"
)

// chainPartner pairs seller B with buyer A (FR-40.3: customer in A, goods from B).
func chainPartner(active bool) Partner {
	return Partner{SellerEntity: "B", BuyerEntity: "A", Active: active}
}

func validChain() DeliveryChain {
	return DeliveryChain{
		ChainID:       "chain-1",
		CustomerOrder: ChainLeg{DocID: "SO-A-1", Entity: "A", Quantity: d("20")},
		IcPurchase:    ChainLeg{DocID: "PO-A-1", Entity: "A", Quantity: d("20")},
		IcSales:       ChainLeg{DocID: "SO-B-1", Entity: "B", Quantity: d("20")},
	}
}

// TestPackingSlipPostings_AC403 is AC-40.3: B's packing slip posts A's product
// receipt and A's customer packing slip with matching quantities.
func TestPackingSlipPostings_AC403(t *testing.T) {
	for _, shipped := range []string{"20", "7.5"} {
		got, err := PackingSlipPostings(validChain(), chainPartner(true), d(shipped))
		if err != nil {
			t.Fatal(err)
		}
		want := []ChainPosting{
			{Kind: PostingProductReceipt, Entity: "A", DocID: "PO-A-1", Quantity: d(shipped)},
			{Kind: PostingCustomerPackingSlip, Entity: "A", DocID: "SO-A-1", Quantity: d(shipped)},
		}
		if len(got) != len(want) {
			t.Fatalf("postings = %+v; want %+v", got, want)
		}
		for i := range want {
			if got[i].Kind != want[i].Kind || got[i].Entity != want[i].Entity || got[i].DocID != want[i].DocID || !got[i].Quantity.Equal(want[i].Quantity) {
				t.Fatalf("posting %d = %+v; want %+v", i, got[i], want[i])
			}
		}
	}
}

func TestPackingSlipPostings_ShippedQuantity(t *testing.T) {
	for _, shipped := range []string{"0", "-1", "20.001"} {
		_, err := PackingSlipPostings(validChain(), chainPartner(true), d(shipped))
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeInvalid {
			t.Fatalf("shipped %s: err = %v; want COM-0001", shipped, err)
		}
	}
}

func TestValidateChain_Rejections(t *testing.T) {
	cases := []struct {
		name    string
		edit    func(*DeliveryChain)
		partner Partner
		code    string
	}{
		{"inactive pairing", func(*DeliveryChain) {}, chainPartner(false), CodeNoPolicy},
		{"pairing for another seller", func(*DeliveryChain) {}, Partner{SellerEntity: "C", BuyerEntity: "A", Active: true}, CodeNoPolicy},
		{"purchase booked by seller", func(c *DeliveryChain) { c.IcPurchase.Entity = "B" }, chainPartner(true), CodeNoPolicy},
		{"sales order booked by buyer", func(c *DeliveryChain) { c.IcSales.Entity = "A" }, chainPartner(true), CodeNoPolicy},
		{"customer order in seller", func(c *DeliveryChain) { c.CustomerOrder.Entity = "B" }, chainPartner(true), CodeInvalid},
		{"no chain id", func(c *DeliveryChain) { c.ChainID = "" }, chainPartner(true), CodeInvalid},
		{"missing document", func(c *DeliveryChain) { c.IcSales.DocID = "" }, chainPartner(true), CodeInvalid},
		{"repeated document", func(c *DeliveryChain) { c.IcSales.DocID = "PO-A-1" }, chainPartner(true), CodeInvalid},
		{"zero quantity", func(c *DeliveryChain) { c.IcPurchase.Quantity = d("0") }, chainPartner(true), CodeInvalid},
		{"quantity mismatch on sales", func(c *DeliveryChain) { c.IcSales.Quantity = d("19") }, chainPartner(true), CodeInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chain := validChain()
			c.edit(&chain)
			err := ValidateChain(chain, c.partner)
			var de *Error
			if !errors.As(err, &de) || de.Code != c.code {
				t.Fatalf("err = %v; want %s", err, c.code)
			}
		})
	}
}

package domain

import "github.com/shopspring/decimal"

// Posting kinds raised in the buyer entity by the seller's packing slip (FR-40.3).
const (
	PostingProductReceipt      = "ProductReceipt"
	PostingCustomerPackingSlip = "CustomerPackingSlip"
)

// ChainLeg is one document of a direct delivery chain.
type ChainLeg struct {
	DocID    string
	Entity   string
	Quantity decimal.Decimal
}

// DeliveryChain is a customer order in buyer entity A, the intercompany purchase
// order from A to seller entity B, and the intercompany sales order in B that
// ships directly to A's customer (FR-40.3).
type DeliveryChain struct {
	ChainID       string
	CustomerOrder ChainLeg
	IcPurchase    ChainLeg
	IcSales       ChainLeg
}

// ChainPosting is one document that B's packing slip posts in the same saga (AC-40.3).
type ChainPosting struct {
	Kind     string
	Entity   string
	DocID    string
	Quantity decimal.Decimal
}

// ValidateChain checks the chain against its partner pairing: the customer
// order and the intercompany purchase belong to the buyer entity, the
// intercompany sales order to the seller, the three documents are distinct and
// they carry the same quantity. A missing or inactive pairing fails with IC-0801.
func ValidateChain(c DeliveryChain, p Partner) error {
	switch {
	case c.ChainID == "":
		return invalid("chain id is required")
	case c.CustomerOrder.DocID == "" || c.IcPurchase.DocID == "" || c.IcSales.DocID == "":
		return invalid("each chain leg needs a document id")
	case c.CustomerOrder.DocID == c.IcPurchase.DocID || c.IcPurchase.DocID == c.IcSales.DocID || c.CustomerOrder.DocID == c.IcSales.DocID:
		return invalid("chain legs must be distinct documents")
	case !c.CustomerOrder.Quantity.IsPositive() || !c.IcPurchase.Quantity.IsPositive() || !c.IcSales.Quantity.IsPositive():
		return invalid("chain quantities must be positive")
	case !c.CustomerOrder.Quantity.Equal(c.IcPurchase.Quantity) || !c.CustomerOrder.Quantity.Equal(c.IcSales.Quantity):
		return invalid("chain quantities must match across the three documents")
	}
	if !p.Active || p.SellerEntity != c.IcSales.Entity || p.BuyerEntity != c.IcPurchase.Entity {
		return &Error{Code: CodeNoPolicy, Detail: "no active IC partner policy between " + c.IcSales.Entity + " and " + c.IcPurchase.Entity}
	}
	if c.CustomerOrder.Entity != p.BuyerEntity {
		return invalid("customer order must belong to the buyer entity")
	}
	return nil
}

// PackingSlipPostings returns the documents that B's packing slip for shipped
// units posts in one saga: A's product receipt against the intercompany purchase
// and A's customer packing slip against the customer order, both at the shipped
// quantity (AC-40.3). A partial shipment is allowed; shipping more than the
// intercompany sales order holds is not.
func PackingSlipPostings(c DeliveryChain, p Partner, shipped decimal.Decimal) ([]ChainPosting, error) {
	if err := ValidateChain(c, p); err != nil {
		return nil, err
	}
	switch {
	case !shipped.IsPositive():
		return nil, invalid("shipped quantity must be positive")
	case shipped.GreaterThan(c.IcSales.Quantity):
		return nil, invalid("shipped quantity must not exceed the intercompany sales order quantity")
	}
	return []ChainPosting{
		{Kind: PostingProductReceipt, Entity: c.IcPurchase.Entity, DocID: c.IcPurchase.DocID, Quantity: shipped},
		{Kind: PostingCustomerPackingSlip, Entity: c.CustomerOrder.Entity, DocID: c.CustomerOrder.DocID, Quantity: shipped},
	}, nil
}

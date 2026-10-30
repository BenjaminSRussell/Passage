// Package domain holds the pricing engine: pure functions over condition
// records. It performs no I/O and reads no clock; every date is supplied by
// the caller so identical inputs always produce identical outputs.
package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// ConditionType is a pricing condition (SAP condition type, AX price/discount kind).
type ConditionType string

const (
	TypePrice     ConditionType = "PRICE"
	TypeLineDisc  ConditionType = "LINE_DISC"
	TypeTotalDisc ConditionType = "TOTAL_DISC"
	// TypeMultiline discounts every line of a product group once the summed quantity
	// of the group reaches the record's from_quantity (FR-26.5, BR-26.4).
	TypeMultiline ConditionType = "MULTILINE_DISC"
	// TypeSurcharge adds a fixed amount and/or DiscPct1 percent to the order net
	// when the subtotal is below the small-order threshold (FR-26.6).
	TypeSurcharge ConditionType = "SURCHARGE"
)

// Record lifecycle states (BR-26.9). Only ACTIVE records take part in resolution.
const (
	StatusDraft     = "DRAFT"
	StatusActive    = "ACTIVE"
	StatusExpired   = "EXPIRED"
	StatusWithdrawn = "WITHDRAWN"
)

// CustomerKind is the customer dimension of a key.
type CustomerKind string

const (
	CustomerKindCustomer CustomerKind = "customer"
	CustomerKindGroup    CustomerKind = "customer_group"
	CustomerKindAll      CustomerKind = "all"
)

// ProductKind is the product dimension of a key.
type ProductKind string

const (
	ProductKindProduct ProductKind = "product"
	ProductKindGroup   ProductKind = "product_group"
	ProductKindVariant ProductKind = "variant"
	ProductKindAll     ProductKind = "all"
)

// Record is a condition record (data model condition_record). Empty Unit means any unit.
type Record struct {
	ID           string
	Version      int
	Status       string
	Type         ConditionType
	CustomerKind CustomerKind
	CustomerKey  string
	ProductKind  ProductKind
	ProductKey   string
	Variant      string
	Currency     string
	Unit         string
	FromQuantity decimal.Decimal
	Amount       decimal.Decimal
	DiscPct1     decimal.Decimal
	DiscPct2     decimal.Decimal
	ValidFrom    time.Time
	ValidTo      time.Time
	SearchNext   bool
	Exclusive    bool
	// CampaignID is the optional campaign dimension of the key (FR-26.2, FR-26.10). The
	// store drops records of a killed, expired or non-targeted campaign before resolve.
	CampaignID string
}

// Header is the document-level part of a resolve request.
type Header struct {
	CustomerID      string
	CustomerGroupID string
	Currency        string
	PricingDate     time.Time
}

// Line is one document line to price.
type Line struct {
	No             int
	ProductID      string
	ProductGroupID string
	Variant        string
	Unit           string
	Quantity       decimal.Decimal
	// Charged is the quantity charged when it is less than Quantity: an inclusive free
	// goods rule gives part of the order free (BR-26.13). Quantity breaks still count
	// Quantity. Zero means the whole Quantity is charged.
	Charged decimal.Decimal
}

// ChargedQuantity is the quantity the line net is computed on.
func (l Line) ChargedQuantity() decimal.Decimal {
	if l.Charged.IsPositive() {
		return l.Charged
	}
	return l.Quantity
}

// Input is everything the engine needs. Steps defaults to DefaultSteps.
type Input struct {
	Header  Header
	Lines   []Line
	Records []Record
	Steps   []ConditionType
	// SmallOrderThreshold is the subtotal below which SURCHARGE applies. Zero disables it.
	SmallOrderThreshold decimal.Decimal
}

// DefaultSteps is the reference pricing procedure (AX_STANDARD subset).
func DefaultSteps() []ConditionType {
	return []ConditionType{TypePrice, TypeLineDisc, TypeMultiline, TypeTotalDisc, TypeSurcharge}
}

// Access is one step of an access sequence (BR-26.1 step 2), most specific first.
type Access struct {
	Seq      int
	Customer CustomerKind
	Product  ProductKind
}

// Name renders the access as "customer x product".
func (a Access) Name() string { return string(a.Customer) + " x " + string(a.Product) }

// DefaultAccesses is the PRICE access sequence from BR-26.1; discounts use it too.
func DefaultAccesses() []Access {
	return []Access{
		{1, CustomerKindCustomer, ProductKindVariant},
		{2, CustomerKindCustomer, ProductKindProduct},
		{3, CustomerKindCustomer, ProductKindGroup},
		{4, CustomerKindGroup, ProductKindProduct},
		{5, CustomerKindGroup, ProductKindGroup},
		{6, CustomerKindCustomer, ProductKindAll},
		{7, CustomerKindAll, ProductKindProduct},
		{8, CustomerKindAll, ProductKindGroup},
		{9, CustomerKindAll, ProductKindAll},
	}
}

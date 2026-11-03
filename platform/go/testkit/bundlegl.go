package testkit

type BundleLine struct {
	Description    string
	Qty            string
	UnitPrice      string
	DiscountPct    string
	RevenueAccount string
}

// BundleInvoiceLines is the invoice of the BR-29.1 bundle (transaction price 10,800.00 against SSPs of 12,000.00): the discount is spread
// over the lines by the biller, not by the SSP allocation. Billing computes the line nets (8,000.00, 1,350.00, 1,450.00), so the share on
// the deferred account 4100 is 2,800.00 where the allocation puts 3,600.00 on the deferred obligations.
var BundleInvoiceLines = []BundleLine{
	{Description: "Appliance", Qty: "1", UnitPrice: "8000.00", DiscountPct: "0", RevenueAccount: "4000"},
	{Description: "Installation milestone", Qty: "1", UnitPrice: "1500.00", DiscountPct: "10", RevenueAccount: "4100"},
	{Description: "Support year", Qty: "1", UnitPrice: "2500.00", DiscountPct: "42", RevenueAccount: "4100"},
}

const (
	BundleInvoiceGross    = "10800.00"
	BundleInvoiceDeferred = "2800.00"
)

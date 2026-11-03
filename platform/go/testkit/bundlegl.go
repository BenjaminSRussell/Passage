package testkit

type BundleLine struct {
	Description    string
	Qty            string
	UnitPrice      string
	DiscountPct    string
	RevenueAccount string
}

// BundleInvoiceLines is the invoice of the BR-29.1 bundle (transaction price 10,800.00 against SSPs of 12,000.00), each line at its
// allocated value (SSP x 0.9), so the lines on the deferred account 4100 carry the 3,600.00 the allocation puts on the deferred obligations.
var BundleInvoiceLines = []BundleLine{
	{Description: "Appliance", Qty: "1", UnitPrice: "8000.00", DiscountPct: "10", RevenueAccount: "4000"},
	{Description: "Installation milestone", Qty: "1", UnitPrice: "1500.00", DiscountPct: "10", RevenueAccount: "4100"},
	{Description: "Support year", Qty: "1", UnitPrice: "2500.00", DiscountPct: "10", RevenueAccount: "4100"},
}

const (
	BundleInvoiceGross    = "10800.00"
	BundleInvoiceDeferred = "3600.00"
)

package testkit

type BundleLine struct {
	Description    string
	Qty            string
	UnitPrice      string
	DiscountPct    string
	RevenueAccount string
	Method         string
	Allocated      string
}

var BundleInvoiceLines = []BundleLine{
	{Description: "Appliance", Qty: "1", UnitPrice: "8000.00", DiscountPct: "0", RevenueAccount: "4000", Method: "POINT_IN_TIME", Allocated: "7200.00"},
	{Description: "Installation milestone", Qty: "1", UnitPrice: "1500.00", DiscountPct: "10", RevenueAccount: "4100", Method: "MILESTONE", Allocated: "1350.00"},
	{Description: "Support year", Qty: "1", UnitPrice: "2500.00", DiscountPct: "42", RevenueAccount: "4100", Method: "RATABLE", Allocated: "2250.00"},
}

const (
	BundleInvoiceGross    = "10800.00"
	BundleInvoiceRevenue  = "7200.00"
	BundleInvoiceDeferred = "3600.00"
)

package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

func validProfile() BufferProfileSpec {
	return BufferProfileSpec{Code: "BUF-MFG", ItemType: BufferManufactured, LeadTimeFactor: decimal.RequireFromString("0.5"),
		VariabilityFactor: decimal.RequireFromString("0.5"), OrderCycleDays: 7, SpikeThresholdPct: decimal.NewFromInt(50),
		PastDays: 90}
}

func TestBufferProfileSpec_Validate(t *testing.T) {
	horizon := -1
	cases := []struct {
		name   string
		mutate func(*BufferProfileSpec)
		field  string
	}{
		{"valid", func(*BufferProfileSpec) {}, ""},
		{"lower-case code", func(s *BufferProfileSpec) { s.Code = "buf" }, "code"},
		{"unknown item type", func(s *BufferProfileSpec) { s.ItemType = "service" }, "itemType"},
		{"negative lead time factor", func(s *BufferProfileSpec) { s.LeadTimeFactor = decimal.NewFromInt(-1) }, "leadTimeFactor"},
		{"variability above 99.999", func(s *BufferProfileSpec) { s.VariabilityFactor = decimal.NewFromInt(100) }, "variabilityFactor"},
		{"negative order cycle", func(s *BufferProfileSpec) { s.OrderCycleDays = -1 }, "orderCycleDays"},
		{"negative minimum", func(s *BufferProfileSpec) { s.MinOrderQty = decimal.NewFromInt(-5) }, "minOrderQty"},
		{"spike above 500 percent", func(s *BufferProfileSpec) { s.SpikeThresholdPct = decimal.NewFromInt(501) }, "spikeThresholdPct"},
		{"negative spike horizon", func(s *BufferProfileSpec) { s.SpikeHorizonDays = &horizon }, "spikeHorizonDays"},
		{"past window below 7 days", func(s *BufferProfileSpec) { s.PastDays = 6 }, "aduWindowPastDays"},
		{"future window above 365", func(s *BufferProfileSpec) { s.FutureDays = 366 }, "aduWindowFutureDays"},
		{"window of 6 days in all", func(s *BufferProfileSpec) { s.PastDays, s.FutureDays = 3, 3 }, "aduWindowPastDays"},
		{"forward-only window of 7 days", func(s *BufferProfileSpec) { s.PastDays, s.FutureDays = 0, 7 }, ""},
		{"spike threshold 0", func(s *BufferProfileSpec) { s.SpikeThresholdPct = decimal.Zero }, "spikeThresholdPct"},
		{"spike threshold with 3 decimals", func(s *BufferProfileSpec) { s.SpikeThresholdPct = decimal.RequireFromString("50.125") }, "spikeThresholdPct"},
		{"factor with 4 decimals", func(s *BufferProfileSpec) { s.LeadTimeFactor = decimal.RequireFromString("0.1234") }, "leadTimeFactor"},
		{"trailing zeros are not extra decimals", func(s *BufferProfileSpec) { s.LeadTimeFactor = decimal.RequireFromString("0.5000") }, ""},
		{"largest numeric(5,3) factor", func(s *BufferProfileSpec) { s.VariabilityFactor = decimal.RequireFromString("99.999") }, ""},
		{"minimum order with 7 decimals", func(s *BufferProfileSpec) { s.MinOrderQty = decimal.RequireFromString("0.0000001") }, "minOrderQty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validProfile()
			tc.mutate(&spec)
			got := spec.Validate()
			if tc.field == "" {
				if len(got) != 0 {
					t.Fatalf("valid profile rejected: %v", got)
				}
				return
			}
			if _, ok := got[tc.field]; !ok || len(got) != 1 {
				t.Fatalf("want exactly field %s, got %v", tc.field, got)
			}
		})
	}
}

func validArrangement() QuotaArrangementSpec {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	return QuotaArrangementSpec{ItemID: ids.New(), SiteID: ids.New(), ValidFrom: day(1), ValidTo: day(30),
		MinSplitQty: decimal.NewFromInt(10), Sources: []QuotaSourceSpec{
			{Type: SourceVendor, VendorID: ids.New(), Quota: decimal.NewFromInt(60), Allocated: decimal.NewFromInt(300)},
			{Type: SourceVendor, VendorID: ids.New(), Quota: decimal.NewFromInt(40), Allocated: decimal.NewFromInt(100)},
		}}
}

func TestQuotaArrangementSpec_Validate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*QuotaArrangementSpec)
		field  string
	}{
		{"valid", func(*QuotaArrangementSpec) {}, ""},
		{"item not a uuid", func(s *QuotaArrangementSpec) { s.ItemID = "x" }, "itemId"},
		{"validity ends before it starts", func(s *QuotaArrangementSpec) { s.ValidTo = s.ValidFrom.AddDate(0, 0, -1) }, "validTo"},
		{"missing validity", func(s *QuotaArrangementSpec) { s.ValidFrom = time.Time{} }, "validFrom"},
		{"no sources", func(s *QuotaArrangementSpec) { s.Sources = nil }, "sources"},
		{"vendor without vendor", func(s *QuotaArrangementSpec) { s.Sources[0].VendorID = "" }, "sources[0].vendorId"},
		{"production without version", func(s *QuotaArrangementSpec) {
			s.Sources[1] = QuotaSourceSpec{Type: SourceProduction, Quota: decimal.NewFromInt(5)}
		}, "sources[1].productionVersionId"},
		{"transfer without warehouse", func(s *QuotaArrangementSpec) {
			s.Sources[1] = QuotaSourceSpec{Type: SourceTransfer, Quota: decimal.NewFromInt(5)}
		}, "sources[1].fromWarehouseId"},
		{"unknown source type", func(s *QuotaArrangementSpec) { s.Sources[0].Type = "stock" }, "sources[0].type"},
		{"negative allocated", func(s *QuotaArrangementSpec) { s.Sources[0].Allocated = decimal.NewFromInt(-1) }, "sources[0].allocatedQty"},
		{"quota beyond numeric(9,4)", func(s *QuotaArrangementSpec) { s.Sources[0].Quota = decimal.RequireFromString("1.00001") }, "sources[0].quota"},
		{"vendor source naming a warehouse too", func(s *QuotaArrangementSpec) { s.Sources[0].FromWarehouseID = ids.New() }, "sources[0].fromWarehouseId"},
		{"same vendor twice", func(s *QuotaArrangementSpec) { s.Sources[1].VendorID = s.Sources[0].VendorID }, "sources[1].vendorId"},
		{"period maximum without its period", func(s *QuotaArrangementSpec) {
			limit := decimal.NewFromInt(500)
			s.Sources[0].MaxQtyPerPeriod = &limit
		}, "sources[0].maxQtyPerPeriod"},
		{"period of 0 days", func(s *QuotaArrangementSpec) {
			limit, days := decimal.NewFromInt(500), 0
			s.Sources[0].MaxQtyPerPeriod, s.Sources[0].PeriodDays = &limit, &days
		}, "sources[0].periodDays"},
		{"all quotas zero", func(s *QuotaArrangementSpec) {
			s.Sources[0].Quota = decimal.Zero
			s.Sources[1].Quota = decimal.Zero
		}, "sources"},
		{"negative minimum split", func(s *QuotaArrangementSpec) { s.MinSplitQty = decimal.NewFromInt(-1) }, "minSplitQty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := validArrangement()
			tc.mutate(&spec)
			got := spec.Validate()
			if tc.field == "" {
				if len(got) != 0 {
					t.Fatalf("valid arrangement rejected: %v", got)
				}
				return
			}
			if _, ok := got[tc.field]; !ok {
				t.Fatalf("want field %s, got %v", tc.field, got)
			}
		})
	}
}

func TestQuotaArrangementSpec_ZeroQuotaFallbackIsAllowed(t *testing.T) {
	// A zero-quota source is a fallback used only when the others are blocked (FR-22.17),
	// so it is valid next to a source with a positive quota.
	spec := validArrangement()
	spec.Sources = append(spec.Sources, QuotaSourceSpec{Type: SourceTransfer, FromWarehouseID: ids.New(), Quota: decimal.Zero})
	if got := spec.Validate(); len(got) != 0 {
		t.Fatalf("fallback source rejected: %v", got)
	}
}

// TestStoredSpecsFeedTheCalculators: a stored profile and arrangement give the
// BR-22.11 zones and the BR-22.12 choice through the existing calculators.
func TestStoredSpecsFeedTheCalculators(t *testing.T) {
	zones, err := ComputeZones(validProfile().Factors(), decimal.NewFromInt(10), decimal.NewFromInt(10))
	if err != nil {
		t.Fatal(err)
	}
	if !zones.TopOfRed.Equal(decimal.NewFromInt(75)) || !zones.TopOfYellow.Equal(decimal.NewFromInt(175)) || !zones.TopOfGreen.Equal(decimal.NewFromInt(245)) {
		t.Fatalf("BR-22.11 zones: %+v", zones)
	}
	spec := validArrangement()
	sources := make([]QuotaSource, len(spec.Sources))
	for i, s := range spec.Sources {
		s.ID = []string{"A", "B"}[i]
		sources[i] = s.Calculator()
	}
	got, err := Allocate(sources, decimal.NewFromInt(100), false, decimal.Zero)
	if err != nil || len(got) != 1 || got[0].SourceID != "B" || !got[0].RatingAfter.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("BR-22.12: want B with rating 5 after, got %+v, %v", got, err)
	}
}

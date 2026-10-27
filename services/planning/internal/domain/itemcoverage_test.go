package domain

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func validItemCoverage() ItemCoverageSpec {
	return ItemCoverageSpec{
		ItemID: testkit.TenantID(1), SiteID: testkit.TenantID(2), CoverageGroupID: testkit.TenantID(3),
		MinQty: decimal.NewFromInt(100), MaxQty: decimal.NewFromInt(400),
		PlannedOrderType: OrderPurchase, PlanningStrategy: StrategyMTSNet, PlanningScope: PlanScopeSite,
	}
}

func TestDefaultItemCoverage_IsValid(t *testing.T) {
	c := DefaultItemCoverage(testkit.TenantID(1), testkit.TenantID(2), testkit.TenantID(3))
	if errs := c.Validate(); errs != nil {
		t.Fatalf("defaults invalid: %v", errs)
	}
	if c.PlanningScope != PlanScopeSite || c.PlannedOrderType != OrderPurchase || c.PlanningStrategy != StrategyMTSNet || !c.MaxQty.IsZero() {
		t.Fatalf("defaults: %+v", c)
	}
}

func TestItemCoverageSpec_Validate(t *testing.T) {
	warehouse := func(c ItemCoverageSpec) ItemCoverageSpec {
		c.PlanningScope = PlanScopeWarehouse
		c.WarehouseID = testkit.TenantID(4)
		return c
	}
	subcontractor := func(c ItemCoverageSpec) ItemCoverageSpec {
		c.PlanningScope = PlanScopeSubcontractor
		c.VendorID = testkit.TenantID(5)
		c.PlannedOrderType = OrderTransfer
		return c
	}
	zero := 0
	neg := -1
	cases := []struct {
		name   string
		spec   ItemCoverageSpec
		fields []string
	}{
		{"valid site scope", validItemCoverage(), nil},
		{"valid warehouse scope", warehouse(validItemCoverage()), nil},
		{"valid subcontractor scope", subcontractor(validItemCoverage()), nil},
		{"warehouse scope without warehouse", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.PlanningScope = PlanScopeWarehouse
			return c
		}(), []string{"warehouseId"}},
		{"site scope with warehouse", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.WarehouseID = testkit.TenantID(4)
			return c
		}(), []string{"warehouseId"}},
		{"subcontractor without vendor", func() ItemCoverageSpec {
			c := subcontractor(validItemCoverage())
			c.VendorID = ""
			return c
		}(), []string{"vendorId"}},
		{"subcontractor scope plans provision transfers only", func() ItemCoverageSpec {
			c := subcontractor(validItemCoverage())
			c.PlannedOrderType = OrderPurchase
			return c
		}(), []string{"plannedOrderType"}},
		{"subcontractor scope with a warehouse", func() ItemCoverageSpec {
			c := subcontractor(validItemCoverage())
			c.WarehouseID = testkit.TenantID(4)
			return c
		}(), []string{"warehouseId"}},
		{"site scope keeps a default vendor", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.VendorID = testkit.TenantID(5)
			return c
		}(), nil},
		{"more than six decimals", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.MinQty = decimal.RequireFromString("1.0000001")
			return c
		}(), []string{"minQty"}},
		{"max at the numeric(18,6) limit", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.MaxQty = decimal.New(1, 12)
			return c
		}(), []string{"maxQty"}},
		{"receipt margin over a year", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.SafetyMarginReceiptDays = 366
			return c
		}(), []string{"safetyMarginReceiptDays"}},
		{"bad buffer profile id", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.BufferProfileID = "BP-1"
			return c
		}(), []string{"bufferProfileId"}},
		{"unknown scope", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.PlanningScope = "network"
			return c
		}(), []string{"planningScope"}},
		{"kanban not accepted yet", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.PlannedOrderType = "kanban"
			return c
		}(), []string{"plannedOrderType"}},
		{"unknown strategy", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.PlanningStrategy = "make-to-whim"
			return c
		}(), []string{"planningStrategy"}},
		{"max below min", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.MaxQty = decimal.NewFromInt(50)
			return c
		}(), []string{"maxQty"}},
		{"zero max means unlimited", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.MaxQty = decimal.Zero
			return c
		}(), nil},
		{"negative min", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.MinQty = decimal.NewFromInt(-1)
			return c
		}(), []string{"minQty"}},
		{"lead time out of range", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.LeadTimeDays = &neg
			return c
		}(), []string{"leadTimeDays"}},
		{"zero lead time override is valid", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.LeadTimeDays = &zero
			return c
		}(), nil},
		{"bad item id", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.ItemID = "FG-100"
			return c
		}(), []string{"itemId"}},
		{"shelf life over 10 years", func() ItemCoverageSpec {
			c := validItemCoverage()
			c.RequiredShelfLifeDays = 4000
			return c
		}(), []string{"requiredShelfLifeDays"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := tc.spec.Validate()
			if len(tc.fields) == 0 {
				if errs != nil {
					t.Fatalf("expected valid, got %v", errs)
				}
				return
			}
			for _, f := range tc.fields {
				if _, ok := errs[f]; !ok {
					t.Fatalf("expected a message for %s, got %v", f, errs)
				}
			}
			if len(errs) != len(tc.fields) {
				t.Fatalf("expected only %v, got %v", tc.fields, errs)
			}
		})
	}
}

package domain

import (
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// MaxQuotaSources bounds the sources of one arrangement.
const MaxQuotaSources = 100

var (
	// maxQuota is the largest numeric(9,4) value.
	maxQuota = decimal.RequireFromString("99999.9999")
)

// QuotaArrangementSpec is a stored quota arrangement (FR-22.17). ValidFrom and
// ValidTo are inclusive calendar dates; sources keep the order they were given in.
type QuotaArrangementSpec struct {
	ItemID      string
	SiteID      string
	ValidFrom   time.Time
	ValidTo     time.Time
	Split       bool
	MinSplitQty decimal.Decimal
	Sources     []QuotaSourceSpec
}

// QuotaSourceSpec is one source of an arrangement. Exactly the reference that
// matches the source type is set: a vendor, a production version or a
// from-warehouse. Allocated is the quantity already booked against the source
// (carried over from a legacy system; later accumulated from orders). A
// per-period maximum comes with its period length.
type QuotaSourceSpec struct {
	ID                  string // empty until stored
	Type                string
	VendorID            string
	ProductionVersionID string
	FromWarehouseID     string
	Quota               decimal.Decimal
	BaseQty             decimal.Decimal
	Allocated           decimal.Decimal
	MaxQtyPerPeriod     *decimal.Decimal
	PeriodDays          *int
	Priority            int
	Blocked             bool
}

// Ref is the reference that identifies the source within its type.
func (s QuotaSourceSpec) Ref() string {
	switch s.Type {
	case SourceVendor:
		return s.VendorID
	case SourceProduction:
		return s.ProductionVersionID
	case SourceTransfer:
		return s.FromWarehouseID
	}
	return ""
}

// Calculator returns the source in the shape the BR-22.12 calculator reads.
func (s QuotaSourceSpec) Calculator() QuotaSource {
	return QuotaSource{ID: s.ID, Type: s.Type, Quota: s.Quota, BaseQty: s.BaseQty, Allocated: s.Allocated,
		Priority: s.Priority, Blocked: s.Blocked}
}

// Validate returns one message per invalid field, keyed by the wire name; nil
// means the spec is valid. It holds the data model invariants an arrangement
// can check alone: valid_to ≥ valid_from, Σ quota of the sources > 0, the
// reference of each source type and no source named twice. The overlap of
// validity periods with other arrangements is the store's check.
func (s QuotaArrangementSpec) Validate() map[string]string {
	f := map[string]string{}
	if !ids.Valid(s.ItemID) {
		f["itemId"] = "must be a UUID"
	}
	if !ids.Valid(s.SiteID) {
		f["siteId"] = "must be a UUID"
	}
	if s.ValidFrom.IsZero() {
		f["validFrom"] = "must be a date as YYYY-MM-DD"
	}
	if s.ValidTo.IsZero() {
		f["validTo"] = "must be a date as YYYY-MM-DD"
	} else if !s.ValidFrom.IsZero() && s.ValidTo.Before(s.ValidFrom) {
		f["validTo"] = "must be on or after validFrom"
	}
	if msg := coverageQtyError(s.MinSplitQty); msg != "" {
		f["minSplitQty"] = msg
	}
	if n := len(s.Sources); n == 0 || n > MaxQuotaSources {
		f["sources"] = "must hold 1 to 100 sources"
		return f
	}
	total := decimal.Zero
	seen := map[string]int{}
	for i, src := range s.Sources {
		prefix := "sources[" + strconv.Itoa(i) + "]."
		refs := map[string]string{SourceVendor: src.VendorID, SourceProduction: src.ProductionVersionID, SourceTransfer: src.FromWarehouseID}
		names := map[string]string{SourceVendor: "vendorId", SourceProduction: "productionVersionId", SourceTransfer: "fromWarehouseId"}
		if _, known := refs[src.Type]; !known {
			f[prefix+"type"] = "must be vendor, production or transfer"
		}
		for typ, ref := range refs {
			switch {
			case typ == src.Type && !ids.Valid(ref):
				f[prefix+names[typ]] = "is required for a " + typ + " source and must be a UUID"
			case typ != src.Type && ref != "":
				f[prefix+names[typ]] = "must be empty for a " + src.Type + " source"
			}
		}
		if ref := src.Ref(); ref != "" {
			key := src.Type + ":" + ref
			if first, dup := seen[key]; dup {
				f[prefix+names[src.Type]] = "repeats source " + strconv.Itoa(first+1) + " of this arrangement"
			} else {
				seen[key] = i
			}
		}
		switch {
		case src.Quota.IsNegative() || src.Quota.GreaterThan(maxQuota):
			f[prefix+"quota"] = "must be 0 to 99999.9999"
		case !src.Quota.Equal(src.Quota.Round(4)):
			f[prefix+"quota"] = "must have at most 4 decimal places"
		}
		if msg := coverageQtyError(src.BaseQty); msg != "" {
			f[prefix+"baseQty"] = msg
		}
		if msg := coverageQtyError(src.Allocated); msg != "" {
			f[prefix+"allocatedQty"] = msg
		}
		switch {
		case (src.MaxQtyPerPeriod == nil) != (src.PeriodDays == nil):
			f[prefix+"maxQtyPerPeriod"] = "and periodDays must be given together"
		case src.MaxQtyPerPeriod != nil:
			if msg := coverageQtyError(*src.MaxQtyPerPeriod); msg != "" || !src.MaxQtyPerPeriod.IsPositive() {
				f[prefix+"maxQtyPerPeriod"] = "must be a positive quantity with at most 6 decimal places"
			}
			if *src.PeriodDays < 1 || *src.PeriodDays > 366 {
				f[prefix+"periodDays"] = "must be 1 to 366"
			}
		}
		if src.Priority < 0 || src.Priority > 999 {
			f[prefix+"priority"] = "must be 0 to 999"
		}
		total = total.Add(src.Quota)
	}
	if len(f) == 0 && !total.IsPositive() {
		f["sources"] = "the quotas of the sources must add up to more than 0"
	}
	if len(f) == 0 {
		return nil
	}
	return f
}

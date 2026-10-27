package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Physical sample kinds (FR-33.20). A POOLED sample is mixed from PRIMARY samples
// of the same order; a RESERVE sample is the retention sample kept past batch expiry.
const (
	SamplePrimary = "PRIMARY"
	SamplePooled  = "POOLED"
	SampleReserve = "RESERVE"
)

// Physical sample statuses. The status is derived on read: a sample whose
// retention expiry has arrived is DUE_FOR_DISPOSAL (AC-33.18), otherwise ACTIVE.
const (
	SampleActive         = "ACTIVE"
	SampleDueForDisposal = "DUE_FOR_DISPOSAL"
)

// DefaultRetentionYears is the retention after batch expiry (21 CFR 211.170,
// parameter retention_sample_years_after_expiry, default 1). MaxRetentionYears is
// the parameter's ceiling.
const (
	DefaultRetentionYears = 1
	MaxRetentionYears     = 10
	maxSampleLabel        = 60
	maxSampleCondition    = 120
	maxPooledPrimaries    = 50
	// DateLayout is the wire and storage format of calendar dates.
	DateLayout = "2006-01-02"
)

// maxQty is the bound of numeric(18,6): 12 integer digits.
var maxQty = decimal.New(1, 12)

// SampleInput is a request to register a physical sample under a quality order.
// RetentionYears is nil when the default applies.
type SampleInput struct {
	QualityOrderID  string          `json:"qualityOrderId"`
	Kind            string          `json:"kind"`
	PrimaryIDs      []string        `json:"primaryIds"`
	Quantity        decimal.Decimal `json:"quantity"`
	Container       string          `json:"container"`
	StorageLocation string          `json:"storageLocation"`
	Condition       string          `json:"condition"`
	BatchExpiresOn  string          `json:"batchExpiresOn"`
	RetentionYears  *int            `json:"retentionYears"`
}

// Sample is a physical sample (FR-33.20). RetentionExpiresOn is the batch expiry
// plus RetentionYears, set when the batch expiry is known.
type Sample struct {
	ID                 string          `json:"id"`
	QualityOrderID     string          `json:"qualityOrderId"`
	Kind               string          `json:"kind"`
	PrimaryIDs         []string        `json:"primaryIds"`
	Quantity           decimal.Decimal `json:"quantity"`
	UOM                string          `json:"uom"`
	Container          string          `json:"container"`
	StorageLocation    string          `json:"storageLocation"`
	Condition          string          `json:"condition"`
	BatchExpiresOn     string          `json:"batchExpiresOn,omitempty"`
	RetentionYears     int             `json:"retentionYears"`
	RetentionExpiresOn string          `json:"retentionExpiresOn,omitempty"`
	Status             string          `json:"status"`
	RecordedBy         string          `json:"recordedBy"`
	CreatedAt          time.Time       `json:"createdAt"`
}

// ValidSampleStatus checks a list filter.
func ValidSampleStatus(s string) bool { return s == SampleActive || s == SampleDueForDisposal }

// RetentionExpiry is the batch expiry moved forward by years. February 29 moves
// to March 1 in a non-leap year (time.AddDate normalisation), which keeps the
// sample at least the full retention period.
func RetentionExpiry(batchExpires time.Time, years int) time.Time {
	return batchExpires.AddDate(years, 0, 0)
}

// SampleStatus returns DUE_FOR_DISPOSAL once the retention expiry day has arrived
// (AC-33.18: expiry today is due), otherwise ACTIVE. retentionExpiresOn is a
// calendar date (empty when no expiry is known); asOf is compared by its UTC day.
func SampleStatus(retentionExpiresOn string, asOf time.Time) string {
	if retentionExpiresOn != "" && retentionExpiresOn <= asOf.UTC().Format(DateLayout) {
		return SampleDueForDisposal
	}
	return SampleActive
}

// WithStatus returns the sample with its status as of asOf.
func (s Sample) WithStatus(asOf time.Time) Sample {
	s.Status = SampleStatus(s.RetentionExpiresOn, asOf)
	return s
}

// ValidateSample returns field errors for a sample request, empty when it is valid.
// A RESERVE (retention) sample needs the batch expiry; for the others it is optional.
func ValidateSample(in SampleInput) map[string]string {
	fields := map[string]string{}
	if !ids.Valid(in.QualityOrderID) {
		fields["qualityOrderId"] = "must be a UUID"
	}
	switch in.Kind {
	case SamplePrimary, SampleReserve:
		if len(in.PrimaryIDs) > 0 {
			fields["primaryIds"] = "only a POOLED sample references primary samples"
		}
	case SamplePooled:
		if len(in.PrimaryIDs) == 0 || len(in.PrimaryIDs) > maxPooledPrimaries {
			fields["primaryIds"] = "a POOLED sample needs 1 to 50 primary samples"
		}
		seen := map[string]bool{}
		for _, p := range in.PrimaryIDs {
			if !ids.Valid(p) {
				fields["primaryIds"] = "must be UUIDs"
			} else if seen[p] {
				fields["primaryIds"] = "must not repeat a primary sample"
			}
			seen[p] = true
		}
	default:
		fields["kind"] = "must be PRIMARY, POOLED or RESERVE"
	}
	if !in.Quantity.IsPositive() {
		fields["quantity"] = "must be positive"
	} else if in.Quantity.Exponent() < -6 || in.Quantity.GreaterThanOrEqual(maxQty) {
		fields["quantity"] = "must have at most 12 integer digits and 6 decimals"
	}
	for name, v := range map[string]string{"container": in.Container, "storageLocation": in.StorageLocation} {
		if utf8.RuneCountInString(strings.TrimSpace(v)) > maxSampleLabel {
			fields[name] = "must be at most 60 characters"
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(in.Condition)) > maxSampleCondition {
		fields["condition"] = "must be at most 120 characters"
	}
	if in.BatchExpiresOn == "" {
		if in.Kind == SampleReserve {
			fields["batchExpiresOn"] = "is required for a RESERVE (retention) sample"
		}
	} else if _, err := time.Parse(DateLayout, in.BatchExpiresOn); err != nil {
		fields["batchExpiresOn"] = "must be a date (YYYY-MM-DD)"
	}
	if in.RetentionYears != nil && (*in.RetentionYears < 0 || *in.RetentionYears > MaxRetentionYears) {
		fields["retentionYears"] = "must be 0 to 10"
	}
	return fields
}

// NewSample builds a sample of an order from a valid request. The unit is the
// order's; the retention expiry is set when the batch expiry is known.
func NewSample(id string, in SampleInput, uom, user string, now time.Time) (Sample, error) {
	if fields := ValidateSample(in); len(fields) > 0 {
		return Sample{}, invalid("sample request failed validation")
	}
	years := DefaultRetentionYears
	if in.RetentionYears != nil {
		years = *in.RetentionYears
	}
	primaries := append([]string{}, in.PrimaryIDs...)
	s := Sample{
		ID:              id,
		QualityOrderID:  in.QualityOrderID,
		Kind:            in.Kind,
		PrimaryIDs:      primaries,
		Quantity:        in.Quantity,
		UOM:             uom,
		Container:       strings.TrimSpace(in.Container),
		StorageLocation: strings.TrimSpace(in.StorageLocation),
		Condition:       strings.TrimSpace(in.Condition),
		RetentionYears:  years,
		RecordedBy:      user,
		CreatedAt:       now.UTC(),
	}
	if in.BatchExpiresOn != "" {
		batch, _ := time.Parse(DateLayout, in.BatchExpiresOn)
		s.BatchExpiresOn = batch.Format(DateLayout)
		s.RetentionExpiresOn = RetentionExpiry(batch, years).Format(DateLayout)
	}
	return s.WithStatus(now), nil
}

// CheckSampleOrder says whether an order takes a new sample: not when cancelled
// (QM-0115), and samples drawn from the lot (PRIMARY and RESERVE; a POOLED sample
// is mixed from primaries) may not exceed the lot quantity (QM-0182). drawn is the
// quantity already drawn from the lot.
func CheckSampleOrder(o Order, s Sample, drawn decimal.Decimal) error {
	if o.Status == StatusCancelled {
		return fail(CodeStatus, "samples cannot be registered on a CANCELLED order")
	}
	if s.Kind != SamplePooled && drawn.Add(s.Quantity).GreaterThan(o.LotQty) {
		return fail(CodeSampleOverLot, "samples drawn ("+drawn.Add(s.Quantity).String()+") would exceed the lot quantity "+o.LotQty.String())
	}
	return nil
}

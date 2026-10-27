package domain

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

// Scheduling agreements (FR-17.15): a vendor × product × site agreement with a
// schedule of dated quantities. A release run snapshots the schedule into a
// numbered release of kind FRC (forecast) or JIT (just-in-time) carrying
// cumulative released and received quantities.
const (
	ReleaseFRC = "FRC"
	ReleaseJIT = "JIT"

	// Release frequencies of a release profile: how often the release run is due.
	FrequencyDaily   = "DAILY"
	FrequencyWeekly  = "WEEKLY"
	FrequencyMonthly = "MONTHLY"

	// MaxScheduleLines caps the schedule rows of one agreement.
	MaxScheduleLines = 500

	// Default release profile (PRD 3.17.13 sa_release_profile): FRC 90 days,
	// JIT 14 days, firm zone 14 days, trade-off zone 28 days, weekly.
	DefaultFRCHorizonDays   = 90
	DefaultJITHorizonDays   = 14
	DefaultFirmZoneDays     = 14
	DefaultTradeoffZoneDays = 28
	DefaultReleaseFrequency = FrequencyWeekly

	// CodeReceivedAboveRelease is PRC-SA-002: a schedule change would leave a
	// line scheduled below what was already received against it, so cumulative
	// received would exceed cumulative released.
	CodeReceivedAboveRelease = "PRC-SA-002"
	// CodeCumulativeDecrease is PRC-SA-003: a release would lower the cumulative
	// released quantity of the previous release without a recorded cumulative
	// reset (BR-17.13).
	CodeCumulativeDecrease = "PRC-SA-003"
)

// ReleaseProfile holds the horizons of a scheduling agreement in days and the
// release frequency.
type ReleaseProfile struct {
	FRCHorizonDays   int
	JITHorizonDays   int
	FirmDays         int
	TradeoffDays     int
	ReleaseFrequency string
}

// DefaultReleaseProfile is the PRD 3.17.13 default.
func DefaultReleaseProfile() ReleaseProfile {
	return ReleaseProfile{
		FRCHorizonDays: DefaultFRCHorizonDays, JITHorizonDays: DefaultJITHorizonDays,
		FirmDays: DefaultFirmZoneDays, TradeoffDays: DefaultTradeoffZoneDays, ReleaseFrequency: DefaultReleaseFrequency,
	}
}

// Zones returns the firm and trade-off horizons as a ZoneProfile.
func (p ReleaseProfile) Zones() ZoneProfile {
	return ZoneProfile{FirmDays: p.FirmDays, TradeoffDays: p.TradeoffDays}
}

// Validate requires non-negative horizons, 0 ≤ firm ≤ trade-off and a known frequency.
func (p ReleaseProfile) Validate() error {
	if p.FRCHorizonDays < 0 || p.JITHorizonDays < 0 {
		return fail(CodeValidation, "release horizons must be >= 0")
	}
	switch p.ReleaseFrequency {
	case FrequencyDaily, FrequencyWeekly, FrequencyMonthly:
	default:
		return fail(CodeValidation, "release frequency must be DAILY, WEEKLY or MONTHLY, got %q", p.ReleaseFrequency)
	}
	return p.Zones().Validate()
}

// NextReleaseDue is the day the next release run is due under the profile's
// frequency: the agreement start before any release, otherwise the last run
// plus one period. It is nil once that day falls after an end of validity.
func (p ReleaseProfile) NextReleaseDue(validFrom Day, validTo *Day, lastRun *Day) *Day {
	next := validFrom
	if lastRun != nil {
		switch p.ReleaseFrequency {
		case FrequencyDaily:
			next = Day{lastRun.AddDate(0, 0, 1)}
		case FrequencyMonthly:
			next = Day{lastRun.AddDate(0, 1, 0)}
		default:
			next = Day{lastRun.AddDate(0, 0, 7)}
		}
	}
	if validTo != nil && next.After(validTo.Time) {
		return nil
	}
	return &next
}

// ScheduleLine is one dated quantity of a scheduling agreement. ReceivedQty is
// the cumulative receipt against the line, written by receipts (wave 18).
type ScheduleLine struct {
	LineNo       int
	DeliveryDate Day
	Qty          decimal.Decimal
	ReceivedQty  decimal.Decimal
	// ReductionReason is the reason code given with the last reduction of the
	// line (required inside the firm zone, BR-17.13).
	ReductionReason string
}

// SchedulingAgreement is the header plus schedule lines of an FR-17.15 agreement.
// UnitPrice per Unit in Currency is its price term.
type SchedulingAgreement struct {
	ID            string
	Number        string
	LegalEntityID string
	VendorID      string
	ProductID     string
	SiteID        string
	Currency      string
	Unit          string
	UnitPrice     decimal.Decimal
	TargetQty     decimal.Decimal
	ValidFrom     Day
	ValidTo       *Day
	Profile       ReleaseProfile
	Lines         []ScheduleLine
	Version       int
}

// Validate checks the header, the profile and the schedule rows.
func (sa SchedulingAgreement) Validate() error {
	if len(sa.Currency) != 3 {
		return fail(CodeValidation, "currency must be an ISO 4217 code")
	}
	if sa.Unit == "" {
		return fail(CodeValidation, "unit is required")
	}
	if sa.UnitPrice.IsNegative() {
		return fail(CodeValidation, "unit price must be >= 0")
	}
	if sa.TargetQty.IsNegative() {
		return fail(CodeValidation, "target quantity must be >= 0")
	}
	if sa.ValidTo != nil && sa.ValidTo.Before(sa.ValidFrom.Time) {
		return fail(CodeValidation, "valid_to %s is before valid_from %s", sa.ValidTo, sa.ValidFrom)
	}
	if err := sa.Profile.Validate(); err != nil {
		return err
	}
	if len(sa.Lines) == 0 {
		return fail(CodeValidation, "a scheduling agreement needs at least one schedule line")
	}
	if len(sa.Lines) > MaxScheduleLines {
		return fail(CodeValidation, "a scheduling agreement may have at most %d schedule lines", MaxScheduleLines)
	}
	for _, l := range sa.Lines {
		if !l.Qty.IsPositive() {
			return fail(CodeValidation, "schedule line %d quantity must be > 0", l.LineNo)
		}
		if l.DeliveryDate.IsZero() {
			return fail(CodeValidation, "schedule line %d needs a delivery date", l.LineNo)
		}
		if l.DeliveryDate.Before(sa.ValidFrom.Time) || (sa.ValidTo != nil && l.DeliveryDate.After(sa.ValidTo.Time)) {
			return fail(CodeValidation, "schedule line %d date %s is outside the agreement validity", l.LineNo, l.DeliveryDate)
		}
	}
	return nil
}

// ValidReleaseKind accepts FRC or JIT.
func ValidReleaseKind(kind string) error {
	if kind != ReleaseFRC && kind != ReleaseJIT {
		return fail(CodeValidation, "release kind must be FRC or JIT, got %q", kind)
	}
	return nil
}

// SAReleaseLine is one schedule row as it appears on a release. Cumulative figures
// run over the schedule in delivery-date order. InHorizon marks rows inside the
// kind's horizon (FRC or JIT days from the run date), the rows EDI DELFOR/DELJIT
// transmit (wave 35).
type SAReleaseLine struct {
	LineNo             int
	ScheduleLineNo     int
	DeliveryDate       Day
	Zone               string
	InHorizon          bool
	Qty                decimal.Decimal
	CumulativeReleased decimal.Decimal
	CumulativeReceived decimal.Decimal
}

// SARelease is a numbered snapshot of a scheduling agreement (SAP ME84, ME85/ME86).
// AgreementVersion is the agreement version the release was taken from; a run
// with the same kind and run date on an unchanged schedule returns this release.
type SARelease struct {
	ID                 string
	AgreementID        string
	ReleaseNo          int
	Number             string
	Kind               string
	RunDate            Day
	AgreementVersion   int
	CumulativeReset    bool
	CumulativeReleased decimal.Decimal
	CumulativeReceived decimal.Decimal
	Lines              []SAReleaseLine
}

// BuildRelease snapshots sa into release releaseNo on runDate. previous is the
// cumulative released quantity of the last release; reset records a cumulative
// reset (a new agreement year, BR-17.13) that lets the figure go down. The
// release number is REL-<agreement number>-<n> (PRD 3.17.15).
func BuildRelease(sa SchedulingAgreement, kind string, runDate Day, releaseNo int, previous decimal.Decimal, reset bool) (SARelease, error) {
	if err := ValidReleaseKind(kind); err != nil {
		return SARelease{}, err
	}
	if err := sa.Validate(); err != nil {
		return SARelease{}, err
	}
	if runDate.Before(sa.ValidFrom.Time) || (sa.ValidTo != nil && runDate.After(sa.ValidTo.Time)) {
		return SARelease{}, fail(CodeValidation, "run date %s is outside the agreement validity", runDate)
	}
	lines := append([]ScheduleLine(nil), sa.Lines...)
	sort.SliceStable(lines, func(i, j int) bool {
		if !lines[i].DeliveryDate.Equal(lines[j].DeliveryDate.Time) {
			return lines[i].DeliveryDate.Before(lines[j].DeliveryDate.Time)
		}
		return lines[i].LineNo < lines[j].LineNo
	})
	horizon := sa.Profile.JITHorizonDays
	if kind == ReleaseFRC {
		horizon = sa.Profile.FRCHorizonDays
	}
	zones := sa.Profile.Zones()
	rel := SARelease{
		AgreementID:        sa.ID,
		ReleaseNo:          releaseNo,
		Number:             fmt.Sprintf("REL-%s-%d", sa.Number, releaseNo),
		Kind:               kind,
		RunDate:            runDate,
		AgreementVersion:   sa.Version,
		CumulativeReset:    reset,
		CumulativeReleased: decimal.Zero,
		CumulativeReceived: decimal.Zero,
	}
	for i, l := range lines {
		rel.CumulativeReleased = rel.CumulativeReleased.Add(l.Qty)
		rel.CumulativeReceived = rel.CumulativeReceived.Add(l.ReceivedQty)
		rel.Lines = append(rel.Lines, SAReleaseLine{
			LineNo:             i + 1,
			ScheduleLineNo:     l.LineNo,
			DeliveryDate:       l.DeliveryDate,
			Zone:               zones.ClassifyZone(runDate, l.DeliveryDate),
			InHorizon:          daysBetween(runDate, l.DeliveryDate) <= horizon,
			Qty:                l.Qty,
			CumulativeReleased: rel.CumulativeReleased,
			CumulativeReceived: rel.CumulativeReceived,
		})
	}
	if err := CheckCumulativeRelease(previous, rel.CumulativeReleased, reset); err != nil {
		return SARelease{}, err
	}
	return rel, nil
}

// daysBetween is the whole days from a to b (negative when b is earlier).
func daysBetween(a, b Day) int {
	return int(b.Sub(a.Time).Hours() / 24)
}

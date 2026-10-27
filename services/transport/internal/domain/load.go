package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Load modes (FR-34.1).
const (
	ModeTL         = "TL"
	ModeLTL        = "LTL"
	ModeParcel     = "PARCEL"
	ModeIntermodal = "INTERMODAL"
)

// LoadModes lists the modes in display order.
var LoadModes = []string{ModeTL, ModeLTL, ModeParcel, ModeIntermodal}

// LoadPlanned is the state of a load built by rule and not yet rated or tendered.
// The other §6.34.4 states (RATED, TENDERED, ACCEPTED, DISPATCHED, IN_TRANSIT,
// DELIVERED, CLOSED, CANCELLED) are set by later steps of the load's life.
const LoadPlanned = "PLANNED"

// MaxBuildShipments bounds one load build request.
const MaxBuildShipments = 500

// Slice-local codes (the catalog leaves TMS-0111 to TMS-0120 unassigned; the
// README lists them).
const (
	// CodeShipmentTooLarge: one shipment exceeds the capacity of an empty load
	// on its own. A shipment is never split, so the build is refused.
	CodeShipmentTooLarge = "TMS-0115"
	// CodeShipmentPlanned: a shipment is already on another load.
	CodeShipmentPlanned = "TMS-0116"
)

var (
	lowerUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// maxScale is the number of decimals a quantity column (numeric(20,6)) keeps.
	maxScale int32 = 6
)

// Capacity is the hard limit of one load in the three dimensions of BR-34.1 (1):
// weight, volume and pallet positions.
type Capacity struct {
	MaxWeightKg decimal.Decimal `json:"maxWeightKg"`
	MaxVolumeM3 decimal.Decimal `json:"maxVolumeM3"`
	MaxPallets  int             `json:"maxPallets"`
}

// LoadShipment is the planning view of one sales, transfer or inbound shipment.
type LoadShipment struct {
	ID             string          `json:"id"`
	ShipFromSiteID string          `json:"shipFromSiteId"`
	ShipToRegion   string          `json:"shipToRegion"`
	Mode           string          `json:"mode"`
	PickupDate     string          `json:"pickupDate"`
	WeightKg       decimal.Decimal `json:"weightKg"`
	VolumeM3       decimal.Decimal `json:"volumeM3"`
	Pallets        int             `json:"pallets"`
}

// Load is a set of shipments moved together. Shipments on one load share
// ship-from site and mode (§6.34.4 invariant) and, when built by rule, ship-to
// region and pickup date (FR-34.1).
type Load struct {
	ID             string          `json:"id"`
	Mode           string          `json:"mode"`
	Status         string          `json:"status"`
	ShipFromSiteID string          `json:"shipFromSiteId"`
	ShipToRegion   string          `json:"shipToRegion"`
	PickupDate     string          `json:"pickupDate"`
	TotalWeightKg  decimal.Decimal `json:"totalWeightKg"`
	TotalVolumeM3  decimal.Decimal `json:"totalVolumeM3"`
	Pallets        int             `json:"pallets"`
	Capacity       Capacity        `json:"capacity"`
	ShipmentIDs    []string        `json:"shipmentIds"`
	Version        int             `json:"version"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// ValidateLoadBuild reports field errors for a rule-based load build.
func ValidateLoadBuild(shipments []LoadShipment, cp Capacity) map[string]string {
	f := map[string]string{}
	if len(shipments) == 0 || len(shipments) > MaxBuildShipments {
		f["shipments"] = fmt.Sprintf("must hold 1 to %d shipments", MaxBuildShipments)
	}
	if !cp.MaxWeightKg.IsPositive() || cp.MaxWeightKg.Exponent() < -maxScale {
		f["capacity.maxWeightKg"] = "must be greater than zero with at most 6 decimals"
	}
	if !cp.MaxVolumeM3.IsPositive() || cp.MaxVolumeM3.Exponent() < -maxScale {
		f["capacity.maxVolumeM3"] = "must be greater than zero with at most 6 decimals"
	}
	if cp.MaxPallets <= 0 {
		f["capacity.maxPallets"] = "must be greater than zero"
	}
	seen := map[string]bool{}
	for i, s := range shipments {
		p := fmt.Sprintf("shipments[%d]", i)
		switch {
		case !lowerUUID.MatchString(s.ID):
			f[p+".id"] = "must be a lower-case UUID"
		case seen[s.ID]:
			f[p+".id"] = "appears more than once in the request"
		}
		seen[s.ID] = true
		if !lowerUUID.MatchString(s.ShipFromSiteID) {
			f[p+".shipFromSiteId"] = "must be a lower-case UUID"
		}
		if n := len(strings.TrimSpace(s.ShipToRegion)); n == 0 || n > 20 {
			f[p+".shipToRegion"] = "must be 1 to 20 characters"
		}
		if !contains(LoadModes, s.Mode) {
			f[p+".mode"] = "must be TL, LTL, PARCEL or INTERMODAL"
		}
		if _, err := time.Parse(time.DateOnly, s.PickupDate); err != nil {
			f[p+".pickupDate"] = "must be a date as YYYY-MM-DD"
		}
		if s.WeightKg.IsNegative() || s.WeightKg.Exponent() < -maxScale {
			f[p+".weightKg"] = "must be zero or more with at most 6 decimals"
		}
		if s.VolumeM3.IsNegative() || s.VolumeM3.Exponent() < -maxScale {
			f[p+".volumeM3"] = "must be zero or more with at most 6 decimals"
		}
		if s.Pallets < 0 {
			f[p+".pallets"] = "must not be negative"
		}
	}
	return f
}

// TooLargeError names the first shipment that exceeds an empty load on its own.
type TooLargeError struct{ ShipmentID string }

func (e *TooLargeError) Error() string {
	return CodeShipmentTooLarge + ": shipment " + e.ShipmentID + " exceeds the load capacity on its own"
}

// BuildLoads groups shipments by ship-from site, ship-to region, mode and pickup
// date (FR-34.1), then packs each group first-fit decreasing by weight. No load
// exceeds capacity in any dimension: a shipment that does not fit an open load
// opens another (BR-34.8 hard split on rule-based building). The output is the
// same for the same input, whatever the input order.
func BuildLoads(shipments []LoadShipment, cp Capacity) ([]Load, error) {
	for _, s := range shipments {
		if s.WeightKg.GreaterThan(cp.MaxWeightKg) || s.VolumeM3.GreaterThan(cp.MaxVolumeM3) || s.Pallets > cp.MaxPallets {
			return nil, &TooLargeError{ShipmentID: s.ID}
		}
	}
	sorted := append([]LoadShipment(nil), shipments...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if ka, kb := groupKey(a), groupKey(b); ka != kb {
			return ka < kb
		}
		if !a.WeightKg.Equal(b.WeightKg) {
			return a.WeightKg.GreaterThan(b.WeightKg)
		}
		return a.ID < b.ID
	})
	var loads []Load
	open := map[string][]int{}
	for _, s := range sorted {
		k := groupKey(s)
		placed := false
		for _, idx := range open[k] {
			if fits(loads[idx], s, cp) {
				addToLoad(&loads[idx], s)
				placed = true
				break
			}
		}
		if placed {
			continue
		}
		l := Load{
			Mode:           s.Mode,
			Status:         LoadPlanned,
			ShipFromSiteID: s.ShipFromSiteID,
			ShipToRegion:   s.ShipToRegion,
			PickupDate:     s.PickupDate,
			TotalWeightKg:  decimal.Zero,
			TotalVolumeM3:  decimal.Zero,
			Capacity:       cp,
		}
		addToLoad(&l, s)
		loads = append(loads, l)
		open[k] = append(open[k], len(loads)-1)
	}
	return loads, nil
}

func groupKey(s LoadShipment) string {
	return strings.Join([]string{s.ShipFromSiteID, s.ShipToRegion, s.Mode, s.PickupDate}, "|")
}

func fits(l Load, s LoadShipment, cp Capacity) bool {
	return l.TotalWeightKg.Add(s.WeightKg).LessThanOrEqual(cp.MaxWeightKg) &&
		l.TotalVolumeM3.Add(s.VolumeM3).LessThanOrEqual(cp.MaxVolumeM3) &&
		l.Pallets+s.Pallets <= cp.MaxPallets
}

func addToLoad(l *Load, s LoadShipment) {
	l.TotalWeightKg = l.TotalWeightKg.Add(s.WeightKg)
	l.TotalVolumeM3 = l.TotalVolumeM3.Add(s.VolumeM3)
	l.Pallets += s.Pallets
	l.ShipmentIDs = append(l.ShipmentIDs, s.ID)
}

package domain

import (
	"fmt"
	"strings"
)

// Load plan stacking (FR-34.18). The layer builder runs in services/routing-py
// (POST /v1/load-plans:stack); this file validates the request and checks the
// reply. Dimensions are whole millimetres and weights whole kilograms.
const (
	MaxPlanPallets      = 200
	maxPlanDimensionMm  = 30_000
	maxPlanWeightKg     = 100_000
	maxPlanStopSequence = 1_000
)

// Trailer is the loading space of a trailer or container. KingpinMm and AxleMm are
// the support points of the axle-weight estimate, measured from the nose; zero
// takes the nose and the rear door.
type Trailer struct {
	LengthMm    int `json:"lengthMm"`
	WidthMm     int `json:"widthMm"`
	MaxHeightMm int `json:"maxHeightMm"`
	MaxWeightKg int `json:"maxWeightKg"`
	KingpinMm   int `json:"kingpinMm,omitempty"`
	AxleMm      int `json:"axleMm,omitempty"`
}

// PlanPallet is one pallet to place. StopSeq is the position of its stop on the
// route: later stops load first (reverse stop order). MaxStackKg bounds the
// weight of a stack built on this pallet.
type PlanPallet struct {
	ID         string `json:"id"`
	LengthMm   int    `json:"lengthMm"`
	WidthMm    int    `json:"widthMm"`
	HeightMm   int    `json:"heightMm"`
	WeightKg   int    `json:"weightKg"`
	Stackable  bool   `json:"stackable"`
	StopSeq    int    `json:"stopSeq"`
	MaxStackKg *int   `json:"maxStackKg,omitempty"`
}

// LoadPlanRequest is a trailer and the pallets to stack on it.
type LoadPlanRequest struct {
	Trailer Trailer      `json:"trailer"`
	Pallets []PlanPallet `json:"pallets"`
}

// Placement is one floor position: the pallets on it bottom to top, the
// position of its corner from the nose-left corner, and its height and weight.
type Placement struct {
	PalletIDs []string `json:"palletIds"`
	XMm       int      `json:"xMm"`
	YMm       int      `json:"yMm"`
	LengthMm  int      `json:"lengthMm"`
	WidthMm   int      `json:"widthMm"`
	HeightMm  int      `json:"heightMm"`
	WeightKg  int      `json:"weightKg"`
}

// AxleEstimate splits the placed weight between the kingpin and the rear axle
// group as a beam on two supports.
type AxleEstimate struct {
	KingpinKg int `json:"kingpinKg"`
	AxleKg    int `json:"axleKg"`
}

// LoadPlan is the stacking result. Every pallet is placed once or listed in
// Unplaced; LoadSequence is the order to put the pallets on the trailer.
type LoadPlan struct {
	Placements         []Placement  `json:"placements"`
	Unplaced           []string     `json:"unplaced"`
	FloorPositionsUsed int          `json:"floorPositionsUsed"`
	LengthUsedMm       int          `json:"lengthUsedMm"`
	Layers             int          `json:"layers"`
	TotalWeightKg      int          `json:"totalWeightKg"`
	LoadSequence       []string     `json:"loadSequence"`
	Axle               AxleEstimate `json:"axle"`
}

// Normalize trims the pallet ids.
func (r *LoadPlanRequest) Normalize() {
	for i := range r.Pallets {
		r.Pallets[i].ID = strings.TrimSpace(r.Pallets[i].ID)
	}
}

// Validate reports field errors for a load plan request.
func (r LoadPlanRequest) Validate() map[string]string {
	f := map[string]string{}
	t := r.Trailer
	if !positive(t.LengthMm, maxPlanDimensionMm) || !positive(t.WidthMm, maxPlanDimensionMm) || !positive(t.MaxHeightMm, maxPlanDimensionMm) {
		f["trailer"] = fmt.Sprintf("length, width and height must be 1 to %d mm", maxPlanDimensionMm)
	}
	if !positive(t.MaxWeightKg, maxPlanWeightKg) {
		f["trailer.maxWeightKg"] = fmt.Sprintf("must be 1 to %d kg", maxPlanWeightKg)
	}
	axle := t.AxleMm
	if axle == 0 {
		axle = t.LengthMm
	}
	if t.KingpinMm < 0 || t.AxleMm < 0 || axle > t.LengthMm || t.KingpinMm >= axle {
		f["trailer.axleMm"] = "kingpin and axle must lie on the trailer, axle behind the kingpin"
	}
	if len(r.Pallets) == 0 || len(r.Pallets) > MaxPlanPallets {
		f["pallets"] = fmt.Sprintf("must hold 1 to %d pallets", MaxPlanPallets)
	}
	seen := map[string]bool{}
	for i, p := range r.Pallets {
		k := fmt.Sprintf("pallets[%d]", i)
		switch {
		case p.ID == "" || len(p.ID) > maxRouteIDLength:
			f[k+".id"] = "must be 1 to 64 characters"
		case seen[p.ID]:
			f[k+".id"] = "duplicate pallet id"
		}
		seen[p.ID] = true
		if !positive(p.LengthMm, maxPlanDimensionMm) || !positive(p.WidthMm, maxPlanDimensionMm) || !positive(p.HeightMm, maxPlanDimensionMm) {
			f[k] = fmt.Sprintf("length, width and height must be 1 to %d mm", maxPlanDimensionMm)
		}
		if !inRange(p.WeightKg, maxPlanWeightKg) || (p.MaxStackKg != nil && !inRange(*p.MaxStackKg, maxPlanWeightKg)) {
			f[k+".weightKg"] = fmt.Sprintf("weights must be 0 to %d kg", maxPlanWeightKg)
		}
		if !inRange(p.StopSeq, maxPlanStopSequence) {
			f[k+".stopSeq"] = fmt.Sprintf("must be 0 to %d", maxPlanStopSequence)
		}
	}
	return f
}

// CheckLoadPlan reports why a reply is not a plan for req, or "" when it is:
// every pallet is placed exactly once or listed as unplaced, no pallet sits on a
// non-stackable one (AC-34.18), and the floor count matches the placements.
func CheckLoadPlan(req LoadPlanRequest, plan LoadPlan) string {
	pallets := make(map[string]PlanPallet, len(req.Pallets))
	for _, p := range req.Pallets {
		pallets[p.ID] = p
	}
	seen := map[string]bool{}
	mark := func(id string) string {
		if _, ok := pallets[id]; !ok {
			return "unknown pallet " + id
		}
		if seen[id] {
			return "pallet " + id + " appears more than once"
		}
		seen[id] = true
		return ""
	}
	for _, pl := range plan.Placements {
		if len(pl.PalletIDs) == 0 {
			return "empty floor position"
		}
		for i, id := range pl.PalletIDs {
			if msg := mark(id); msg != "" {
				return msg
			}
			if i < len(pl.PalletIDs)-1 && !pallets[id].Stackable {
				return "pallet " + id + " is not stackable but carries another pallet"
			}
		}
	}
	for _, id := range plan.Unplaced {
		if msg := mark(id); msg != "" {
			return msg
		}
	}
	if len(seen) != len(pallets) {
		return "a pallet is neither placed nor unplaced"
	}
	if plan.FloorPositionsUsed != len(plan.Placements) {
		return "floor positions do not match the placements"
	}
	return ""
}

func positive(v, max int) bool { return v > 0 && v <= max }

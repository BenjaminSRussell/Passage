package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Route optimization (FR-34.2, BR-34.1). The solver runs in services/routing-py
// (POST /v1/routes:solve); this file validates the request, fingerprints it and
// checks the reply. Coordinates are planar kilometres in a local projection until
// the OSRM matrix adapter lands.
const (
	MaxRouteStops    = 500
	MaxRouteVehicles = 50
	MinVRPTimeLimitS = 5
	// MaxVRPTimeLimitS is the longest solver budget a synchronous call accepts. The
	// platform router ends every request after 30 s, so the PRD's 60 s default
	// (vrp_time_limit_s 5 to 600) needs the asynchronous optimizer run (NOT-IMPLEMENTED.md).
	MaxVRPTimeLimitS       = 25
	DefaultVRPTimeLimitS   = 20
	DefaultSpeedKmh        = 60
	DefaultMaxRouteMin     = 660 // 11 h driving (BR-34.1 item 3)
	DefaultLatenessPerMin  = 50  // lateness_penalty_per_min, money units (BR-34.1 item 2)
	maxRouteIDLength       = 64
	maxRouteCoordinateKm   = 20_000
	maxRouteWindowMin      = 7 * 24 * 60
	maxRouteQuantity       = 1_000_000
	maxRouteFixedCost      = 1_000_000
	maxRouteSpeedKmh       = 200
	maxRouteLatenessPerMin = 10_000
)

// Route codes. TMS-0104 is the catalog's dropped-stops warning; TMS-0123 is
// slice-local (the catalog leaves it unassigned) for a solver that is not
// configured, cannot be reached or answers with something that is not a plan.
const (
	CodeStopsDropped         = "TMS-0104"
	CodeOptimizerUnavailable = "TMS-0123"
)

// RoutePoint is a planar location in kilometres.
type RoutePoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// RouteStop is one stop with its time window in minutes from route start.
type RouteStop struct {
	ID         string     `json:"id"`
	Point      RoutePoint `json:"point"`
	Earliest   int        `json:"earliest"`
	Latest     int        `json:"latest"`
	ServiceMin int        `json:"serviceMin"`
	WeightKg   int        `json:"weightKg"`
	VolumeL    int        `json:"volumeL"`
	Pallets    int        `json:"pallets"`
}

// RouteVehicle is one vehicle of the fleet (BR-34.1 items 1 and 4). FixedCost is
// the solver's cost weight in whole money units, not an accounting amount.
type RouteVehicle struct {
	ID          string `json:"id"`
	CapacityKg  int    `json:"capacityKg"`
	CapacityL   int    `json:"capacityL"`
	Pallets     int    `json:"pallets"`
	FixedCost   int    `json:"fixedCost"`
	MaxRouteMin int    `json:"maxRouteMin"`
}

// RouteRequest is the VRP input. Normalize fills the defaults; LatenessPerMin is a
// pointer so that an explicit 0 (no lateness penalty) differs from "use the default".
type RouteRequest struct {
	Depot          RoutePoint     `json:"depot"`
	Stops          []RouteStop    `json:"stops"`
	Vehicles       []RouteVehicle `json:"vehicles"`
	SpeedKmh       float64        `json:"speedKmh"`
	LatenessPerMin *int           `json:"latenessPerMin"`
	TimeLimitS     int            `json:"timeLimitS"`
}

// RouteSolutionStop is a served stop with its arrival and lateness in minutes.
type RouteSolutionStop struct {
	StopID     string `json:"stopId"`
	ArrivalMin int    `json:"arrivalMin"`
	LateMin    int    `json:"lateMin"`
}

// RouteSolutionRoute is one vehicle's route.
type RouteSolutionRoute struct {
	VehicleID  string              `json:"vehicleId"`
	Stops      []RouteSolutionStop `json:"stops"`
	DistanceKm float64             `json:"distanceKm"`
	EndMin     int                 `json:"endMin"`
}

// RouteSolution is the solver output. Dropped stops are reported, never silently
// omitted (BR-34.1).
type RouteSolution struct {
	Routes  []RouteSolutionRoute `json:"routes"`
	Dropped []string             `json:"dropped"`
}

// RoutePlan is a stored optimization: the normalized request, its hash (the
// idempotency key) and the solver's routes.
type RoutePlan struct {
	RoutePlanSummary
	Request  RouteRequest  `json:"request"`
	Solution RouteSolution `json:"solution"`
}

// RoutePlanSummary is the list view of a plan.
type RoutePlanSummary struct {
	ID              string    `json:"id"`
	RequestHash     string    `json:"requestHash"`
	StopCount       int       `json:"stopCount"`
	VehicleCount    int       `json:"vehicleCount"`
	RouteCount      int       `json:"routeCount"`
	DroppedCount    int       `json:"droppedCount"`
	TotalDistanceKm float64   `json:"totalDistanceKm"`
	TimeLimitS      int       `json:"timeLimitS"`
	CreatedBy       string    `json:"createdBy"`
	CreatedAt       time.Time `json:"createdAt"`
}

// NewRoutePlan summarizes a checked solution for storage. The total distance is
// rounded to metres.
func NewRoutePlan(req RouteRequest, hash string, sol RouteSolution) RoutePlan {
	total := 0.0
	routes := 0
	for _, rt := range sol.Routes {
		total += rt.DistanceKm
		if len(rt.Stops) > 0 {
			routes++
		}
	}
	return RoutePlan{
		RoutePlanSummary: RoutePlanSummary{
			RequestHash:     hash,
			StopCount:       len(req.Stops),
			VehicleCount:    len(req.Vehicles),
			RouteCount:      routes,
			DroppedCount:    len(sol.Dropped),
			TotalDistanceKm: math.Round(total*1000) / 1000,
			TimeLimitS:      req.TimeLimitS,
		},
		Request:  req,
		Solution: sol,
	}
}

// Normalize fills defaults and trims identifiers. Call it before Validate and Hash.
func (r *RouteRequest) Normalize() {
	if r.SpeedKmh == 0 {
		r.SpeedKmh = DefaultSpeedKmh
	}
	if r.TimeLimitS == 0 {
		r.TimeLimitS = DefaultVRPTimeLimitS
	}
	if r.LatenessPerMin == nil {
		v := DefaultLatenessPerMin
		r.LatenessPerMin = &v
	}
	for i := range r.Stops {
		r.Stops[i].ID = strings.TrimSpace(r.Stops[i].ID)
	}
	for i := range r.Vehicles {
		r.Vehicles[i].ID = strings.TrimSpace(r.Vehicles[i].ID)
		if r.Vehicles[i].MaxRouteMin == 0 {
			r.Vehicles[i].MaxRouteMin = DefaultMaxRouteMin
		}
	}
}

// Validate reports field errors for a normalized request. The bounds keep every
// value inside what the solver's integer model handles.
func (r RouteRequest) Validate() map[string]string {
	f := map[string]string{}
	if len(r.Stops) == 0 || len(r.Stops) > MaxRouteStops {
		f["stops"] = fmt.Sprintf("must hold 1 to %d stops", MaxRouteStops)
	}
	if len(r.Vehicles) == 0 || len(r.Vehicles) > MaxRouteVehicles {
		f["vehicles"] = fmt.Sprintf("must hold 1 to %d vehicles", MaxRouteVehicles)
	}
	if r.TimeLimitS < MinVRPTimeLimitS || r.TimeLimitS > MaxVRPTimeLimitS {
		f["timeLimitS"] = fmt.Sprintf("must be %d to %d seconds", MinVRPTimeLimitS, MaxVRPTimeLimitS)
	}
	if !finite(r.SpeedKmh) || r.SpeedKmh <= 0 || r.SpeedKmh > maxRouteSpeedKmh {
		f["speedKmh"] = fmt.Sprintf("must be above 0 and at most %d", maxRouteSpeedKmh)
	}
	if r.LatenessPerMin != nil && (*r.LatenessPerMin < 0 || *r.LatenessPerMin > maxRouteLatenessPerMin) {
		f["latenessPerMin"] = fmt.Sprintf("must be 0 to %d", maxRouteLatenessPerMin)
	}
	if !validPoint(r.Depot) {
		f["depot"] = fmt.Sprintf("coordinates must be finite and within %d km", maxRouteCoordinateKm)
	}
	seen := map[string]bool{}
	for i, s := range r.Stops {
		k := fmt.Sprintf("stops[%d]", i)
		switch {
		case s.ID == "" || len(s.ID) > maxRouteIDLength:
			f[k+".id"] = "must be 1 to 64 characters"
		case seen[s.ID]:
			f[k+".id"] = "duplicate stop id"
		}
		seen[s.ID] = true
		if s.Earliest < 0 || s.Latest < s.Earliest || s.Latest > maxRouteWindowMin {
			f[k+".latest"] = fmt.Sprintf("window must satisfy 0 <= earliest <= latest <= %d", maxRouteWindowMin)
		}
		if !inRange(s.ServiceMin, maxRouteWindowMin) || !inRange(s.WeightKg, maxRouteQuantity) ||
			!inRange(s.VolumeL, maxRouteQuantity) || !inRange(s.Pallets, maxRouteQuantity) {
			f[k] = "service time and quantities must be 0 or more and within bounds"
		}
		if !validPoint(s.Point) {
			f[k+".point"] = fmt.Sprintf("coordinates must be finite and within %d km", maxRouteCoordinateKm)
		}
	}
	vseen := map[string]bool{}
	for i, v := range r.Vehicles {
		k := fmt.Sprintf("vehicles[%d]", i)
		switch {
		case v.ID == "" || len(v.ID) > maxRouteIDLength:
			f[k+".id"] = "must be 1 to 64 characters"
		case vseen[v.ID]:
			f[k+".id"] = "duplicate vehicle id"
		}
		vseen[v.ID] = true
		if !inRange(v.CapacityKg, maxRouteQuantity) || !inRange(v.CapacityL, maxRouteQuantity) ||
			!inRange(v.Pallets, maxRouteQuantity) || !inRange(v.FixedCost, maxRouteFixedCost) {
			f[k] = "capacities and fixed cost must be 0 or more and within bounds"
		}
		if v.MaxRouteMin <= 0 || v.MaxRouteMin > maxRouteWindowMin {
			f[k+".maxRouteMin"] = fmt.Sprintf("must be 1 to %d", maxRouteWindowMin)
		}
	}
	return f
}

// Hash returns the SHA-256 of the normalized request. It is the idempotency key
// of route optimization (§6.34.5 "key = request hash"): the same request returns
// the stored plan instead of solving again.
func (r RouteRequest) Hash() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// CheckSolution reports why a solver reply is not a plan for req, or "" when it
// is: every stop of the request is served exactly once or listed as dropped, and
// every route names a vehicle of the request at most once. A reply that loses a
// stop is refused, so a stop is never dropped silently (BR-34.1).
func CheckSolution(req RouteRequest, sol RouteSolution) string {
	stops := make(map[string]bool, len(req.Stops))
	for _, s := range req.Stops {
		stops[s.ID] = false
	}
	vehicles := make(map[string]bool, len(req.Vehicles))
	for _, v := range req.Vehicles {
		vehicles[v.ID] = false
	}
	mark := func(id string) string {
		done, ok := stops[id]
		switch {
		case !ok:
			return "unknown stop " + id
		case done:
			return "stop " + id + " appears more than once"
		}
		stops[id] = true
		return ""
	}
	for _, rt := range sol.Routes {
		used, ok := vehicles[rt.VehicleID]
		switch {
		case !ok:
			return "unknown vehicle " + rt.VehicleID
		case used:
			return "vehicle " + rt.VehicleID + " has more than one route"
		}
		vehicles[rt.VehicleID] = true
		if !finite(rt.DistanceKm) || rt.DistanceKm < 0 {
			return "route distance of vehicle " + rt.VehicleID + " is not a distance"
		}
		for _, s := range rt.Stops {
			if msg := mark(s.StopID); msg != "" {
				return msg
			}
		}
	}
	for _, id := range sol.Dropped {
		if msg := mark(id); msg != "" {
			return msg
		}
	}
	for id, done := range stops {
		if !done {
			return "stop " + id + " is neither routed nor dropped"
		}
	}
	return ""
}

func validPoint(p RoutePoint) bool {
	return finite(p.X) && finite(p.Y) && math.Abs(p.X) <= maxRouteCoordinateKm && math.Abs(p.Y) <= maxRouteCoordinateKm
}

func inRange(v, max int) bool { return v >= 0 && v <= max }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

package domain

import "testing"

// The assumed driving time is the great-circle distance times 1.3 at 40 km/h, rounded up.
func TestTravelMinutes(t *testing.T) {
	if got := TravelMinutes(41.88, -87.63, 41.88, -87.63); got != 0 {
		t.Fatalf("same point: %d", got)
	}
	// One degree of latitude is 111.19 km: 111.19 * 1.3 / 40 * 60 = 216.8 minutes.
	if got := TravelMinutes(41, -87, 42, -87); got != 217 {
		t.Fatalf("one degree north: %d, want 217", got)
	}
	if a, b := TravelMinutes(41.88, -87.63, 41.95, -87.7), TravelMinutes(41.95, -87.7, 41.88, -87.63); a != b || a < 1 {
		t.Fatalf("not symmetric or free: %d %d", a, b)
	}
	if got := TravelMinutes(0, 179.9, 0, -179.9); got != 44 {
		t.Fatalf("across the antimeridian: %d, want 44", got)
	}
}

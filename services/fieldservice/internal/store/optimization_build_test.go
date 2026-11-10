package store

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

func ref[T any](v T) *T { return &v }

func skippedReasons(s []Skipped) map[string]string {
	out := map[string]string{}
	for _, x := range s {
		out[x.ID] = x.Reason
	}
	return out
}

func techAt(code string, lat, lon *float64) Technician {
	return Technician{ID: ids.New(), Code: code, TerritoryIDs: []string{"t1"}, Skills: []string{"HVAC"}, TimeZone: "America/Chicago",
		ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true, HomeLatitude: lat, HomeLongitude: lon}
}

func orderAt(lat, lon *float64, minutes int) plannedOrder {
	return plannedOrder{WorkOrder: WorkOrder{ID: ids.New(), TerritoryID: "t1", EstDurationMinutes: minutes, Latitude: lat, Longitude: lon}, priority: 4}
}

func heldBy(t Technician, lat, lon float64, start, end time.Time) plannedOrder {
	return plannedOrder{WorkOrder: WorkOrder{ID: ids.New(), TerritoryID: "t1", EstDurationMinutes: int(end.Sub(start) / time.Minute), Latitude: &lat, Longitude: &lon,
		TechnicianID: &t.ID, AssignedStart: &start, AssignedEnd: &end}, priority: 4}
}

func TestBuildRequest_SnapshotOfTheDay(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, loc) }
	ana := techAt("ANA", ref(41.88), ref(-87.63))
	ana.OvertimeAllowed = true
	noHome := techAt("NOH", nil, nil)
	utc := techAt("UTC", ref(41.88), ref(-87.63))
	utc.TimeZone = "UTC"
	sunday := techAt("SUN", ref(41.88), ref(-87.63))
	sunday.ShiftDays = []int{7}

	held := heldBy(ana, 41.881, -87.631, at(14, 0), at(15, 0))
	located := orderAt(ref(41.9), ref(-87.65), 60)
	located.WindowStart, located.WindowEnd = ref(at(9, 0)), ref(at(12, 0))
	due := at(17, 0)
	located.due, located.priority = &due, 2
	noGeo := orderAt(nil, nil, 30)
	otherDay := orderAt(ref(41.9), ref(-87.6), 30)
	otherDay.WindowStart, otherDay.WindowEnd = ref(at(9, 0).AddDate(0, 0, 1)), ref(at(12, 0).AddDate(0, 0, 1))

	req, skipped, err := buildRequest("2026-10-26", "America/Chicago", 15, []Technician{ana, noHome, utc, sunday},
		[]plannedOrder{located, noGeo, otherDay}, []plannedOrder{held})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{noHome.ID: "NO_HOME", utc.ID: "TIME_ZONE", sunday.ID: "OFF_DAY", noGeo.ID: "NO_LOCATION", otherDay.ID: "WINDOW_OUTSIDE_DAY"}
	if got := skippedReasons(skipped); !maps.Equal(got, want) {
		t.Fatalf("skipped %v, want %v", got, want)
	}
	if len(req.Technicians) != 1 || len(req.WorkOrders) != 2 || len(req.Pinned) != 1 || req.TimeLimitSeconds != 15 || req.BufferMinutes != domain.BufferMinutes {
		t.Fatalf("request shape: %d technicians, %d work orders, %d pinned", len(req.Technicians), len(req.WorkOrders), len(req.Pinned))
	}
	tech := req.Technicians[0]
	if tech.ShiftStart != 480 || tech.ShiftEnd != 1020+domain.OvertimeMinutes || tech.OvertimeMinutes != domain.OvertimeMinutes || tech.MaxDailyMinutes != 480 || tech.Home != 0 {
		t.Fatalf("technician %+v", tech)
	}
	if p := req.Pinned[0]; p.WorkOrderID != held.ID || p.TechnicianID != ana.ID || p.Start != 840 || p.End != 900 || p.Sequence != 1 {
		t.Fatalf("pinned %+v", p)
	}
	var open = req.WorkOrders[1]
	if open.ID != located.ID || open.WindowStart == nil || *open.WindowStart != 540 || *open.WindowEnd != 720 || open.Due == nil || *open.Due != 1020 || open.Priority != 2 || open.Duration != 60 {
		t.Fatalf("open order %+v", open)
	}
	n := len(req.Travel)
	if n != 3 {
		t.Fatalf("matrix %d, want a home and two stops", n)
	}
	for i := range req.Travel {
		if len(req.Travel[i]) != n || req.Travel[i][i] != 0 {
			t.Fatalf("matrix row %d: %v", i, req.Travel[i])
		}
		for j := range req.Travel[i] {
			if req.Travel[i][j] != req.Travel[j][i] {
				t.Fatalf("matrix not symmetric at %d,%d", i, j)
			}
		}
	}
	if req.Travel[0][2] != domain.TravelMinutes(41.88, -87.63, 41.9, -87.65) {
		t.Fatalf("travel from home to the open order %d", req.Travel[0][2])
	}
}

func TestBuildRequest_HeldRouteTravelCannotBeReached(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, loc) }
	ana, bob := techAt("ANA", ref(41.88), ref(-87.63)), techAt("BOB", ref(41.88), ref(-87.63))
	atShiftStart := heldBy(ana, 41.95, -87.7, at(8, 0), at(9, 0))
	reachableVisit := heldBy(bob, 41.95, -87.7, at(10, 0), at(11, 0))

	req, skipped, err := buildRequest("2026-10-26", "America/Chicago", 10, []Technician{ana, bob}, []plannedOrder{orderAt(ref(41.9), ref(-87.65), 60)},
		[]plannedOrder{atShiftStart, reachableVisit})
	if err != nil {
		t.Fatal(err)
	}
	if got := skippedReasons(skipped); got[ana.ID] != "HELD_ROUTE_UNREACHABLE" || len(got) != 1 {
		t.Fatalf("skipped %v", got)
	}
	ids := []string{}
	for _, tech := range req.Technicians {
		ids = append(ids, tech.ID)
	}
	if !slices.Equal(ids, []string{bob.ID}) || len(req.Pinned) != 1 || req.Pinned[0].WorkOrderID != reachableVisit.ID {
		t.Fatalf("technicians %v pinned %+v", ids, req.Pinned)
	}
}

func TestBuildRequest_HeldVisitWithoutPositionIsAtHome(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	ana := techAt("ANA", ref(41.88), ref(-87.63))
	start := time.Date(2026, 10, 26, 13, 0, 0, 0, loc)
	held := heldBy(ana, 0, 0, start, start.Add(time.Hour))
	held.Latitude, held.Longitude = nil, nil
	req, _, err := buildRequest("2026-10-26", "America/Chicago", 10, []Technician{ana}, []plannedOrder{orderAt(ref(41.9), ref(-87.65), 60)}, []plannedOrder{held})
	if err != nil {
		t.Fatal(err)
	}
	if req.WorkOrders[0].ID != held.ID || req.Travel[0][req.WorkOrders[0].Location] != 0 {
		t.Fatalf("held visit %+v travel %v", req.WorkOrders[0], req.Travel[0])
	}
}

func TestBuildRequest_HeldVisitTheTechnicianCannotWorkAnymoreIsReported(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, loc) }
	lostSkill, shortShift, intact := techAt("SKL", ref(41.88), ref(-87.63)), techAt("SHF", ref(41.88), ref(-87.63)), techAt("OK", ref(41.88), ref(-87.63))
	needsMore := heldBy(lostSkill, 41.95, -87.7, at(10, 0), at(11, 0))
	needsMore.RequiredSkills = []string{"HVAC", "EPA"}
	pastShift := heldBy(shortShift, 41.95, -87.7, at(16, 30), at(17, 30))
	fine := heldBy(intact, 41.95, -87.7, at(10, 0), at(11, 0))
	fine.RequiredSkills = []string{"HVAC"}
	windowed := heldBy(intact, 41.96, -87.7, at(13, 0), at(14, 0))
	windowed.WindowStart, windowed.WindowEnd = ref(at(8, 0)), ref(at(12, 0))

	req, skipped, err := buildRequest("2026-10-26", "America/Chicago", 10, []Technician{lostSkill, shortShift, intact}, []plannedOrder{orderAt(ref(41.9), ref(-87.65), 60)},
		[]plannedOrder{needsMore, pastShift, fine})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{lostSkill.ID: "HELD_VISIT_STALE", needsMore.ID: "HELD_VISIT_STALE", shortShift.ID: "HELD_VISIT_STALE", pastShift.ID: "HELD_VISIT_STALE"}
	if got := skippedReasons(skipped); !maps.Equal(got, want) {
		t.Fatalf("skipped %v, want %v", got, want)
	}
	if len(req.Technicians) != 1 || req.Technicians[0].ID != intact.ID || len(req.Pinned) != 1 || req.Pinned[0].WorkOrderID != fine.ID {
		t.Fatalf("technicians %+v pinned %+v", req.Technicians, req.Pinned)
	}

	_, skipped, err = buildRequest("2026-10-26", "America/Chicago", 10, []Technician{intact}, nil, []plannedOrder{windowed})
	if err != nil || skippedReasons(skipped)[windowed.ID] != "HELD_VISIT_STALE" {
		t.Fatalf("a visit ending after its window: %+v %v", skipped, err)
	}
}

func TestBuildRequest_TravelBeyondADayNeverReachesTheOptimizer(t *testing.T) {
	ana := techAt("ANA", ref(41.88), ref(-87.63))
	nearby := orderAt(ref(41.9), ref(-87.65), 60)
	nullIsland := orderAt(ref(0.0), ref(0.0), 60)

	req, skipped, err := buildRequest("2026-10-26", "America/Chicago", 10, []Technician{ana}, []plannedOrder{nearby, nullIsland}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := skippedReasons(skipped); !maps.Equal(got, map[string]string{nullIsland.ID: "TOO_FAR"}) || len(req.WorkOrders) != 1 || req.WorkOrders[0].ID != nearby.ID {
		t.Fatalf("skipped %v work orders %+v", got, req.WorkOrders)
	}

	remote := techAt("REM", ref(0.0), ref(0.0))
	req, skipped, err = buildRequest("2026-10-26", "America/Chicago", 10, []Technician{ana, remote}, []plannedOrder{nearby}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := skippedReasons(skipped); !maps.Equal(got, map[string]string{remote.ID: "TOO_FAR"}) || len(req.Technicians) != 1 {
		t.Fatalf("skipped %v technicians %+v", got, req.Technicians)
	}

	req, skipped, err = buildRequest("2026-10-26", "America/Chicago", 10, []Technician{ana, remote}, []plannedOrder{nearby, nullIsland}, nil)
	if err != nil || len(skipped) != 0 || len(req.Technicians) != 2 || len(req.WorkOrders) != 2 {
		t.Fatalf("both reachable by someone: %+v %+v %v", skipped, req, err)
	}
	for i, row := range req.Travel {
		for j, minutes := range row {
			if minutes < 0 || minutes > maxTravelMinutes {
				t.Fatalf("travel %d,%d is %d, outside what dispatch accepts", i, j, minutes)
			}
		}
	}
}

func TestBuildRequest_HeldVisitFarFromTheTechnicianCannotBeReached(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	ana := techAt("ANA", ref(41.88), ref(-87.63))
	start := time.Date(2026, 10, 26, 16, 0, 0, 0, loc)
	far := heldBy(ana, 0, 0, start, start.Add(30*time.Minute))
	_, skipped, err := buildRequest("2026-10-26", "America/Chicago", 10, []Technician{ana}, []plannedOrder{orderAt(ref(41.9), ref(-87.65), 60)}, []plannedOrder{far})
	if err != nil || skippedReasons(skipped)[ana.ID] != "HELD_ROUTE_UNREACHABLE" {
		t.Fatalf("skipped %+v %v", skipped, err)
	}
}

func TestBuildRequest_VisitFromThePreviousNightDoesNotDropTheTechnician(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	ana := techAt("ANA", ref(41.88), ref(-87.63))
	ana.ShiftEndMinute, ana.OvertimeAllowed = 1440, true
	start := time.Date(2026, 10, 26, 23, 30, 0, 0, loc)
	overnight := heldBy(ana, 41.9, -87.65, start, start.Add(time.Hour))

	req, skipped, err := buildRequest("2026-10-27", "America/Chicago", 10, []Technician{ana}, []plannedOrder{orderAt(ref(41.9), ref(-87.65), 60)}, []plannedOrder{overnight})
	if err != nil || len(skipped) != 0 || len(req.Technicians) != 1 || len(req.Pinned) != 0 || len(req.WorkOrders) != 1 {
		t.Fatalf("skipped %+v technicians %+v pinned %+v work orders %d err %v", skipped, req.Technicians, req.Pinned, len(req.WorkOrders), err)
	}
}

func TestBuildRequest_VisitRunningIntoTheShiftIsStaleOnlyPastTheShiftStart(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	start := time.Date(2026, 10, 26, 23, 30, 0, 0, loc)
	for _, tc := range []struct {
		name  string
		end   time.Time
		stale bool
	}{
		{"ends at the shift start", time.Date(2026, 10, 27, 8, 0, 0, 0, loc), false},
		{"runs a minute into the shift", time.Date(2026, 10, 27, 8, 1, 0, 0, loc), true},
	} {
		ana := techAt("ANA", ref(41.88), ref(-87.63))
		overnight := heldBy(ana, 41.9, -87.65, start, tc.end)
		req, skipped, err := buildRequest("2026-10-27", "America/Chicago", 10, []Technician{ana}, []plannedOrder{orderAt(ref(41.9), ref(-87.65), 60)}, []plannedOrder{overnight})
		if err != nil {
			t.Fatal(err)
		}
		if !tc.stale {
			if len(skipped) != 0 || len(req.Technicians) != 1 || len(req.Pinned) != 0 {
				t.Fatalf("%s: skipped %+v technicians %d pinned %d", tc.name, skipped, len(req.Technicians), len(req.Pinned))
			}
			continue
		}
		if got := skippedReasons(skipped); !maps.Equal(got, map[string]string{ana.ID: "HELD_VISIT_STALE", overnight.ID: "HELD_VISIT_STALE"}) || len(req.Technicians) != 0 {
			t.Fatalf("%s: skipped %v technicians %d", tc.name, got, len(req.Technicians))
		}
	}
}

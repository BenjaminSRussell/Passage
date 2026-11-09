package domain

import (
	"fmt"
	"testing"
	"time"
)

func placeResource(t *testing.T) Resource {
	t.Helper()
	r := res("r1", "R1", true, allDays(8*60, 16*60))
	r.Matrix = brMatrix(t)
	r.CurrentGroup = "A"
	return r
}

func campaignOps() []Operation {
	return []Operation{
		{ID: "j1", OrderID: "o1", OperNumber: 10, Minutes: 120, SetupGroup: "B"},
		{ID: "j2", OrderID: "o2", OperNumber: 10, Minutes: 120, SetupGroup: "C"},
	}
}

func TestPlaceCampaign_ContiguousWithLeadingSetupAndChangeover(t *testing.T) {
	out, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Start: at(0, 8, 0), HorizonDays: 14, Operations: campaignOps()})
	if conf != nil {
		t.Fatal(conf)
	}
	p := out.Placements
	if len(p) != 2 {
		t.Fatalf("placements = %d", len(p))
	}
	if p[0].ChangeoverMinutes != 30 || !p[0].Start.Equal(at(0, 8, 0)) || !p[0].End.Equal(at(0, 10, 30)) {
		t.Fatalf("first = %+v, want leading setup A->B 30 min then 120 min", p[0])
	}
	if p[1].ChangeoverMinutes != 20 || !p[1].Start.Equal(p[0].End) || !p[1].End.Equal(at(0, 12, 50)) {
		t.Fatalf("second = %+v, want B->C 20 min straight after the first", p[1])
	}
	if !out.Start.Equal(at(0, 8, 0)) || !out.End.Equal(at(0, 12, 50)) {
		t.Fatalf("span %v - %v", out.Start, out.End)
	}
}

func TestPlaceCampaign_CleanoutIsOneWindowAndNeverSplit(t *testing.T) {
	ops := campaignOps()
	ops[0].Minutes = 300 // 30 + 300 ends 13:30, leaving 150 open minutes in the day
	cl := []PlannedCleanout{{AfterOrderID: "o1", SetupGroup: "B", Minutes: 180, Reason: "batches"}}
	out, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Start: at(0, 8, 0), HorizonDays: 14, Operations: ops, Cleanouts: cl})
	if conf != nil {
		t.Fatal(conf)
	}
	w := out.Cleanouts[0]
	if !w.Start.Equal(at(1, 8, 0)) || !w.End.Equal(at(1, 11, 0)) {
		t.Fatalf("cleanout %v - %v, want the whole 180 min on the next day (150 min left today)", w.Start, w.End)
	}
	if !out.Placements[1].Start.Equal(w.End) {
		t.Fatalf("second order starts %v, want after the cleanout %v", out.Placements[1].Start, w.End)
	}
}

func TestPlaceCampaign_MovesBehindAForeignJobInsteadOfInterleaving(t *testing.T) {
	// A foreign job sits between the two campaign jobs' natural slots.
	booked := []Busy{{ResourceID: "r1", JobID: "x", Start: at(0, 10, 30), End: at(0, 11, 30)}}
	out, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Booked: booked, Start: at(0, 8, 0), HorizonDays: 14, Operations: campaignOps()})
	if conf != nil {
		t.Fatal(conf)
	}
	for _, pl := range out.Placements {
		if pl.Start.Before(at(0, 11, 30)) {
			t.Fatalf("job %s starts %v before the foreign job ends: the campaign was interleaved", pl.OperationID, pl.Start)
		}
	}
	if !out.Placements[0].Start.Equal(at(0, 11, 30)) {
		t.Fatalf("first job starts %v, want 11:30", out.Placements[0].Start)
	}
	if !out.Placements[1].Start.Equal(out.Placements[0].End) {
		t.Fatalf("jobs are not back to back: %v then %v", out.Placements[0].End, out.Placements[1].Start)
	}
}

func TestPlaceCampaign_FitsInFrontOfAForeignJobWhenItAllFits(t *testing.T) {
	booked := []Busy{{ResourceID: "r1", JobID: "x", Start: at(0, 14, 0), End: at(0, 15, 0)}}
	out, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Booked: booked, Start: at(0, 8, 0), HorizonDays: 14, Operations: campaignOps()})
	if conf != nil || !out.End.Equal(at(0, 12, 50)) {
		t.Fatalf("conf %v end %v, want the campaign to end 12:50 before the foreign job", conf, out.End)
	}
}

func TestPlaceCampaign_RefusesWhenNoCapacityIsLeft(t *testing.T) {
	_, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Start: at(0, 8, 0), HorizonDays: 1,
		Operations: []Operation{{ID: "j1", OrderID: "o1", Minutes: 2000, SetupGroup: "B"}}})
	if conf == nil || conf.Code != CodeOutsideCapacity {
		t.Fatalf("conf = %+v, want %s", conf, CodeOutsideCapacity)
	}
}

func TestPlaceCampaign_HoldsBackAnOrderThatIsNotReady(t *testing.T) {
	ops := campaignOps()
	ops[1].ReadyAt = at(1, 9, 0)
	out, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Start: at(0, 8, 0), HorizonDays: 14, Operations: ops})
	if conf != nil {
		t.Fatal(conf)
	}
	if out.Placements[1].Start.Before(at(1, 9, 0)) {
		t.Fatalf("second order starts %v before it is ready", out.Placements[1].Start)
	}
	var _ time.Time = out.End
}

func TestPlaceCampaign_ReadyGapDoesNotHideAForeignJob(t *testing.T) {
	// The first order ends 10:30 and the second is not ready before 14:00; a
	// foreign job at 11:00-12:00 would sit between them.
	ops := campaignOps()
	ops[1].ReadyAt = at(0, 14, 0)
	booked := []Busy{{ResourceID: "r1", JobID: "x", Start: at(0, 11, 0), End: at(0, 12, 0)}}
	out, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Booked: booked, Start: at(0, 8, 0), HorizonDays: 14, Operations: ops})
	if conf != nil {
		t.Fatal(conf)
	}
	p := out.Placements
	if !p[0].Start.Equal(at(0, 12, 0)) {
		t.Fatalf("first order starts %v, want behind the foreign job at 12:00", p[0].Start)
	}
	if !p[1].Start.Equal(p[0].End) {
		t.Fatalf("orders are not back to back: %v then %v", p[0].End, p[1].Start)
	}
}

func TestPlaceCampaign_DenseResourceGivesUpAtTheTryLimit(t *testing.T) {
	// A round-the-clock resource where every 150 min free stretch (exactly the
	// first order with its leading setup) is followed by a one-minute foreign
	// job, and the second order is not ready for 50 days: every try finds a
	// foreign job in the ready gap, so placement must stop with SCHED-0149.
	r := res("r1", "R1", true, allDays(0, 24*60))
	r.Matrix = brMatrix(t)
	r.CurrentGroup = "A"
	var booked []Busy
	for k := 0; k < 600; k++ {
		s := at(0, 0, 0).Add(time.Duration(k*151+150) * time.Minute)
		booked = append(booked, Busy{ResourceID: "r1", JobID: fmt.Sprintf("x%d", k), Start: s, End: s.Add(time.Minute)})
	}
	ops := campaignOps()
	ops[1].ReadyAt = at(50, 0, 0)
	_, conf := PlaceCampaign(CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{r}, Booked: booked, Start: at(0, 0, 0), HorizonDays: 60, Operations: ops})
	if conf == nil || conf.Code != CodeCampaignWindow {
		t.Fatalf("conf = %+v, want %s once the tries run out", conf, CodeCampaignWindow)
	}
}

// predPlacement is a scheduled job of operation id on resource r0 from start to end.
func predPlacement(id string, oper int, start, end time.Time, overlapBP int) Placement {
	return Placement{OperationID: id, OrderID: "o1", OperNumber: oper, ResourceID: "r0", Segments: []Segment{{Start: start, End: end}},
		Start: start, End: end, OverlapBP: overlapBP}
}

func TestPlaceCampaign_StartsBehindTheOrdersPredecessor(t *testing.T) {
	ops := campaignOps()
	ops[0].OperNumber = 20
	pred := Operation{ID: "p1", OrderID: "o1", OperNumber: 10, Minutes: 120, QueueAfter: 15}
	in := CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Start: at(0, 8, 0), HorizonDays: 14, Operations: ops,
		Chains: map[string][]Operation{"o1": {pred, ops[0]}}}
	if _, conf := PlaceCampaign(in); conf == nil || conf.Code != CodeCampaignPredecessor || conf.JobID != "p1" {
		t.Fatalf("unscheduled predecessor: %+v", conf)
	}
	// Scheduled 08:00-10:00 with a 15 min queue after it: the campaign job starts 10:15.
	in.Current = map[string]Placement{"p1": predPlacement("p1", 10, at(0, 8, 0), at(0, 10, 0), 0)}
	out, conf := PlaceCampaign(in)
	if conf != nil || !out.Placements[0].Start.Equal(at(0, 10, 15)) || !out.Placements[1].Start.Equal(out.Placements[0].End) {
		t.Fatalf("placements = %+v conf=%v, want the first job at 10:15 and the second straight after", out.Placements, conf)
	}
	// With half the quantity overlapping, the job may start once half the
	// predecessor is through (09:00) plus the queue, as a run would place it.
	in.Current["p1"] = predPlacement("p1", 10, at(0, 8, 0), at(0, 10, 0), 5000)
	if out, conf = PlaceCampaign(in); conf != nil || !out.Placements[0].Start.Equal(at(0, 9, 15)) {
		t.Fatalf("overlapped predecessor: %+v conf=%v, want 09:15", out.Placements, conf)
	}
}

func TestPlaceCampaign_PushesTheOrdersLaterOperation(t *testing.T) {
	r2 := res("r2", "R2", true, allDays(8*60, 16*60))
	ops := campaignOps()
	succ := Operation{ID: "s1", OrderID: "o1", OperNumber: 20, Minutes: 60, QueueBefore: 10, Candidates: []string{"r2"}}
	cur := Placement{OperationID: "s1", OrderID: "o1", OperNumber: 20, ResourceID: "r2", Segments: []Segment{{Start: at(0, 8, 0), End: at(0, 9, 0)}},
		Start: at(0, 8, 0), End: at(0, 9, 0)}
	in := CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t), r2}, Start: at(0, 8, 0), HorizonDays: 14, Operations: ops,
		Booked:  []Busy{{ResourceID: "r2", Start: at(0, 8, 0), End: at(0, 9, 0), JobID: "s1"}},
		Chains:  map[string][]Operation{"o1": {ops[0], succ}},
		Current: map[string]Placement{"s1": cur}}
	out, conf := PlaceCampaign(in)
	if conf != nil {
		t.Fatal(conf)
	}
	// The campaign job of o1 ends 10:30; the later operation follows its 10 min queue.
	if len(out.Pushed) != 1 || out.Pushed[0].OperationID != "s1" || !out.Pushed[0].Start.Equal(at(0, 10, 40)) || out.Pushed[0].ResourceID != "r2" {
		t.Fatalf("pushed = %+v, want s1 on R2 from 10:40", out.Pushed)
	}
	in.Firmed = map[string]bool{"s1": true}
	if _, conf := PlaceCampaign(in); conf == nil || conf.Code != CodeFirmed || conf.JobID != "s1" {
		t.Fatalf("firmed later operation: %+v", conf)
	}
}

func TestPlaceCampaign_ChecksTheNetworkStepIntoTheCampaignJob(t *testing.T) {
	wait := 30
	n, err := NewResourceNetwork("MIX", []NetworkEdge{{From: "r0", To: "r1", MaxWaitMinutes: &wait}})
	if err != nil {
		t.Fatal(err)
	}
	ops := campaignOps()
	ops[0].OperNumber = 20
	chain := []Operation{{ID: "p1", OrderID: "o1", OperNumber: 10, Minutes: 60}, ops[0]}
	chain[1].Network = "MIX"
	// The predecessor ends 07:00; the shift opens at 08:00, an hour over the 30 min maximum wait.
	in := CampaignPlaceInput{ResourceID: "r1", Resources: []Resource{placeResource(t)}, Start: at(0, 8, 0), HorizonDays: 14, Operations: ops,
		Chains: map[string][]Operation{"o1": chain}, Current: map[string]Placement{"p1": predPlacement("p1", 10, at(0, 6, 0), at(0, 7, 0), 0)},
		Networks: []ResourceNetwork{n}}
	if _, conf := PlaceCampaign(in); conf == nil || conf.Code != CodeNetworkWait {
		t.Fatalf("network wait: %+v", conf)
	}
}

package store

import (
	"fmt"
	"os"
	"sort"
	"testing"
	"time"
)

const (
	workforceSize     = 1200
	workforceEntities = 3
	workforceFanout   = 6
	workforceSpanDays = 5 * 365
	asOfSamples       = 50000
	asOfShortSamples  = 5000
	asOfP95Limit      = 50 * time.Millisecond
)

var workforceEntityIDs = [workforceEntities]string{
	"00000000-0000-4000-8000-0000000e0001",
	"00000000-0000-4000-8000-0000000e0002",
	"00000000-0000-4000-8000-0000000e0003",
}

type workforceMember struct {
	Number   string
	Entity   string
	Parent   int
	HireDate time.Time
}

func hrWorkforce1200() []workforceMember {
	start := time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC)
	perEntity := workforceSize / workforceEntities
	out := make([]workforceMember, 0, workforceSize)
	for e := 0; e < workforceEntities; e++ {
		for k := 0; k < perEntity; k++ {
			parent := -1
			if k > 0 {
				parent = e*perEntity + (k-1)/workforceFanout
			}
			hire := start
			if k > 0 {
				hire = start.AddDate(0, 0, (k*37+e*11)%workforceSpanDays)
			}
			out = append(out, workforceMember{Number: fmt.Sprintf("W%d-%04d", e+1, k), Entity: workforceEntityIDs[e], Parent: parent, HireDate: hire})
		}
	}
	return out
}

func TestHRWorkforce1200IsDeterministicAndShaped(t *testing.T) {
	a, b := hrWorkforce1200(), hrWorkforce1200()
	if len(a) != workforceSize {
		t.Fatalf("workforce has %d members, want %d", len(a), workforceSize)
	}
	perEntity := map[string]int{}
	for i, m := range a {
		if m != b[i] {
			t.Fatalf("member %d differs between runs", i)
		}
		perEntity[m.Entity]++
		if m.Parent >= i || (m.Parent >= 0 && a[m.Parent].Entity != m.Entity) {
			t.Fatalf("member %d reports to %d outside its entity or later in the list", i, m.Parent)
		}
	}
	if len(perEntity) != workforceEntities {
		t.Fatalf("entities = %v", perEntity)
	}
}

func TestWorkerAsOfP95AtTwelveHundredWorkers(t *testing.T) {
	s, ctx := newStore(t)
	dept := "00000000-0000-4000-8000-0000000d0001"
	people := hrWorkforce1200()
	workerIDs := make([]string, len(people))
	positionIDs := make([]string, len(people))
	for i, m := range people {
		var reportsTo *string
		if m.Parent >= 0 {
			reportsTo = &positionIDs[m.Parent]
		}
		pos := mustPosition(t, s, ctx, "POS-"+m.Number, dept, m.Entity, "1.00", false, time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC), reportsTo)
		positionIDs[i] = pos.ID
		w := mustWorker(t, s, ctx, m.Number)
		workerIDs[i] = w.ID
		emp, _, err := s.CreateEmployment(ctx, w.ID, Employment{
			LegalEntityID: m.Entity, EmploymentType: "FULL_TIME", AssignmentKind: "PRIMARY", HireDate: m.HireDate.Format("2006-01-02"), ValidFrom: m.HireDate,
		}, "")
		if err != nil {
			t.Fatalf("employment %s: %v", m.Number, err)
		}
		mustAssignment(t, s, ctx, pos.ID, w.ID, emp.ID, "1.00", m.HireDate, nil)
	}

	samples := asOfSamples
	if os.Getenv("HR_PERF_FULL") == "" {
		samples = asOfShortSamples
	}
	durations := make([]time.Duration, 0, samples)
	seed := uint64(42)
	for n := 0; n < samples; n++ {
		seed = seed*6364136223846793005 + 1442695040888963407
		i := int(seed>>33) % len(people)
		seed = seed*6364136223846793005 + 1442695040888963407
		at := people[i].HireDate.AddDate(0, 0, int(seed>>33)%(workforceSpanDays+365))
		begin := time.Now()
		view, err := s.WorkerAsOf(ctx, workerIDs[i], at, at.Format("2006-01-02"))
		durations = append(durations, time.Since(begin))
		if err != nil {
			t.Fatalf("as-of %s on %s: %v", people[i].Number, at.Format("2006-01-02"), err)
		}
		if view.WorkerID != workerIDs[i] {
			t.Fatalf("as-of returned worker %s, want %s", view.WorkerID, workerIDs[i])
		}
	}
	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
	p95 := durations[len(durations)*95/100]
	t.Logf("as-of over %d workers, %d reads: p50 %s p95 %s max %s", len(people), len(durations), durations[len(durations)/2], p95, durations[len(durations)-1])
	if p95 >= asOfP95Limit {
		t.Fatalf("as-of p95 = %s, want under %s", p95, asOfP95Limit)
	}
}

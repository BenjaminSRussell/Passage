package domain

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var floorNow = time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)

func TestClassify_BR25_4(t *testing.T) {
	at := func(s int) time.Time { return floorNow.Add(time.Duration(s) * time.Second) }
	tests := []struct {
		name     string
		lastSeq  int64
		in       []MachineEvent
		accepted []int64
		dropped  int
		stale    int
		gaps     []SeqGap
	}{
		{"first batch with no history accepts without gap", 0, []MachineEvent{{Seq: 5, At: at(0)}, {Seq: 6, At: at(1)}}, []int64{5, 6}, 0, 0, nil},
		{"seq at or below last seen is late, for the store to check", 6, []MachineEvent{{Seq: 6, At: at(0)}, {Seq: 7, At: at(1)}}, []int64{7}, 0, 0, nil},
		{"out of order batch is sorted and gaps reported", 2, []MachineEvent{{Seq: 6, At: at(0)}, {Seq: 4, At: at(1)}}, []int64{4, 6}, 0, 0, []SeqGap{{From: 3, To: 3}, {From: 5, To: 5}}},
		{"event older than 7 days is stale", 0, []MachineEvent{{Seq: 1, At: floorNow.Add(-MaxEventAge - time.Second)}, {Seq: 2, At: at(0)}}, []int64{2}, 1, 1, nil},
		{"duplicate within batch counts once", 0, []MachineEvent{{Seq: 3, At: at(0)}, {Seq: 3, At: at(0)}}, []int64{3}, 1, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Classify(tt.lastSeq, floorNow, tt.in)
			if got := seqs(c.Accepted); !equalSeqs(got, tt.accepted) {
				t.Fatalf("accepted %v, want %v", got, tt.accepted)
			}
			if c.Dropped != tt.dropped || c.Stale != tt.stale {
				t.Fatalf("dropped %d stale %d, want %d %d", c.Dropped, c.Stale, tt.dropped, tt.stale)
			}
			if len(c.Gaps) != len(tt.gaps) {
				t.Fatalf("gaps %+v, want %+v", c.Gaps, tt.gaps)
			}
			for i := range tt.gaps {
				if c.Gaps[i] != tt.gaps[i] {
					t.Fatalf("gap %d = %+v, want %+v", i, c.Gaps[i], tt.gaps[i])
				}
			}
		})
	}
}

// Regression (AC-25.3): the ingest reports seq 7-8 as a gap after 9 arrived, so
// the edge replays 7 and 8 below the high-water mark. They must come back as late
// (the store keeps them unless stored), not be dropped as duplicates; a repeat
// within the replay counts once, and no new gap is reported for late events.
func TestClassify_GapReplayIsLateNotDropped(t *testing.T) {
	c := Classify(9, floorNow, []MachineEvent{{Seq: 8, At: floorNow}, {Seq: 7, At: floorNow}, {Seq: 8, At: floorNow}, {Seq: 10, At: floorNow}})
	if got := seqs(c.Late); !equalSeqs(got, []int64{7, 8}) {
		t.Fatalf("late %v, want [7 8]", got)
	}
	if got := seqs(c.Accepted); !equalSeqs(got, []int64{10}) || c.Dropped != 1 || len(c.Gaps) != 0 {
		t.Fatalf("accepted %v dropped %d gaps %+v, want [10] 1 none", got, c.Dropped, c.Gaps)
	}
}

// Property: accepted events strictly increase past lastSeq, late events are at
// or below it and distinct, nothing is lost or double counted, and every gap is
// a non-empty run of missing sequence numbers above lastSeq.
func TestClassify_PropertyNoLossNoDuplicate(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		last := rapid.Int64Range(0, 50).Draw(t, "lastSeq")
		n := rapid.IntRange(0, 40).Draw(t, "n")
		in := make([]MachineEvent, n)
		for i := range in {
			in[i] = MachineEvent{Index: i, Seq: rapid.Int64Range(1, 100).Draw(t, "seq"), At: floorNow}
		}
		c := Classify(last, floorNow, in)
		prev := last
		for _, e := range c.Accepted {
			if e.Seq <= prev {
				t.Fatalf("accepted %d after %d", e.Seq, prev)
			}
			prev = e.Seq
		}
		prevLate := int64(0)
		for _, e := range c.Late {
			if e.Seq > last || e.Seq <= prevLate {
				t.Fatalf("late %d after %d with lastSeq %d", e.Seq, prevLate, last)
			}
			prevLate = e.Seq
		}
		if len(c.Accepted)+len(c.Late)+c.Dropped != n {
			t.Fatalf("accepted %d + late %d + dropped %d != %d", len(c.Accepted), len(c.Late), c.Dropped, n)
		}
		distinct := map[int64]bool{}
		for _, e := range in {
			distinct[e.Seq] = true
		}
		if len(c.Accepted)+len(c.Late) != len(distinct) {
			t.Fatalf("kept %d events, want one per distinct seq (%d)", len(c.Accepted)+len(c.Late), len(distinct))
		}
		for _, g := range c.Gaps {
			if g.From > g.To || g.From <= last {
				t.Fatalf("bad gap %+v after %d", g, last)
			}
		}
	})
}

func TestDowntimeStepFor(t *testing.T) {
	tests := []struct {
		name  string
		open  bool
		state string
		want  DowntimeStep
	}{
		{"DOWN opens when none open", false, MachineDown, DowntimeStart},
		{"DOWN while open changes nothing", true, MachineDown, DowntimeNone},
		{"RUNNING closes an open downtime", true, MachineRunning, DowntimeEnd},
		{"IDLE closes an open downtime", true, MachineIdle, DowntimeEnd},
		{"RUNNING with none open changes nothing", false, MachineRunning, DowntimeNone},
		{"no state changes nothing", true, "", DowntimeNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DowntimeStepFor(tt.open, tt.state); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

// AC-25.6: 11 minutes of DOWN is past the andon threshold; 10 minutes is not.
func TestAndonThresholds(t *testing.T) {
	start := floorNow
	if AndonDue(start, start.Add(10*time.Minute)) {
		t.Fatal("andon due at exactly 10 minutes")
	}
	if !AndonDue(start, start.Add(11*time.Minute)) {
		t.Fatal("no andon at 11 minutes")
	}
	if AndonCleared(start, start.Add(4*time.Minute+59*time.Second)) {
		t.Fatal("cleared before 5 minutes")
	}
	if !AndonCleared(start, start.Add(5*time.Minute)) {
		t.Fatal("not cleared at 5 minutes")
	}
}

func TestExportCSV(t *testing.T) {
	row := ExportRow{WorkerID: "w-1", WorkDate: "2026-10-20", PayType: "overtime-1.5", Currency: "USD"}
	row.Hours = "2.00"
	row.Multiplier = "1.5"
	row.Amount = "60.00"
	rows := []ExportRow{row}
	out, err := ExportCSV("b-1", rows)
	if err != nil {
		t.Fatal(err)
	}
	want := "batchId,workerId,workDate,payType,hours,multiplier,amount,currency\nb-1,w-1,2026-10-20,overtime-1.5,2.00,1.5,60.00,USD\n"
	if out != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", out, want)
	}
	quoted, err := ExportCSV("b-1", []ExportRow{{WorkerID: "w,1", PayType: `a"b`, Currency: "USD", Hours: "0", Multiplier: "1", Amount: "0"}})
	if err != nil || !strings.Contains(quoted, `"w,1"`) || !strings.Contains(quoted, `"a""b"`) {
		t.Fatalf("quoting: %q %v", quoted, err)
	}
}

func seqs(in []MachineEvent) []int64 {
	out := make([]int64, 0, len(in))
	for _, e := range in {
		out = append(out, e.Seq)
	}
	return out
}

func equalSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

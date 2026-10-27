package domain

import (
	"errors"
	"math"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var t0 = time.Date(2026, 10, 21, 8, 15, 3, 250_000_000, time.UTC)

const resourceUUID = "0190a0b0-0000-7000-8000-000000000001"

func TestCounterDelta_BR25_4(t *testing.T) {
	tests := []struct {
		name      string
		prev, cur uint32
		want      uint64
	}{
		{"plain increase", 100, 110, 10},
		{"no change", 42, 42, 0},
		{"rollover at max", math.MaxUint32 - 4, 5, 10},
		{"rollover to zero", math.MaxUint32, 0, 1},
		{"full range from zero", 0, math.MaxUint32, math.MaxUint32},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CounterDelta(tc.prev, tc.cur); got != tc.want {
				t.Fatalf("CounterDelta(%d,%d) = %d, want %d", tc.prev, tc.cur, got, tc.want)
			}
		})
	}
}

func TestCounterDelta_SumsIncrementsAcrossRollover(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		steps := rapid.SliceOfN(rapid.Uint32(), 1, 50).Draw(t, "steps")
		var counter uint32 = rapid.Uint32().Draw(t, "start")
		var total uint64
		for _, d := range steps {
			next := counter + d // wraps modulo 2^32, like the machine register
			total += CounterDelta(counter, next)
			counter = next
		}
		var want uint64
		for _, d := range steps {
			want += uint64(d)
		}
		if total != want {
			t.Fatalf("summed deltas %d, want %d", total, want)
		}
	})
}

func TestSequencer_Accept_BR25_4(t *testing.T) {
	type step struct {
		seq  int64
		ts   time.Time
		want Decision
		gap  *Gap
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"in order", []step{{1, t0, Accepted, nil}, {2, t0, Accepted, nil}}},
		{"duplicate seq dropped", []step{{5, t0, Accepted, nil}, {5, t0, DuplicateDropped, nil}, {4, t0, DuplicateDropped, nil}}},
		{"gap reported", []step{{1, t0, Accepted, nil}, {4, t0, Accepted, &Gap{From: 2, To: 3}}}},
		{"stale dropped", []step{{1, t0.Add(-8 * 24 * time.Hour), StaleDropped, nil}, {1, t0, Accepted, nil}}},
		{"seven day old accepted", []step{{1, t0.Add(-7 * 24 * time.Hour), Accepted, nil}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := NewSequencer()
			now := t0
			for i, st := range tc.steps {
				got, gap := s.Accept("M-101", st.seq, st.ts, now)
				if got != st.want {
					t.Fatalf("step %d: decision %v, want %v", i, got, st.want)
				}
				if (gap == nil) != (st.gap == nil) || (gap != nil && *gap != *st.gap) {
					t.Fatalf("step %d: gap %v, want %v", i, gap, st.gap)
				}
			}
		})
	}
}

func TestSequencer_IsPerMachine(t *testing.T) {
	s := NewSequencer()
	if d, _ := s.Accept("M-1", 10, t0, t0); d != Accepted {
		t.Fatal("first event of M-1 rejected")
	}
	if d, _ := s.Accept("M-2", 1, t0, t0); d != Accepted {
		t.Fatal("first event of M-2 rejected; sequences must be per machine")
	}
	if s.Last("M-1") != 10 || s.Last("M-2") != 1 || s.Last("M-3") != 0 {
		t.Fatal("last seq tracking wrong")
	}
}

func TestSequencer_NoLossNoDuplicatesProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// A replayed stream of seqs 1..n with random duplicates must accept each seq once.
		n := rapid.IntRange(1, 200).Draw(t, "n")
		s := NewSequencer()
		accepted := map[int64]int{}
		for seq := int64(1); seq <= int64(n); seq++ {
			d, gap := s.Accept("M-7", seq, t0, t0)
			if d != Accepted || gap != nil {
				t.Fatalf("in-order seq %d: decision %v gap %v", seq, d, gap)
			}
			accepted[seq]++
			if rapid.Bool().Draw(t, "replay") {
				if d, _ := s.Accept("M-7", seq, t0, t0); d != DuplicateDropped {
					t.Fatalf("replayed seq %d not dropped: %v", seq, d)
				}
			}
		}
		if len(accepted) != n {
			t.Fatalf("accepted %d distinct seqs, want %d", len(accepted), n)
		}
	})
}

func validEvent() MachineEvent {
	good := int64(12)
	return MachineEvent{MachineID: "M-101", ResourceID: resourceUUID, Seq: 184233, Kind: KindCount, TS: t0, GoodCount: &good}
}

func TestMachineEventValidate(t *testing.T) {
	stateOK := MachineEvent{MachineID: "M-1", ResourceID: resourceUUID, Seq: 1, Kind: KindState, TS: t0, State: "DOWN"}
	stateBad := stateOK
	stateBad.State = "BROKEN"
	countNoGood := validEvent()
	countNoGood.GoodCount = nil
	noResource := validEvent()
	noResource.ResourceID = "not-a-uuid"
	badKind := validEvent()
	badKind.Kind = "vibes"
	measureNoName := MachineEvent{MachineID: "M-1", ResourceID: resourceUUID, Seq: 2, Kind: KindMeasurement, TS: t0}

	tests := []struct {
		name    string
		evt     MachineEvent
		wantErr bool
	}{
		{"valid count", validEvent(), false},
		{"valid state", stateOK, false},
		{"unknown state", stateBad, true},
		{"count without good count", countNoGood, true},
		{"resource not uuid", noResource, true},
		{"unknown kind", badKind, true},
		{"measurement without name", measureNoName, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.evt.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidEvent) {
				t.Fatalf("error does not wrap ErrInvalidEvent: %v", err)
			}
		})
	}
}

func TestEnvelope_BR25_4(t *testing.T) {
	ce, err := Envelope("plant-1", "edge-7", validEvent())
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]string{
		"type":            "dev.passage.shopfloor.machine.event.v1",
		"id":              "M-101-184233",
		"source":          "urn:passage:iot-edge:plant-1:edge-7",
		"subject":         "M-101",
		"time":            "2026-10-21T08:15:03.250Z",
		"datacontenttype": "application/json",
		"specversion":     "1.0",
	}
	got := map[string]string{
		"type": ce.Type, "id": ce.ID, "source": ce.Source, "subject": ce.Subject,
		"time": ce.Time, "datacontenttype": ce.DataContentType, "specversion": ce.SpecVersion,
	}
	for k, want := range checks {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if _, err := Envelope("", "edge-7", validEvent()); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("empty site accepted: %v", err)
	}
}

func TestBuffer_AgeAndCapacity(t *testing.T) {
	b := NewBuffer(24*time.Hour, 3)
	for i := 0; i < 3; i++ {
		if b.Push(t0, []byte{byte(i)}) {
			t.Fatalf("push %d evicted below capacity", i)
		}
	}
	if !b.Push(t0, []byte{3}) {
		t.Fatal("push at capacity should evict the oldest")
	}
	if b.Evicted() != 1 || b.Len() != 3 {
		t.Fatalf("evicted=%d len=%d", b.Evicted(), b.Len())
	}
	if got := b.FillPct(t0); got != 100 {
		t.Fatalf("fill = %v, want 100", got)
	}
	drained := b.Drain(t0, 2)
	if len(drained) != 2 || drained[0][0] != 1 || drained[1][0] != 2 {
		t.Fatalf("drain order wrong: %v", drained)
	}
	if got := b.FillPct(t0.Add(25 * time.Hour)); got != 0 {
		t.Fatalf("aged-out payloads still buffered: fill %v", got)
	}
}

func TestBuffer_DrainNeverExceedsLength(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		capacity := rapid.IntRange(1, 50).Draw(t, "capacity")
		pushes := rapid.IntRange(0, 100).Draw(t, "pushes")
		b := NewBuffer(24*time.Hour, capacity)
		for i := 0; i < pushes; i++ {
			b.Push(t0, []byte{byte(i)})
		}
		want := pushes
		if want > capacity {
			want = capacity
		}
		if b.Len() != want {
			t.Fatalf("len %d, want %d", b.Len(), want)
		}
		n := rapid.IntRange(0, 120).Draw(t, "drain")
		if got := len(b.Drain(t0, n)); got > want {
			t.Fatalf("drained %d from %d", got, want)
		}
	})
}

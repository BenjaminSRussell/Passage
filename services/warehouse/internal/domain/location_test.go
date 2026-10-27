package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestFits_RefusesOverLimitWithDimensionReason(t *testing.T) {
	tests := []struct {
		name   string
		cap    Capacity
		used   Usage
		add    Usage
		wantOK bool
		reason string
	}{
		{"unlimited accepts anything", Capacity{}, Usage{LPs: 99}, Usage{Weight: dec("1000")}, true, ""},
		{"lps at the limit is refused", Capacity{MaxLPs: intPtr(1)}, Usage{LPs: 1}, Usage{LPs: 1}, false, "lps 1/1"},
		{"lps under the limit is accepted", Capacity{MaxLPs: intPtr(2)}, Usage{LPs: 1}, Usage{LPs: 1}, true, ""},
		{"items over the limit", Capacity{MaxItems: intPtr(2)}, Usage{Items: 2}, Usage{Items: 1}, false, "items 2/2"},
		{"weight exactly at the limit", Capacity{MaxWeight: decPtr("14.4")}, Usage{}, Usage{Weight: dec("14.4")}, true, ""},
		{"weight one micro-kilo over", Capacity{MaxWeight: decPtr("14.4")}, Usage{Weight: dec("14.4")}, Usage{Weight: dec("0.000002")}, false, "weight 14.4/14.4"},
		{"remaining 0.000001 kg refuses 0.000002 kg", Capacity{MaxWeight: decPtr("1")}, Usage{Weight: dec("0.999999")}, Usage{Weight: dec("0.000002")}, false, "weight 0.999999/1"},
		{"volume over the limit", Capacity{MaxVolume: decPtr("2")}, Usage{Volume: dec("1.5")}, Usage{Volume: dec("0.6")}, false, "volume 1.5/2"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, ok := tc.cap.Fits(tc.used, tc.add)
			if ok != tc.wantOK || reason != tc.reason {
				t.Fatalf("Fits = (%q, %v), want (%q, %v)", reason, ok, tc.reason, tc.wantOK)
			}
		})
	}
}

func TestReceive_RefusalLeavesLocationUnchanged(t *testing.T) {
	l := Location{ID: "L", Name: "L", Capacity: Capacity{MaxLPs: intPtr(1), AllowMixedItems: true}}
	if _, ok := l.Receive("A", dec("5"), Usage{LPs: 1, Weight: dec("2")}); !ok {
		t.Fatal("first receipt should fit")
	}
	before := l.load()
	beforeQty := l.Stock["A"]
	reason, ok := l.Receive("B", dec("1"), Usage{LPs: 1, Weight: dec("1")})
	if ok || reason != "capacity: lps 1/1" {
		t.Fatalf("second receipt = (%q, %v), want refused with lps 1/1", reason, ok)
	}
	if after := l.load(); !after.Weight.Equal(before.Weight) || after.LPs != before.LPs || after.Items != before.Items {
		t.Errorf("usage changed on refusal: before %+v after %+v", before, after)
	}
	if !l.Stock["A"].Equal(beforeQty) || l.holds("B") {
		t.Error("stock changed on refusal")
	}
}

func TestCapacity_NeverExceededProperty(t *testing.T) {
	// AC-31.9 / BR-31.2: over random receipt sequences, every accepted state
	// stays within every limit, and a refused receipt changes nothing.
	rapid.Check(t, func(t *rapid.T) {
		maxW := decimal.NewFromInt(rapid.Int64Range(1, 40).Draw(t, "maxW"))
		maxLPs := rapid.IntRange(1, 6).Draw(t, "maxLPs")
		maxItems := rapid.IntRange(1, 3).Draw(t, "maxItems")
		l := Location{ID: "L", Name: "L", Capacity: Capacity{MaxWeight: &maxW, MaxLPs: &maxLPs, MaxItems: &maxItems, AllowMixedItems: true}}
		ops := rapid.IntRange(1, 200).Draw(t, "ops")
		for i := 0; i < ops; i++ {
			item := rapid.SampledFrom([]string{"A", "B", "C", "D"}).Draw(t, "item")
			weight := decimal.New(rapid.Int64Range(1, 2_000_000).Draw(t, "weight"), -6)
			lps := rapid.IntRange(0, 2).Draw(t, "lps")
			before := l.load()
			beforeQty := l.Stock[item]
			_, ok := l.Receive(item, weight, Usage{Weight: weight, LPs: lps})
			if !ok {
				if after := l.load(); !after.Weight.Equal(before.Weight) || after.LPs != before.LPs || after.Items != before.Items || !l.Stock[item].Equal(beforeQty) {
					t.Fatalf("refused receipt changed the location: %+v -> %+v", before, after)
				}
				continue
			}
			after := l.load()
			if after.Weight.GreaterThan(maxW) || after.LPs > maxLPs || after.Items > maxItems {
				t.Fatalf("capacity exceeded: %+v with limits weight %s lps %d items %d", after, maxW, maxLPs, maxItems)
			}
		}
	})
}

func TestLocationName_RoundTrip(t *testing.T) {
	name, err := LocationName("A", 1, 2, 3)
	if err != nil || name != "A-01-02-03" {
		t.Fatalf("LocationName = %q, %v; want A-01-02-03", name, err)
	}
	prefix, parts, err := ParseLocationName(name)
	if err != nil || prefix != "A" || len(parts) != 3 || parts[0] != 1 || parts[2] != 3 {
		t.Fatalf("ParseLocationName = %q %v %v", prefix, parts, err)
	}
	for _, bad := range []string{"A", "A-1-02-03", "A-01-0x", "-01-02"} {
		if _, _, err := ParseLocationName(bad); err == nil {
			t.Errorf("ParseLocationName(%q) should fail", bad)
		}
	}
	if _, err := LocationName("A", 100); err == nil {
		t.Error("component 100 should be refused")
	}
}

func TestLocationName_PropertyRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		prefix := rapid.StringMatching(`[A-Z]{1,3}`).Draw(t, "prefix")
		parts := rapid.SliceOfN(rapid.IntRange(0, 99), 1, 4).Draw(t, "parts")
		name, err := LocationName(prefix, parts...)
		if err != nil {
			t.Fatal(err)
		}
		gotPrefix, gotParts, err := ParseLocationName(name)
		if err != nil || gotPrefix != prefix || len(gotParts) != len(parts) {
			t.Fatalf("round trip of %q failed: %q %v %v", name, gotPrefix, gotParts, err)
		}
		for i := range parts {
			if gotParts[i] != parts[i] {
				t.Fatalf("part %d: %d != %d", i, gotParts[i], parts[i])
			}
		}
	})
}

func TestNestDepth(t *testing.T) {
	tests := []struct {
		parent  int
		want    int
		wantErr bool
	}{
		{0, 1, false},
		{4, 5, false},
		{5, 0, true}, // depth 6 is refused (WMS-0104)
	}
	for _, tc := range tests {
		got, err := NestDepth(tc.parent)
		if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
			t.Errorf("NestDepth(%d) = %d, %v", tc.parent, got, err)
		}
		if err != nil {
			if de, ok := err.(*Error); !ok || de.Code != CodeNestDepth {
				t.Errorf("NestDepth(%d) error code = %v, want %s", tc.parent, err, CodeNestDepth)
			}
		}
	}
}

func TestValidLPID(t *testing.T) {
	long := make([]byte, MaxLPIDLength+1)
	for i := range long {
		long[i] = 'A'
	}
	tests := []struct {
		id   string
		want bool
	}{
		{"", false},
		{"PAL-0001.A_2", true},
		{"376104250000000017", true},
		{string(long[:40]), true},
		{string(long), false},
		{"BAD ID", false},
		{"ÄBC", false},
	}
	for _, tc := range tests {
		if got := ValidLPID(tc.id); got != tc.want {
			t.Errorf("ValidLPID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestSSCC18(t *testing.T) {
	// Body 37610425000000001 has GS1 check digit 7: weights 3,1 from the right.
	if !ValidSSCC("376104250000000017") {
		t.Error("376104250000000017 should be a valid SSCC")
	}
	if ValidSSCC("376104250000000016") {
		t.Error("a wrong check digit must be refused")
	}
	got, err := SSCC18(0, "7612345", "000000001")
	if err != nil || len(got) != 18 || !ValidSSCC(got) || got[:8] != "07612345" {
		t.Fatalf("SSCC18 = %q, %v", got, err)
	}
	if _, err := SSCC18(10, "7612345", "000000001"); err == nil {
		t.Error("extension digit 10 must be refused")
	}
	if _, err := SSCC18(0, "761234", "000000001"); err == nil {
		t.Error("15 digits of prefix and serial must be refused")
	}
}

func TestCheckTransition(t *testing.T) {
	tests := []struct {
		from, to  WorkStatus
		confirmed int
		wantErr   bool
	}{
		{WorkOpen, WorkAssigned, 0, false},
		{WorkAssigned, WorkOpen, 0, false},
		{WorkAssigned, WorkInProgress, 0, false},
		{WorkInProgress, WorkCompleted, 3, false},
		{WorkInProgress, WorkPartiallyComplete, 1, false},
		{WorkOpen, WorkCancelled, 0, false},
		{WorkAssigned, WorkCancelled, 0, false},
		{WorkBlockedNoLocation, WorkOpen, 0, false},
		// Refused: cancel after a confirmed line, anything out of COMPLETED,
		// skipping ASSIGNED and IN_PROGRESS, cancelled and partially completed
		// as terminal states, and cancelling an in-progress pick.
		{WorkOpen, WorkCancelled, 1, true},
		{WorkCompleted, WorkOpen, 0, true},
		{WorkCompleted, WorkCancelled, 0, true},
		{WorkOpen, WorkCompleted, 0, true},
		{WorkCancelled, WorkOpen, 0, true},
		{WorkPartiallyComplete, WorkOpen, 0, true},
		{WorkInProgress, WorkCancelled, 0, true},
	}
	for _, tc := range tests {
		err := CheckTransition(tc.from, tc.to, tc.confirmed)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s -> %s (confirmed %d): err = %v, wantErr %v", tc.from, tc.to, tc.confirmed, err, tc.wantErr)
		}
		if de, ok := err.(*Error); err != nil && (!ok || de.Code != CodeIllegalTransition) {
			t.Errorf("%s -> %s: code = %v, want %s", tc.from, tc.to, err, CodeIllegalTransition)
		}
	}
}

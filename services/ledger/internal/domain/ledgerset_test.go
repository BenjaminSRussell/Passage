package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestApplyMappings_LatestEffectiveMappingWins(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }
	mappings := []AccountMapping{
		{Source: "1000", Target: "1100", EffectiveFrom: d(1)},
		{Source: "1000", Target: "1200", EffectiveFrom: d(10)},
		{Source: "2000", Target: "2100", EffectiveFrom: d(20)},
	}
	lines := []LineInput{line("1000", SideDebit, "5.00"), line("2000", SideCredit, "5.00")}
	for _, tc := range []struct {
		name string
		date time.Time
		want [2]string
	}{
		{"before any mapping", time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC), [2]string{"1000", "2000"}},
		{"first mapping", d(5), [2]string{"1100", "2000"}},
		{"superseded", d(10), [2]string{"1200", "2000"}},
		{"second source", d(25), [2]string{"1200", "2100"}},
	} {
		got := ApplyMappings(lines, mappings, tc.date)
		if got[0].Account != tc.want[0] || got[1].Account != tc.want[1] {
			t.Errorf("%s: accounts %s, %s; want %v", tc.name, got[0].Account, got[1].Account, tc.want)
		}
	}
	if lines[0].Account != "1000" {
		t.Fatal("the input lines must not be modified")
	}
}

func TestProperty_MappingFollowsGreatestEffectiveDateOnOrBeforePosting(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day := func(t *rapid.T, label string) time.Time {
		return base.AddDate(0, 0, rapid.IntRange(0, 60).Draw(t, label))
	}
	rapid.Check(t, func(t *rapid.T) {
		lines := balancedEntry(t)
		for i := range lines {
			lines[i].Dimensions = map[string]string{"DEPT": rapid.SampledFrom([]string{"D1", "D2"}).Draw(t, "dept")}
			lines[i].Text = rapid.StringMatching(`[a-z]{0,8}`).Draw(t, "text")
		}
		var mappings []AccountMapping
		for n := rapid.IntRange(0, 6).Draw(t, "mappings"); n > 0; n-- {
			mappings = append(mappings, AccountMapping{Source: rapid.SampledFrom([]string{"1000", "2000"}).Draw(t, "source"),
				Target: rapid.SampledFrom([]string{"3000", "4000", "5000"}).Draw(t, "target"), EffectiveFrom: day(t, "from")})
		}
		posting := day(t, "posting")
		mapped := ApplyMappings(lines, mappings, posting)
		if len(mapped) != len(lines) {
			t.Fatalf("line count changed: %d != %d", len(mapped), len(lines))
		}
		for i, l := range lines {
			want := l.Account
			var best time.Time
			for _, m := range mappings {
				if m.Source == l.Account && !m.EffectiveFrom.After(posting) && (best.IsZero() || m.EffectiveFrom.After(best)) {
					want, best = m.Target, m.EffectiveFrom
				}
			}
			got := mapped[i]
			if got.Account != want || got.Side != l.Side || !got.Amount.Equal(l.Amount) || got.Text != l.Text || got.Dimensions["DEPT"] != l.Dimensions["DEPT"] {
				t.Fatalf("line %d: got %+v, want account %s with %+v otherwise", i, got, want, l)
			}
		}
	})
}

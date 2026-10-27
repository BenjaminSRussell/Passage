package domain

import (
	"testing"

	"pgregory.net/rapid"
)

// TestSwitching_Table covers BR-33.2 transitions.
func TestSwitching_Table(t *testing.T) {
	rep := func(n int, rejected bool) []bool {
		out := make([]bool, n)
		for i := range out {
			out[i] = rejected
		}
		return out
	}
	cases := []struct {
		name    string
		enabled bool
		history []bool
		want    Severity
	}{
		{"fresh", true, nil, SeverityNormal},
		{"one rejection stays normal", true, []bool{true}, SeverityNormal},
		{"two of five rejected tightens (AC-33.8)", true, []bool{false, true, false, false, true}, SeverityTightened},
		{"five accepted restore normal from tightened", true, append([]bool{false, true, false, false, true}, rep(5, false)...), SeverityNormal},
		{"ten accepted reduce when enabled", true, rep(10, false), SeverityReduced},
		{"ten accepted do not reduce when disabled", false, rep(10, false), SeverityNormal},
		{"rejection returns reduced to normal", true, append(rep(10, false), true), SeverityNormal},
	}
	for _, c := range cases {
		if got := ReplaySwitching(c.enabled, c.history).Severity; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// TestSwitching_Property: with switching disabled, reduced is never reached; and
// tightened is left only after a run of five accepted lots.
func TestSwitching_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		enabled := rapid.Bool().Draw(t, "enabled")
		history := rapid.SliceOfN(rapid.Bool(), 0, 60).Draw(t, "history")
		s := NewSwitchState()
		streak := 0
		for _, rejected := range history {
			prev := s.Severity
			s = s.Apply(rejected, enabled)
			if !enabled && s.Severity == SeverityReduced {
				t.Fatal("reduced reached with switching disabled")
			}
			if prev == SeverityTightened && s.Severity == SeverityNormal {
				if streak < 4 || rejected {
					t.Fatalf("left tightened after %d accepted in a row", streak+1)
				}
			}
			if rejected {
				streak = 0
			} else {
				streak++
			}
		}
	})
}

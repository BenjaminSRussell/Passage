package domain

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

func TestRiskScore_MaxConsequenceTimesLikelihood(t *testing.T) {
	cases := []struct {
		name string
		c    Consequences
		lik  int
		want int
	}{
		// AC-44.16: SAFETY 3, PRODUCTION 5, likelihood 4 -> risk 20.
		{"ac-44-16", Consequences{Safety: 3, Environment: 1, Production: 5, Quality: 1, Cost: 2}, 4, 20},
		{"all ones", Consequences{1, 1, 1, 1, 1}, 1, 1},
		{"all fives", Consequences{5, 5, 5, 5, 5}, 5, 25},
		{"cost dominates", Consequences{1, 1, 1, 1, 4}, 3, 12},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RiskScore(tc.c, tc.lik)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("RiskScore = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRiskScore_RejectsOutOfRange(t *testing.T) {
	cases := []struct {
		name string
		c    Consequences
		lik  int
	}{
		{"zero safety", Consequences{0, 1, 1, 1, 1}, 1},
		{"six production", Consequences{1, 1, 6, 1, 1}, 1},
		{"zero likelihood", Consequences{1, 1, 1, 1, 1}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RiskScore(tc.c, tc.lik); !errors.Is(err, ErrCriticalityScale) {
				t.Fatalf("err = %v, want ErrCriticalityScale", err)
			}
		})
	}
}

func TestCriticalityFromRisk(t *testing.T) {
	cases := []struct {
		risk, safety, want int
	}{
		{20, 3, 5}, // AC-44.16 risk >= 20
		{25, 1, 5}, // risk >= 20
		{4, 5, 5},  // SAFETY = 5 overrides low risk
		{15, 2, 4}, // >= 15
		{19, 2, 4}, // just below 20
		{10, 2, 3}, // >= 10
		{5, 1, 2},  // >= 5
		{4, 1, 1},  // below 5
		{1, 1, 1},  // minimum
		{9, 4, 2},  // SAFETY 4 does not force 5
	}
	for _, tc := range cases {
		if got := CriticalityFromRisk(tc.risk, tc.safety); got != tc.want {
			t.Errorf("CriticalityFromRisk(%d, %d) = %d, want %d", tc.risk, tc.safety, got, tc.want)
		}
	}
}

func TestCriticalityFromRisk_MonotonicInRisk(t *testing.T) {
	prev := 0
	for risk := 1; risk <= 25; risk++ {
		got := CriticalityFromRisk(risk, 1)
		if got < prev {
			t.Fatalf("criticality fell from %d to %d at risk %d", prev, got, risk)
		}
		prev = got
	}
}

// Property (BR-44.15): every valid assessment gives a risk score of 1-25 equal to
// the worst consequence times the likelihood, and raising any score never lowers
// the derived criticality.
func TestRiskScore_Properties(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		score := func(label string) int { return rapid.IntRange(1, 5).Draw(rt, label) }
		c := Consequences{Safety: score("safety"), Environment: score("environment"), Production: score("production"),
			Quality: score("quality"), Cost: score("cost")}
		lik := score("likelihood")
		risk, err := RiskScore(c, lik)
		if err != nil {
			rt.Fatalf("valid scores refused: %v", err)
		}
		if risk < 1 || risk > 25 || risk != max(c.Safety, c.Environment, c.Production, c.Quality, c.Cost)*lik {
			rt.Fatalf("risk %d for %+v x %d", risk, c, lik)
		}
		crit := CriticalityFromRisk(risk, c.Safety)
		if crit < 1 || crit > 5 {
			rt.Fatalf("criticality %d out of range", crit)
		}
		worse := c
		worse.Safety = min(5, c.Safety+1)
		worseRisk, _ := RiskScore(worse, lik)
		if CriticalityFromRisk(worseRisk, worse.Safety) < crit {
			rt.Fatalf("raising safety lowered criticality: %+v", c)
		}
	})
}

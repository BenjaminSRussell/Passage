package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func TestSampler_SameSeedGivesTheSameDecisions(t *testing.T) {
	a, b := domain.NewSampler(7, 11), domain.NewSampler(7, 11)
	other := domain.NewSampler(8, 11)
	rate := dec("37.5")
	differs := false
	for range 200 {
		x, y, z := a.Blocks(rate), b.Blocks(rate), other.Blocks(rate)
		if x != y {
			t.Fatal("two samplers with one seed disagree")
		}
		differs = differs || x != z
	}
	if !differs {
		t.Fatal("a different seed gave the identical sequence")
	}
}

func TestSampler_Boundaries(t *testing.T) {
	s := domain.NewSampler(1, 2)
	for range 500 {
		if s.Blocks(dec("0")) {
			t.Fatal("0% blocked an invoice")
		}
		if !s.Blocks(dec("100")) {
			t.Fatal("100% let an invoice through")
		}
	}
	var none *domain.Sampler
	if none.Blocks(dec("100")) {
		t.Fatal("a missing sampler must never block")
	}
}

func TestSampler_BlocksAboutTheConfiguredShare(t *testing.T) {
	for _, tc := range []struct {
		rate     string
		min, max int
	}{{"10", 900, 1100}, {"0.50", 20, 80}, {"99.99", 9980, 10000}} {
		s := domain.NewSampler(42, 99)
		blocked := 0
		for range 10000 {
			if s.Blocks(dec(tc.rate)) {
				blocked++
			}
		}
		if blocked < tc.min || blocked > tc.max {
			t.Errorf("rate %s%% blocked %d of 10000, want %d to %d", tc.rate, blocked, tc.min, tc.max)
		}
	}
}

func TestSampler_RandomSeedsDiffer(t *testing.T) {
	a, err := domain.NewRandomSampler()
	if err != nil {
		t.Fatal(err)
	}
	b, err := domain.NewRandomSampler()
	if err != nil {
		t.Fatal(err)
	}
	same := true
	for range 64 {
		same = same && a.Blocks(dec("50")) == b.Blocks(dec("50"))
	}
	if same {
		t.Fatal("two randomly seeded samplers produced 64 identical decisions")
	}
}

func TestValidSampleRate(t *testing.T) {
	for rate, want := range map[string]bool{"0": true, "100": true, "12.34": true, "0.01": true, "100.01": false, "-0.01": false, "12.345": false, "1e2": true} {
		if got := domain.ValidSampleRate(decimal.RequireFromString(rate)); got != want {
			t.Errorf("ValidSampleRate(%s) = %v, want %v", rate, got, want)
		}
	}
}

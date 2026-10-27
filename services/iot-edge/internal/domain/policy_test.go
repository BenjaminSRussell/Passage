package domain

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func goodConfig() EdgeConfig {
	return EdgeConfig{
		Version:  2,
		Machines: []MachineSpec{{MachineID: "M-101", ResourceID: resourceUUID}},
		Tags: []TagMapping{
			{Address: "ns=2;s=M101.Count", MachineID: "M-101", Kind: KindCount},
			{Address: "ns=2;s=M101.State", MachineID: "M-101", Kind: KindState},
			{Address: "ns=2;s=M101.Temp", MachineID: "M-101", Kind: KindMeasurement, Name: "temp_c", Unit: "Cel"},
			{Address: "ns=2;s=M101.Down", MachineID: "M-101", Kind: KindDowntime},
		},
		WriteRules: []WriteRule{{Node: "ns=2;s=Vessel1.TempSP", Min: 60, Max: 80, Unit: "Cel"}},
	}
}

func TestEdgeConfigValidate_FR25_19(t *testing.T) {
	mutate := func(f func(*EdgeConfig)) EdgeConfig {
		c := goodConfig()
		f(&c)
		return c
	}
	tests := []struct {
		name    string
		cfg     EdgeConfig
		wantErr bool
	}{
		{"valid", goodConfig(), false},
		{"version zero", mutate(func(c *EdgeConfig) { c.Version = 0 }), true},
		{"resource not a uuid", mutate(func(c *EdgeConfig) { c.Machines[0].ResourceID = "x" }), true},
		{"machine listed twice", mutate(func(c *EdgeConfig) { c.Machines = append(c.Machines, c.Machines[0]) }), true},
		{"tag to unknown machine", mutate(func(c *EdgeConfig) { c.Tags[0].MachineID = "M-9" }), true},
		{"address mapped twice", mutate(func(c *EdgeConfig) { c.Tags[1].Address = c.Tags[0].Address }), true},
		{"unknown kind", mutate(func(c *EdgeConfig) { c.Tags[0].Kind = "vibes" }), true},
		{"measurement without name", mutate(func(c *EdgeConfig) { c.Tags[2].Name = "" }), true},
		{"write min above max", mutate(func(c *EdgeConfig) { c.WriteRules[0].Min = 90 }), true},
		{"write limit infinite", mutate(func(c *EdgeConfig) { c.WriteRules[0].Max = math.Inf(1) }), true},
		{"write limit NaN", mutate(func(c *EdgeConfig) { c.WriteRules[0].Min = nan() }), true},
		{"duplicate write node", mutate(func(c *EdgeConfig) { c.WriteRules = append(c.WriteRules, c.WriteRules[0]) }), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error does not wrap ErrInvalidConfig: %v", err)
			}
		})
	}
}

func TestNextApplied_OneVersionAtATime(t *testing.T) {
	cfg := goodConfig()
	applied, err := NextApplied(1, cfg)
	if err != nil || applied != 2 {
		t.Fatalf("newer version: applied=%d err=%v, want 2", applied, err)
	}
	applied, err = NextApplied(2, cfg)
	if err == nil || applied != 2 {
		t.Fatalf("same version must be refused and keep 2, got %d err=%v", applied, err)
	}
	older := goodConfig()
	older.Version = 1
	if applied, err := NextApplied(2, older); err == nil || applied != 2 {
		t.Fatalf("older version accepted: %d %v", applied, err)
	}
}

func TestNormalize_FR25_9(t *testing.T) {
	cfg := goodConfig()
	ts := time.Date(2026, 10, 21, 8, 15, 3, 0, time.UTC)
	count, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.Count", Seq: 7, TS: ts, Number: 12})
	if err != nil || count.Kind != KindCount || count.GoodCount == nil || *count.GoodCount != 12 || count.ResourceID != resourceUUID {
		t.Fatalf("count: %+v err=%v", count, err)
	}
	state, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.State", Seq: 8, TS: ts, Text: "RUNNING"})
	if err != nil || state.State != "RUNNING" {
		t.Fatalf("state: %+v err=%v", state, err)
	}
	down, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.Down", Seq: 9, TS: ts, Text: "JAM"})
	if err != nil || down.State != "DOWN" || down.ReasonCode == nil || *down.ReasonCode != "JAM" {
		t.Fatalf("downtime: %+v err=%v", down, err)
	}
	meas, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.Temp", Seq: 10, TS: ts, Number: 71.4})
	if err != nil || meas.Measurement == nil || meas.Measurement.Name != "temp_c" || meas.Measurement.Unit != "Cel" {
		t.Fatalf("measurement: %+v err=%v", meas, err)
	}
	if _, err := cfg.Normalize(Sample{Address: "ns=2;s=unmapped", Seq: 11, TS: ts}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unmapped address accepted: %v", err)
	}
	if _, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.Count", Seq: 12, TS: ts, Number: 1.5}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("fractional count delta accepted: %v", err)
	}
	if _, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.State", Seq: 13, TS: ts, Text: "BROKEN"}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("unknown state accepted: %v", err)
	}
	for name, v := range map[string]float64{"count +Inf": math.Inf(1), "count NaN": nan(), "count beyond 2^53": 1 << 60} {
		if _, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.Count", Seq: 14, TS: ts, Number: v}); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	for name, v := range map[string]float64{"measurement NaN": nan(), "measurement -Inf": math.Inf(-1)} {
		if _, err := cfg.Normalize(Sample{Address: "ns=2;s=M101.Temp", Seq: 15, TS: ts, Number: v}); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	// A normalized measurement must survive the JSON envelope the connector publishes.
	if _, err := json.Marshal(meas); err != nil {
		t.Fatalf("measurement does not encode: %v", err)
	}
}

func TestCheckWrite_BR25_15(t *testing.T) {
	cfg := goodConfig()
	const sp = "ns=2;s=Vessel1.TempSP"
	tests := []struct {
		name    string
		req     WriteRequest
		allowed bool
	}{
		{"allowlisted, in limits, recipe running", WriteRequest{Node: sp, Value: 72, RecipeInExecution: true}, true},
		{"limits are inclusive", WriteRequest{Node: sp, Value: 80, RecipeInExecution: true}, true},
		{"node not on allowlist", WriteRequest{Node: "ns=2;s=Other", Value: 72, RecipeInExecution: true}, false},
		{"above limit", WriteRequest{Node: sp, Value: 80.5, RecipeInExecution: true}, false},
		{"below limit", WriteRequest{Node: sp, Value: 59.9, RecipeInExecution: true}, false},
		{"NaN is outside limits", WriteRequest{Node: sp, Value: nan(), RecipeInExecution: true}, false},
		{"no recipe in execution", WriteRequest{Node: sp, Value: 72}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := cfg.CheckWrite(tc.req)
			if (err == nil) != tc.allowed {
				t.Fatalf("CheckWrite = %v, allowed want %v", err, tc.allowed)
			}
			if err != nil && !errors.Is(err, ErrWriteRefused) {
				t.Fatalf("refusal does not wrap ErrWriteRefused: %v", err)
			}
		})
	}
}

func TestCheckWrite_InsideLimitsProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cfg := goodConfig()
		v := rapid.Float64Range(-1000, 1000).Draw(t, "value")
		inside := v >= 60 && v <= 80
		err := cfg.CheckWrite(WriteRequest{Node: "ns=2;s=Vessel1.TempSP", Value: v, RecipeInExecution: true})
		if (err == nil) != inside {
			t.Fatalf("value %v: allowed=%v, want %v", v, err == nil, inside)
		}
	})
}

func TestReadbackMatches_BR25_15(t *testing.T) {
	if !ReadbackMatches(72.0, 72.0, 0.05) || !ReadbackMatches(72.0, 72.04, 0.05) {
		t.Fatal("readback within tolerance flagged as deviation")
	}
	if ReadbackMatches(72.0, 71.9, 0.05) {
		t.Fatal("readback outside tolerance not flagged")
	}
	if CategoryDeviation != "deviation" {
		t.Fatalf("category = %q", CategoryDeviation)
	}
}

func TestDecideCert_BR25_14(t *testing.T) {
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	failing := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	tests := []struct {
		name     string
		notAfter time.Time
		failing  *time.Time
		want     CertAction
	}{
		{"60 days left", now.Add(60 * day), nil, CertOK},
		{"exactly 30 days left is still ok", now.Add(30 * day), nil, CertOK},
		{"29 days left starts renewal", now.Add(29 * day), nil, CertRenew},
		{"renewal failing 6 days", now.Add(20 * day), failing(6 * day), CertRenew},
		{"renewal failing 7 days alerts", now.Add(20 * day), failing(7 * day), CertAlert},
		{"expired stops publishing", now.Add(-time.Second), nil, CertExpired},
		{"expired wins over failing alert", now, failing(8 * day), CertExpired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DecideCert(now, tc.notAfter, tc.failing); got != tc.want {
				t.Fatalf("DecideCert = %s, want %s", got, tc.want)
			}
		})
	}
	if PublishAllowed(CertExpired) || !PublishAllowed(CertRenew) {
		t.Fatal("publishing gate wrong")
	}
}

func TestRings_FR25_19(t *testing.T) {
	next, ok := NextRing(RingCanary)
	if !ok || next != RingOne {
		t.Fatalf("canary -> %s %v", next, ok)
	}
	next, ok = NextRing(RingOne)
	if !ok || next != RingAll {
		t.Fatalf("ring1 -> %s %v", next, ok)
	}
	if _, ok := NextRing(RingAll); ok {
		t.Fatal("all has no next ring")
	}
	if _, ok := NextRing(Ring("beta")); ok {
		t.Fatal("unknown ring promoted")
	}
}

func TestRollout_HealthyWindowAndRollback(t *testing.T) {
	start := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if CanPromote(start, start.Add(23*time.Hour)) || !CanPromote(start, start.Add(24*time.Hour)) {
		t.Fatal("promotion window is 24 h")
	}
	before := start.Add(-time.Minute)
	beat := start.Add(2 * time.Minute)
	tests := []struct {
		name string
		last *time.Time
		now  time.Time
		want bool
	}{
		{"within five minutes, no heartbeat yet", nil, start.Add(4 * time.Minute), false},
		{"five minutes, no heartbeat", nil, start.Add(5 * time.Minute), true},
		{"five minutes, only pre-upgrade heartbeat", &before, start.Add(5 * time.Minute), true},
		{"heartbeat after upgrade is healthy", &beat, start.Add(time.Hour), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RollbackDue(start, tc.last, tc.now); got != tc.want {
				t.Fatalf("RollbackDue = %v, want %v", got, tc.want)
			}
		})
	}
}

func nan() float64 {
	zero := 0.0
	return zero / zero
}

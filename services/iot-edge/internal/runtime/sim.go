package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/BenjaminSRussell/passage/services/iot-edge/internal/domain"
)

// SimMachine is one simulated machine: a count tag whose absolute 32-bit counter rises every tick,
// and a state tag reporting RUNNING, or DOWN for a stretch of ticks.
type SimMachine struct {
	CountAddress string
	StateAddress string
	// Start is the counter's first value, so a test can start near 2^32 and cross the rollover.
	Start uint32
	// PerTick is how far the counter rises each tick.
	PerTick uint32
	// DownFrom and DownTicks make the machine report DOWN for DownTicks ticks from tick DownFrom.
	DownFrom, DownTicks int
}

// Simulator is a Source that stands in for real machines (dev, demo plant and tests). Its time is
// the injected clock's, one reading per tag per tick, so a run is deterministic.
type Simulator struct {
	Machines []SimMachine
	Tick     time.Duration
	Ticks    int // 0 runs until ctx ends
	Now      func() time.Time
}

// SimConfig returns the configuration document that maps a simulator's tags for the given
// machines: machine i is "SIM-i" on the resource whose UUID resourceID(i) returns.
func SimConfig(version int, machines int, resourceID func(i int) string) (domain.EdgeConfig, []SimMachine) {
	cfg := domain.EdgeConfig{Version: version}
	var sims []SimMachine
	for i := 1; i <= machines; i++ {
		id := fmt.Sprintf("SIM-%d", i)
		cfg.Machines = append(cfg.Machines, domain.MachineSpec{MachineID: id, ResourceID: resourceID(i)})
		count, state := fmt.Sprintf("sim/%d/count", i), fmt.Sprintf("sim/%d/state", i)
		cfg.Tags = append(cfg.Tags,
			domain.TagMapping{Address: count, MachineID: id, Kind: domain.KindCount},
			domain.TagMapping{Address: state, MachineID: id, Kind: domain.KindState})
		sims = append(sims, SimMachine{CountAddress: count, StateAddress: state, PerTick: 1})
	}
	return cfg, sims
}

// Run emits the readings of every tick.
func (s *Simulator) Run(ctx context.Context, emit func(Reading)) error {
	now := s.Now
	if now == nil {
		now = time.Now
	}
	counters := make([]uint32, len(s.Machines))
	for i, m := range s.Machines {
		counters[i] = m.Start
	}
	tick := time.NewTicker(max(s.Tick, time.Millisecond))
	defer tick.Stop()
	for n := 0; s.Ticks == 0 || n < s.Ticks; n++ {
		if n > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-tick.C:
			}
		}
		ts := now()
		for i, m := range s.Machines {
			state := "RUNNING"
			if m.DownTicks > 0 && n >= m.DownFrom && n < m.DownFrom+m.DownTicks {
				state = "DOWN"
			} else {
				counters[i] += m.PerTick // wraps at 2^32 like the machine's counter
			}
			emit(Reading{Address: m.CountAddress, TS: ts, Number: float64(counters[i])})
			emit(Reading{Address: m.StateAddress, TS: ts, Text: state})
		}
	}
	return nil
}

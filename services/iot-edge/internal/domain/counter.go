// Package domain holds the pure rules of the iot-edge connector: counter rollover,
// per-machine sequence acceptance, the machine event schema (BR-25.4) and the
// store-and-forward buffer (FR-25.9). It performs no I/O.
package domain

// counterModulus is 2^32, the range of an absolute 32-bit machine counter.
const counterModulus uint64 = 1 << 32

// CounterDelta returns the count between two readings of an absolute 32-bit
// counter. A reading below the previous one is a rollover (BR-25.4), so the delta
// wraps modulo 2^32. Counts are deltas since the previous count event.
func CounterDelta(prev, cur uint32) uint64 {
	if cur >= prev {
		return uint64(cur - prev)
	}
	return counterModulus - uint64(prev) + uint64(cur)
}

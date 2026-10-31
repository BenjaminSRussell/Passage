package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// fakeBroker models the broker the publisher writes to. While down it refuses
// writes. crashAt makes one write land in the log and then lose its
// acknowledgement (the broker crashed between write and ack).
type fakeBroker struct {
	log      []string
	downFrom time.Time
	downTo   time.Time
	crashAt  time.Time
}

var errBrokerDown = errors.New("broker unavailable")

func (f *fakeBroker) publish(now time.Time, batch [][]byte) error {
	if !now.Before(f.downFrom) && now.Before(f.downTo) {
		return errBrokerDown
	}
	for _, p := range batch {
		f.log = append(f.log, string(p))
	}
	if now.Equal(f.crashAt) {
		return errBrokerDown
	}
	return nil
}

// Wave 25 AC2 ("no loss on broker restart"), edge side. Four machines produce
// 10,000 events in one simulated minute (2,500 each, one per machine every 24 ms),
// through the Sequencer into the Buffer. The publisher ticks every 100 ms and
// sends up to 500 buffered events. At 20.000 s the broker writes the batch and
// crashes before acknowledging it, then refuses writes until 25.000 s.
//
// Hand-derived: the tick at 19.900 s acknowledged every event with ts <= 19.900 s,
// so the batch lost at 20.000 s holds ts in (19.900, 20.000], that is k-1 = 830..833
// (24 ms steps: 830 x 24 = 19,920 and 833 x 24 = 19,992), 4 timestamps x 4 machines
// = 16 events. Peek/Ack keeps them and they are sent again: the broker log holds
// 10,016 entries, each of the 10,000 (machine, seq) at least once and exactly those
// 16 twice. Nothing is evicted (capacity 20,000 > the 848-event backlog).
// The same run with Drain (remove before the ack) loses every batch drained while
// the broker refused writes: ticks 20.1 s to 24.9 s took ts in (20.000, 24.900],
// k-1 = 834..1037 (834 x 24 = 20,016, 1037 x 24 = 24,888), 204 x 4 = 816 events.
func TestPublisher_BrokerRestartLosesNothingWithPeekAck(t *testing.T) {
	run := func(useDrain bool) (*fakeBroker, *Buffer) {
		start := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
		at := func(ms int) time.Time { return start.Add(time.Duration(ms) * time.Millisecond) }
		broker := &fakeBroker{downFrom: at(20_100), downTo: at(25_000), crashAt: at(20_000)}
		buf := NewBuffer(24*time.Hour, 20_000)
		seq := NewSequencer()
		machines := []string{"M-1", "M-2", "M-3", "M-4"}
		k := 1
		for ms := 0; ms <= 61_000; ms += 100 {
			now := at(ms)
			// Produce every event stamped at or before this tick.
			for ; k <= 2500 && (k-1)*24 <= ms; k++ {
				ts := at((k - 1) * 24)
				for _, m := range machines {
					if d, gap := seq.Accept(m, int64(k), ts, now); d != Accepted || gap != nil {
						t.Fatalf("sequencer %s/%d: %s %v", m, k, d, gap)
					}
					if buf.Push(now, fmt.Appendf(nil, "%s/%d", m, k)) {
						t.Fatalf("evicted at %s/%d", m, k)
					}
				}
			}
			if useDrain {
				_ = broker.publish(now, buf.Drain(now, 500))
				continue
			}
			batch, through := buf.Peek(now, 500)
			if len(batch) == 0 {
				continue
			}
			if broker.publish(now, batch) == nil {
				buf.Ack(through)
			}
		}
		return broker, buf
	}

	broker, buf := run(false)
	seen := map[string]int{}
	for _, e := range broker.log {
		seen[e]++
	}
	dups := 0
	for _, n := range seen {
		dups += n - 1
	}
	if len(seen) != 10_000 || len(broker.log) != 10_016 || dups != 16 || buf.Len() != 0 || buf.Evicted() != 0 {
		t.Fatalf("peek/ack: distinct %d, log %d, duplicates %d, left %d, evicted %d; want 10000, 10016, 16, 0, 0",
			len(seen), len(broker.log), dups, buf.Len(), buf.Evicted())
	}
	for _, m := range []string{"M-1", "M-2", "M-3", "M-4"} {
		for k := 831; k <= 834; k++ {
			if seen[fmt.Sprintf("%s/%d", m, k)] != 2 {
				t.Fatalf("%s/%d sent %d times, want 2", m, k, seen[fmt.Sprintf("%s/%d", m, k)])
			}
		}
	}

	lossy, _ := run(true)
	distinct := map[string]bool{}
	for _, e := range lossy.log {
		distinct[e] = true
	}
	// With Drain the 816 events drained while the broker was down are gone.
	if len(distinct) != 10_000-816 {
		t.Fatalf("drain run kept %d distinct events, want %d", len(distinct), 10_000-816)
	}
}

// Ack removes only what was peeked: payloads pushed after the Peek stay, and an
// Ack after the peeked payloads were evicted removes nothing else.
func TestBuffer_PeekAckKeepsLaterPayloads(t *testing.T) {
	b := NewBuffer(24*time.Hour, 3)
	b.Push(t0, []byte("a"))
	b.Push(t0, []byte("b"))
	got, through := b.Peek(t0, 5)
	if len(got) != 2 || b.Len() != 2 {
		t.Fatalf("peek = %q, len %d", got, b.Len())
	}
	b.Push(t0, []byte("c"))
	b.Ack(through)
	if rest, _ := b.Peek(t0, 5); len(rest) != 1 || string(rest[0]) != "c" {
		t.Fatalf("after ack = %q, want [c]", rest)
	}
	_, through = b.Peek(t0, 1) // peeks c
	b.Push(t0, []byte("d"))    // [c d]
	b.Push(t0, []byte("e"))    // [c d e]
	b.Push(t0, []byte("f"))    // capacity 3: c evicted, [d e f]
	b.Ack(through)             // c is already gone; d, e, f stay
	if b.Len() != 3 || b.Evicted() != 1 {
		t.Fatalf("len %d evicted %d, want 3 and 1", b.Len(), b.Evicted())
	}
	if none, through := NewBuffer(time.Hour, 1).Peek(t0, 1); len(none) != 0 || through != 0 {
		t.Fatalf("empty peek = %q, %d", none, through)
	}
}

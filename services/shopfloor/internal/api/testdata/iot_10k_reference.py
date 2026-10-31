"""Independent reference for TestHTTP_IoT10kEventsBrokerRestart (Wave 25 AC2, AC-25.3/25.4).

Run with `python3 -I iot_10k_reference.py`. It shares no code with the Go engine: it
re-states the fixture from the test's comment, applies the BR-25.5 formula from the PRD
text with exact fractions, and replays the delivery plan against a set of stored
(machine, seq) keys. The Go test hard-codes the numbers this prints.

Fixture: four machines, 2,500 events each (10,000 in one minute), seq k = 1..2500 at
T0 + (k-1) * 24 ms. Seq 1 is RUNNING and seq 2500 is OFF on every machine. For k = 2..2499:
  M-1 count good 1; every k % 100 == 0 is good 0 reject 1.
  M-2 k = 1001 DOWN, 1002..1500 alarm, 1501 RUNNING; other k count good 2,
      every k % 40 == 0 is good 1 reject 1.
  M-3 even k measurement; odd k count good 3, plus reject 1 when k % 7 == 0.
  M-4 k = 2 SETUP, 3..250 measurement, 251 RUNNING; 252..2499 count good 1,
      every k % 25 == 0 is good 0 reject 2.
Ideal cycle seconds: M-1 0.02, M-2 0.01, M-3 0.02, M-4 0.025. Idle threshold 300 s
(the default); the longest gap between counts while RUNNING is checked below to be far
under it, so BR-25.6 idle never applies.

Delivery: events merged by (ts, machine) and cut into 20 batches of 500, so batch b holds
seq 125b+1..125b+125 of every machine. Order: 0-6, (7 lost in the broker restart), 8, 9,
then the consumer re-reads from its last committed offset 5-9 (7 included), then 10-13,
14, 14 (redelivered), 15, 17, 16 (out of order), 18, 19.
"""

from fractions import Fraction
from decimal import Decimal, ROUND_HALF_UP

STEP_MS = 24
N = 2500
IDEAL = {"M-1": Fraction(2, 100), "M-2": Fraction(1, 100), "M-3": Fraction(2, 100), "M-4": Fraction(25, 1000)}
IDLE_MS = 300_000


def event(machine, k):
    """Return (kind, state, good, reject) for seq k of a machine."""
    if k == 1:
        return ("state", "RUNNING", 0, 0)
    if k == N:
        return ("state", "OFF", 0, 0)
    if machine == "M-1":
        return ("count", None, 0, 1) if k % 100 == 0 else ("count", None, 1, 0)
    if machine == "M-2":
        if k == 1001:
            return ("state", "DOWN", 0, 0)
        if 1002 <= k <= 1500:
            return ("alarm", None, 0, 0)
        if k == 1501:
            return ("state", "RUNNING", 0, 0)
        return ("count", None, 1, 1) if k % 40 == 0 else ("count", None, 2, 0)
    if machine == "M-3":
        if k % 2 == 0:
            return ("measurement", None, 0, 0)
        return ("count", None, 3, 1 if k % 7 == 0 else 0)
    if machine == "M-4":
        if k == 2:
            return ("state", "SETUP", 0, 0)
        if 3 <= k <= 250:
            return ("measurement", None, 0, 0)
        if k == 251:
            return ("state", "RUNNING", 0, 0)
        return ("count", None, 0, 2) if k % 25 == 0 else ("count", None, 1, 0)
    raise ValueError(machine)


def round_half_up(x, places):
    q = Decimal(1).scaleb(-places)
    return (Decimal(x.numerator) / Decimal(x.denominator)).quantize(q, rounding=ROUND_HALF_UP)


def oee(machine):
    total = good = 0
    sched = run = down = 0
    state, since, last_count_or_run = None, None, None
    max_gap = 0
    for k in range(1, N + 1):
        kind, st, g, r = event(machine, k)
        t = (k - 1) * STEP_MS
        if kind == "count":
            total += g + r
            good += g
            if state == "RUNNING":
                max_gap = max(max_gap, t - last_count_or_run)
            last_count_or_run = t
        if st is not None:
            if state is not None and state != "OFF":
                span = t - since
                sched += span
                if state == "RUNNING":
                    max_gap = max(max_gap, t - last_count_or_run)
                    run += span
                else:
                    down += span
            if st == "RUNNING":
                last_count_or_run = t
            state, since = st, t
    assert state == "OFF"
    assert max_gap < IDLE_MS, max_gap
    planned = Fraction(sched, 60_000)  # minutes; no downtime is coded as planned
    run_min = Fraction(run, 60_000)
    a = run_min / planned
    p = IDEAL[machine] * total / 60 / run_min
    over = p > 1
    p = min(p, Fraction(1))
    q = Fraction(good, total)
    return {
        "planned": round_half_up(planned, 4), "run": round_half_up(run_min, 4), "down": round_half_up(Fraction(down, 60_000), 4),
        "total": total, "good": good,
        "A": round_half_up(a, 4), "P": round_half_up(p, 4), "Q": round_half_up(q, 4), "OEE": round_half_up(a * p * q, 4),
        "overRate": over, "maxCountGapMs": max_gap,
    }


def deliveries():
    order = [0, 1, 2, 3, 4, 5, 6, 8, 9, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 14, 15, 17, 16, 18, 19]
    machines = ["M-1", "M-2", "M-3", "M-4"]
    stored = set()
    last = {m: 0 for m in machines}
    out = []
    for b in order:
        seqs = range(125 * b + 1, 125 * b + 126)
        accepted = dropped = 0
        gaps = []
        for m in machines:
            # A gap is a missing run above the machine's highest stored seq.
            if last[m] > 0 and seqs[0] > last[m] + 1:
                gaps.append((m, last[m] + 1, seqs[0] - 1))
            for k in seqs:
                if (m, k) in stored:
                    dropped += 1
                else:
                    stored.add((m, k))
                    accepted += 1
            last[m] = max(last[m], seqs[-1])
        out.append((b, accepted, dropped, gaps))
    return out, len(stored), len(order) * 500


if __name__ == "__main__":
    for m in ["M-1", "M-2", "M-3", "M-4"]:
        print(m, oee(m))
    plan, stored, delivered = deliveries()
    for b, acc, drop, gaps in plan:
        print("batch", b, "accepted", acc, "dropped", drop, "gaps", gaps)
    print("delivered", delivered, "stored", stored,
          "accepted", sum(p[1] for p in plan), "dropped", sum(p[2] for p in plan),
          "gaps", sum(len(p[3]) for p in plan))

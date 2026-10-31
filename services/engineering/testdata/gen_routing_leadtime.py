#!/usr/bin/env python3
# Independent oracle for the Wave 09 proof "Routing capacity calc:
# sequential/parallel operations produce correct lead times (fixtures)"
# (AC-9.4: sequential, two-branch parallel and overlap fixtures, results to 4
# decimals).
#
# Checks the 12 hand-derived fixtures in routing-leadtime.json and writes
# routing-leadtime-adversarial.json: secondaries beside a primary that has
# predecessors, two secondaries with efficiency and queue, overlap transfer
# batches (slow successor, fast successor, a partial last batch where the
# second-to-last batch governs, a chain, an overlap into a join, overlap equal
# to the quantity), operations numbered out of route order and a decimal
# quantity with repeating-decimal efficiency.
#
# No code is shared with the Go engine; arithmetic is exact (fractions) and
# every batch of an overlap is evaluated (the engine evaluates only the batches
# that can bind). Rules (FR-9.4, FR-9.10, BR-9.7, BR-9.8, BR-23.5):
#   - run(o, x) = run_h x x / process_qty x 100 / efficiency_pct (0 means 100);
#     setup is not scaled; queue before, queue after and transit are elapsed.
#   - an operation's next_operation_no is its successor; 0 ends the route; when
#     no operation names a successor they follow in operation-number order.
#   - an operation starts when every predecessor has released it; a secondary
#     runs alongside the primary that shares its next operation, so it starts
#     no earlier than that primary starts. It then queues, sets up and runs:
#     run start = start + queue before + setup, run end = run start + run(qty),
#     finish = run end + queue after + transit.
#   - without overlap a predecessor releases at its finish. With overlap
#     quantity k (0 < k < qty) units move in batches of k (the last one holds
#     the rest); batch i leaves when the predecessor has run min(i x k, qty)
#     units, timed back from its run end, and arrives after its queue after and
#     transit. The successor may start when batch 1 arrives, and cannot end its
#     run before any batch i has arrived and the units from batch i on are run.
#   - lead time = latest finish of an operation that ends the route, reported
#     rounded half-up to 4 decimals; calendar days = ceil(hours / hours per day).
#
# Run: python3 -I gen_routing_leadtime.py
import json
from fractions import Fraction as F
from pathlib import Path

HERE = Path(__file__).resolve().parent


def num(o, key, default="0"):
    return F(o.get(key) or default)


def fmt4(x):
    scaled = x * 10000
    whole = scaled.numerator // scaled.denominator
    if scaled - whole >= F(1, 2):
        whole += 1
    s = "%d.%04d" % (whole // 10000, whole % 10000)
    return s.rstrip("0").rstrip(".")


def ceil(x):
    return -((-x.numerator) // x.denominator)


def lead_time(ops, qty, hours_per_day):
    qty, hpd = F(qty), F(hours_per_day)
    by_no = {o["no"]: o for o in ops}
    explicit = any(o.get("next") for o in ops)
    numbers = sorted(by_no)
    succ = {}
    for i, n in enumerate(numbers):
        succ[n] = by_no[n].get("next", 0) if explicit else (numbers[i + 1] if i + 1 < len(numbers) else 0)
    primary = lambda n: by_no[n].get("priority", "primary") in ("", "primary")

    def run(n, units):
        o = by_no[n]
        eff = num(o, "efficiencyPct") or F(100)
        return num(o, "run") * units / num(o, "processQty", "1") * 100 / eff

    timing = {}

    def times(n):
        if n in timing:
            return timing[n]
        o = by_no[n]
        start = F(0)
        preds = [p for p in numbers if succ[p] == n]
        for p in preds:
            ps, pe, pf = times(p)
            k = num(by_no[p], "overlapQty")
            if 0 < k < qty:
                first = pe - run(p, qty - k) + num(by_no[p], "queueAfter") + num(by_no[p], "transit")
                start = max(start, first)
            else:
                start = max(start, pf)
        if not primary(n):
            for p in numbers:
                if primary(p) and succ[p] == succ[n]:
                    start = max(start, times(p)[0])
        run_end = start + num(o, "queueBefore") + num(o, "setup") + run(n, qty)
        for p in preds:
            k = num(by_no[p], "overlapQty")
            if not 0 < k < qty:
                continue
            ps, pe, pf = times(p)
            batches = ceil(qty / k)
            for i in range(2, batches + 1):
                done = min(i * k, qty)
                arrives = pe - run(p, qty - done) + num(by_no[p], "queueAfter") + num(by_no[p], "transit")
                run_end = max(run_end, arrives + run(n, qty - (i - 1) * k))
        finish = run_end + num(o, "queueAfter") + num(o, "transit")
        timing[n] = (start, run_end, finish)
        return timing[n]

    total = max(times(n)[2] for n in numbers if succ[n] == 0)
    # Rounding to 4 decimals must not hinge on the engine's 16-digit division.
    scaled = total * 10000
    assert abs(scaled - (scaled.numerator // scaled.denominator) - F(1, 2)) > F(1, 10**8), "4th decimal on a half"
    return fmt4(total), ceil(total / hpd)


def op(no, nxt=0, priority="primary", setup="0", run="0", processQty="1", queueBefore="0", queueAfter="0",
       transit="0", efficiencyPct="100", overlapQty=None):
    o = {"no": no, "next": nxt, "priority": priority, "setup": setup, "run": run, "processQty": processQty,
         "queueBefore": queueBefore, "queueAfter": queueAfter, "transit": transit, "efficiencyPct": efficiencyPct}
    if overlapQty is not None:
        o["overlapQty"] = overlapQty
    return o


ADVERSARIAL = [
    ("secondary-beside-delayed-primary", "100", [
        op(10, 20, setup="1", run="0.04"), op(20, 40, run="0.01"),
        op(30, 40, "secondary1", setup="0.5", run="0.035"), op(40, 0, setup="1")]),
    ("two-secondaries-efficiency-queue", "100", [
        op(10, 20, run="0.02"), op(20, 50, run="0.03"), op(30, 50, "secondary1", run="0.02", efficiencyPct="50"),
        op(40, 50, "secondary2", setup="1", run="0.03", queueBefore="0.5"), op(50, 0, run="0.01")]),
    ("overlap-slow-successor", "100", [
        op(10, 20, setup="1", run="0.05", overlapQty="10"), op(20, 0, setup="0.5", run="0.1")]),
    ("overlap-fast-successor-last-batch", "100", [
        op(10, 20, setup="1", run="0.1", overlapQty="25"), op(20, 0, setup="0.5", run="0.02")]),
    ("overlap-partial-last-batch", "100", [
        op(10, 20, run="0.1", transit="0.5", overlapQty="30"), op(20, 0, run="0.09")]),
    ("overlap-equal-to-quantity-is-none", "100", [
        op(10, 20, setup="1", run="0.05", overlapQty="100"), op(20, 0, setup="0.5", run="0.1")]),
    ("overlap-chain-three", "100", [
        op(10, 20, setup="0.5", run="0.02", overlapQty="20"),
        op(20, 30, setup="0.25", run="0.03", queueAfter="0.25", overlapQty="20"), op(30, 0, run="0.01")]),
    ("overlap-into-join-with-secondary", "100", [
        op(10, 30, setup="1", run="0.06", overlapQty="10"), op(20, 30, "secondary1", setup="2", run="0.02"),
        op(30, 0, run="0.05")]),
    ("numbered-out-of-route-order", "100", [
        op(10, 20, setup="0.5", run="0.01"), op(20, 0, run="0.02", transit="1"), op(30, 10, setup="2", queueAfter="0.5")]),
    ("decimal-quantity-repeating-efficiency", "37.5", [
        op(10, 20, setup="0.75", run="0.3", processQty="2.5", efficiencyPct="90"),
        op(20, 0, run="0.125", efficiencyPct="70", overlapQty=None, queueBefore="0.2")]),
]


def main():
    original = json.loads((HERE / "routing-leadtime.json").read_text())
    for f in original:
        hours, days = lead_time(f["operations"], f["quantity"], f["hoursPerDay"])
        assert (hours, days) == (f["expectedHours"], f["expectedDays"]), (f["name"], hours, days)
    print("routing-leadtime.json: %d fixtures confirmed" % len(original))
    out = []
    for name, qty, ops in ADVERSARIAL:
        hours, days = lead_time(ops, qty, "8")
        out.append({"name": name, "quantity": qty, "hoursPerDay": "8", "operations": ops,
                    "expectedHours": hours, "expectedDays": days})
        print("  %-40s %8s h %d d" % (name, hours, days))
    text = "[\n" + ",\n".join(json.dumps(f, separators=(",", ":")) for f in out) + "\n]\n"
    (HERE / "routing-leadtime-adversarial.json").write_text(text)


if __name__ == "__main__":
    main()

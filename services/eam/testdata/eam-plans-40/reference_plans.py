#!/usr/bin/env python3
"""Independent reference for the eam-plans-40 fixture (PRD 8.44, AC-44.2).

Run with `python3 -I reference_plans.py` from this directory. It reads
fixture.json and writes expected.json. It shares no code with the Go engine:
it re-derives BR-44.1 from the PRD text with exact rational arithmetic
(fractions.Fraction) and its own calendar month arithmetic.

Rules taken from the PRD and the service README (not from the Go code):

* FR-44.3 average daily usage: over the readings of the last 30 days
  (reading_at in [newest - 30 d, newest]), the sum of the increments between
  consecutive readings divided by the days between the first and the last of
  them; fewer than two readings, or no time between them, keeps the previous
  average (0 for a new counter).
* BR-44.1 (a) T = calendar.add(base, interval), base = last completion or the
  line start. calendar.add of N months/years lands on the same day of the
  target month, or on its last day when the month is shorter (Jan 31 + 1 month
  = Feb 28; Feb 29 + 2 years = Feb 28), keeping the time of day, as every
  calendar does (SAP, D365, PostgreSQL interval arithmetic).
* BR-44.1 (b) R = last completed reading + counter interval;
  F = now + ceil((R - last reading) / avg) whole days (a reading already past
  R gives F = now); avg = 0 gives no forecast.
* BR-44.1 (c) TIME: T; COUNTER: F; TIME_OR_COUNTER: min(T, F);
  TIME_AND_COUNTER: max(T, F), none while F is unknown.
* BR-44.1 (d) generate when next_due - early_tolerance_pct x interval <=
  now + horizon_days and the line has no open work order. The interval of a
  time line is its actual length from the base (T - base); a counter-only
  line has no time interval, so no early window.
* BR-44.1 triggers: every reading reschedules the lines on that counter, a
  completion (BR-44.2) records the completion time and the counter's last
  reading on the line and reschedules it, and the nightly sweep reschedules
  every line.
"""

import calendar
import json
from datetime import datetime, timedelta, timezone
from fractions import Fraction
from math import ceil

UTC = timezone.utc
DAY = timedelta(days=1)


def parse_ts(s):
    return datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=UTC)


def parse_day(s):
    return datetime.strptime(s, "%Y-%m-%d").replace(tzinfo=UTC)


def iso(t):
    return t.astimezone(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


def add_months(t, n):
    y, m0 = divmod(t.month - 1 + n, 12)
    y += t.year
    m = m0 + 1
    d = min(t.day, calendar.monthrange(y, m)[1])
    return t.replace(year=y, month=m, day=d)


def cal_add(t, n, unit):
    if unit == "DAYS":
        return t + n * DAY
    if unit == "WEEKS":
        return t + 7 * n * DAY
    if unit == "MONTHS":
        return add_months(t, n)
    if unit == "YEARS":
        return add_months(t, 12 * n)
    raise ValueError(unit)


def num(x):
    """A Fraction as a JSON number (every fixture value is a multiple of 1/2)."""
    if x.denominator == 1:
        return int(x)
    return float(x)


class Counter:
    def __init__(self, spec):
        self.spec = spec
        self.readings = []  # (at, value)
        self.last = Fraction(0)
        self.avg = Fraction(0)

    def usage_on(self, d):
        rate = Fraction(0)
        for step in self.spec["profile"]:
            if parse_day(step["from"]) <= d:
                rate = Fraction(str(step["rate"]))
        weekend = d.weekday() >= 5
        if self.spec.get("weekdaysOnly") and weekend:
            return Fraction(0)
        if self.spec.get("weekendsOnly") and not weekend:
            return Fraction(0)
        return rate

    def record(self, at, value):
        self.readings.append((at, value))
        self.last = value
        window = [(a, v) for a, v in self.readings if at - 30 * DAY <= a <= at]
        if len(window) < 2:
            return
        span = Fraction(int((window[-1][0] - window[0][0]).total_seconds()), 86400)
        if span <= 0:
            return
        total = Fraction(0)
        for (_, pv), (_, cv) in zip(window, window[1:]):
            total += cv - pv
        self.avg = total / span


class Line:
    def __init__(self, spec, counter):
        self.spec = spec
        self.code = spec["code"]
        self.kind = spec["kind"]
        self.counter = counter
        self.start = parse_ts(spec["startAt"])
        self.last_at = parse_ts(spec["lastCompletedAt"]) if spec.get("lastCompletedAt") else None
        self.last_reading = Fraction(str(spec.get("lastCompletedReading", 0)))
        self.early = spec.get("earlyTolerancePct", 10)
        self.horizon = spec.get("horizonDays", 90)
        self.offset = spec.get("completeOffsetDays", 0)
        self.open = None
        self.orders = []
        self.next_due = None
        self.next_reading = None

    def has_time(self):
        return self.kind in ("TIME", "TIME_OR_COUNTER", "TIME_AND_COUNTER")

    def has_counter(self):
        return self.kind in ("COUNTER", "TIME_OR_COUNTER", "TIME_AND_COUNTER")

    def evaluate(self, now):
        T, span = None, timedelta(0)
        if self.has_time():
            base = self.last_at or self.start
            T = cal_add(base, self.spec["intervalValue"], self.spec["intervalUnit"])
            span = T - base
        R, F = None, None
        if self.has_counter():
            R = self.last_reading + Fraction(str(self.spec["counterInterval"]))
            c = self.counter
            if c.avg > 0:
                remaining = max(Fraction(0), R - c.last)
                F = now + ceil(remaining / c.avg) * DAY
        if self.kind == "TIME":
            due = T
        elif self.kind == "COUNTER":
            due = F
        elif self.kind == "TIME_OR_COUNTER":
            due = T if F is None else min(T, F)
        else:
            due = None if F is None else max(T, F)
        self.next_due, self.next_reading = due, R
        if due is None or self.open is not None:
            return
        window_s = Fraction(int(span.total_seconds()) * self.early, 100)
        assert window_s.denominator == 1, (self.code, window_s)
        window = timedelta(seconds=int(window_s))
        if due - window <= now + self.horizon * DAY:
            wo = {"generatedAt": iso(now), "dueAt": iso(due), "dueReading": None if R is None else num(R)}
            self.open = (due, wo)
            self.orders.append(wo)

    def complete(self, now):
        self.open = None
        self.last_at = now
        if self.counter is not None:
            self.last_reading = self.counter.last
        self.evaluate(now)


def simulate(fx):
    counters = {c["name"]: Counter(c) for c in fx["counters"]}
    hist = parse_day(fx["historyStart"])
    sim = parse_day(fx["simStart"])
    end = sim + fx["days"] * DAY
    lines = []
    value = {n: Fraction(str(c.spec["initial"])) for n, c in counters.items()}
    d = hist
    while d < end:
        for name in sorted(counters):
            c = counters[name]
            if (d - hist).days % c.spec["readEveryDays"] == 0:
                c.record(d, value[name])
                for ln in lines:
                    if ln.counter is c:
                        ln.evaluate(d)
        if d == sim:
            for p in fx["plans"]:
                ln = Line(p, counters.get(p.get("counter")))
                lines.append(ln)
                ln.evaluate(d)
        if d >= sim:
            noon = d + timedelta(hours=12)
            due_now = [ln for ln in lines if ln.open is not None
                       and ln.open[0].replace(hour=0, minute=0, second=0) + ln.offset * DAY <= d]
            for ln in sorted(due_now, key=lambda x: x.code):
                ln.complete(noon)
            night = d + timedelta(hours=23)
            for ln in lines:
                ln.evaluate(night)
        for name, c in counters.items():
            value[name] += c.usage_on(d)
        d += DAY
    return lines


def main():
    with open("fixture.json") as f:
        fx = json.load(f)
    lines = simulate(fx)
    by = {ln.code: ln for ln in lines}

    # Hand-derived literals (worked by hand from BR-44.1, independent of the loop above).
    # P03: 1 month from 2025-12-31 is 2026-01-31 00:00; tolerance 0 and horizon 3 generate
    # it at the 2026-01-28 23:00 sweep. Completed 2026-01-31 12:00, + 1 month is
    # 2026-02-28 12:00 (month end), generated at the 2026-02-25 23:00 sweep.
    assert by["P03"].orders[0] == {"generatedAt": "2026-01-28T23:00:00Z", "dueAt": "2026-01-31T00:00:00Z", "dueReading": None}
    assert by["P03"].orders[1] == {"generatedAt": "2026-02-25T23:00:00Z", "dueAt": "2026-02-28T12:00:00Z", "dueReading": None}
    # P19: C09 reads 49,000 + 30 x 100 = 52,000 on 2026-01-01, past R = 51,000, so the
    # first order is due at once. Completed at 52,000: R = 53,000; the 2026-01-09 reading
    # of 52,800 leaves 2 days at 100/day, 2026-01-11 00:00 <= now + horizon 2.
    assert by["P19"].orders[0] == {"generatedAt": "2026-01-01T00:00:00Z", "dueAt": "2026-01-01T00:00:00Z", "dueReading": 51000}
    assert by["P19"].orders[1] == {"generatedAt": "2026-01-09T00:00:00Z", "dueAt": "2026-01-11T00:00:00Z", "dueReading": 53000}
    # P26 (BR-44.1 shape): T = 2025-10-15 + 90 d = 2026-01-13; C12 at 12,295 with 6.5/day
    # gives F = 2026-01-01 + ceil(205 / 6.5 = 31.5) = 2026-02-02; min is T, inside 30 d.
    assert by["P26"].orders[0] == {"generatedAt": "2026-01-01T00:00:00Z", "dueAt": "2026-01-13T00:00:00Z", "dueReading": 12500}
    # P05: 2024-02-29 + 2 years is 2026-02-28 (no Feb 29 in 2026).
    assert by["P05"].orders[0]["dueAt"] == "2026-02-28T00:00:00Z"
    # P16 and P40 sit on an idle counter: no forecast, no orders.
    assert by["P16"].orders == [] and by["P40"].orders == []

    out = {
        "comment": "Generated by reference_plans.py (python3 -I) from fixture.json; do not edit by hand.",
        "plans": {
            ln.code: {
                "orders": ln.orders,
                "final": {
                    "lastCompletedAt": iso(ln.last_at) if ln.last_at else None,
                    "nextDueAt": iso(ln.next_due) if ln.next_due else None,
                    "nextDueReading": None if ln.next_reading is None else num(ln.next_reading),
                },
            }
            for ln in lines
        },
    }
    with open("expected.json", "w") as f:
        json.dump(out, f, indent=1, sort_keys=True)
        f.write("\n")
    total = sum(len(ln.orders) for ln in lines)
    print(f"{len(lines)} plans, {total} work orders")


if __name__ == "__main__":
    main()

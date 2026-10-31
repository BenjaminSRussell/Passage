"""Ticket AC2 / AC-45.4 (feasibility half): dispatch plans respect skills, territory,
time windows, shifts, travel with buffer and the daily load (BR-45.3).

`oracle` below is written from the BR-45.3 text and shares no code with
passage_dispatch.model. Three properties use it:

* every greedy plan, on the original generator and on an adversarial one (tight
  windows, a skill nobody has, asymmetric travel, buffers 0-15, overtime), has no
  oracle violation, and the plan plus the unassigned list covers each work order
  exactly once;
* plan_violations agrees with the oracle rule for rule on mutated plans (shifted,
  reassigned, re-sequenced, duplicated and invented assignments), so the checker
  the solver relies on is itself right on infeasible input;
* no work order is left unassigned that a technician with an empty route could
  have served alone.

Hypothesis runs derandomized, so CI sees the same examples on every run. The
hand scenarios at the end pin the greedy rule on small cases worked by hand.
"""

from __future__ import annotations

import math
from collections import Counter

from hypothesis import HealthCheck, given, settings
from hypothesis import strategies as st

from passage_dispatch import Assignment, Problem, Technician, WorkOrder, plan_violations, solve_greedy

PROOF = settings(max_examples=300, deadline=None, derandomize=True, suppress_health_check=[HealthCheck.too_slow])


def oracle(problem: Problem, plan: list[Assignment]) -> set[tuple[str, str]]:
    """BR-45.3 hard constraints as a set of (rule, subject) pairs; empty = feasible."""
    techs = {t.id: t for t in problem.technicians}
    wos = {w.id: w for w in problem.work_orders}
    found: set[tuple[str, str]] = set()
    known = []
    for a in plan:
        if a.work_order_id not in wos:
            found.add(("unknown", a.work_order_id))
        elif a.technician_id not in techs:
            found.add(("unknown", a.technician_id))
        else:
            known.append(a)
    for wid, n in Counter(a.work_order_id for a in known).items():
        if n > 1:
            found.add(("assigned twice", wid))
    for tid, tech in techs.items():
        route = sorted((a for a in known if a.technician_id == tid), key=lambda a: a.sequence)
        if len({a.sequence for a in route}) < len(route):
            # No defined visiting order, so the route cannot be walked.
            found.add(("sequence", tid))
            continue
        free_at, at, minutes = tech.shift_start, tech.home, 0
        for a in route:
            w = wos[a.work_order_id]
            leg = problem.travel[at][w.location]
            if not set(w.skills).issubset(tech.skills):
                found.add(("skill", w.id))
            if w.territory not in tech.territories:
                found.add(("territory", w.id))
            if a.start + w.duration != a.end:
                found.add(("duration", w.id))
            if not (tech.shift_start <= a.start and a.end <= tech.shift_end):
                found.add(("shift", w.id))
            if (w.window_start is not None and a.start < w.window_start) or (w.window_end is not None and a.end > w.window_end):
                found.add(("window", w.id))
            if a.start - free_at < leg + problem.buffer_minutes:
                found.add(("travel", w.id))
            minutes += leg + w.duration
            free_at, at = a.end, w.location
        if route:
            minutes += problem.travel[at][tech.home]
        if minutes > tech.max_daily_minutes + tech.overtime_minutes:
            found.add(("load", tid))
    return found


RULE_OF_MESSAGE = [
    ("lacks skills", "skill"),
    ("outside territory", "territory"),
    ("duration does not match", "duration"),
    ("outside shift", "shift"),
    ("time window", "window"),
    ("travel from previous", "travel"),
    ("daily load", "load"),
    ("more than once", "assigned twice"),
    ("duplicate sequence", "sequence"),
    ("unknown", "unknown"),
]


def engine_rules(problem: Problem, plan: list[Assignment]) -> set[tuple[str, str]]:
    out = set()
    for msg in plan_violations(problem, plan):
        rule = next(r for needle, r in RULE_OF_MESSAGE if needle in msg)
        if rule == "unknown":
            subject = msg.split()[-1]  # "unknown work order W9"
        elif rule == "assigned twice":
            subject = msg.split()[0]  # "W1 assigned more than once"
        else:
            subject = msg.split(":")[0]  # "W1: ..." or "T1: ..."
        out.add((rule, subject))
    return out


SKILLS = ["hvac", "elec", "plumb"]
RARE = "gas"  # required by some work orders, held by no technician
TERRITORIES = ["north", "south", "east"]


@st.composite
def metric_problems(draw) -> Problem:
    """The original generator's shape: plane points, ceil-Euclidean travel."""
    n = draw(st.integers(2, 8))
    pts = draw(st.lists(st.tuples(st.integers(0, 100), st.integers(0, 100)), min_size=n, max_size=n))
    travel = tuple(tuple(math.ceil(math.dist(p, q)) for q in pts) for p in pts)
    techs = []
    for i in range(draw(st.integers(1, 4))):
        start = draw(st.integers(360, 600))
        techs.append(Technician(
            id=f"T{i}",
            skills=frozenset(draw(st.lists(st.sampled_from(SKILLS), max_size=3, unique=True))),
            territories=frozenset(draw(st.lists(st.sampled_from(TERRITORIES[:2]), min_size=1, max_size=2, unique=True))),
            shift_start=start, shift_end=start + draw(st.integers(240, 600)), home=draw(st.integers(0, n - 1)),
            max_daily_minutes=draw(st.integers(240, 600)), overtime_minutes=draw(st.sampled_from([0, 60, 120]))))
    wos = []
    for j in range(draw(st.integers(0, 10))):
        ws = we = None
        if draw(st.booleans()):
            ws = draw(st.integers(360, 900))
            we = ws + draw(st.integers(60, 480))
        wos.append(WorkOrder(
            id=f"W{j}", territory=draw(st.sampled_from(TERRITORIES[:2])),
            skills=frozenset(draw(st.lists(st.sampled_from(SKILLS), max_size=2, unique=True))),
            duration=draw(st.integers(15, 180)), location=draw(st.integers(0, n - 1)), priority=draw(st.integers(1, 4)),
            window_start=ws, window_end=we))
    return Problem(technicians=tuple(techs), work_orders=tuple(wos), travel=travel)


@st.composite
def tight_problems(draw) -> Problem:
    """Adversarial: windows barely longer than the job (sometimes shorter), one-sided
    windows, a skill no technician holds, asymmetric travel, any buffer."""
    n = draw(st.integers(1, 10))
    travel = tuple(tuple(0 if i == j else draw(st.integers(1, 90)) for j in range(n)) for i in range(n))
    techs = []
    for i in range(draw(st.integers(1, 6))):
        start = draw(st.integers(300, 660))
        techs.append(Technician(
            id=f"T{i}",
            skills=frozenset(draw(st.lists(st.sampled_from(SKILLS), max_size=3, unique=True))),
            territories=frozenset(draw(st.lists(st.sampled_from(TERRITORIES), min_size=1, max_size=3, unique=True))),
            shift_start=start, shift_end=start + draw(st.integers(60, 720)), home=draw(st.integers(0, n - 1)),
            max_daily_minutes=draw(st.integers(30, 600)), overtime_minutes=draw(st.sampled_from([0, 0, 30, 120]))))
    wos = []
    for j in range(draw(st.integers(0, 30))):
        duration = draw(st.integers(5, 240))
        kind = draw(st.sampled_from(["none", "tight", "start-only", "end-only", "too-short"]))
        ws = we = None
        if kind in ("tight", "start-only", "too-short"):
            ws = draw(st.integers(300, 1200))
        if kind == "tight":
            we = ws + duration + draw(st.integers(0, 30))
        elif kind == "too-short":
            we = ws + draw(st.integers(0, duration - 1)) if duration > 1 else ws
        elif kind == "end-only":
            we = draw(st.integers(300, 1400))
        skills = draw(st.lists(st.sampled_from(SKILLS + [RARE]), max_size=3, unique=True))
        wos.append(WorkOrder(
            id=f"W{j}", territory=draw(st.sampled_from(TERRITORIES)), skills=frozenset(skills), duration=duration,
            location=draw(st.integers(0, n - 1)), priority=draw(st.integers(1, 4)), window_start=ws, window_end=we))
    return Problem(technicians=tuple(techs), work_orders=tuple(wos), travel=travel,
                   buffer_minutes=draw(st.integers(0, 15)))


def check_feasible_partition(problem: Problem) -> tuple[list[Assignment], list[str]]:
    plan, unassigned = solve_greedy(problem)
    assert oracle(problem, plan) == set()
    assert sorted([a.work_order_id for a in plan] + unassigned) == sorted(w.id for w in problem.work_orders)
    return plan, unassigned


@PROOF
@given(metric_problems())
def test_greedy_plans_pass_the_independent_oracle(problem: Problem):
    check_feasible_partition(problem)


@PROOF
@given(tight_problems())
def test_greedy_plans_pass_the_oracle_on_adversarial_problems(problem: Problem):
    plan, unassigned = check_feasible_partition(problem)
    wos = {w.id: w for w in problem.work_orders}
    # The skill nobody holds is never served; a window shorter than the job never is.
    for a in plan:
        w = wos[a.work_order_id]
        assert RARE not in w.skills
        assert w.window_start is None or w.window_end is None or w.window_end - w.window_start >= w.duration


def earliest_alone(problem: Problem, t: Technician, w: WorkOrder) -> Assignment:
    start = max(t.shift_start + problem.travel[t.home][w.location] + problem.buffer_minutes, w.window_start or 0)
    return Assignment(w.id, t.id, start, start + w.duration, 0)


@PROOF
@given(tight_problems())
def test_nothing_unassigned_that_an_idle_technician_could_serve(problem: Problem):
    plan, unassigned = solve_greedy(problem)
    busy = {a.technician_id for a in plan}
    wos = {w.id: w for w in problem.work_orders}
    for wid in unassigned:
        for t in problem.technicians:
            if t.id not in busy:
                assert oracle(problem, [earliest_alone(problem, t, wos[wid])]) != set(), (wid, t.id)


@st.composite
def mutated_plans(draw):
    problem = draw(tight_problems())
    plan, unassigned = solve_greedy(problem)
    plan = list(plan)
    tech_ids = [t.id for t in problem.technicians]
    for _ in range(draw(st.integers(1, 4))):
        op = draw(st.sampled_from(["shift", "stretch", "retech", "resequence", "duplicate", "drop", "invent", "unassigned"]))
        if op in ("shift", "stretch", "retech", "resequence", "duplicate", "drop") and not plan:
            op = "invent"
        if op == "unassigned" and not unassigned:
            op = "invent"
        if op == "invent":
            plan.append(Assignment(draw(st.sampled_from(["ghost", "W0"])), draw(st.sampled_from(tech_ids + ["nobody"])),
                                   draw(st.integers(0, 1440)), draw(st.integers(0, 1440)), draw(st.integers(0, 40))))
            continue
        if op == "unassigned":
            wid = draw(st.sampled_from(unassigned))
            start = draw(st.integers(0, 1400))
            dur = next(w.duration for w in problem.work_orders if w.id == wid)
            plan.append(Assignment(wid, draw(st.sampled_from(tech_ids)), start, start + dur, draw(st.integers(0, 40))))
            continue
        i = draw(st.integers(0, len(plan) - 1))
        a = plan[i]
        if op == "shift":
            d = draw(st.integers(-120, 120))
            plan[i] = Assignment(a.work_order_id, a.technician_id, a.start + d, a.end + d, a.sequence)
        elif op == "stretch":
            plan[i] = Assignment(a.work_order_id, a.technician_id, a.start, a.end + draw(st.integers(-30, 30)), a.sequence)
        elif op == "retech":
            plan[i] = Assignment(a.work_order_id, draw(st.sampled_from(tech_ids)), a.start, a.end, a.sequence)
        elif op == "resequence":
            plan[i] = Assignment(a.work_order_id, a.technician_id, a.start, a.end, draw(st.integers(0, 40)))
        elif op == "duplicate":
            plan.append(a)
        elif op == "drop":
            plan.pop(i)
    return problem, plan


@PROOF
@given(mutated_plans())
def test_checker_agrees_with_the_oracle_on_mutated_plans(case):
    problem, plan = case
    assert engine_rules(problem, plan) == oracle(problem, plan)


# Hand-worked greedy scenarios. Travel between locations 0, 1, 2 is 10, 30 and 20
# minutes; the buffer is 5; shifts are 08:00-17:00 (480-1020).
TRAVEL = ((0, 10, 30), (10, 0, 20), (30, 20, 0))


def T(id="T1", skills=("hvac",), max_daily=480, overtime=0, territories=("north",)):
    return Technician(id, frozenset(skills), frozenset(territories), 480, 1020, 0, max_daily, overtime)


def W(id="W1", skills=("hvac",), duration=60, location=1, priority=4, window=(None, None), territory="north"):
    return WorkOrder(id, territory, frozenset(skills), duration, location, priority, window[0], window[1])


def test_hand_skill_routes_work_to_the_qualified_technician():
    # W1 (elec, priority 1) can only go to T2: 480 + 30 + 5 = 515. W2 (hvac) to T1: 480 + 10 + 5 = 495.
    p = Problem((T("T1"), T("T2", skills=("elec",))), (W("W1", ("elec",), location=2, priority=1), W("W2", priority=2)), TRAVEL)
    plan, unassigned = solve_greedy(p)
    assert sorted(plan, key=lambda a: a.work_order_id) == [Assignment("W1", "T2", 515, 575, 0), Assignment("W2", "T1", 495, 555, 0)]
    assert unassigned == []


def test_hand_window_start_makes_the_technician_wait():
    p = Problem((T(),), (W(window=(600, 700)),), TRAVEL)
    assert solve_greedy(p) == ([Assignment("W1", "T1", 600, 660, 0)], [])


def test_hand_window_too_early_for_the_drive_is_unassigned():
    # Earliest arrival 495 + 60 = 555 is past the window end 540.
    p = Problem((T(),), (W(window=(480, 540)),), TRAVEL)
    assert solve_greedy(p) == ([], ["W1"])


def test_hand_overtime_allowance_admits_the_job():
    # Load 10 + 90 + 10 back = 110 minutes.
    job = W(duration=90)
    assert solve_greedy(Problem((T(max_daily=100),), (job,), TRAVEL)) == ([], ["W1"])
    assert solve_greedy(Problem((T(max_daily=100, overtime=10),), (job,), TRAVEL)) == ([Assignment("W1", "T1", 495, 585, 0)], [])


def test_hand_territory_outside_the_technician_is_unassigned():
    assert solve_greedy(Problem((T(),), (W(territory="south"),), TRAVEL)) == ([], ["W1"])


def test_hand_second_stop_waits_for_travel_and_buffer():
    # W1 495-555 at location 1; to location 2 is 20 + 5, so W2 starts 580. Load 10+60+20+30+30 = 150.
    p = Problem((T(),), (W("W1", priority=1), W("W2", duration=30, location=2, priority=2)), TRAVEL)
    assert solve_greedy(p) == ([Assignment("W1", "T1", 495, 555, 0), Assignment("W2", "T1", 580, 610, 1)], [])

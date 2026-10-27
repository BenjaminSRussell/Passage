"""Table-driven checks of each BR-45.3 hard constraint."""

import pytest

from passage_dispatch import Assignment, Problem, Technician, WorkOrder, plan_violations

TRAVEL = ((0, 10), (10, 0))  # two locations, 10 minutes apart


def tech(**over) -> Technician:
    base = dict(
        id="T1",
        skills=frozenset({"hvac"}),
        territories=frozenset({"north"}),
        shift_start=480,
        shift_end=1020,
        home=0,
        max_daily_minutes=480,
        overtime_minutes=0,
    )
    base.update(over)
    return Technician(**base)


def wo(**over) -> WorkOrder:
    base = dict(
        id="W1",
        territory="north",
        skills=frozenset({"hvac"}),
        duration=60,
        location=1,
    )
    base.update(over)
    return WorkOrder(**base)


def problem(t: Technician, w: WorkOrder) -> Problem:
    return Problem(technicians=(t,), work_orders=(w,), travel=TRAVEL)


# First legal start: shift start + travel from home (10) + buffer (5) = 495.
OK = Assignment("W1", "T1", 495, 555, 0)


@pytest.mark.parametrize(
    ("name", "t", "w", "a", "rule"),
    [
        ("feasible", tech(), wo(), OK, None),
        ("missing skill", tech(skills=frozenset()), wo(), OK, "lacks skills"),
        ("wrong territory", tech(), wo(territory="south"), OK, "outside territory"),
        ("before shift", tech(shift_start=500), wo(), OK, "outside shift"),
        ("after shift", tech(shift_end=550), wo(), OK, "outside shift"),
        ("before window", tech(), wo(window_start=600, window_end=700), OK, "before time window"),
        ("after window", tech(), wo(window_end=550), OK, "after time window"),
        ("no travel buffer", tech(), wo(), Assignment("W1", "T1", 490, 550, 0), "travel from previous"),
        ("wrong duration", tech(), wo(), Assignment("W1", "T1", 495, 500, 0), "duration"),
        ("over daily limit", tech(max_daily_minutes=50), wo(), OK, "daily load"),
        ("overtime allowance", tech(max_daily_minutes=100, overtime_minutes=120), wo(), OK, None),
    ],
)
def test_single_assignment_rules(name, t, w, a, rule):
    errors = plan_violations(problem(t, w), [a])
    if rule is None:
        assert errors == [], name
    else:
        assert any(rule in e for e in errors), (name, errors)


def test_double_assignment_is_reported():
    p = Problem(technicians=(tech(),), work_orders=(wo(),), travel=TRAVEL)
    errors = plan_violations(p, [OK, Assignment("W1", "T1", 600, 660, 1)])
    assert any("more than once" in e for e in errors)


def test_unknown_ids_are_reported():
    p = problem(tech(), wo())
    errors = plan_violations(p, [Assignment("nope", "T1", 495, 555, 0), Assignment("W1", "ghost", 495, 555, 0)])
    assert any("unknown work order" in e for e in errors)
    assert any("unknown technician" in e for e in errors)


def test_duplicate_sequence_is_reported():
    w2 = wo(id="W2")
    p = Problem(technicians=(tech(),), work_orders=(wo(), w2), travel=TRAVEL)
    errors = plan_violations(p, [OK, Assignment("W2", "T1", 700, 760, 0)])
    assert any("duplicate sequence" in e for e in errors)


def test_consecutive_stops_need_travel_and_buffer():
    w2 = wo(id="W2", location=0)
    p = Problem(technicians=(tech(),), work_orders=(wo(), w2), travel=TRAVEL)
    # W1 ends at 555; travel 1->0 is 10 and buffer 5, so W2 cannot start before 570.
    too_soon = Assignment("W2", "T1", 560, 620, 1)
    on_time = Assignment("W2", "T1", 570, 630, 1)
    assert any("previous stop" in e for e in plan_violations(p, [OK, too_soon]))
    assert plan_violations(p, [OK, on_time]) == []


def test_problem_rejects_non_square_matrix():
    with pytest.raises(ValueError):
        Problem(technicians=(tech(),), work_orders=(wo(),), travel=((0, 1), (1,)))

"""Job-shop model, OR-Library parser and CP-SAT solver (FR-23.6, BR-23.9).

The model is the classic job shop: each job is an ordered list of operations,
each operation runs on one machine for a fixed number of minutes, and a machine
runs at most one operation at a time. The objective is

    sum_j weight_j * max(0, end_j - due_j)  +  lambda * makespan

(jobs without a due date contribute no lateness term)

with lambda = lambda_tenths / 10 (default 0.1, BR-23.9). OR-Tools is imported
lazily so the pure helpers (parser, validator) work without it.
"""

from __future__ import annotations

from dataclasses import dataclass, field

# Statuses reported by CP-SAT, by name. The Go scheduler accepts FEASIBLE and
# OPTIMAL and keeps its heuristic schedule on UNKNOWN (BR-23.9).
STATUS_OPTIMAL = "OPTIMAL"
STATUS_FEASIBLE = "FEASIBLE"
STATUS_UNKNOWN = "UNKNOWN"
STATUS_INFEASIBLE = "INFEASIBLE"

DEFAULT_TIME_LIMIT_S = 60.0
DEFAULT_LAMBDA_TENTHS = 1  # lambda = 0.1
DEFAULT_WORKERS = 8
MAX_OPERATIONS = 2000  # FR-23.6 window size


@dataclass(frozen=True)
class Operation:
    machine: int
    minutes: int


@dataclass
class Job:
    """An ordered chain of operations. due is the due minute (None: no due
    date, so the job adds no lateness and only the makespan term applies)."""

    operations: list[Operation]
    due: int | None = None
    weight: int = 1


@dataclass
class Instance:
    machines: int
    jobs: list[Job] = field(default_factory=list)

    @property
    def operation_count(self) -> int:
        return sum(len(j.operations) for j in self.jobs)


@dataclass
class Solution:
    status: str
    starts: list[list[int]]
    makespan: int
    total_lateness: int
    objective: float | None


def parse_orlib(text: str) -> Instance:
    """Parse an OR-Library job-shop file.

    The first data line is "jobs machines [optimum]". Each job line holds
    machine/minutes pairs with machines numbered from 0. Lines starting with
    '#' and blank lines are ignored.
    """
    rows = [ln.strip() for ln in text.splitlines() if ln.strip() and not ln.lstrip().startswith("#")]
    if not rows:
        raise ValueError("empty instance")
    header = rows[0].split()
    if len(header) < 2:
        raise ValueError("header must start with 'jobs machines'")
    n_jobs, n_machines = int(header[0]), int(header[1])
    if len(rows) < n_jobs + 1:
        raise ValueError(f"expected {n_jobs} job lines, found {len(rows) - 1}")
    jobs: list[Job] = []
    for row in rows[1 : n_jobs + 1]:
        nums = [int(x) for x in row.split()]
        if len(nums) != 2 * n_machines:
            raise ValueError(f"job line needs {2 * n_machines} numbers, has {len(nums)}")
        ops = [Operation(machine=nums[i], minutes=nums[i + 1]) for i in range(0, len(nums), 2)]
        jobs.append(Job(operations=ops))
    inst = Instance(machines=n_machines, jobs=jobs)
    problems = validate_instance(inst)
    if problems:
        raise ValueError("; ".join(problems))
    return inst


def validate_instance(inst: Instance) -> list[str]:
    """Return human-readable problems with the instance (empty when valid)."""
    out: list[str] = []
    if inst.machines < 1:
        out.append("machines must be at least 1")
    for j, job in enumerate(inst.jobs):
        if not job.operations:
            out.append(f"job {j} has no operations")
        for k, op in enumerate(job.operations):
            if not 0 <= op.machine < inst.machines:
                out.append(f"job {j} operation {k} uses machine {op.machine} outside 0..{inst.machines - 1}")
            if op.minutes < 1:
                out.append(f"job {j} operation {k} must run at least 1 minute")
    return out


def check_schedule(inst: Instance, starts: list[list[int]]) -> list[str]:
    """Verify precedence and machine exclusivity of a schedule.

    Returns the list of violations; an empty list means the schedule is feasible.
    """
    problems: list[str] = []
    if len(starts) != len(inst.jobs):
        return ["starts must have one row per job"]
    per_machine: dict[int, list[tuple[int, int, str]]] = {m: [] for m in range(inst.machines)}
    for j, job in enumerate(inst.jobs):
        if len(starts[j]) != len(job.operations):
            problems.append(f"job {j} has wrong number of starts")
            continue
        prev_end = 0
        for k, op in enumerate(job.operations):
            s = starts[j][k]
            if s < 0:
                problems.append(f"job {j} operation {k} starts before time 0")
            if s < prev_end:
                problems.append(f"job {j} operation {k} starts before its predecessor ends")
            prev_end = s + op.minutes
            per_machine[op.machine].append((s, s + op.minutes, f"{j}/{k}"))
    for m, spans in per_machine.items():
        spans.sort()
        for (s1, e1, a), (s2, _, b) in zip(spans, spans[1:]):
            if s2 < e1:
                problems.append(f"machine {m}: {a} overlaps {b}")
    return problems


def solve(
    inst: Instance,
    time_limit_s: float = DEFAULT_TIME_LIMIT_S,
    lambda_tenths: int = DEFAULT_LAMBDA_TENTHS,
    workers: int = DEFAULT_WORKERS,
) -> Solution:
    """Solve the instance with CP-SAT and return starts plus solver status."""
    from ortools.sat.python import cp_model  # local import: optional dependency

    if inst.operation_count > MAX_OPERATIONS:
        raise ValueError(f"instance has {inst.operation_count} operations; the window limit is {MAX_OPERATIONS}")
    if validate_instance(inst):
        raise ValueError("; ".join(validate_instance(inst)))

    horizon = sum(op.minutes for job in inst.jobs for op in job.operations)
    model = cp_model.CpModel()
    starts: dict[tuple[int, int], object] = {}
    ends: dict[tuple[int, int], object] = {}
    machine_intervals: dict[int, list[object]] = {m: [] for m in range(inst.machines)}

    for j, job in enumerate(inst.jobs):
        for k, op in enumerate(job.operations):
            s = model.NewIntVar(0, horizon, f"s_{j}_{k}")
            e = model.NewIntVar(0, horizon, f"e_{j}_{k}")
            machine_intervals[op.machine].append(model.NewIntervalVar(s, op.minutes, e, f"i_{j}_{k}"))
            starts[j, k] = s
            ends[j, k] = e
            if k > 0:
                model.Add(s >= ends[j, k - 1])

    for intervals in machine_intervals.values():
        if intervals:
            model.AddNoOverlap(intervals)

    last_ends = [ends[j, len(job.operations) - 1] for j, job in enumerate(inst.jobs) if job.operations]
    makespan = model.NewIntVar(0, horizon, "makespan")
    if last_ends:
        model.AddMaxEquality(makespan, last_ends)
    else:
        model.Add(makespan == 0)

    tardiness_terms = []
    for j, job in enumerate(inst.jobs):
        if not job.operations or job.weight == 0 or job.due is None:
            continue
        tard = model.NewIntVar(0, horizon, f"t_{j}")
        model.Add(tard >= ends[j, len(job.operations) - 1] - job.due)
        tardiness_terms.append(job.weight * tard)

    # Scale by 10 so lambda = lambda_tenths / 10 stays integral.
    model.Minimize(10 * sum(tardiness_terms) + lambda_tenths * makespan)

    solver = cp_model.CpSolver()
    solver.parameters.max_time_in_seconds = float(time_limit_s)
    solver.parameters.num_workers = int(workers)
    status = solver.Solve(model)
    name = solver.StatusName(status)
    if status not in (cp_model.OPTIMAL, cp_model.FEASIBLE):
        return Solution(status=name, starts=[], makespan=0, total_lateness=0, objective=None)

    out_starts = [
        [int(solver.Value(starts[j, k])) for k in range(len(job.operations))] for j, job in enumerate(inst.jobs)
    ]
    total_late = 0
    for j, job in enumerate(inst.jobs):
        if job.operations and job.due is not None:
            total_late += max(0, int(solver.Value(ends[j, len(job.operations) - 1])) - job.due)
    return Solution(
        status=name,
        starts=out_starts,
        makespan=int(solver.Value(makespan)),
        total_lateness=total_late,
        objective=float(solver.ObjectiveValue()),
    )

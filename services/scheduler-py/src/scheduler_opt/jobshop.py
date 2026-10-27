"""Job-shop model, OR-Library parser and CP-SAT solver (FR-23.6, BR-23.9).

The model is the classic job shop: each job is an ordered list of operations,
each operation runs on one machine for a fixed number of minutes, and a machine
runs at most one operation at a time. The objective is

    sum_j weight_j * max(0, end_j - due_j)  +  lambda * makespan

(jobs without a due date contribute no lateness term)

with lambda = lambda_tenths / 10 (default 0.1, BR-23.9). OR-Tools is imported
lazily so the pure helpers (parser, validator, checker) work without it.

Two extensions follow the Passage resource-network rules (FR-23.13, BR-23.11):

* an operation may list ``candidates``, the machines it may run on with its
  named ``machine`` first; exactly one is chosen;
* an operation may carry the ``network`` edges that govern the step from the
  previous operation of its job into it. The pair of machines the two
  operations run on must be an edge, and the next operation starts no earlier
  than the edge's transfer time and no later than its maximum wait after the
  previous one ends. Allowed pairs are encoded with ``AddAllowedAssignments``
  on the machine-choice variables of the two operations.
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
class Edge:
    """An allowed transition between the machines of consecutive operations."""

    from_machine: int
    to_machine: int
    transfer: int = 0
    max_wait: int | None = None


@dataclass(frozen=True)
class Operation:
    machine: int
    minutes: int
    # Machines the operation may run on, ``machine`` first; empty: ``machine`` only.
    candidates: tuple[int, ...] = ()
    # Edges governing the step from the job's previous operation into this
    # one; None leaves the step unconstrained.
    network: tuple[Edge, ...] | None = None


def options(op: Operation) -> tuple[int, ...]:
    """Machines an operation may run on."""
    return op.candidates or (op.machine,)


def allowed_pairs(job: Job, k: int) -> list[tuple[int, int, Edge]]:
    """Choice index pairs (a, b) for operations k and k+1 that an edge of
    operation k+1's network allows, with the edge."""
    net = job.operations[k + 1].network
    if net is None:
        return []
    a_opts, b_opts = options(job.operations[k]), options(job.operations[k + 1])
    edges = {(e.from_machine, e.to_machine): e for e in net}
    return [
        (a, b, edges[a_opts[a], b_opts[b]])
        for a in range(len(a_opts))
        for b in range(len(b_opts))
        if (a_opts[a], b_opts[b]) in edges
    ]


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
    # Machine chosen for each operation (the fixed machine without candidates).
    machines: list[list[int]] = field(default_factory=list)


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
        before = len(out)
        if not job.operations:
            out.append(f"job {j} has no operations")
        for k, op in enumerate(job.operations):
            if not 0 <= op.machine < inst.machines:
                out.append(f"job {j} operation {k} uses machine {op.machine} outside 0..{inst.machines - 1}")
            if op.minutes < 1:
                out.append(f"job {j} operation {k} must run at least 1 minute")
            if op.candidates:
                if op.candidates[0] != op.machine:
                    out.append(f"job {j} operation {k} must list its machine {op.machine} first among its candidates")
                if len(set(op.candidates)) != len(op.candidates):
                    out.append(f"job {j} operation {k} lists a candidate machine twice")
                for m in op.candidates:
                    if not 0 <= m < inst.machines:
                        out.append(f"job {j} operation {k} lists candidate machine {m} outside 0..{inst.machines - 1}")
            if op.network is None:
                continue
            if k == 0:
                out.append(f"job {j} operation 0 has no previous operation for its network step")
                continue
            for e in op.network:
                if e.transfer < 0 or (e.max_wait is not None and e.max_wait < e.transfer):
                    out.append(
                        f"job {j} operation {k} edge {e.from_machine}->{e.to_machine} needs 0 <= transfer <= max wait"
                    )
            if not allowed_pairs(job, k - 1):
                out.append(f"job {j} has no allowed network transition between operations {k - 1} and {k}")
        if job.operations and len(out) == before and not _has_machine_path(job):
            out.append(f"job {j} has no chain of machines that every network step allows")
    return out


def _has_machine_path(job: Job) -> bool:
    """Whether some choice of machines satisfies every network step of the job."""
    reachable = set(range(len(options(job.operations[0]))))
    for k in range(len(job.operations) - 1):
        if job.operations[k + 1].network is None:
            reachable = set(range(len(options(job.operations[k + 1]))))
            continue
        reachable = {b for a, b, _ in allowed_pairs(job, k) if a in reachable}
        if not reachable:
            return False
    return True


def check_schedule(inst: Instance, starts: list[list[int]], machines: list[list[int]] | None = None) -> list[str]:
    """Verify precedence, machine exclusivity, machine choice and network steps.

    ``machines`` gives the machine chosen per operation; when omitted each
    operation uses its named machine. Returns the list of violations; an empty
    list means the schedule is feasible.
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
        prev_machine = -1
        for k, op in enumerate(job.operations):
            s = starts[j][k]
            m = machines[j][k] if machines is not None else op.machine
            if m not in options(op):
                problems.append(f"job {j} operation {k} runs on machine {m}, which is not one of its options")
            if s < 0:
                problems.append(f"job {j} operation {k} starts before time 0")
            if s < prev_end:
                problems.append(f"job {j} operation {k} starts before its predecessor ends")
            if op.network is not None and k > 0:
                edge = next((e for e in op.network if (e.from_machine, e.to_machine) == (prev_machine, m)), None)
                wait = s - prev_end
                if edge is None:
                    problems.append(f"job {j} moves from machine {prev_machine} to {m} outside the network")
                elif wait < edge.transfer or (edge.max_wait is not None and wait > edge.max_wait):
                    problems.append(
                        f"job {j} waits {wait} min before operation {k}; the edge allows {edge.transfer} to {edge.max_wait}"
                    )
            prev_end, prev_machine = s + op.minutes, m
            if m in per_machine:
                per_machine[m].append((s, s + op.minutes, f"{j}/{k}"))
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

    # Transfers can hold a job idle, so they widen the horizon.
    horizon = sum(op.minutes for job in inst.jobs for op in job.operations) + sum(
        max((e.transfer for e in op.network), default=0) for job in inst.jobs for op in job.operations if op.network
    )
    model = cp_model.CpModel()
    starts: dict[tuple[int, int], object] = {}
    ends: dict[tuple[int, int], object] = {}
    choices: dict[tuple[int, int], object] = {}
    presences: dict[tuple[int, int], list[object]] = {}
    machine_intervals: dict[int, list[object]] = {m: [] for m in range(inst.machines)}

    for j, job in enumerate(inst.jobs):
        for k, op in enumerate(job.operations):
            s = model.NewIntVar(0, horizon, f"s_{j}_{k}")
            e = model.NewIntVar(0, horizon, f"e_{j}_{k}")
            starts[j, k] = s
            ends[j, k] = e
            opts = options(op)
            in_network = op.network is not None or (k + 1 < len(job.operations) and job.operations[k + 1].network is not None)
            if len(opts) > 1 or in_network:
                # One optional interval per machine option; exactly one is present.
                choice = model.NewIntVar(0, len(opts) - 1, f"c_{j}_{k}")
                lits = []
                for i, m in enumerate(opts):
                    present = model.NewBoolVar(f"p_{j}_{k}_{i}")
                    machine_intervals[m].append(model.NewOptionalIntervalVar(s, op.minutes, e, present, f"i_{j}_{k}_{i}"))
                    model.Add(choice == i).OnlyEnforceIf(present)
                    lits.append(present)
                model.AddExactlyOne(lits)
                choices[j, k], presences[j, k] = choice, lits
            else:
                machine_intervals[op.machine].append(model.NewIntervalVar(s, op.minutes, e, f"i_{j}_{k}"))
            if k > 0:
                model.Add(s >= ends[j, k - 1])

    for j, job in enumerate(inst.jobs):
        for k in range(len(job.operations) - 1):
            if job.operations[k + 1].network is None:
                continue
            pairs = allowed_pairs(job, k)
            model.AddAllowedAssignments([choices[j, k], choices[j, k + 1]], [(a, b) for a, b, _ in pairs])
            for a, b, edge in pairs:
                both = [presences[j, k][a], presences[j, k + 1][b]]
                model.Add(starts[j, k + 1] >= ends[j, k] + edge.transfer).OnlyEnforceIf(both)
                if edge.max_wait is not None:
                    model.Add(starts[j, k + 1] <= ends[j, k] + edge.max_wait).OnlyEnforceIf(both)

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
    out_machines = [
        [
            options(op)[int(solver.Value(choices[j, k]))] if (j, k) in choices else op.machine
            for k, op in enumerate(job.operations)
        ]
        for j, job in enumerate(inst.jobs)
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
        machines=out_machines,
    )

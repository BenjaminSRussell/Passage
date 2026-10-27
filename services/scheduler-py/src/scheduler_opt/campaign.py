"""Campaign sequencing with CP-SAT AddCircuit (FR-23.14, BR-23.12).

A campaign runs its orders back to back on one resource. The sequence minimises
the total changeover minutes, starting from the resource's current setup group.

The changeover matrix has n+1 rows and columns. Index 0 is the resource's
current state; index i (1..n) is campaign order i-1. Entry [a][b] is the
changeover from a to b. Column 0 is never charged: the closing arc back to the
start is free, so only the changeovers between orders and the first changeover
from the current state count.

Limits: at most MAX_CAMPAIGN_ORDERS orders and a 30 s default time limit
(FR-23.14). The caller keeps its nearest-neighbour sequence (BR-23.12) when
the solver finds no solution.
"""

from __future__ import annotations

from dataclasses import dataclass

DEFAULT_CAMPAIGN_LIMIT_S = 30.0
MAX_CAMPAIGN_ORDERS = 50
MAX_CHANGEOVER_MINUTES = 10080  # one week, the scheduling service's cap


@dataclass(frozen=True)
class CampaignSequence:
    status: str
    # Zero-based indices of the campaign orders in run order; empty when no solution.
    sequence: list[int]
    changeover_minutes: int


def changeover_matrix(current: str, groups: list[str], table: dict[tuple[str, str], int]) -> list[list[int]]:
    """Build the n+1 changeover matrix from setup groups.

    ``table`` maps (from group, to group) to minutes. Same group costs zero,
    and column 0 (back to the current state) is never charged. A missing pair
    raises ValueError so that the caller sees the gap rather than a silent
    zero; the Go side falls back to the route setup and warns SCHED-0012.
    """
    labels = [current, *groups]
    out: list[list[int]] = []
    for a in labels:
        row: list[int] = []
        for j, b in enumerate(labels):
            if a == b or j == 0:
                row.append(0)
                continue
            if (a, b) not in table:
                raise ValueError(f"setup matrix has no entry {a} -> {b}")
            row.append(int(table[a, b]))
        out.append(row)
    return out


def validate_matrix(changeover: list[list[int]]) -> list[str]:
    """Return problems with a changeover matrix (empty when valid)."""
    n = len(changeover) - 1
    out: list[str] = []
    if n < 1:
        return ["campaign needs at least one order"]
    if n > MAX_CAMPAIGN_ORDERS:
        out.append(f"campaign has {n} orders; the limit is {MAX_CAMPAIGN_ORDERS}")
    for i, row in enumerate(changeover):
        if len(row) != n + 1:
            out.append(f"row {i} has {len(row)} entries; the matrix must be square")
            continue
        for j, v in enumerate(row):
            if not 0 <= v <= MAX_CHANGEOVER_MINUTES:
                out.append(f"entry [{i}][{j}] must be 0 to {MAX_CHANGEOVER_MINUTES} minutes")
    return out


def sequence_campaign(
    changeover: list[list[int]],
    time_limit_s: float = DEFAULT_CAMPAIGN_LIMIT_S,
    workers: int = 8,
) -> CampaignSequence:
    """Find the order sequence with minimum total changeover minutes."""
    from ortools.sat.python import cp_model  # local import: optional dependency

    problems = validate_matrix(changeover)
    if problems:
        raise ValueError("; ".join(problems))
    n = len(changeover) - 1

    model = cp_model.CpModel()
    arcs: list[tuple[int, int, object]] = []
    cost_terms = []
    lits: dict[tuple[int, int], object] = {}
    for a in range(n + 1):
        for b in range(n + 1):
            if a == b:
                continue
            lit = model.NewBoolVar(f"x_{a}_{b}")
            arcs.append((a, b, lit))
            lits[a, b] = lit
            if b != 0 and changeover[a][b]:
                cost_terms.append(changeover[a][b] * lit)
    # Every node, including the current-state start node 0, lies on the circuit.
    model.AddCircuit(arcs)
    model.Minimize(sum(cost_terms) if cost_terms else 0)

    solver = cp_model.CpSolver()
    solver.parameters.max_time_in_seconds = float(time_limit_s)
    solver.parameters.num_workers = int(workers)
    status = solver.Solve(model)
    name = solver.StatusName(status)
    if status not in (cp_model.OPTIMAL, cp_model.FEASIBLE):
        # UNKNOWN or INFEASIBLE: the caller keeps its nearest-neighbour sequence.
        return CampaignSequence(status=name, sequence=[], changeover_minutes=0)

    succ = {a: b for (a, b), lit in lits.items() if solver.Value(lit)}
    sequence: list[int] = []
    node = succ.get(0)
    while node is not None and node != 0 and len(sequence) < n:
        sequence.append(node - 1)
        node = succ.get(node)
    path = [0, *[i + 1 for i in sequence]]
    total = sum(changeover[a][b] for a, b in zip(path, path[1:]))
    return CampaignSequence(status=name, sequence=sequence, changeover_minutes=int(total))

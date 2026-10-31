"""Independent reference for ANSI/ASQ Z1.4 single sampling (Wave 33 proof, AC3).

Writes z14-tables.json next to this file: Table I (sample size code letters for
every inspection level) and, for Tables II-A (normal), II-B (tightened) and II-C
(reduced), the plan every code letter and AQL column resolves to after following
the table's arrows: sample size n, acceptance number Ac and rejection number Re.

The Go engine (z14.go) holds the master tables as a literal, cell-by-cell
transcription with their arrows. This script does not read it. It rebuilds each
master table from the table's own structure instead: in all three tables a cell
depends only on the diagonal d = letter index + AQL column index (letters A..R,
AQL 0.010..1000), with fixed Ac/Re sequences along the diagonals, and a handful
of edge cells where an arrow would leave the table. Those edge cells were read
off the master tables of MIL-STD-105D (public domain, whose single sampling
tables ANSI/ASQ Z1.4 reproduces) and are listed explicitly below. Two
independent constructions that agree on all 1,248 cells are the transcription
check the PRD asks for (z14-tables.json).

Run: python3 -I z14_reference.py   (standard library only; deterministic output)
"""

import json
import os

LETTERS = "ABCDEFGHJKLMNPQR"  # Table I letters; S exists only in Table II-B
AQLS = ["0.010", "0.015", "0.025", "0.040", "0.065", "0.10", "0.15", "0.25", "0.40", "0.65",
        "1.0", "1.5", "2.5", "4.0", "6.5", "10", "15", "25", "40", "65",
        "100", "150", "250", "400", "650", "1000"]

# Table I: upper lot bound of each band (None = and over) and the letter per level.
BANDS = [8, 15, 25, 50, 90, 150, 280, 500, 1200, 3200, 10000, 35000, 150000, 500000, None]
TABLE_I = {
    "S-1": "AAAABBBBCCCCDDD",
    "S-2": "AAABBBCCCDDDEEE",
    "S-3": "AABBCCDDEEFFGGH",
    "S-4": "AABCCDEEFGGHJJK",
    "I":   "AABCCDEFGHJKLMN",
    "II":  "ABCDEFGHJKLMNPQ",
    "III": "BCDEFGHJKLMNPQR",
}

SIZE_NORMAL = [2, 3, 5, 8, 13, 20, 32, 50, 80, 125, 200, 315, 500, 800, 1250, 2000, 3150]
SIZE_REDUCED = [2, 2, 2, 3, 5, 8, 13, 20, 32, 50, 80, 125, 200, 315, 500, 800]

DOWN, UP = "v", "^"


def normal_cell(r, d):
    """Table II-A: 0/1 on d=14, then up, down, and 1/2 .. 21/22 on d=17..24.
    Letters A..E (r <= 4) continue with 30/31 and 44/45 on d=25 and 26."""
    seq = {14: (0, 1), 17: (1, 2), 18: (2, 3), 19: (3, 4), 20: (5, 6), 21: (7, 8),
           22: (10, 11), 23: (14, 15), 24: (21, 22)}
    if r <= 4:
        seq.update({25: (30, 31), 26: (44, 45)})
    if d in seq:
        return seq[d]
    if d <= 13 or d == 16:
        return DOWN
    return UP  # d == 15, and everything past the last plan of the row


def tightened_cell(r, d):
    """Table II-B: 0/1 on d=15, two down arrows, then 1/2 .. 18/19 on d=18..24.
    Letters A..E continue with 27/28 and 41/42 on d=25 and 26."""
    seq = {15: (0, 1), 18: (1, 2), 19: (2, 3), 20: (3, 4), 21: (5, 6), 22: (8, 9),
           23: (12, 13), 24: (18, 19)}
    if r <= 4:
        seq.update({25: (27, 28), 26: (41, 42)})
    if d in seq:
        return seq[d]
    if d <= 14 or d in (16, 17):
        return DOWN
    return UP


def reduced_cell(r, d):
    """Table II-C from letter C down: 0/1 on d=14, up, down, then 0/2, 1/3, 1/4,
    2/5, 3/6, 5/8, 7/10, 10/13 on d=17..24; C..E continue with 14/17 and 21/24.
    Letters A and B (sample size 2, like C) carry their own sequences."""
    if r == 0:
        return normal_cell(0, d)  # identical to Table II-A row A
    if r == 1:
        seq = {14: (0, 1), 17: (0, 2), 18: (1, 3), 19: (2, 4), 20: (3, 5), 21: (5, 6),
               22: (7, 8), 23: (10, 11), 24: (14, 15), 25: (21, 22), 26: (30, 31)}
    else:
        seq = {14: (0, 1), 17: (0, 2), 18: (1, 3), 19: (1, 4), 20: (2, 5), 21: (3, 6),
               22: (5, 8), 23: (7, 10), 24: (10, 13)}
        if r <= 4:
            seq.update({25: (14, 17), 26: (21, 24)})
    if d in seq:
        return seq[d]
    if d <= 13 or d == 16:
        return DOWN
    return UP


# Edge cells read off the master tables, where the diagonal rule would point
# out of the table: the top row has no plan above, the bottom row none below.
EXCEPTIONS = {
    # (table, letter, AQL): cell
    ("NORMAL", "A", "10"): DOWN,      # d=15 would be an up arrow
    ("NORMAL", "R", "0.015"): UP,     # d=16 would be a down arrow
    ("TIGHTENED", "A", "10"): DOWN,   # d=15 would be 0/1; row A has no 0/1 plan in II-B
    ("TIGHTENED", "R", "0.015"): UP,  # d=16 would be a down arrow
    ("REDUCED", "A", "10"): DOWN,     # d=15 would be an up arrow
    ("REDUCED", "R", "0.015"): UP,    # d=16 would be a down arrow
}


def build(table, cell_fn, rows):
    grid = []
    for r, letter in enumerate(rows):
        row = []
        for j, aql in enumerate(AQLS):
            row.append(EXCEPTIONS.get((table, letter, aql), cell_fn(r, r + j)))
        grid.append(row)
    return grid


def tightened_grid():
    grid = build("TIGHTENED", tightened_cell, LETTERS)
    # Row S (3150) of Table II-B holds one plan, 1/2 at AQL 0.025, reached only
    # through the down arrows of Q and R in that column.
    grid.append([(1, 2) if aql == "0.025" else None for aql in AQLS])
    return grid


def resolve(grid, sizes, r, j):
    i = r
    step = None
    while True:
        cell = grid[i][j]
        if isinstance(cell, tuple):
            ac, re = cell
            return [sizes[i], ac, re]
        if cell is None:
            raise ValueError("arrow lands on a blank cell")
        if step is None:
            step = 1 if cell == DOWN else -1
        elif (cell == DOWN) != (step == 1):
            raise ValueError("arrows reverse")
        i += step


def main():
    grids = {
        "NORMAL": (build("NORMAL", normal_cell, LETTERS), SIZE_NORMAL),
        "TIGHTENED": (tightened_grid(), SIZE_NORMAL),
        "REDUCED": (build("REDUCED", reduced_cell, LETTERS), SIZE_REDUCED),
    }
    plans = {}
    for sev, (grid, sizes) in grids.items():
        plans[sev] = {letter: [resolve(grid, sizes, r, j) for j in range(len(AQLS))]
                      for r, letter in enumerate(LETTERS)}
    out = {
        "source": "ANSI/ASQ Z1.4 single sampling = MIL-STD-105D Tables I, II-A, II-B, II-C; "
                  "built by z14_reference.py (python3 -I), not by the engine",
        "aqls": AQLS,
        "bands": BANDS,
        "tableI": {lvl: list(s) for lvl, s in TABLE_I.items()},
        "plans": plans,
    }
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "z14-tables.json")
    with open(path, "w", encoding="utf-8") as f:
        json.dump(out, f, indent=1, sort_keys=True)
        f.write("\n")


if __name__ == "__main__":
    main()

# ADR-0002: frePPLe comparison for master planning (wave 22, AC4)

**Status:** Proposed — 2026-11-04 (awaiting a decision by the deciders; nothing in the code depends on it)
**Deciders:** Ben Russell, Zach Demanche
**Issues:** [#22](https://github.com/BenjaminSRussell/Passage/issues/22) (AC4), [#479](https://github.com/BenjaminSRussell/Passage/issues/479)

## Context

Wave 22's fourth proof reads: "Optional comparison: results match frePPLe on a shared dataset within tolerance."
The phase-4 PRD names a `frepple-compare` fixture (a shared dataset exported to frePPLe, expected within ±1 day
and ±1% quantity) and lists the frePPLe comparison harness as a risk mitigation for a two-person team
(`docs/prd/phase-4-manufacturing.md`). Nothing of it is built (`services/planning/NOT-IMPLEMENTED.md`, AC4).

What already stands in for an external reference:

- `testdata/mrp-textbook`: 42 CPIM-style scenarios whose expected planned orders come from an independent
  bucketed MRP (`reference_mrp.py`) checked against hand-derived plans; `TestRun_TextbookFixtures` matches all 42.
- Property tests on pegging and the net requirements view, and the `mrp-50k` determinism test (AC2, AC-22.3).

frePPLe is open source (AGPL-3.0 community edition, Python/C++, Django and its own Postgres schema). Its planning
semantics differ from Passage's AX-style engine: it is built around constrained planning (capacity, material and
lead time), uses its own buffer, operation and demand-priority model, and has no direct equivalent of AX coverage codes, time fences or
action/futures messages. A comparison would therefore need a mapping layer and an unconstrained, lot-for-lot
subset of the data before "within tolerance" means anything.

## Options

1. **Build the comparison harness (as the PRD suggests).** Export a shared dataset (a subset of `mrp-textbook`
   or `mrp-50k`) to frePPLe's input format, run frePPLe in CI (container), and diff planned orders within ±1 day
   and ±1% quantity.
   - For: an independent, industry-used engine as a second opinion; closes AC4 as written.
   - Against: a frePPLe image, its Postgres and a translator to maintain in CI; the semantic gap means the
     comparable subset is roughly the lot-for-lot textbook cases the fixtures already cover; AGPL code in the
     test toolchain needs a licence check even if it never ships.
2. **Drop AC4 from the wave's Definition of Done and keep the native planner as the only engine.** Record the
   decision on #22, mark AC4 "won't do" there and in `NOT-IMPLEMENTED.md`, and rely on the textbook fixtures and
   property tests as the correctness reference.
   - For: no new toolchain; the proof that matters (correct plans) is already carried by an independent
     reference implementation; the AC is marked optional in the issue.
   - Against: no comparison with a production-grade external planner; a customer or auditor asking "does it
     match a known engine?" gets the fixtures, not frePPLe.
3. **Defer: keep AC4 open as optional, outside the DoD, and revisit with real customer data.** A one-off manual
   comparison on a customer's dataset when one is available, not a CI harness.
   - For: keeps the door open at no current cost.
   - Against: an open, optional box on #22 that no one owns; the DoD stays ambiguous.

## Recommendation

Option 2, as #479 proposes: drop the frePPLe comparison from wave 22's Definition of Done, keep the native
planner, and treat `mrp-textbook` (independent reference MRP plus hand-derived plans) as the correctness proof.
If a customer later asks for an external comparison, do it once, by hand, on their data (option 3's manual run),
rather than maintaining a frePPLe harness in CI.

## Consequences (if accepted)

- #22: AC4 is checked off as "dropped by ADR-0002", not as passed.
- `services/planning/NOT-IMPLEMENTED.md`: the AC4 and "frePPLe comparison" rows change from "later slice" to
  "dropped (ADR-0002)".
- The `frepple-compare` fixture in the phase-4 PRD test plan is no longer expected.

Until a decider accepts or rejects this ADR, AC4 stays open on #22 and the NOT-IMPLEMENTED rows stay as they are.

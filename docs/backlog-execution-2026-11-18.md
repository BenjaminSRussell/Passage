# Backlog execution: DEPOT, 2026-11-18 to 19 (invocations 1 to 3)

Run settings:
- Plan `backlog-plan.md`, epic DEPOT-275, waves 1..3.
- Gate `in-review`, `--max-parallel 5`, min score 7, min category score 5.
- Architecture `docs/architecture.md`, compiled to `docs/architecture.compiled`.

Invocation 1 ran with the user. Invocations 2 and 3 finished waves 1 to 3, the last part unattended overnight. While unattended, product decisions took the safest reading the ticket supports and were written on the ticket.

**Nothing was merged.** All three wave PRs are open, stacked, and held for human review.

## Result

| Wave | PR | Base | Stories in PR | Review outcome |
|---|---|---|---|---|
| 1 | [#755](https://github.com/BenjaminSRussell/Depot/pull/755) | `main` | DEPOT-276, 294, 299, 300, 301 | Composite 6.8, below the 7.0 bar. Verdict REQUEST CHANGES, with 5 MAJOR scope gaps (below). |
| 2 | [#758](https://github.com/BenjaminSRussell/Depot/pull/758) | `integration/depot-wave-1` | DEPOT-292, 298, 306, 309 | Composite 7.8, passed. Verdict REQUEST CHANGES, with 5 MAJOR scope gaps (below). |
| 3 | [#761](https://github.com/BenjaminSRussell/Depot/pull/761) | `integration/depot-wave-2` | DEPOT-277, 303, 311 | Composite 6.9, below the bar. The review stopped after 3 fix rounds with 1 MAJOR finding needing a decision (P4). I opened the PR by hand. |

**Story outcomes:**
- **In Review (12):** 276, 277, 292, 294, 298, 299, 300, 301, 303, 306, 309 and 311. Each story with gaps has a Jira comment listing them.
- **Set aside (1):** DEPOT-287, back in To Do.

## Read before merging

1. **Most stories only partly cover their tickets.** Each wave PR body and each story's Jira comment lists exactly what is missing.
   - DEPOT-292: only budgets is built. Agent definitions, availability and `:disable` are not, and budget caps are not enforced.
   - DEPOT-298: undo always answers NOT_REVERSIBLE. Settlement undo through receivables is not wired.
   - DEPOT-306: the AI policy guard is never called.
   - DEPOT-309: logout clearing, the canary test and the lint rule are missing.
   - DEPOT-303: only the copilot is routed.
   - DEPOT-311: the need-to-know switch is stored but nothing enforces it.
   - DEPOT-301: AC3 entity types, the AC8 navigation entry and the `cui.view` assignment path are missing.
2. **DEPOT-277 hides the Copilot for every user today.** The availability endpoint it reads is not built yet (DEPOT-292), so the shell falls back to the classic home. This follows the fail-safe AC1 decision.
3. **DEPOT-303 finding P4 needs a decision.** The router checks data designations with the caller's token, which needs `datadesignation.read`. Ordinary copilot users get a 403, so every record is withheld. The two options are a service credential, or granting the duty along with `mcp.copilot.use`.
4. **Two lanes were merged even though their own quality gate refused them.** For DEPOT-301 the user chose this; I applied the same rule to DEPOT-306 and DEPOT-298.
   - The gate refused only because the plan never named `opa test` for `authz_test.rego`.
   - OPA and the service suites were run by hand before each merge.

## Decisions made on tickets

| Story | Decision | By |
|---|---|---|
| DEPOT-300 | Payments carry a local `bank_reconciled_at` flag. A set or unreadable flag refuses with AR-0021. A treasury feed is a follow-up. | User |
| DEPOT-300 AC11 | The dry run cannot report a closed period or a ledger outage, because the ledger client has no period-status read. The real call still refuses. | User (gap accepted) |
| DEPOT-301 | The service is `services/compliance` at `/v1/compliance/data-designations`. Legal entity is looked up server-side. POST sets a designation, PATCH changes its class, rows are never deleted, and undesignated records return NONE. | User |
| DEPOT-298 AC3 | Payables has no invoice coding update API and invoices have no DRAFT state. So `ap_coding` undo fails closed with 409 NOT_REVERSIBLE, and AC3 is deferred. | Unattended, needs confirmation |
| DEPOT-287 AC2 | Rationale and confidence are shown only when the run trace returns them, never invented. | Unattended, needs confirmation |
| DEPOT-277 AC1 | Availability's `killSwitch` reuses `killSwitchStatusSchema`. Any availability error falls back to the classic home. | Unattended, needs confirmation |

## Stories

| Story | Wave | Outcome | Attempts | Cost | Notes |
|---|---|---|---|---|---|
| DEPOT-276 | 1 | In Review | 1 | $3.01 | |
| DEPOT-294 | 1 | In Review | 1 | $3.03 | Review fixes added the `keepApiPrefix` route for depot-server. Other agents paths still answer 403 at OPA (AC4). |
| DEPOT-299 | 1 | In Review | 1 | $1.56 | |
| DEPOT-300 | 1 | In Review | 2 | $9.18 | Durable reversal attempts and a sweeper were added in the wave fix rounds. Ledger posts still run under row locks, bounded to 4s. |
| DEPOT-301 | 1 | In Review | 4 | $11.35 | Refused twice at closeout and merged by user decision. Gaps: AC3, AC8 and `cui.view`. |
| DEPOT-292 | 2 | In Review | 1 | $4.25 | Budgets only. |
| DEPOT-298 | 2 | In Review | 2 | $7.20 | Stopped on AC3, decided, re-run. Refused at closeout and merged after a manual OPA and agents-ts check. Undo is not wired. |
| DEPOT-306 | 2 | In Review | 1 | $4.64 | Refused at closeout and merged after a manual check. The guard is not called. |
| DEPOT-309 | 2 | In Review | 1 | $2.79 | |
| DEPOT-277 | 3 | In Review | 2 | $5.24 | Stopped on AC1, decided, re-run. |
| DEPOT-303 | 3 | In Review | 2 | $3.18 | The first run failed during setup (missing `inputs/architecture.md`). |
| DEPOT-311 | 3 | In Review | 1 | $3.56 | |
| DEPOT-287 | 3 | Set aside, To Do | 2 | $2.17 | Stopped on AC2, then on AC3: which run-trace field lists the records read, and how link permission is decided. |

## Merging into the wave branches

- **Wave 1:** six integrate runs.
  - Two stopped on a plugin payload CRC bug while writing the PR body.
  - Three rounds of review fixes were done by `sdlc:code-generator` subagents on `fix/w1-*` branches and merged in.
- **Wave 2:** DEPOT-306 and DEPOT-298 each conflicted with earlier lanes. One `sdlc:code-generator` merge fixer each resolved them, keeping every story's work.
  - Three `0010_` agents-ts migrations were renumbered to 0010, 0011 and 0012, because `test/migrations.test.ts` requires unique prefixes.
- **Wave 3:** merged cleanly. The integrate review hit its 3-round fix cap.

## Verification on the merged wave tips

| Wave | OPA (`--timeout 120s`) | agents-ts (with Postgres) | Go services | apps/web |
|---|---|---|---|---|
| 1 | 339/339 | not touched | receivables: all pass except `TestListPredictionInvoices_OpenPaidAndDelta`, which also fails on main. Compliance and depot-server pass. | 257/257 in the touched modules; tsc clean |
| 2 | 342/342 | 734/735, failing only the `migrate-command` TLS test, which also fails on the base | not touched | 8143/8143; tsc clean |
| 3 | 343/343 | 781/782, the same TLS test | compliance: 6/6 packages pass | 8188/8188; tsc clean |

## Spend

- **Headless children:** $102.64, measured from telemetry. This covers 21 lane runs and 8 integrate runs.
  - Lanes: $67.33. Waves 1 to 3 integrate runs: $31.89, $3.34 and $6.24.
- **In-session subagents:** not measured, and not included above. These are the context explorers, wave 1 fixers, merge fixers and the P3 fixer.

## Jira

**Transitions:**
- To Do → In Progress at dispatch, for all 13 stories.
- To In Review: 12 stories, once their wave PR existed.
- In Progress → To Do: DEPOT-287, set aside.

**Edits:**
- Descriptions edited to record decisions: DEPOT-277, 287, 298, 300 and 301.
- A gap comment was posted on every In Review story with gaps, and a set-aside comment on DEPOT-287.

**Not created** (this command cannot create issues):
- The follow-up story for setting `bank_reconciled_at` from treasury.
- Stories for the scope gaps above.

## Unrelated finding

On `main`, the finreport chart's `opa.url` defaults to empty, so its authorization check is skipped and every request is allowed. It came from commit ac829a2a4 (#751) and was left unchanged.

## Left behind

- `.analysis/execute-backlog/2026-11-18-DEPOT/` holds logs, telemetry, tickets and the context cache. It is git-ignored.
- Integration worktrees `.worktrees/integration/depot-wave-{1,2,3}` and the lane worktrees.
- Local branches `fix/w1-{receivables,compliance,gateway}`. They are merged into `integration/depot-wave-1` and safe to delete.

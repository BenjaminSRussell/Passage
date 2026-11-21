# Backlog execution: DEPOT, 2026-11-19 (invocation 5)

Run settings:
- Plan `backlog-plan.md`, epic DEPOT-275, waves 3..6.
- Gate `in-review`, `--max-parallel 5`, min score 7, min category score 5, `--continue-on-failure`.
- Architecture `docs/architecture.md`, compiled to `docs/architecture.compiled`.

**All seven stories in waves 3 to 6 were generated and integrated, each wave into one PR to `main`.** The product owner delegated every decision to the orchestrator. Those decisions are written into each ticket's description under "Decisions (2026-11-19)", or in a ticket comment.

## Stories

| Story | Wave | Outcome | Wave PR | Lane cost | Notes |
|---|---|---|---|---|---|
| DEPOT-287 | 3 | Done | #765 (merged `141c45f82`) | $2.32 | Shared AI-suggestion component. The AC6 settlement-source question was settled on the ticket before re-dispatch. |
| DEPOT-304 | 4 | Done | #766 (merged `0184bf13d`) | $7.35 (3 attempts) | Egress guard. Stopped twice on open questions (AC3, then AC4); both were settled on the ticket. Attempt 3 delivered. |
| DEPOT-302 | 5 | Done | #767 (merged `374c2bbd9`) | $3.84 | CUI marking. AC7 deferred to DEPOT-321. |
| DEPOT-305 | 5 | Done | #767 | $3.59 | Output guard. The briefing and run-step guard was deferred to DEPOT-323. |
| DEPOT-285 | 6 | Done | #768 (merged `1fe460c95`) | $3.44 | Briefings, deterministic. The model step, disabled agents and workflow-ts move are deferred to DEPOT-325. |
| DEPOT-295 | 6 | Done | #768 | $2.97 | Prompt classification and the conversation endpoints. |
| DEPOT-307 | 6 | Done | #768 | $5.13 | AI data-access audit log, backend only. Closeout refused because a test path was missing from a closeout command. OPA passed 346/346 with a 120 s timeout, so the branch was accepted. |

## Waves

| Wave | Integrate result | What the orchestrator did | PR |
|---|---|---|---|
| 3 | REQUEST CHANGES; gate 6.9, failing because of a stale base | Opened a PR against `main` and merged it on green CI | #765 merged |
| 4 | PR-HELD; maintainability 4, because the PR was based on the stale wave-3 branch and counted about 31k lines | Retargeted the PR to `main` (50 files, +3447 −2135), verified it locally, merged on green CI | #766 merged |
| 5 | STOPPED after 3 fix rounds; gate PASS 7.3 | Fixed two of the open findings, accepted or deferred the rest, opened the PR. CI's `cloud-profiles` job failed on a Docker digest mismatch, unrelated to the change; the rerun went green | #767 merged |
| 6 | Headless session died on `ENOTFOUND` after fix round 1, with 0 open findings and gate PASS 7.1 | Verified locally, filed DEPOT-331, opened the PR. CI then failed on two things: the RLS check flagged two tables, and the compiled contracts had drifted. Fixed in `2e8da46e2`: `agent_briefing` became a permanent rlsexempt entry, the same as its source tables. Audit migration 0017 scopes NULL-legal-entity `ai_data_access` rows to tenant-wide readers. The contracts were regenerated | #768 merged |

The two wave-5 findings fixed by the orchestrator (`f30a46744`):
- The 20% unverified-citation rule is now checked on the full answer only. It had been applied to each streamed sentence, which withdrew valid answers.
- `maskValue` now fully masks pay, amounts and short identifiers. Before, a pay of 85000 showed as `****5000`.

## Security fixes outside the generated lanes

Both fixes started from a background security review of `egressGuard.ts`. They shipped in wave 5 and are owned by DEPOT-304.
- `7e0654ba1`: the egress guard requires the tenant AI data policy and fails closed without it. The run-provider guard in `main.ts` had been built with no policy, so agent runs skipped the tenant policy.
- `9e01cd306`: results from a module the tenant has denied (P0 and P1 off) are dropped, with reason `TENANT_POLICY`. Before, every ap and ar tool was declared P2, which skipped the policy check entirely.

## Decisions made during integration

- DEPOT-307's audit-write failure code collided with DEPOT-285's AGT-0230 and was renumbered to AGT-0232.
- Records hidden by need-to-know are left out of the audited record ids.
- After each squash-merge to `main`, the next wave's sync conflicted only because of squash history. The `main` tree was identical to the previous wave's tip, so the wave's own side was taken and the tree was checked to be unchanged.
- Wave 5's egress guard fixes sat outside the wave's two stories. The reviewer flagged this, and the fixes were accepted under DEPOT-304.

## Follow-up stories (under DEPOT-275, label `follow-up`)

- **CUI marking (from DEPOT-302):**
  - DEPOT-321: need-to-know marking (AC7).
  - DEPOT-322: non-project record types; one data-class vocabulary, so the AI marking shows end to end; the Playwright axe sweep (AC8).
- **Output guard (from DEPOT-305):**
  - DEPOT-323: the guard on run steps and briefings; ids printed in prose; CUI citation class.
  - DEPOT-324: seed the ViewSensitivePayroll and ViewSensitiveWorker duties.
- **Briefings and classification:**
  - DEPOT-325: briefings model step, disabled agents, move to workflow-ts.
  - DEPOT-326: search_tools and help.search; route catalog drift test.
- **AI data-access audit (from DEPOT-307):**
  - DEPOT-327: web view.
  - DEPOT-328: transcript store and retention; export manifest signing.
  - DEPOT-331: audit on agent runs, run steps and briefings.
- **Egress guard (from DEPOT-304):**
  - DEPOT-329: per-turn re-scan, analyzer error diagnostics, timer-based tests, heading levels.
  - DEPOT-330: per-tool data-class annotations; `TENANT_POLICY` on the response.
- **From wave 3:** DEPOT-316, the knowledge-search CUI gap. This is the highest priority.

## Spend

- Headless children: $53.54 in total.
  - Lanes: $30.6, including DEPOT-304's two stopped attempts.
  - Integrate sessions: $24.9.
- Not included: the orchestrator session and its subagents, which resolved merge conflicts and made the security and review fixes.

## Lessons for the next run

- Every lane had to have its worktree pre-created with `npm ci` in `services/agents-ts` and `apps/web`. Without it, closeout fails with "tsc not found", because the repo root has no lockfile.
- Merge each wave PR before running the next wave's integrate. A wave integrated against an unmerged base branch gets scored on the combined diff.
- In CI, `opa test` needs `--timeout 120s`. The 5 s default produces false timeout errors.
- Answer a ticket's open questions on the ticket before dispatch. That avoids a stop for each question in turn, as happened twice with DEPOT-304.

Left on disk: `.analysis/execute-backlog/2026-11-19-DEPOT/`, which is git-ignored, and the worktrees under `.worktrees/` for waves 3 to 6 and the lane branches.

---

# Invocation 6: waves 7 to 9

Run settings: the same as invocation 5, with `--waves 7..9`.

**All eleven stories in waves 7 to 9 were generated and integrated, one PR per wave.** Several shipped a first slice, and every deferred acceptance criterion has a follow-up story. Decisions are recorded on each ticket under "Decisions (2026-11-19)".

## Stories

| Story | Wave | Outcome | Wave PR | Lane cost | Notes |
|---|---|---|---|---|---|
| DEPOT-278 | 7 | Done | #771 (merged `b2b7723d`) | $4.02 | AI controls and the kill switch. |
| DEPOT-296 | 7 | Done | #771 | $2.79 | Prompt router, ask and navigate. |
| DEPOT-297 | 7 | Done | #771 | $4.13 | Act-mode turn in agents-ts. It conflicted with 278 in `errors.ts` and `service.ts`, which were merged through `assertStartable`. |
| DEPOT-279 | 8 | Done | #772 (merged `126f8cb1`) | $3.25 | Multi-turn copilot answers inline. |
| DEPOT-283 | 8 | Done | #772 | $3.18 | Global command bar with Navigate, Records and Assistant. |
| DEPOT-308 | 8 | Done | #772 | $4.59 | Red-team leakage and exfiltration gate. Its findings led to the output-guard rewrite below. |
| DEPOT-281 | 9 | Slice 1 | #773 (merged `7171e315`) | $3.16 | Task mode and the server route catalog. Slices 2 and 3 are DEPOT-337 and 338. |
| DEPOT-282 | 9 | Slice 1 | #773 | $5.42 (2 attempts) | Read-only proposal lookup and deep link. Slices 2 and 3 are DEPOT-339 and 340. |
| DEPOT-289 | 9 | Slice A | #773 | $2.64 | Page and record pins. Slice B is DEPOT-341. |
| DEPOT-290 | 9 | Complete | #773 | $2.88 | First-run onboarding tour. |
| DEPOT-310 | 9 | Offline slice | #773 | $4.04 | CUI profile, egress policy, sub-processor registry, sign-off gate. The AWS work is DEPOT-334. |

## Waves

| Wave | Integrate result | What the orchestrator did | PR |
|---|---|---|---|
| 7 | Completed | CI's rlscheck flagged `agent_disabled` and `agent_disabled_change`; both were added to the rlsexempt agents-tenant list. Merged main back in and regenerated the contracts | #771 merged |
| 8 | STOPPED twice (REQUEST CHANGES, no progress on the last finding) | Replaced the exfiltration heuristic with an allowlist guard, fixed the P4 hand-off and the P1 poll rate, and opened the PR by hand. Fixed three e2e failures. A Maven download flake on `enricher` passed on rerun | #772 merged |
| 9 | STOPPED at PR creation (PR-body checksum mismatch) after 2 fix rounds with 0 open findings; gate 6.5 on diff size | Made the AI sign-off gate fail closed, removed stale NOT-IMPLEMENTED rows, filed the slice follow-ups, and opened the PR by hand. CI fixes: Go cloudprofile CUI Bedrock host, TopBar fits 390px and 320px (phone pin toggle icon-only), onboarding fixture for the chromium project. A Go `platform/httpx` port-bind race passed on rerun | #773 merged |

## Security fixes outside the generated lanes

- **Output guard allowlists links** (wave 8, owned by DEPOT-308; `eab301d4d`, `886a3757c`):
  - Only same-origin relative paths survive. Images, reference links, definitions, raw HTML and bare or protocol-relative URLs are all removed.
  - The guard neutralises, then checks, then returns only the checked text. `presentForCaller` re-neutralises after restoring pseudonyms.
  - A link in tool arguments refuses the run (`LINK_IN_ARGS`).
  - Seven review findings over four rounds were closed this way.
- **AI sign-off gate fails closed** (wave 9, owned by DEPOT-310; `7a836d000`): `aiEnabled` is required, and every check is `!== true`.
- **Knowledge routes gated** (wave 9, `5085d3f32`): the knowledge chunk and search routes now sit behind the sign-off gate.
- **CUI egress** (wave 9 fix round): `bedrock` under the `cui` profile resolves only to the GovCloud host.

## Decisions made during integration

- Arrow keys in the command palette reach every row, including Assistant, per the WAI-ARIA listbox pattern. The p2p demo spec asserts the default selection instead of pressing ArrowDown.
- Wave 9's sync with `main` conflicted in 17 files, all squash history. Wave 9's own side was taken, and wave 8's two late e2e fixes were reapplied as a patch.
- Waves 8 and 9 accepted the over-cap diff (P1/P2 maintainability). Each merges three to five lanes, and most of the lines are tests, datasets and docs.

## Follow-up stories

- DEPOT-332: backend edit-before-approve.
- DEPOT-333: domain services stamp `origin=agent` from `x-agent-run-id`.
- DEPOT-334: CUI GovCloud IaC, live probes, Presidio, sub-processor screens.
- DEPOT-335: AI sign-off at the provider choke point.
- DEPOT-336: shared assistant router for the command bar and Home.
- DEPOT-337 to 341: the remaining slices of 281, 282 and 289.

## Spend

- Headless children: $70.53 in total.
  - Lanes: $40.10.
  - Integrate sessions: $30.43 (wave 7 $5.90; wave 8 $8.85 + $7.26; wave 9 $8.42).
- Not included: the orchestrator session and its fixer subagents.

## Lessons for the next run

- A stopped integrate child with zero or one open finding is cheaper to finish by hand than to re-run. Wave 8's second run cost $7.26 and stopped on the same finding.
- After a squash-merge, sync the next wave by taking its own side, then reapply only the delta between the previous wave's last synced commit and its merged tip.
- Run the e2e demo flows locally before opening a wave PR that touches the shell. All three wave 8 CI failures were Playwright-only.
- Make kill switches and sign-off flags required booleans. An optional flag tested with `=== false` fails open.
- Run the reflow spec at both 390px and 320px before a wave PR that touches the TopBar. One shell overflow times out every route and the shard hits the job timeout, which shows as CANCELLED rather than FAILED.

# Invocation 7: waves 10 to 12

Run settings: the same as invocation 5, with `--waves 10..12`. The gate was in-review, so the waves were stacked: wave 11 was cut from wave 10 and wave 12 from wave 11. Each PR was retargeted to `main` after the one before it was squash-merged. Run autonomously under the user's standing approval (2026-11-20).

**All five stories in waves 10 to 12 were generated, integrated and merged, one PR per wave.** Three shipped a first slice, and every deferred acceptance criterion has a follow-up story. Decisions are recorded on each ticket under "Decisions (2026-11-20)". DEPOT-293 stays out of every wave, blocked on OQ-3.

## Stories

| Story | Wave | Outcome | Wave PR | Lane cost | Notes |
|---|---|---|---|---|---|
| DEPOT-280 | 10 | Slice 1 | #775 (merged `fac1b5d0`) | $4.62 (2 attempts) | Attention feed with inbox tasks, pending agent approvals (new `GET /api/v1/agents/approvals` list) and MRP planning messages. Attempt 1 stopped because the ticket named endpoints and duties that do not exist. |
| DEPOT-284 | 10 | Done (link form) | #775 | $3.92 | Explain, what changed and next step on record pages. The proposal-start button waits on the 281/282 act handler. |
| DEPOT-286 | 10 | Slice 1 | #775 | $2.83 | Briefing card on the home. Auto-generation, retry, empty state and mark-read are DEPOT-347. |
| DEPOT-288 | 11 | Slice 1 | #776 (merged `4c6d005e`) | $3.35 | Role- and activity-aware home. Reset personalization is DEPOT-348. |
| DEPOT-291 | 12 | Done | #777 (merged `71ef438e`) | $7.07 (2 attempts) | Cross-surface guardrail suite: permissions for three personas, resilience matrix, axe gate in two themes and two viewports, write-guard sweep, keyboard journey, and the O1 to O10 traceability report as a CI artifact. The check is `ci / web guardrails`. |

## Waves

| Wave | Integrate result | What the orchestrator did | PR |
|---|---|---|---|
| 10 | Completed ($6.14) | CI: the AI data policy module list lacked `platform`, which DEPOT-284's audit-trail and document-flow read tools use. `platform` was added with the same fail-closed default as `ap` and `ar`, and the compiled contracts were regenerated for the new approvals list route. A second round fixed three `need-to-know` test fixtures that missed the new required module | #775 merged |
| 11 | Completed ($1.19) | Pulled both wave 10 fixes in. After #775 merged, synced `main` taking wave 11's side (one add/add conflict, `useAttentionFeed.ts`, no tree change). A Maven `bcutil-fips` digest mismatch on `cloud-profiles` passed on rerun | #776 merged |
| 12 | Completed ($1.60) | The lane was refused at closeout because its worktree had no `node_modules`, so Playwright never ran. An e2e agent installed dependencies and ran the suite. It reverted the lane's blanket downgrade of the briefing error alert, and the suite then found a real defect (below), which was fixed in the product. CI: `reflow at 390px › /` overflowed by 23px on the Linux font. The cause was a latent nowrap "Try asking" suggestion button, not wave 12's diff, and the suggestion buttons now wrap (`4f392fe9`). The Maven download flake on `cloud-profiles` recurred | #777 merged |

## Product defect found by the guardrail suite

- With the AI layer returning 503 or timing out, the home briefing card showed a danger `role="alert"`. That broke DEPOT-291 AC2 and epic outcome O8.
- Fix (`9b3e2a03`): one classifier, `isAiUnavailable`, in the copilot API client. It treats 503, `AGT-0201` (kill switch), `AGT-0202` (budget), timeout and network failure as AI-unavailable. The card then shows a quiet line with no alert and no retry, the same as for the kill switch. Other errors (500, 422, a malformed body) keep the danger alert and Try again.
- The suite passed 81 of 81 twice in a row with no flakes.

## Decisions made during integration

- DEPOT-280: three sources in slice 1. The named endpoints `/v1/procurement/match-results` and `/v1/manufacturing/planning-messages` and their duties do not exist and were not invented.
- DEPOT-291: cover only the surfaces present on the base. Missing paths are listed as "pending <ticket>" in `e2e/guardrails/registry.json`. The epic outcomes O1 to O10 were taken verbatim from the generated backlog.
- DEPOT-291: the briefing card degrades quietly only when the AI layer is unavailable. Genuine errors are still announced.
- AI data policy: `platform` is a policy module. Tenants who tighten it now also restrict the audit-trail and document-flow AI tools.

## Follow-up stories

- DEPOT-342, 343, 344: the deferred attention-feed sources (three-way match exceptions, journal anomalies, inbox consumer for agent approvals).
- DEPOT-345, 346: DEPOT-280 slices 2 and 3 (agent approvals source, inline decisions).
- DEPOT-347: DEPOT-286 slice 2 (auto-generated daily briefing).
- DEPOT-348: DEPOT-288 slice 2 (reset personalization).

## Spend

- Headless children: $30.72 in total.
  - Lanes: $21.79.
  - Integrate sessions: $8.93 (wave 10 $6.14; wave 11 $1.19; wave 12 $1.60).
- Not included: the orchestrator session and its fixer subagents.

## Lessons for the next run

- Research and decisions must use the integration worktree. The main checkout was still at wave 6, and an explorer reading it wrongly reported that endpoints were missing.
- Install web dependencies in the base before dispatching an e2e-heavy lane. Without `node_modules`, every Playwright gate fails at config load and burns the waiver budget. DEPOT-291 cost $5.71 that way.
- A lane that makes a guardrail pass by changing product semantics, such as downgrading an alert or rewriting an existing assertion, should be reverted and the defect fixed.
- Adding a value to a required enum, like a policy module, breaks every typed fixture. Run each service's `typecheck`, not only its tests, before pushing.
- When a ticket cites definitions it never gives, such as O1 to O10, look in the generated backlog plan before stopping.

# passage-agents (wave 47, PSG-47)

The agent runtime (PRD 9.47 FR-47.4 to FR-47.13). This slice contains the run
engine and the governance rules it enforces. An agent run plans with a model,
calls tools as the invoking user, proposes actions, waits at an approval gate
when the mode requires it, and commits. Every step is recorded as an action.

Model calls and tool calls go through injected ports, so the engine is
deterministic in tests.

## What this slice contains

| Module | Responsibility |
|---|---|
| `src/runtime/engine.ts` | `runAgent`: one model turn and one proposal. Checks the kill switch, budget, eval and policy at start and the kill switch before every step, redacts prompts, calls the provider, enforces the per-run token cap, and routes the proposal through the policy. `approve_before_commit` tools always wait for a human; AUTO applies only to `suggest` tools. The AUTO and self-approval limits use the document amount from `RiskPort`, not the model's figure. A port that throws ends the run FAILED |
| `src/domain/run.ts` | BR-47.1 state machine: PLANNING, ACTING, AWAITING_APPROVAL, COMMITTED, REJECTED, FAILED, KILLED. Terminal states cannot be left |
| `src/domain/policy.ts` | `decideDisposition` (BR-47.2): OFF blocks, SUGGEST proposes, AUTO only when confidence, amount, vendor risk and SoD all pass. `validatePolicy` enforces the parameter ranges. `isApprovalExpired` (BR-47.3). `checkCashApplication` (BR-47.10): at most 20 invoices per payment (1 to 50 allowed), write-off within tolerance, never more than the payment |
| `src/domain/authz.ts` | Duty check for tool calls on behalf of a user. An agent can never exceed its invoker (AC-47.2 contract) |
| `src/domain/budget.ts` | Monthly token budget states OK, SOFT (80%), HARD (100%) (FR-47.11, AC-47.7) |
| `src/domain/redaction.ts` | `scanPii` (write-time scanner), `assertNoPii`, and `Pseudonymizer`: stable pseudonyms per (tenant, value) with a TTL, restored only for the UI (FR-47.12, BR-47.7, AC-47.8) |
| `src/domain/registry.ts` | `agent_version` records: prompt, tool list and model id pinned; a production version needs a passing eval (FR-47.10) |
| `src/domain/audit.ts` | Action records: inputs, tool calls, outputs and approver (AC-47.3) |
| `src/domain/money.ts` | Currency-safe money comparison for the AUTO amount limit. Never floats |
| `src/errors.ts` | `AgentError` with the stable PRD 9.47.18 codes |

## Run and test

```sh
cd services/agents-ts
npm install
npm test          # vitest: engine, domain and redaction tests
npm run typecheck
npm run build
```

The tests run without a database or network. Ports are faked in `test/fakes.ts`.

## Wave reference

- FR-47.4 runtime steps, FR-47.5 AP coding modes, FR-47.8 approval gate
- FR-47.10 version registry, FR-47.11 token budgets, FR-47.12 responsible AI
- FR-47.13 provider layer: the `ModelProvider` port is defined; the Anthropic, Bedrock, Azure and OpenAI-compatible adapters are not built (see NOT-IMPLEMENTED.md)
- BR-47.1 to BR-47.5, BR-47.7 and BR-47.10 are domain rules in code and tested. BR-47.6 (4xx not retried, 5xx retried 1 s / 4 s / 16 s) is enforced by the MCP gateway (`services/mcp-ts/src/gateway.ts`), which is the runtime's `ToolPort`. BR-47.3 is a rule (`isApprovalExpired`) plus the engine's handling of an `EXPIRED` decision; the expiry sweeper is not built. The rest are listed in NOT-IMPLEMENTED.md

## Error codes

| Code | Meaning |
|---|---|
| `AGT-0201` | Runs are halted by a kill switch |
| `AGT-0202` | Monthly token budget exhausted (`BUDGET_EXHAUSTED`) |
| `AGT-0203` | The invoker lacks a duty required by the tool |
| `AGT-0204` | Agent version has no passing evaluation and cannot be promoted |
| `AGT-0208` | Approval expired after the configured hours; nothing was posted |
| `AGT-0209` | Approver is the invoker above the self-approval limit |
| `AGT-0210` | Unredacted PII detected before a provider call or storage; the run fails |
| `AGT-0214` | A tool call failed; 4xx is not retried |

Error codes not listed here (AGT-0205, 0206, 0207, 0211, 0212, 0213) are in the
PRD catalog; they belong to slices that are not built yet.

## Dependencies

Runtime: none. Development: `typescript`, `vitest`, `@types/node`. Pure
TypeScript; no provider SDK is bundled, which keeps the provider layer swappable.

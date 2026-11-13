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
| `src/runtime/engine.ts` | `runAgent`: one model turn and one proposal. Checks the kill switch, budget, eval and policy at start and the kill switch before every step, redacts prompts, calls the provider, enforces the per-run token cap, and routes the proposal through the policy. `approve_before_commit` tools always wait for a human; AUTO applies only to `suggest` tools. The AUTO and self-approval limits use the document amount from `RiskPort`, not the model's figure. A port that throws ends the run FAILED. Once the model has proposed an action, every way out of the run (commit, rejection, denial, kill switch, a port that throws) leaves exactly one action row for it, keeping the approver of any human decision (AC-47.3; `test/audit-coverage.test.ts`). A row the write-time scanner refuses (AGT-0210) does not change how the run ends: a kill still ends KILLED, and a tool call that committed still ends COMMITTED, its row carrying the committed reference in place of the tool output |
| `src/domain/run.ts` | BR-47.1 state machine: PLANNING, ACTING, AWAITING_APPROVAL, COMMITTED, REJECTED, FAILED, KILLED. Terminal states cannot be left |
| `src/domain/policy.ts` | `decideDisposition` (BR-47.2): OFF blocks, SUGGEST proposes, AUTO only when confidence, amount, vendor risk and SoD all pass. `validatePolicy` enforces the parameter ranges. `isApprovalExpired` (BR-47.3). `checkCashApplication` (BR-47.10): at most 20 invoices per payment (1 to 50 allowed), write-off within tolerance, never more than the payment; with open amounts, no allocation above its invoice's open amount and no write-off above what stays open |
| `src/domain/approval.ts` | Approval rules (BR-47.3, 9.47.5): `agent.approval_expiry_h` range check, the hourly sweeper's selection `expiredApprovals`, and `decideApproval` for the decision endpoint (approver duty AGT-0203, expiry AGT-0208, self-approval above the limit AGT-0209) |
| `src/domain/copilot.ts` | BR-47.11: `gateCopilotTools` answers a tool the user lacks with AGT-0203 and the catalog text; `CopilotAnswerCache` serves an answer only to the same tenant, user, duty set and mode, for 5 minutes |
| `src/domain/authz.ts` | Duty check for tool calls on behalf of a user. An agent can never exceed its invoker (AC-47.2 contract) |
| `src/domain/budget.ts` | Monthly token budget states OK, SOFT (80%), HARD (100%) (FR-47.11, AC-47.7) |
| `src/domain/redaction.ts` | `scanPii` (write-time scanner), `assertNoPii`, and `Pseudonymizer`: stable pseudonyms per (tenant, value) with a TTL, restored only for the UI (FR-47.12, BR-47.7, AC-47.8) |
| `src/domain/registry.ts` | `agent_version` records: prompt, tool list and model id pinned; a production version needs a passing eval (FR-47.10) |
| `src/store/registry.ts`, `src/cli/record-eval.ts`, `migrations/0006_versions_evals_partitions.sql` | Persisted registry (FR-47.10, BR-47.8). `agent_version` holds each definition a pod runs (agent id, version, prompt hash, model, prompt, tools, duties), registered at start; a deployment that pins another model for the same version is a second definition. `eval_run` holds the eval pipeline's gate results per definition and dataset version. A run starts only when the newest `eval_run` of its exact definition passed (AGT-0204). `node dist/cli/record-eval.js report.json` stores an `evals` CLI report for the definitions this deployment runs (same image and model settings). Both tables are platform rows with no tenant column, append-only for `passage_app` (allowlisted in tools/rlscheck) |
| `agent_action` partitions (migration 0006) | `agent_action` is range-partitioned by `run_month`, the UTC month the run started (a generated column on `agent_run`); the primary key and the foreign key to `agent_run (tenant_id, run_id, run_month)` keep every action of a run in its run's partition and (tenant, run, seq) unique. Each pod calls `agent_action_ensure_partitions(2)` (SECURITY DEFINER) at start and daily; rows of a month without a partition land in `agent_action_default` and move out when the month is created. The migration moves existing rows |
| `src/domain/audit.ts` | Action records: inputs, tool calls, outputs and approver (AC-47.3) |
| `src/domain/money.ts` | Currency-safe money comparison for the AUTO amount limit. Never floats |
| `src/providers/wire.ts` | The one forced tool (`propose_action`) every adapter calls, and `toModelResponse`: validates arguments (allow-listed tool, confidence in 0..1, decimal amount, ISO currency) before the engine sees them |
| `src/providers/anthropic.ts` | Anthropic Messages API adapter (default provider) |
| `src/providers/openai.ts` | OpenAI chat-completions adapter for OpenAI-compatible open-weights endpoints (vLLM, TGI) and Azure OpenAI (`auth: "api-key"`, `query` for `api-version`) |
| `src/providers/http.ts` | `postJson`: timeout; transport errors, timeouts and 5xx are an outage, 429 and other 4xx a rejection; error messages never include the key or the body |
| `src/providers/failover.ts` | BR-47.9: after 60 s of primary outage, calls move to a residency-compliant secondary, or the run pauses (AGT-0206). While failed over the primary is retried once per 30 s, and `reportPrimaryHealthy` lets the health probe return traffic at once |
| `src/domain/killswitch.ts` | `KillSwitchRegistry`: applies `killswitch.changed` versions in order, ignores duplicates, fails closed after 10 s without a heartbeat (AC-47.6) |
| `src/domain/knowledge.ts` | FR-47.14: reciprocal rank fusion of keyword and embedding results, and the citation gate (similarity >= 0.75, else AGT-0212) |
| `src/domain/reconcile.ts` | 9.47.9: provider-billed against recorded tokens per run, 1% tolerance, estimated runs (BR-47.4) flagged separately |
| `src/domain/events.ts` | Payload builders and validation for `run.completed`, `approval.requested` (redacted proposal; AGT-0210 on raw PII), `budget.threshold` (the cost-guard alert, AC-47.4) and `killswitch.changed`. Tool names and decisions only; no inputs, outputs or display text |
| `src/errors.ts` | `AgentError` with the stable PRD 9.47.18 codes |
| `src/api/router.ts` | `createAgentsHandler`: the node:http listener for the routes this slice serves (`GET`/`POST /api/v1/agents/kill-switch`, `POST /api/v1/agents/runs/{run_id}/actions/{id}/decision`). Takes the caller from the verified bearer token (`auth.ts`), caps bodies at 64 KiB, answers RFC 9457 problems and never echoes an internal error |
| `src/api/killswitch.ts` | Kill switch toggle (AC-47.6): tenant scope needs agents.admin and the caller's own tenant, global scope platform.global.admin. Idempotent by scope. The `killswitch.changed` event is published first and applied to the local registry only after it succeeded. `readKillSwitchStatus` serves the administration screen |
| `src/store/killswitch.ts`, `src/runtime/killswitch-poller.ts`, `migrations/0005_kill_switch.sql` | The kill switch every pod shares (AC-47.6). `kill_switch_change` is an append-only log of toggles with a version from one sequence; `PgKillSwitchLog.toggle` takes a transaction advisory lock, decides idempotence from the stored state, inserts the row and writes its `killswitch.changed` outbox event in the same transaction, so versions commit in order. `KillSwitchPoller` reads `version > applied` every `AGENTS_KILLSWITCH_POLL_MS` on every pod and heartbeats only after a successful read, so a pod that cannot read the table for 10 s fails closed. A starting pod reads only the latest toggle of each switch. The chart runs two replicas |
| `src/api/decision.ts` | Approval decision (FR-47.8): approver duty (AGT-0203), self-approval (AGT-0209), expiry recorded as REJECTED/EXPIRED (BR-47.3). Idempotent by action id with a first-writer-wins `ApprovalStore.record`; a different decision on a decided action is 409. Every store call is scoped to the caller's tenant |
| `src/api/auth.ts` | Caller authentication: verifies the bearer token (RS256 against the realm's JWKS, issuer, audience, expiry, not-before) and takes the tenant (`tid`), user (`sub`) and duties from it. The X-Passage-* edge headers are never the source of identity; present ones must match the token. 401 AGT-0401 otherwise, 503 AGT-0503 when the keys cannot be read |
| `src/api/principal.ts` | The `Principal` type, the HTTP error codes and the mapping of `AgentError` to status |
| `src/runtime/killswitch-feed.ts` | `KillSwitchFeed`: applies each consumed `killswitch.changed` message to a worker's registry and keeps the fail-closed window open on heartbeats. A malformed message, one without an envelope tenant, or a tenant toggle addressed to another tenant throws so the consumer sends it to the DLQ |
| `src/main.ts`, `src/config.ts`, `src/server.ts` | Process entrypoint. 12-factor config (`MCP_URL`, `DATABASE_URL`, `MIGRATE_ON_START`, `AGENTS_PROVIDER` anthropic/openai/bedrock, `AGENTS_MODEL` default `claude-sonnet-5-5`, `AGENTS_COPILOT_MODEL`, `AGENTS_MODEL_OVERRIDES` per agent, `AGENTS_RUN_TOKEN_CAP`, and the provider keys from the environment only), a provider factory (one adapter per model id, never built until the first run needs it), the public listener (`GET /healthz`, `GET /readyz`, the API) and the admin listener on `ADMIN_ADDR` (`/metrics`, `/healthz`). The Claude ids to choose from are `claude-opus-5-5`, `claude-sonnet-5-5` and `claude-haiku-5-5`; nothing is hard-wired. Helm chart `charts/agent-runtime`, `Dockerfile` (build from the repository root) |
| `src/runtime/mcp.ts` | Fetch client of `services/mcp-ts` (`POST /mcp`): `tools/list` and `tools/call` with the invoking user's bearer token and edge identity headers, plus `x-agent-run-id`. It is the engine's `ToolPort`; it holds no credentials of its own |
| `src/runtime/service.ts`, `src/api/runs.ts` | Run start and read. `POST /api/v1/agents/{agent_id}/runs` checks the agent, `agents.invoke` and the agent's own duties (AGT-0203), the kill switch (AGT-0201), a passing eval (AGT-0204) and the month's budget (AGT-0202), opens the gateway session (502 AGT-0214 when it is down), then runs `runAgent` in the background and answers 202 with the run id. Every agent runs in suggest mode: no autonomous posting in v1. `GET /api/v1/agents/runs/{run_id}` (status, tokens, waiting approvals) and `GET .../actions` (audit rows) need `agents.run.read` and see only the caller's tenant |
| `src/store/approvals.ts`, `src/runtime/approvals.ts`, `migrations/0002_approvals.sql` | Persisted approvals. `approval_request` holds each request under tenant RLS; `PgApprovalStore.record` is first-writer-wins (`UPDATE ... WHERE decided_at IS NULL`) behind the decision endpoint. `createPersistedApprovalPort` is the engine's approval gate: it stores the request (PII-free: tool, confidence, amount), polls for the first decision and, past `agent.approval_expiry_h`, records REJECTED/EXPIRED (BR-47.3) as `system:expiry` |
| `src/runtime/copilot.ts`, `src/api/copilot.ts` | Copilot chat backend, `POST /api/v1/copilot/chat` (duty `mcp.copilot.use`). Up to `AGENTS_COPILOT_MAX_TURNS` model turns with read tools only (`get`, `list`, `search`): each call goes to the gateway as the user, results are redacted and fed back. A tool outside the user's allowed set is answered AGT-0203 and never called (BR-47.11); a knowledge answer must pass the 0.75 citation gate or it is AGT-0212; answers are cached per tenant, user, duty set and mode for 5 minutes. A budget hold is reserved and settled per turn, and each turn is one audited run of agent `copilot` with an action row per tool call. `Accept: text/event-stream` gets `token` frames and one `done` frame (the panel's contract); anything else gets JSON |

## Configuration

| Variable | Meaning | Default |
|---|---|---|
| `DATABASE_URL` | Postgres, role `passage_app` (required) | |
| `MCP_URL` | Base URL of the MCP gateway (required) | |
| `AGENTS_JWT_ISSUER`, `AGENTS_JWT_AUDIENCE` | Issuer (realm URL) and audience of the callers' bearer tokens; the issuer is required | audience `passage-api` |
| `AGENTS_JWKS_URL` or `AGENTS_JWKS_FILE` | The realm's signing keys: its certs endpoint, or a mounted key set (chart `auth.jwksConfigMap`); one is required | |
| `AGENTS_PROVIDER` | `anthropic`, `openai` (OpenAI-compatible, Azure) or `bedrock` | `anthropic` |
| `AGENTS_MODEL`, `AGENTS_COPILOT_MODEL`, `AGENTS_MODEL_OVERRIDES` | Model ids; the overrides are a JSON object of agent id to model id | `claude-sonnet-5-5` |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` + `AGENTS_OPENAI_BASE_URL`, `AWS_REGION` + `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` | Provider credentials, from a secret; a missing one fails the first run naming the variable | |
| `AGENTS_RUN_TOKEN_CAP`, `AGENTS_COPILOT_TOKEN_CAP`, `AGENTS_COPILOT_MAX_TURNS` | Per-run and per-chat token caps, chat turns | 20000, 8000, 4 |
| `AGENTS_APPROVAL_EXPIRY_H`, `AGENTS_SELF_APPROVAL_LIMIT`, `AGENTS_SELF_APPROVAL_CURRENCY` | BR-47.3 expiry (1 to 240 hours) and the self-approval limit | 72, 0, USD |
| `AGENTS_KILLSWITCH_POLL_MS` | How often each pod reads the shared kill switch (100 to 5000) | 1000 |
| `HTTP_ADDR`, `ADMIN_ADDR`, `MIGRATE_ON_START`, `AGENTS_DB_POOL` | Listeners and database | `0.0.0.0:8080`, `0.0.0.0:9090`, off, 10 |

## Run and test

```sh
cd services/agents-ts
npm install
npm test          # vitest: engine, domain and redaction tests
npm run typecheck
npm run build
```

Postgres-backed tests (`approvals-pg`, `budget-store`) run when `PASSAGE_TEST_DATABASE_URL` (owner) and `PASSAGE_TEST_APP_DATABASE_URL` (`passage_app`) are set, as in CI; without them they are skipped.

The tests run without a database or network. Ports are faked in `test/fakes.ts`.
CI runs typecheck, tests and build for this package, `services/mcp-ts` and
`evals` (the `agents` job in `.github/workflows/ci.yml`).

Wave 47 proof tests: `test/audit-coverage.test.ts` (one audit row per
proposal on every path, and a seeded 5,000-run property check that no tool is
called past the invoker's duties or the allow-list and that raw IBANs in the
model's tool arguments reach the row pseudonymized), `test/cost-guard.test.ts`
(a per-tenant ledger over the engine: alert at 80%, alert at 100%, start
rejected with AGT-0202 and no provider call, tenants isolated).

## Wave reference

- FR-47.4 runtime steps, FR-47.5 AP coding modes, FR-47.8 approval gate
- FR-47.10 version registry, FR-47.11 token budgets, FR-47.12 responsible AI
- FR-47.13 provider layer: Anthropic, OpenAI-compatible (Azure through the same adapter) and Amazon Bedrock (Converse API, in-tree SigV4 checked against AWS SDK reference signatures) adapters, with parity tests in `test/providers.test.ts` and `test/bedrock.test.ts`
- FR-47.5 to 47.7 agent definitions: `defaultCatalog` in `src/agents/catalog.ts` builds version 1 of the five agents (prompts, read and propose tools only, required duties); wiring them into the runtime is not built
- BR-47.9 provider failover: `createFailoverProvider` in `src/providers/failover.ts`; only outages (5xx, timeouts, transport errors) count, a rejected request never fails over
- FR-47.12 kill switch: `KillSwitchRegistry` and the `killswitch.changed` payload; the engine checks it before the provider call (AC-47.6). Toggles are stored in `kill_switch_change` and every pod polls it (`test/killswitch-pg.test.ts`: a toggle on one pod halts runs on another, concurrent toggles commit in version order, a pod that cannot read the table fails closed)
- FR-47.8 approval decision: the endpoint and its rules run against `PgApprovalStore` (`approval_request`, migration 0002)
- AC-47.4 cost guard: `budgetThresholdEvent` fires once when the budget moves into SOFT (80%) or HARD (100%). The event is the alert; routing it to a pager is the alerting consumer's job
- FR-47.14 knowledge retrieval: hybrid fusion and the 0.75 citation gate (AGT-0212) in `src/domain/knowledge.ts`; the search tool and index are not built
- 9.47.9 reconciliation: `reconcileRuns` compares billed and recorded tokens within 1%; the daily job is not built
- BR-47.1 to BR-47.5, BR-47.7, BR-47.9 and BR-47.10 are domain rules in code and tested. BR-47.6 (4xx not retried, 5xx retried 1 s / 4 s / 16 s) is enforced by the MCP gateway (`services/mcp-ts/src/gateway.ts`), which is the runtime's `ToolPort`. BR-47.3 is a rule (`isApprovalExpired`) plus the engine's handling of an `EXPIRED` decision; the expiry sweeper is not built. The rest are listed in NOT-IMPLEMENTED.md

## Error codes

| Code | Meaning |
|---|---|
| `AGT-0201` | Runs are halted by a kill switch |
| `AGT-0202` | Monthly token budget exhausted (`BUDGET_EXHAUSTED`) |
| `AGT-0203` | The invoker lacks a duty required by the tool |
| `AGT-0204` | Agent version has no passing evaluation and cannot be promoted |
| `AGT-0208` | Approval expired after the configured hours; nothing was posted |
| `AGT-0209` | Approver is the invoker above the self-approval limit |
| `AGT-0210` | Unredacted PII detected before a provider call or storage; the run fails, except that a kill still ends KILLED and a tool call that already committed still ends COMMITTED |
| `AGT-0206` | Provider unavailable (transport, timeout, 5xx), rate-limited or rejected (4xx), or an unusable response; the run fails. Only the first group counts towards failover |
| `AGT-0212` | No knowledge source reaches citation similarity 0.75; the copilot says so instead of guessing |
| `AGT-0214` | A tool call failed; 4xx is not retried. Also 502 when the MCP gateway cannot be reached at run start |
| `AGT-0215` | A field the entity metadata tags `phi` is in text bound for a model; the run fails before any model call and the budget hold is released (#606) |
| `AGT-0400` | Malformed request: body not a JSON object, unknown field or invalid value |
| `AGT-0401` | The edge did not identify a tenant and user |
| `AGT-0403` | A tenant administrator toggled another tenant's kill switch |
| `AGT-0404` | No such route, or no approval request for this action in the caller's tenant |
| `AGT-0405` | Method not allowed on the route (`Allow` lists the methods) |
| `AGT-0409` | The action was already decided differently |
| `AGT-0413` | Request body above 64 KiB |
| `AGT-0500` | Unexpected error; the detail is never echoed |

Error codes not listed here (AGT-0205, 0207, 0211, 0213) are in the
PRD catalog; they belong to slices that are not built yet.

## Dependencies

Runtime: none. Development: `typescript`, `vitest`, `@types/node`. Pure
TypeScript; no provider SDK is bundled, which keeps the provider layer swappable.

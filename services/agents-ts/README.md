# depot-agents (wave 47, DPT-47)

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
| `src/runtime/outputGuard.ts` | Output guard (DEPOT-305): releases the copilot answer sentence by sentence after checking every pseudonym token and `[[cite:id]]` marker against this turn's `GuardContext`, that in the copilot chat no P3 value appears unless the viewer holds the duty for its domain (`agents.sensitive-payroll.view`, PRD `ViewSensitivePayroll`, for PAY; `agents.sensitive-worker.view`, PRD `ViewSensitiveWorker`, for SSN and TAXID; IBAN, CARD and BANKACCT are always masked; the agent run paths are tracked in DEPOT-350), and that no raw identifier was written by the model; checks the whole answer once more, neutralises links first, then runs every check on the neutralised text and returns only that text (`src/domain/exfilLinks.ts`, a linear scanner with an allowlist: only a same-origin relative path such as `/purchasing/purchase-orders/123` survives; every other markdown link becomes its label, every image, raw HTML `img`/`a`/`iframe`/`script`/`form`/`svg` tag and reference definition is removed, and autolinks, bare URLs, `www.` hosts and `data:`/`javascript:` URIs become `[link removed]`), re-applies it after pseudonym restore, holds a streamed link back until it is complete, and refuses a model-proposed or act proposal arg that holds any disallowed link (`LINK_IN_ARGS`), raises `dataClass` to the class of the inputs and fails closed with `AGT-0220` |
| `src/runtime/egressGuard.ts`, `src/domain/identifiers.ts` | Egress guard (DEPOT-304): before each copilot model call it scans the typed text and every tool result through an `Analyzer` port (in-process, `scanPii`), replaces IBAN (mod-97), card (Luhn), tax id (EIN, VAT, GSTIN, RFC), bank account, SSN and pay values with stable `[TYPE-n]` pseudonyms, scans the assembled prompt again and blocks with `AGT-0219` on an analyzer error, a missed 2 s deadline or a residual finding. One `egress.guard` action row per call carries the value-free `guard_result` (migration 0013); the response carries `dataClass`. Duration is exported as `agents_egress_guard_duration_seconds` |
| `src/domain/registry.ts` | `agent_version` records: prompt, tool list and model id pinned; a production version needs a passing eval (FR-47.10) |
| `src/store/registry.ts`, `src/cli/record-eval.ts`, `migrations/0006_versions_evals_partitions.sql` | Persisted registry (FR-47.10, BR-47.8). `agent_version` holds each definition a pod runs (agent id, version, prompt hash, model, prompt, tools, duties), registered at start; a deployment that pins another model for the same version is a second definition. `eval_run` holds the eval pipeline's gate results per definition and dataset version. A run starts only when the newest `eval_run` of its exact definition passed (AGT-0204). `node dist/cli/record-eval.js report.json` stores an `evals` CLI report for the definitions this deployment runs (same image and model settings, and the owner role's `DATABASE_URL`). Both tables are platform rows with no tenant column (allowlisted in tools/rlscheck): `agent_version` is append-only for `depot_app`, and `eval_run` is read-only for it, so a pod cannot record a passing eval for itself |
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
| `src/api/auth.ts` | Caller authentication: verifies the bearer token (RS256 against the realm's JWKS, issuer, audience, expiry, not-before) and takes the tenant (`tid`), user (`sub`) and duties from it. The X-Depot-* edge headers are never the source of identity; present ones must match the token. 401 AGT-0401 otherwise, 503 AGT-0503 when the keys cannot be read |
| `src/api/principal.ts` | The `Principal` type, the HTTP error codes and the mapping of `AgentError` to status |
| `src/runtime/killswitch-feed.ts` | `KillSwitchFeed`: applies each consumed `killswitch.changed` message to a worker's registry and keeps the fail-closed window open on heartbeats. A malformed message, one without an envelope tenant, or a tenant toggle addressed to another tenant throws so the consumer sends it to the DLQ |
| `src/main.ts`, `src/config.ts`, `src/server.ts` | Process entrypoint. 12-factor config (`MCP_URL`, `DATABASE_URL`, `MIGRATE_ON_START`, `AGENTS_PROVIDER` anthropic/openai/bedrock, `AGENTS_MODEL` default `claude-sonnet-5-5`, `AGENTS_COPILOT_MODEL`, `AGENTS_MODEL_OVERRIDES` per agent, `AGENTS_RUN_TOKEN_CAP`, and the provider keys from the environment only), a provider factory (one adapter per model id, never built until the first run needs it), the public listener (`GET /healthz`, `GET /readyz`, the API) and the admin listener on `ADMIN_ADDR` (`/metrics`, `/healthz`). The Claude ids to choose from are `claude-opus-5-5`, `claude-sonnet-5-5` and `claude-haiku-5-5`; nothing is hard-wired. Helm chart `charts/agent-runtime`, `Dockerfile` (build from the repository root) |
| `src/runtime/mcp.ts` | Fetch client of `services/mcp-ts` (`POST /mcp`): `tools/list` and `tools/call` with the invoking user's bearer token and edge identity headers, plus `x-agent-run-id`. It is the engine's `ToolPort`; it holds no credentials of its own |
| `src/runtime/service.ts`, `src/api/runs.ts` | Run start and read. `POST /api/v1/agents/{agent_id}/runs` checks the agent, `agents.invoke` and the agent's own duties (AGT-0203), the kill switch (AGT-0201), a passing eval (AGT-0204) and the month's budget (AGT-0202), opens the gateway session (502 AGT-0214 when it is down), then runs `runAgent` in the background and answers 202 with the run id. Every agent runs in suggest mode: no autonomous posting in v1. `GET /api/v1/agents/runs/{run_id}` (status, tokens, waiting approvals) and `GET .../actions` (audit rows) need `agents.run.read` and see only the caller's tenant |
| `src/store/approvals.ts`, `src/runtime/approvals.ts`, `migrations/0002_approvals.sql` | Persisted approvals. `approval_request` holds each request under tenant RLS; `PgApprovalStore.record` is first-writer-wins (`UPDATE ... WHERE decided_at IS NULL`) behind the decision endpoint. `createPersistedApprovalPort` is the engine's approval gate: it stores the request (PII-free: tool, confidence, amount), polls for the first decision and, past `agent.approval_expiry_h`, records REJECTED/EXPIRED (BR-47.3) as `system:expiry` |
| `src/runtime/copilot.ts`, `src/api/copilot.ts` | Copilot chat backend, `POST /api/v1/copilot/chat` (duty `mcp.copilot.use`). Up to `AGENTS_COPILOT_MAX_TURNS` model turns with read tools only (`get`, `list`, `search`): each call goes to the gateway as the user, results are redacted and fed back. A tool outside the user's allowed set is answered AGT-0203 and never called (BR-47.11); a knowledge answer must pass the 0.75 citation gate or it is AGT-0212; answers are cached per tenant, user, duty set and mode for 5 minutes. A budget hold is reserved and settled per turn, and each turn is one audited run of agent `copilot` with an action row per tool call. `Accept: text/event-stream` gets `token` frames and one `done` frame (the panel's contract); anything else gets JSON. `mode=route` (also on `POST /api/v1/agents/copilot/conversations/{conversationId}/messages`, after `POST /api/v1/agents/copilot/conversations` returned a stateless id) classifies the prompt as `ask`, `navigate`, `act` or `clarify` with no tool: ordered rules in `src/domain/promptClass.ts` (act, then navigate, then ask, first match wins) decide without a model call, otherwise one no-tool model call through the same egress guard, output guard, budget hold and kill check answers `{kind, confidence}`, and a confidence below 0.6 or a top-two gap under 0.15 is `clarify` with two candidates. Route answers are always JSON, never cached, and a refusal (AGT-0201, 0202, 0206, 0218, 0219, 0220) is HTTP 200 `{refusal}` with no `kind` |

## Reaching the API from the shell (DEPOT-294)

There is no web BFF (ADR-0009): the shell reaches `/api/v1/agents/**`, copilot conversations included, through the API gateway (HTTPRoute `agents` on `depot-api`, see `deploy/gateway/README.md`) or the depot-server proxy. There is no cookie session and no token exchange: the gateway verifies the bearer token and the OPA duty check, and `createAuthenticator` verifies the token again here. Streamed responses and upstream problem bodies pass through unchanged. The handlers for definitions, availability, the approvals list and briefing narratives are not served by this service yet (`NOT-IMPLEMENTED.md`); each story that adds one must add its duty to `policies/authz/data.json` with a Rego case, because the gateway answers 403 for a path no duty names. Only `mcp.copilot.use` for the two conversation POSTs is in place. The briefing routes (`/api/v1/agents/briefings`, `:generate`, `{id}`, `{id}:mark-read`) are served under `agents.invoke` and `agents.run.read`.

## Configuration

| Variable | Meaning | Default |
|---|---|---|
| `DATABASE_URL` | Postgres, role `depot_app` (required) | |
| `MCP_URL` | Base URL of the MCP gateway (required) | |
| `AGENTS_JWT_ISSUER`, `AGENTS_JWT_AUDIENCE` | Issuer (realm URL) and audience of the callers' bearer tokens; the issuer is required | audience `depot-api` |
| `AGENTS_JWKS_URL` or `AGENTS_JWKS_FILE` | The realm's signing keys: its certs endpoint, or a mounted key set (chart `auth.jwksConfigMap`); one is required | |
| `AGENTS_PROVIDER` | `anthropic`, `openai` (OpenAI-compatible, Azure) or `bedrock` | `anthropic` |
| `AGENTS_BOUNDARY` | `standard` or `cui`. The copilot routes each call by the highest data class in its context: in `standard` no CUI or UNKNOWN record reaches a model, `cui` is refused at startup until agent runs and run steps are routed (DEPOT-303 slice 2) | `standard` |
| `AGENTS_AI_ENABLED` | `true` or `false`; anything else refuses to start. The AI sign-off gate: only an exact `true` lets `GET /api/v1/agents/availability` report `copilotEnabled: true`; `false` reports `copilotEnabled: false` with `signoffPending: true` and the web shows "not available in this deployment until sign-off". The chart default is `true`, the `cui` cloud profile sets `false` (`docs/runbooks/cui-deployment.md`) | `false` |
| `AGENTS_COMPLIANCE_URL` | Gateway base URL of services/compliance, where record data classes are resolved with agents' own signed service token (needs `DEPOT_SERVICE_SIGNING_KEY` or `DEPOT_SERVICE_SIGNING_KEY_FILE`, `DEPOT_SERVICE_NAME=agents` and a caller token with one `le` claim). Unset, or any of those missing: every record is UNKNOWN and is withheld from the model | unset |
| `AGENTS_AUDIT_URL` | Base URL of services/audit, where each copilot tool read is recorded (`POST /v1/audit/ai-data-access`, signed with agents' own service token, needs `DEPOT_SERVICE_SIGNING_KEY` or `_FILE` and a caller token with one `le` claim). Unset, or any of those missing: the write fails and every copilot call that read a tool result is refused with `AGT-0232` before the model is called | unset |
| `AGENTS_MODEL`, `AGENTS_COPILOT_MODEL`, `AGENTS_MODEL_OVERRIDES` | Model ids; the overrides are a JSON object of agent id to model id | `claude-sonnet-5-5` |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` + `AGENTS_OPENAI_BASE_URL`, `AWS_REGION` + `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` | Provider credentials, from a secret; a missing one fails the first run naming the variable | |
| `AGENTS_RUN_TOKEN_CAP`, `AGENTS_COPILOT_TOKEN_CAP`, `AGENTS_COPILOT_MAX_TURNS` | Per-run and per-chat token caps, chat turns | 20000, 8000, 8 |
| `AGENTS_TASK_CONNECT_TIMEOUT_MS` | How long `mode=task` waits for the MCP gateway session before answering from the catalog with no tool, 100 to 10000 | 2000 |
| `AGENTS_OUTPUT_GUARD_TIMEOUT_MS` | Deadline of the output guard on one answer or sentence, 100 to 10000; a miss withholds the answer (`AGT-0220`) | 2000 |
| `AGENTS_NEED_TO_KNOW_CACHE_MS` | How long the need-to-know switch read from the compliance service is reused, 1 to 60000; an answer built from CUI records is never cached | 10000 |
| `AGENTS_RUN_ENGINE`, `AGENTS_WORKFLOW_URL`, `DEPOT_SERVICE_PUBLIC_KEYS` | `inprocess` (dev fallback: a run is one promise in the pod) or `temporal` (each run is workflow-ts `agentRunWorkflow`, so a run resumes after an approval across pod restarts; needs the workflow URL and workflow-ts's public key, see "Runs as workflows") | `inprocess` |
| `AGENTS_APPROVAL_EXPIRY_H`, `AGENTS_SELF_APPROVAL_LIMIT`, `AGENTS_SELF_APPROVAL_CURRENCY` | BR-47.3 expiry (1 to 240 hours) and the self-approval limit | 72, 0, USD |
| `AGENTS_KILLSWITCH_POLL_MS` | How often each pod reads the shared kill switch (100 to 5000) | 1000 |
| `HTTP_ADDR`, `ADMIN_ADDR`, `MIGRATE_ON_START`, `AGENTS_DB_POOL` | Listeners and database | `0.0.0.0:8080`, `0.0.0.0:9090`, off, 10 |

## Runs as workflows

With `AGENTS_RUN_ENGINE=temporal` (FR-47.4, FR-47.8) `POST /api/v1/agents/{id}/runs` still answers 202 with the run id,
but the run is a Temporal workflow, `agentRunWorkflow` in `services/workflow-ts` (the only package with the Temporal SDK).
The run's logic stays here, as idempotent steps (`src/runtime/steps.ts`) over durable state (migration 0007), and the
workflow drives them as activities through `POST /api/v1/agents/runs/{id}/steps/{begin|plan|resolve|commit|abort}`.

**Service to service.** The two services authenticate each other with signed service tokens (the platform/go/tenant format,
Ed25519, 60 s, tenant-level, the run's user as actor). workflow-ts calls the step routes here (audience `agents`, grant
`agents.run.workflow`, verified in `src/api/serviceToken.ts` against `DEPOT_SERVICE_PUBLIC_KEYS`; the actor must be the user who
started the run). This service starts and wakes the workflow with its own token (`src/api/serviceSigner.ts`, audience
`workflow-ts`, grant `agents.run.signal`, key from `DEPOT_SERVICE_SIGNING_KEY[_FILE]`), verified by workflow-ts. workflow-ts
trusts `X-Depot-*` headers on its edge routes, so these routes take only the token and are not exposed at the edge; the wake is
authorized as the agents service, so approvers need no workflow duty. Charts: `workflowUrl` is the `workflows` Service,
workflow-ts's pods reach the API port as a generated peer (`ingressPolicy.peers`, `deploy/scripts/gen_networkpolicies.py`), and workflow-ts's `agentsUrl` is the `agent-runtime` Service.

1. `create` (the HTTP start, after the duty, kill switch, evaluation and budget checks) stores the input and the caller's
   token in `agent_run_state` and `agent_run_credential` and starts the workflow with ids only (the workflow id is
   `agent-run:{tenant}:{run}`, so a start retried after a timeout is the same run; a start that is refused removes the rows).
2. `begin` reserves the budget hold (one hold per run id) and opens the run. `plan` checks the kill switch, makes the one
   model call on pseudonymized text and stores the answer, its usage and the proposal's hash **before anything else can fail**
   (phase `called`), so a retried `plan` never calls the model again; then it applies the gates and risk and either opens the
   approval request (carrying the proposal hash) or goes straight to commit (AUTO).
3. The workflow waits for the approval as a signal (`agent-approval-decided`) with a timer for the BR-47.3 expiry. The signal only
   wakes it: `resolve` reads the stored decision, the source of truth, and with `expire` records REJECTED/EXPIRED for an undecided
   request (first writer wins against a human). It also re-reads hourly. A lost, refused or late wake never fails the run.
4. **The commit and the approver's session.** The tools run through the MCP gateway under a user's bearer token, and the platform has
   no delegated service credential the gateway accepts (workflow-ts's service tokens are verified by the services, not by mcp-ts), so
   the commit runs as the approver with the token their decision request carried. That token is kept in `agent_run_credential` only
   when the decision was given by that same user, on the request the run is waiting on; a replay of a decision by anyone else, or
   a decision for a run that is not waiting, stores nothing. The commit never falls back to the starter's token. Access tokens live
   five minutes: if the approver's token has expired when the run resumes (`resolve` or `commit`), the run is **put back to awaiting
   a new approval** (a new request, a fresh window) and the approver is asked again; it does not fail. A commit interrupted by a
   restart mid-call is never repeated (the run fails and says to check the target), and one that succeeded and then lost its audit
   write is finished COMMITTED without calling the tool again; the commit checks the proposal against the hash the approval carries.
5. Every step records its action row with `recordAction`, idempotent per `(run, seq)`; `finishRun` treats a run that is already
   finished as done, so a retried finish completes. A step that exhausts its retries (hours, for server and network errors) ends
   the run FAILED through `abort`, which retries without limit, closes the open approval request and purges the credentials.
6. **Retention (BR-47.7).** When a run ends its credentials are deleted and its prompt, pseudonym map and the model's answer with
   real values are emptied; `STATE_RETENTION_MS` (24 h) after that the state row is deleted by a sweep that runs for the tenant at
   each run start, together with any expired credential. Until then the prompt and map sit in plaintext in `agent_run_state` under
   tenant row-level security; there is no Vault transit in this service (not used anywhere in agents-ts), see NOT-IMPLEMENTED.md.

The in-process engine (`src/runtime/engine.ts`) stays as the dev fallback and is unchanged. Temporal tests are in
workflow-ts (`test/agentRun.test.ts`, CI only); the steps are tested here with fakes, including failures partway through each
step (`test/steps.test.ts`, `test/steps-api.test.ts`), and against Postgres (`test/runstate-pg.test.ts`).

## Run and test

```sh
cd services/agents-ts
npm install
npm test          # vitest: engine, domain and redaction tests
npm run typecheck
npm run build
```

Task-mode drafts derived from help chunks are untrusted model output: the route comes only from the catalog and every param and draft field is validated against the catalog's declared fields before a navigation intent is returned.

After changing a prompt (for example the task-mode instruction in `src/domain/routeCatalog.ts`), re-record the red-team replay with `REDTEAM_RECORD=1 npm run redteam` (see `evals/README.md`); a missing or stale row fails with MISSING or STALE. When apps/web modules legitimately shrink, refresh `test/fixtures/web-modules-snapshot.json` (the route catalog drift test says how).

Postgres-backed tests (`approvals-pg`, `budget-store`) run when `DEPOT_TEST_DATABASE_URL` (owner) and `DEPOT_TEST_APP_DATABASE_URL` (`depot_app`) are set, as in CI; without them they are skipped.

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
| `AGT-0220` | The output guard withheld the model answer (a token or record id outside this turn, a sensitive value the viewer holds no duty for, a raw identifier, a guard error or a missed `AGENTS_OUTPUT_GUARD_TIMEOUT_MS`); the user sees only the safe message, the stream carries `answer.withdrawn`, nothing is cached and the reason is on the `output.guard` row |
| `AGT-0219` | The egress guard blocked the model call (analyzer error, timeout or a finding that survived redaction); no provider call was made and the guard error is recorded on the run |
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

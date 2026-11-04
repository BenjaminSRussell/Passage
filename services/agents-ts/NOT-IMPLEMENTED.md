# NOT-IMPLEMENTED: passage-agents (PSG-47, wave 47)

Slice 1 built the run engine, the policy and budget rules, redaction and the
version registry, all with tests. Slice 2 adds the Anthropic and
OpenAI-compatible provider adapters (Azure OpenAI through the same adapter)
with a parity test, the kill-switch registry with fail-closed staleness, the
kill-switch guard before any provider call (AC-47.6), the payload builders for
`run.completed`, `budget.threshold` and `killswitch.changed` with the cost-guard
alert on the budget threshold, the provider failover with the 60 s rule and
residency port (BR-47.9), the token reconciliation comparison (9.47.9) and the
hybrid-search fusion and citation gate (FR-47.14). Slice 3 adds the
approval domain rules (`src/domain/approval.ts`: the BR-47.3 expiry parameter
check and the hourly sweeper's selection, and the decision rules with AGT-0203,
AGT-0208 and AGT-0209), the PII-free `approval.requested` payload in
`src/domain/events.ts`, open-amount checks in the BR-47.10 cash application
rule (`checkCashApplication`), and the BR-47.11 copilot duty gate and answer
cache (`src/domain/copilot.ts`), all with tests. Slice 4 adds the Amazon
Bedrock adapter (`src/providers/bedrock.ts`: Converse API with the forced
proposal tool, the same `toModelResponse` as the other adapters, in-tree SigV4
signing checked against AWS SDK reference signatures, parity with Anthropic in
`test/bedrock.test.ts`) and the five PRD agent definitions
(`src/agents/catalog.ts`: AP coding, cash application, MRP triage, collections,
anomaly), each with a prompt that treats document text as data, a model-visible
tool list of reads and one propose/draft tool, and AgentInvoke plus its own
duties. Slice 5 adds the first HTTP surface (`src/api/`, tested over a real
local server in `test/router.test.ts`): the kill switch toggle and status
(`GET`/`POST /api/v1/agents/kill-switch`, AC-47.6; idempotent by scope, the
event published before the local registry applies it), the approval decision
(`POST /api/v1/agents/runs/{run_id}/actions/{id}/decision`, FR-47.8;
idempotent by action id, tenant-scoped store, expiry recorded as
REJECTED/EXPIRED), and the worker-side kill switch feed
(`src/runtime/killswitch-feed.ts`) that applies consumed `killswitch.changed`
messages and sends malformed ones to the DLQ by throwing. The copilot web
module gains the kill switch screen. The rows below are not built.
"later slice of PSG-47" means the wave continues in a later build. "handoff"
means another owner must act.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Temporal `AgentRunWorkflow` (FR-47.4): activities per tool call with 60 s timeout and retry policy, 24 h run timeout incl. approval wait | later slice of PSG-47 | The engine is the workflow body without Temporal; wiring needs the `agents` Temporal namespace and worker |
| Multi-step run loop (FR-47.4 plan -> tool calls -> proposal): read-tool calls feeding further model turns before the proposal | later slice of PSG-47 | The engine runs one model turn that yields one proposal; a proposed read-only tool ends the run FAILED. The loop belongs with the Temporal workflow, where each tool call is an activity |
| Approval inbox integration (FR-47.8): publishing `dev.passage.agent.approval.requested.v1` from the engine's `ApprovalPort`, and resuming the waiting run with the recorded decision | later slice of PSG-47 | `approvalRequestedEvent` in `src/domain/events.ts` builds and validates the payload (redacted proposal, AGT-0210 on raw PII), and the decision endpoint records the decision through `ApprovalStore`. The inbox is wave 4's service and its consumer contract is not fixed; publishing needs the outbox, and resuming needs the Temporal signal |
| Persisted `ApprovalStore` behind the decision endpoint: `agent_action` rows with `approver_user_id`, `decision`, `decided_at`, written with `UPDATE ... WHERE decision IS NULL` under RLS | later slice of PSG-47 | The endpoint and its rules are built and tested against the port (`src/api/decision.ts`); no database layer exists yet, and nothing populates the open requests until the engine's approval gate writes them |
| HTTP API for runs, budgets, eval trigger, promotion, knowledge (`/api/v1/agents/...`, 9.47.5) | later slice of PSG-47 | Only the kill switch and the approval decision are served (`api/openapi.yaml`). The others need the persisted runs, budgets and versions |
| Persistence of `agent_version`, `eval_run` and `knowledge_chunk` tables, and partitioning of `agent_action` | later slice of PSG-47 | `agent_run`, `agent_action`, `token_budget`, `budget_hold` and `outbox` are built (`migrations/0001_agents.sql`, tenant RLS, `src/store/pg.ts`). `runAgent` requires `deps.audit` and `deps.budgets`: it opens the run as RUNNING before any model call, reserves a hold sized by the per-run cap (expiring after 24 h by the database clock; an expired hold is swept inside `reserve` and charged in full), settles it with the actual usage right after the model call so an approval wait never holds or loses spent tokens (the settle is retried 3 times; a provider that answered with an HTTP error status or a connection refused or not found (ECONNREFUSED, ENOTFOUND) releases the hold with zero, while a timeout or a mid-response drop keeps it, and a failed settle leaves it, for the full-cap sweep, with the run's `budgetState` UNKNOWN; provider usage that is not a non-negative safe integer is charged at the full cap), bounds each call before it is made (a call is refused when the UTF-8 byte length of the system text, prompt and tool schema together exceeds the input the run cap leaves, which bounds input tokens for any script; the provider gets a max output tokens limit of the cap less a four-characters-per-token estimate of that input, so a hold is overshot only when a provider reports more usage than that bound allows, charged in full and followed by no further call in the run), and at the end writes the action rows; an audit failure after the commit is logged, retried once as FAILED_AFTER_COMMIT and returns the COMMITTED result with `postCommitError`. `agent_run` rows can only change while RUNNING (trigger). `migrate()` repeats integration-hub-ts `pgStore.migrate`: there is no shared TS library yet. Budgets are per tenant per month, created lazily from the 50M/10M default. `openAgentStore(env)` requires `DATABASE_URL` and migrates on `MIGRATE_ON_START=true`, as integration-hub-ts does. Action rows are buffered to the run's end, so a crash mid-run leaves a RUNNING row without actions until the Temporal workflow records each step. There is no `main.ts` yet that calls `openAgentStore` |
| Cost-guard alert routing (AC-47.4): paging or notifying on `budget.threshold` | handoff | `budget.threshold` is written to the outbox when a tenant month budget crosses its soft percent or 100%; the consumer that routes it to an alert channel is live infrastructure |
| Outbox wiring for the other events (FR-47.5 to 47.12): `budget.threshold` is written to the outbox by `PgAgentStore.settle`; `run.completed`, `approval.requested` and `killswitch.changed` payloads are built in `src/domain/events.ts` but not yet written; `version.promoted` is not built | later slice of PSG-47 | `events/asyncapi.yaml` lists the planned types and the four schemas; channels stay empty until the relay and consumers exist |
| Kill switch delivery channel: the Kafka consumer (with consumer inbox) that calls `KillSwitchFeed.handle` and `heartbeat` on every worker, and the outbox writer behind the router's `publish` port (AC-47.6, BR-47.5) | later slice of PSG-47 | The feed and the registry are built and tested; a toggle reaches a second worker's registry through the feed in `test/api-handlers.test.ts`. The broker wiring, the database version sequence behind `KillSwitchVersionSource` and the heartbeat timer are not built |
| Durable audit of kill switch toggles and the kill-switch and promotion log inquiry (AC-47.6, 9.47.15) | later slice of PSG-47 | Every toggle carries actor, version and time in its `killswitch.changed` event; the audit row and the inquiry need the database slice |
| Bedrock credentials from Vault (FR-47.12, 9.47.10) and the embeddings call on every adapter (FR-47.13) | handoff | The adapter takes injected static credentials; the Vault projection belongs to the platform owner. Chat completion with tool use is built for all three adapters; embeddings come with the knowledge store |
| Provider health probe job (9.47.16, every 30 s) | later slice of PSG-47 | While failed over, `createFailoverProvider` retries the primary on one call per 30 s and returns to it when it answers; `reportPrimaryHealthy` is the hook for the probe. The scheduled probe job itself needs the scheduler |
| Residency data behind the failover check: the `residencyAllows` port is injected and tested, but wave 48 supplies the endpoint regions | handoff | Wave 48 owns tenant residency and endpoint regions |
| Daily token reconciliation job and persistence of its report (9.47.16) | later slice of PSG-47 | The pure comparison `reconcileRuns` (1% tolerance, BR-47.4 estimated flag) is built and tested in `src/domain/reconcile.ts`. The job needs persisted `agent_run` rows and the provider usage report |
| Wiring the five agent definitions (FR-47.5 to 47.7) to the runtime and their eval datasets: registering them at start-up, promotion after a passing eval, the run start selecting the pinned version and checking `requiredDuties` | later slice of PSG-47 | Definitions, prompts and tool lists are built in `src/agents/catalog.ts`. The tool names follow the PRD convention; they become real once the owning services annotate their OpenAPI with `x-passage-tool`. The run-start endpoint is not built, so nothing checks `requiredDuties` yet |
| Knowledge retrieval `passage.knowledge.search` (FR-47.14): the search tool, embedding store and pgvector index | later slice of PSG-47 | Hybrid fusion (reciprocal rank fusion) and the 0.75 citation gate with AGT-0212 are built in `src/domain/knowledge.ts`. The tool, the `knowledge_chunk` table and the index need wave 45 articles and the knowledge database |
| Payment-date prediction model (FR-47.15, AC-47.12) | later slice of PSG-47 | Python work (gradient-boosted trees) belongs in a Python service, outside these owned paths |
| Copilot chat BFF (`POST /api/v1/copilot/chat`, streaming) | later slice of PSG-47 | The shell panel calls it; no server code yet. The duty gate and answer cache it must use are in `src/domain/copilot.ts` |
| Responsible-AI: prompt hash logging and UI labels for generated text | later slice of PSG-47 | The UI shows the badge in the copilot module. Model cards are rendered by `evals/src/report.ts`; prompt hashing needs the registry table |
| Redaction scanner on persisted `agent_action.input_json` at write time (9.47.4 invariant) | later slice of PSG-47 | `scanPii`/`assertNoPii` exist and are tested; they run at the write path when the store exists |
| Identity: on-behalf-of token exchange (Keycloak `act` claim, FR-47.2) | handoff | Owned by edge-identity / platform (wave 2). The engine receives the invoker's duties |
| Wave-48 residency check and tenant residency data (BR-47.9, FR-47.12, AGT-0207) | handoff | Owned by wave 48 |
| Process entrypoint, Helm chart `charts/agent-runtime`, image and ServiceMonitor | later slice of PSG-47 | `createAgentsHandler` is a request listener with injected stores; there is no `main.ts` that starts it, because its stores, outbox and feed consumer do not exist yet. Nothing to lint |
| PII categories beyond EMAIL, IBAN and US SSN: person names, tax ids, wave-42 classification and country packs (FR-47.12, 9.47.20) | later slice of PSG-47 / handoff | The scanner and pseudonymizer cover the three pattern-detectable categories only. Names need the wave-42 PII classification of fields; country packs (Aadhaar, CURP/RFC, NIR) are owned by waves 42 and 48 |
| Approval expiry sweeper job (hourly, BR-47.3, 9.47.16) | later slice of PSG-47 | `expiredApprovals` selects the expired requests and `isApprovalExpired` drives the engine's `EXPIRED` handling; the scheduled job needs the persisted approval tasks |
| Concrete cash application agent applying `checkCashApplication` to its proposals | later slice of PSG-47 | The BR-47.10 rule (invoice limit, write-off tolerance, open amounts) is built and tested in `src/domain/policy.ts`, and the agent's prompt and tools are in `src/agents/catalog.ts`. Calling the rule on each proposal, with the wave-30 tolerance lookup, is not built |
| Agent runs, agent administration (mode, thresholds), eval dashboard and budget screens (9.47.6, 9.47.14) | later slice of PSG-47 | The copilot web module has the kill switch screen (`/copilot/kill-switch`). There is no runs, budgets, versions or admin API to list from yet |
| BR-47.11 on the copilot chat path: answering with AGT-0203 and serving only the asker's own cached answers | later slice of PSG-47 | `gateCopilotTools` and `CopilotAnswerCache` (keyed by tenant, user, duty set, mode and prompt, 5-minute TTL) are built and tested in `src/domain/copilot.ts`; the chat BFF that uses them is not. BR-47.8 (dataset re-baselining) is built in `evals/src/rebaseline.ts` |

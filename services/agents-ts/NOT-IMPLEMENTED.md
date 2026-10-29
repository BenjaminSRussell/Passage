# NOT-IMPLEMENTED: passage-agents (PSG-47, wave 47)

Slice 1 builds the run engine, the policy and budget rules, redaction and the
version registry, all with tests. The rows below are not built. "later slice of
PSG-47" means the wave continues in a later build. "handoff" means another
owner must act.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Temporal `AgentRunWorkflow` (FR-47.4): activities per tool call with 60 s timeout and retry policy, 24 h run timeout incl. approval wait | later slice of PSG-47 | The engine is the workflow body without Temporal; wiring needs the `agents` Temporal namespace and worker |
| Multi-step run loop (FR-47.4 plan -> tool calls -> proposal): read-tool calls feeding further model turns before the proposal | later slice of PSG-47 | The engine runs one model turn that yields one proposal; a proposed read-only tool ends the run FAILED. The loop belongs with the Temporal workflow, where each tool call is an activity |
| Approval inbox integration and `dev.passage.agent.approval.requested.v1` (FR-47.8) | later slice of PSG-47 | `ApprovalPort` is defined; the workflow inbox is wave 4's service |
| Approval decision endpoint and approver != invoker check (9.47.5, AGT-0209 at the API) | later slice of PSG-47 | HTTP layer not built. Engine-side checks exist in domain rules only |
| HTTP API (`/api/v1/agents/...`, copilot, knowledge, kill switch, budgets, eval trigger, promote) | later slice of PSG-47 | `api/openapi.yaml` has no paths yet; the endpoint list is in 9.47.5 |
| Persistence: `agent_run`, `agent_action`, `agent_version`, `token_budget`, `eval_run` tables, RLS, partitioning | later slice of PSG-47 | No database layer in this slice; state is in-memory per run |
| Events: `run.completed`, `approval.requested`, `budget.threshold`, `killswitch.changed`, `version.promoted` (FR-47.5 to 47.12) | later slice of PSG-47 | Outbox wiring needs the database slice. `events/asyncapi.yaml` lists the planned types only |
| Kill switch propagation within 10 s across workers (AC-47.6, BR-47.5) | later slice of PSG-47 | The engine checks an injected `killSwitch` before each step; the propagation channel is not built |
| Provider adapters: Anthropic API, Amazon Bedrock, Azure OpenAI, OpenAI-compatible open-weights (FR-47.13, AC-47.10) and feature parity test | later slice of PSG-47 | `ModelProvider` port only. Adapters need provider SDK approval and vault-held keys |
| Provider failover to a residency-checked secondary, 60 s rule (BR-47.9) | later slice of PSG-47 | Depends on adapters and the wave-48 residency data |
| Provider-reported token usage reconciliation to within 1% (9.47.9) | later slice of PSG-47 | Engine records tokens from the port; the daily reconciliation job is not built |
| Tenant and global kill switch endpoint and audit (AC-47.6) | later slice of PSG-47 | Engine honours the flag; the endpoint is not built |
| Concrete AP coding, cash application, MRP triage, collections and anomaly agents (FR-47.5 to 47.7) | later slice of PSG-47 | Only the policy and engine exist. Each agent needs its prompt, tools and eval dataset |
| Knowledge retrieval `passage.knowledge.search` (FR-47.14), pgvector index | later slice of PSG-47 | Depends on wave 45 articles and the knowledge database |
| Payment-date prediction model (FR-47.15, AC-47.12) | later slice of PSG-47 | Python work (gradient-boosted trees) belongs in a Python service, outside these owned paths |
| Copilot chat BFF (`POST /api/v1/copilot/chat`, streaming) | later slice of PSG-47 | The shell panel calls it; no server code yet |
| Responsible-AI: prompt hash logging, model card per version, UI labels for generated text | later slice of PSG-47 | The UI shows the badge in the copilot module; model cards need the registry table |
| Cost guard alerts (AC-47.4: budget alert raised at 80%) | later slice of PSG-47 | Budget state is computed; alert emission is the event slice |
| Redaction scanner on persisted `agent_action.input_json` at write time (9.47.4 invariant) | later slice of PSG-47 | `scanPii`/`assertNoPii` exist and are tested; they run at the write path when the store exists |
| Identity: on-behalf-of token exchange (Keycloak `act` claim, FR-47.2) | handoff | Owned by edge-identity / platform (wave 2). The engine receives the invoker's duties |
| Wave-48 residency check and tenant residency data (BR-47.9, FR-47.12) | handoff | Owned by wave 48 |
| Helm chart `charts/agent-runtime`, image and ServiceMonitor | later slice of PSG-47 | No entrypoint or image yet; nothing to lint |
| PII categories beyond EMAIL, IBAN and US SSN: person names, tax ids, wave-42 classification and country packs (FR-47.12, 9.47.20) | later slice of PSG-47 / handoff | The scanner and pseudonymizer cover the three pattern-detectable categories only. Names need the wave-42 PII classification of fields; country packs (Aadhaar, CURP/RFC, NIR) are owned by waves 42 and 48 |
| Approval expiry sweeper (hourly, BR-47.3, 9.47.16) | later slice of PSG-47 | `isApprovalExpired` and the engine's `EXPIRED` handling exist; the job needs the persisted approval tasks |
| Concrete cash application agent applying `checkCashApplication` to its proposals | later slice of PSG-47 | The BR-47.10 rule is built and tested; the agent that calls it (prompt, tools, wave-30 tolerance lookup) is not |
| Agent runs, agent administration and eval dashboard screens (9.47.6, 9.47.14) | later slice of PSG-47 | There is no runs, budgets or admin API to list from yet; the copilot web module shows the panel and the MCP tool catalog |
| Business-rule tests BR-47.8 (dataset re-baselining) and BR-47.11 (copilot answers with reason) | later slice of PSG-47 | Depend on the eval store and copilot chat path |

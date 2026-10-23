# Agents (MCP gateway, copilot, governed agents, evals)

## Purpose & Boundaries
Context `agents` (supporting; database `passage_agents`, operational tables only; wave 47, hooks in 30/36/46). Deployables `agents-ts` (MCP gateway at `/v1/agents/mcp` — NOT a separate deployable; copilot API; agent runtime on Temporal namespace `agents`), `agents-py` (eval harness at `evals/`) and `model-serving-py` in `services/agents-py/` (embedding, reranker, Presidio analyzer, anomaly scorer). Agents are advisory: no ERP process depends on the LLM; if provider down/budget-stopped/killed the manual flow proceeds. No business data persisted in the AI layer (index holds docs/metadata/catalogs only).

## Interfaces
- `POST /v1/agents/mcp` (MCP streamable HTTP; OAuth 2.1 delegated token; `agent.invoke`). ~400 generated tools named `passage.<domain>.<entity>.<verb>`; `tools/list` filtered by duty; tools `strict:true`, `additionalProperties:false`; output <= 200 rows + cursor, 64 KB cap. `x-passage-approval` ∈ none|suggest|approve_before_commit; every posting/financial method non-overridable `approve_before_commit`.
- Copilot: `POST /v1/agents/copilot/conversations`, `.../{id}/messages` (streamed). Read-only; fixed 9 core tools (`passage.meta.search_tools|describe_tool|call_tool`, `passage.analytics.metric.search|query`, `passage.platform.audit_trail.get|document_flow.get`, `passage.workflow.history.get`, `passage.help.search`); `call_tool` rejects any tool without readOnlyHint; max 8 tool calls/turn.
- `GET|POST /v1/agents/definitions`, `/definitions/{id}:disable`, `POST /v1/agents/kill-switch` (tenant|global), `POST /runs` (202), `GET /runs/{id}`, `/runs/{id}:cancel`, `GET /approvals`, `POST /approvals/{id}:decide`, `GET /budgets`, `PUT /budgets/{agentId}`, `GET /prompts`, `POST /eval-runs` (Svc:`eval.run`, 202), `/.well-known/oauth-protected-resource`.
- Five business agents (workflow shape; model called only at judgment step with no tools, structured output): `ap_coding`, `cash_application` (Haiku 5.5 extraction + Sonnet 5.5 ranking), `mrp_triage`, `collections_outreach`, `journal_anomaly` (classical ML scorer, threshold > 0.8). Write tools: `passage.ap.invoice.propose_coding`, `passage.ar.payment.propose_settlement`, `passage.planning.message.propose_action`, `passage.ar.collection_outreach.propose_draft`, `passage.ledger.journal.flag_anomaly`.

## Data Ownership
`agent_run`, `agent_action` (redacted; retained 7 years), `tool_definition`, `agent_version`, `prompt_version`, `eval_dataset`, `eval_run`, `agent_kill_switch`, budgets, `retrieval_chunk` (pgvector 1024-d, HNSW m=16 ef_construction=64, FTS tsv; corpus help|entity_meta|message|tool|metric; global rows tenant `00000000-0000-0000-0000-000000000000`; RLS tenant_access + global_read/global_write; roles `passage_indexer`). Copilot transcripts redacted 30 days (assumed).

## Events Produced
`agents.action.executed.v1`, `agents.approval.requested.v1`, `agents.run.completed.v1`, `agents.killswitch.changed.v1`, `agents.budget.threshold.v1` (80% SOFT, 100% HARD).

## Events Consumed
`agents-costguard` (action.executed, killswitch.changed, budget.threshold), `agents-triggers` (vendorinvoice.captured, sales.payment.received, dunningletter.issued).

## Dependencies
internal: platform (workflow inbox, tasks), edge-identity (token exchange, OPA), analytics (Cube), sales/procurement/ledger/manufacturing (tools), eventing-backbone. external: LLM provider via provider layer (Anthropic default `claude-sonnet-5-5`/`claude-haiku-5-5`/`claude-opus-5-5` judge; Bedrock, Azure, vLLM adapters), pgvector, Presidio, Temporal, Cube.

## Gotchas & Constraints
- Permission inheritance: OBO token (RFC 8693, `act`=agent, 15 min); OPA evaluated at gateway AND domain API; event-triggered runs act as configured run owner (OQ-3 delegation mechanism undecided — such runs cannot start until decided). Actor `agent:<agent-id>@user:<sub>`. NFR-SEC6: 0 actions exceeding the user.
- Untrusted data wrapping (`<untrusted-data source=...>`), PII pseudonymization `[TYPE_n]` stable 24 h, duty-aware re-hydration; `INSTRUCTIONS_IN_DATA` flag disables auto mode; no outbound tools; AP auto mode only codes a DRAFT invoice (never `:post`), requires calibrated confidence >= 0.95, amount < limit (default USD 5,000/EUR 4,500), no flags, every choice in candidate set.
- Kill switch <= 10 s: write table, OPA data, event + 2 s polling, signal open runs; runs -> KILLED. Run states PLANNING→ACTING→(AWAITING_APPROVAL→COMMITTED|REJECTED)|COMMITTED|FAILED|KILLED. Approval tasks expire 72 h; per-run cap 400k tokens; per-tenant monthly budget.
- Model params: never send temperature/top_p/top_k/budget_tokens, forced tool_choice or prefill; set `output_config.effort` explicitly; cache breakpoints BP1 (shared) BP2 (tenant); no timestamps in cached prefix.
- No graph store (Apache AGE revisit trigger); `document_flow.get` max_depth <= 6, max_nodes <= 50.
- Evals gate promotion: AP top-1 >= 0.92 / top-3 >= 0.98; cash precision >= 0.90, recall >= 0.75; refusal >= 0.99; McNemar paired gate; thresholds marked [ASSUMED — companion PRD]. Token metering weight for cache reads undecided (OQ-2).

## Source Sections
- Backend 5.19 (965-985); AI Feature Design (6749-7696): 2 (6791-6807), 3 (6809-6824), 4 (6826-6963), 5 (6965-7019), 6 (7021-7123), 7 (7125-7150), 8 (7152-7400), 9 (7402-7435), 10 (7437-7489), 11 (7490-7555), 12 (7557-7652), 13 (7654-7660), 14 (7662-7696); Events 3.9 (4080-4084)

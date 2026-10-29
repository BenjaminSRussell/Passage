# NOT-IMPLEMENTED: passage-mcp (PSG-47, wave 47)

Slice 1 built the gateway's pure core: tool model, catalog, OpenAPI tool
generation, JSON-RPC handler, the permission-inheriting gateway and the fetch
upstream. Slice 2 adds the HTTP transport (`src/server.ts`, `src/main.ts`), the
health probes, the `X-Passage-Api-Build` header, the gateway-side contract test
over 400 generated tools (`test/contract400.test.ts`), opaque read cursors
(`cursor` in, `nextCursor` out, MCP `_meta`), and per-pod `RateLimit-*` /
`Retry-After` headers with a 429 `MCP-0429` refusal, keyed by tenant and user.
Slice 3 validates read tools' upstream bodies against the tool's `outputSchema`
(`src/schema.ts`, 502 `MCP-0502` on a violation).
The rows below are what the ticket and PRD 9.47 ask for and are not built.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Server-initiated SSE stream on `GET /mcp` (streamable HTTP) | later slice of PSG-47 | Only JSON responses are offered; clients POST every message. The SSE stream needs session state for server-to-client messages |
| TLS for `mcp.passage.<tenant>` | handoff | Terminated at the Envoy Gateway (edge-identity owns certificates). The service listens on plain HTTP inside the cluster |
| Generating the catalog from real OpenAPI documents of every Passage service on deploy (tool catalog regeneration job, 9.47.16) | later slice of PSG-47 | Depends on the services' `x-passage-tool` annotations, which do not exist yet. `main.ts` loads one JSON document from `PASSAGE_OPENAPI_JSON` |
| `tool_definition` table and `GET` catalog inquiry (9.47.14) | later slice of PSG-47 | No database layer in this slice; the catalog is in-process |
| On-behalf-of token exchange (Keycloak `act` claim, 15-min token refresh by Temporal activity, FR-47.2) | handoff | Needs the platform/edge-identity token exchange; this gateway forwards the caller's token it is given |
| Verifying the bearer token at the edge | handoff | Envoy (`deploy/gateway`) verifies the JWT, strips client copies of `x-passage-tenant`, `x-passage-user` and `x-passage-duties`, and sets them from the claims; the MCP host must be routed through that gateway policy |
| Shared rate-limit store across gateway pods | later slice of PSG-47 | Counters are per pod (`src/ratelimit.ts`), so N pods allow up to N times the limit per user; the edge's per-tenant limit still applies. A shared Valkey store waits on the ADR. The 600 per minute default is an assumption, not a PRD figure |
| OPA decision at the gateway (`authz.Policy`) | handoff | The duty check is local (from the duty set); OPA stays authoritative at the API, which is the contract the gateway relies on |
| Live-API half of the 400-tool contract: the API itself returns 403 for a user without the duty (FR-47.2) | later slice of PSG-47 | The gateway side is covered over 400 generated tools; the services and their OpenAPI annotations do not exist yet |
| Audit to `agent_action` store (AC-47.2 `DENIED` log) | later slice of PSG-47 | The gateway calls an injected `AuditSink`; `main.ts` writes JSON lines to stdout. The durable sink is part of the runtime's database slice |
| Helm chart `charts/mcp-gateway` | later slice of PSG-47 | Image and chart are not built in this slice; the entrypoint now exists, so the chart can follow |
| Copilot chat BFF (`POST /api/v1/copilot/chat`) | later slice of PSG-47 | Not part of the gateway; see `apps/web/src/modules/copilot/NOT-IMPLEMENTED.md`. Model provider adapters are built in `services/agents-ts` |
| Full `outputSchema` conformance: `$ref`, `oneOf`/`allOf`, formats and patterns; `outputSchema` and `structuredContent` on the MCP wire | later slice of PSG-47 | `src/schema.ts` checks type, `nullable`, enum, properties, required and items on read tools only. `tools/list` does not advertise `outputSchema` yet, so results carry text content only and need no `structuredContent` |

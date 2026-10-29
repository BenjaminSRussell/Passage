# NOT-IMPLEMENTED: passage-mcp (PSG-47, wave 47)

Slice 1 builds the gateway's pure core: tool model, catalog, OpenAPI tool
generation, JSON-RPC handler, the permission-inheriting gateway and the fetch
upstream. The rows below are what the ticket and PRD 9.47 ask for and this slice
does not build.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| `/mcp` HTTP server (streamable HTTP and SSE transport), TLS, health endpoints | later slice of PSG-47 | Needs the service entrypoint and the platform HTTP kernel wiring; the JSON-RPC handler is ready to be mounted |
| Generating the catalog from real OpenAPI documents of every Passage service on deploy (tool catalog regeneration job, 9.47.16) | later slice of PSG-47 | Depends on the services' `x-passage-tool` annotations, which do not exist yet |
| `tool_definition` table and `GET` catalog inquiry (9.47.14) | later slice of PSG-47 | No database layer in this slice; the catalog is in-process |
| On-behalf-of token exchange (Keycloak `act` claim, 15-min token refresh by Temporal activity, FR-47.2) | handoff | Needs the platform/edge-identity token exchange; this gateway forwards the caller's token it is given |
| OPA decision at the gateway (`authz.Policy`) | handoff | The duty check is local (from the token's duty set); OPA stays authoritative at the API, which is the contract the gateway relies on |
| Contract test: 400 tools x user without duty receives 403 from gateway and API (FR-47.2) | later slice of PSG-47 | The 400-tool catalog and the live API do not exist yet; the gateway-side 403 is covered for one tool |
| Audit to `agent_action` store (AC-47.2 `DENIED` log) | later slice of PSG-47 | The gateway calls an injected `AuditSink`; the durable sink is part of the runtime's database slice |
| Read-tool pagination with cursors (FR-47.1 "cursor") | later slice of PSG-47 | Only the 200-row cap is enforced; the cursor contract depends on the upstream APIs |
| Rate-limit headers and `X-Passage-Api-Build` (API rules) | later slice of PSG-47 | Belongs to the HTTP layer |
| `outputSchema` validation against MCP spec (tool schema conformance test) | later slice of PSG-47 | `outputSchema` is carried but not validated |
| Helm chart `charts/mcp-gateway` | later slice of PSG-47 | No HTTP entrypoint or image yet; nothing to lint |
| Model provider adapters (FR-47.13) and the copilot chat BFF (`POST /api/v1/copilot/chat`) | later slice of PSG-47 | Not part of the gateway; see `services/agents-ts/NOT-IMPLEMENTED.md` |

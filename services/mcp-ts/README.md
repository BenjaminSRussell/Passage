# passage-mcp (wave 47, PSG-47)

The MCP gateway (`mcp.passage.<tenant>`, PRD 9.47 FR-47.1 and FR-47.2). It turns
Passage API operations marked `x-passage-tool` in OpenAPI into governed MCP tools.
Every call runs with the invoking user's token, so the Passage APIs and OPA
decide what the caller may do. The gateway holds no credentials of its own.

## What this slice contains

| Module | Responsibility |
|---|---|
| `src/tool.ts` | Tool model, name rule (`passage.<domain>.<entity>.<verb>`), validation, `tenant_id` stripping, MCP wire shape (`x-passage-duty`, `x-passage-approval`, annotations) |
| `src/catalog.ts` | Builds a validated catalog (duplicate names refused). `visibleTools` returns only tools whose duty the caller holds (AC-47.1) |
| `src/gateway.ts` | `callTool`: unknown tool, `tenant_id` rejection, duty check before any upstream call (AC-47.2), required-argument check, page cap of 200 rows, token forwarding with `traceparent` and `x-agent-run-id`, retries for idempotent tools only (BR-47.6), audit entry for every outcome |
| `src/openapi.ts` | `toolsFromOpenApi`: generates tool definitions from an OpenAPI 3.1 document; `tenant_id` and header parameters are left out of the input schema |
| `src/jsonrpc.ts` | MCP JSON-RPC handler: `initialize`, `tools/list`, `tools/call`, notifications |
| `src/upstream.ts` | `createFetchUpstream`: calls the Passage API with `fetch`, forwarding the user's bearer token |
| `api/openapi.yaml` | Contract for `POST /mcp` (the JSON-RPC wire) and the tool shape |

Pure logic is separated from I/O: the gateway takes `Upstream`, `AuditSink`,
`sleep` and `now` as injected interfaces, so the contract tests use fakes.

## Run and test

```sh
cd services/mcp-ts
npm install
npm test          # vitest: tool, jsonrpc, gateway contract tests
npm run typecheck # tsc --noEmit
npm run build     # tsc -p tsconfig.build.json
```

No database is needed. The tests do not touch the network.

## Wave reference

- FR-47.1 MCP gateway: `tool.ts`, `catalog.ts`, `openapi.ts`, `jsonrpc.ts`
- FR-47.2 Permission inheritance: `gateway.ts` (duty check, token forwarding), contract tests in `test/gateway.test.ts`
- AC-47.1 (`tools/list` filtered by duty), AC-47.2 (403 and `DENIED` audit)
- BR-47.6 (4xx not retried, 5xx retried 1 s / 4 s / 16 s)

## Error codes

| Code | HTTP / JSON-RPC | Meaning |
|---|---|---|
| `AGT-0203` | 403 (`-32003` in JSON-RPC) | The caller lacks the tool's `x-passage-duty`, or the API (OPA) refused the call; both are audited `DENIED` (PRD 9.47.18) |
| `AGT-0214` | upstream status, or 503 for an unreachable upstream | The tool failed; a 4xx is not retried (PRD 9.47.18) |
| `MCP-0400` | 400 | The caller supplied `tenant_id`; the tenant comes from the token |
| `MCP-0404` | 404 | Unknown tool name |
| `MCP-0422` | 400 | A required argument (for example a path parameter) is missing; the API is not called |
| JSON-RPC `-32600` | | Invalid request |
| JSON-RPC `-32601` | | Method not found |
| JSON-RPC `-32602` | | `params.name` is missing |

## Dependencies

Runtime: none. Development: `typescript`, `vitest`, `@types/node`, pinned to the
versions used by `apps/web`. No runtime dependency was added.

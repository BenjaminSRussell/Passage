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
| `src/server.ts` | HTTP transport on `node:http`: `POST /mcp` (single and batched JSON-RPC, 202 for notifications, 1 MiB body cap), `GET /healthz`, `GET /readyz`, `X-Passage-Api-Build` on every response. `principalFromHeaders` builds the caller from the bearer token and the edge headers |
| `src/main.ts` | Entrypoint: 12-factor config (`PASSAGE_API_URL`, `PASSAGE_OPENAPI_JSON`, `HTTP_ADDR`, `SERVICE_VERSION`), loads the tool catalog from the OpenAPI JSON, starts the server. Audit lines go to stdout as JSON without token or argument values |
| `src/ratelimit.ts` | Per-pod fixed-window limiter keyed by tenant and user; `RateLimit-*` and `Retry-After` headers. Counters are in memory, so each pod enforces its own limit until the shared-store ADR is settled |
| `src/schema.ts` | `schemaViolations`: validates a read tool's 200 body against its `outputSchema` (type, `nullable`, enum, properties, required, items; `$ref` and other keywords are not checked); the gateway returns `MCP-0502` on violation. Write tools are not checked, because their upstream has already committed |
| `src/upstream.ts` | `createFetchUpstream`: calls the Passage API with `fetch`, forwarding the user's bearer token |
| `api/openapi.yaml` | Contract for `POST /mcp` (the JSON-RPC wire), health probes and the tool shape |
| `Dockerfile`, `charts/mcp-gateway` | Image (distroless Node 22, nonroot; build from the repository root) and Helm chart: 3 replicas, read-only root filesystem, OpenAPI JSON mounted from the `openApi.configMap` ConfigMap, probes, ingress NetworkPolicy (`networkPolicy.ingressNamespaces` admits the edge) |

Identity at the HTTP edge: the bearer token is the user's Keycloak token, already
verified by the gateway. The tenant, user and duty set come from `x-passage-tenant`,
`x-passage-user` and `x-passage-duties`, which the edge sets from the verified token's
claims after stripping client copies (`deploy/gateway`). This service never decodes
the JWT. Requests without valid headers, or with the nil platform tenant, get 401;
ids are lowercased. This is the same trust model and the same rules as the kernel's
tenant middleware (`platform/go/tenant`).

Pure logic is separated from I/O: the gateway takes `Upstream`, `AuditSink`,
`sleep` and `now` as injected interfaces, so the contract tests use fakes.

## Run and test

```sh
cd services/mcp-ts
npm install
npm test          # vitest: tool, jsonrpc, gateway, 400-tool contract, server and rate-limit tests
npm run typecheck # tsc --noEmit
npm run build     # tsc -p tsconfig.build.json

# run locally against an API and its OpenAPI document as JSON
PASSAGE_API_URL=http://localhost:8080 PASSAGE_OPENAPI_JSON=./openapi.json HTTP_ADDR=127.0.0.1:8090 node dist/main.js
```

No database is needed. The server tests bind an ephemeral port on 127.0.0.1;
nothing else touches the network.

## Wave reference

- FR-47.1 MCP gateway: `tool.ts`, `catalog.ts`, `openapi.ts`, `jsonrpc.ts`
- FR-47.2 Permission inheritance: `gateway.ts` (duty check, token forwarding), contract tests in `test/gateway.test.ts`
- AC-47.1 (`tools/list` filtered by duty), AC-47.2 (403 and `DENIED` audit)
- BR-47.6 (4xx not retried, 5xx retried 1 s / 4 s / 16 s)
- FR-47.1 read paging: at most 200 rows per call, opaque `cursor` forwarded, upstream `nextCursor`/`next_cursor` returned in `result._meta.nextCursor`
- FR-47.2 contract over 400 generated tools: `test/contract400.test.ts` (gateway side)

## Error codes

| Code | HTTP / JSON-RPC | Meaning |
|---|---|---|
| `AGT-0203` | 403 (`-32003` in JSON-RPC) | The caller lacks the tool's `x-passage-duty`, or the API (OPA) refused the call; both are audited `DENIED` (PRD 9.47.18) |
| `AGT-0214` | upstream status, or 503 for an unreachable upstream | The tool failed; a 4xx is not retried (PRD 9.47.18) |
| `MCP-0400` | 400 | The caller supplied `tenant_id` (the tenant comes from the token), or a read `cursor` that is not a string of 1 to 512 characters |
| `MCP-0401` | 401 | Bearer token or edge identity headers missing or malformed, or the nil platform tenant |
| `MCP-0404` | 404 | Unknown tool name, or an unknown HTTP path |
| `MCP-0405` | 405 | Only POST is accepted on `/mcp` |
| `MCP-0413` | 413 | Request body over 1 MiB |
| `MCP-0422` | 400 | A required argument (for example a path parameter) is missing; the API is not called |
| `MCP-0429` | 429 | Per-pod rate limit for the tenant and user exceeded; `Retry-After` gives the wait. Limit is 600 per minute by default (`src/ratelimit.ts`); a batch counts once and holds at most 50 messages |
| `MCP-0500` | 500 | Internal error; no detail returned |
| `MCP-0502` | 502 | A read tool's upstream body does not match the tool's `outputSchema` (`src/schema.ts`, MCP tool schema conformance); audited as `UPSTREAM_ERROR` |
| JSON-RPC `-32600` | | Invalid request |
| JSON-RPC `-32601` | | Method not found |
| JSON-RPC `-32602` | | `params.name` is missing |

## Dependencies

Runtime: none. Development: `typescript`, `vitest`, `@types/node`, pinned to the
versions used by `apps/web`. No runtime dependency was added.

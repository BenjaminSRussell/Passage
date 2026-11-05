# Copilot module (wave 47, PSG-47)

The shell's copilot page (PRD 9.47 FR-47.3) and the MCP tool catalog
(9.47.14). Four copilot modes: ask the data, explain this record, draft,
summarize. Every answer shows its citations, the tool calls it made, and a
generated-text badge. The tool catalog lists the MCP tools the signed-in user
may call, with each tool's duty, approval mode and arguments. The agent kill
switch page (FR-47.12, AC-47.6) shows whether agent runs are halted and lets an
agents.admin halt or resume their tenant (a platform.global.admin holder also the global switch).

## Files

| File | Responsibility |
|---|---|
| `module.ts` | Module contract: `id`, `title`, routes `/copilot`, `/copilot/tools`, `/copilot/tools/:name`, `/copilot/kill-switch`, duty-gated `nav` (`mcp.copilot.use`; `agents.admin` for the kill switch). Route wrappers pass navigation and the URL parameter, so the pages render without a router. Auto-discovered by `modules/registry.ts` |
| `pages.tsx` | `CopilotHome`: page header and the panel |
| `CopilotPanel.tsx` | Mode group, message box (`FormField`), Send with a loading state, an empty state before the first answer, the error alert, and the answer: badge, text, Tool calls, Sources, Insert draft (draft mode only, on click) |
| `ToolCatalogPage.tsx` | Tool catalog list (`DataTable "Tools"`): tool, domain, duty, approval, access; filter by name or duty and approval mode; loading, empty, no-match and error (with Try again) states |
| `ToolDetailPage.tsx` | One tool: governance facts (duty, approval, read/write, retry safety) and its arguments table; loading, not-available and error states |
| `KillSwitchPage.tsx` | Agent kill switch: tenant and global switch state (badge, last change, author), halt or resume behind a confirmation dialog, the global switch only for platform.global.admin, warnings for a stale runtime or a global halt; loading, error (with Try again) and refused-toggle states |
| `domain.ts` | Pure rules: mode list and labels, the generated-text badge, `canInsertDraft` (only a non-refused draft, only on click), tool-call labels, approval-mode labels |
| `api/client.ts` | `postChat` to `POST /api/v1/copilot/chat`. The response is validated with zod, and a response not marked `generated: true` is rejected |
| `api/stream.ts` | `askCopilot`, the panel's client: asks for an event stream or JSON, passes streamed tokens to the panel, and validates the final answer with the same schema as `postChat`. `streamChat` and `parseSseFrames` are the SSE frame reader (`token`, `done`, `error`) |
| `api/agents.ts` | `getKillSwitch` and `setKillSwitch`: `GET`/`POST /api/v1/agents/kill-switch` (services/agents-ts/api/openapi.yaml), validated with zod; a refusal raises the problem's `code` and `detail` |
| `api/mcp.ts` | `listTools`: JSON-RPC `tools/list` to the MCP gateway at `/api/v1/mcp`, validated with zod |
| `pages.module.css` | Layout for the module, design tokens only |

## Run and test

```sh
cd apps/web
npx vitest --run src/modules/copilot
npx tsc --noEmit
```

No new npm dependencies. The module uses React, react-router, zod and
@testing-library/react, which `apps/web` already has.

## Wave reference

- FR-47.3 modes and citations: `pages.tsx`, `CopilotPanel.tsx`
- FR-47.1 / AC-47.1 and 9.47.14 tool catalog: `ToolCatalogPage.tsx`, `ToolDetailPage.tsx` (the gateway filters by duty)
- 9.47.6 rules: the badge is never hidden; drafts go in only on click (`canInsertDraft`); `Ctrl+/` is not built yet (see NOT-IMPLEMENTED.md)
- BR-47.11 refusal: a refusal from the API is shown with its code, and no draft is inserted

## Errors

The panel shows the API's `refusal.code` (for example `AGT-0203` when the user lacks
a duty, `AGT-0202` when the budget is exhausted) with its message. A non-2xx
response shows "Copilot is unavailable (HTTP <status>)".

## Needs

- Web BFF (shell owner, wave 5): forward `/api/v1/agents/kill-switch` to the
  agent runtime with the edge identity headers.
- Web BFF (shell owner, wave 5): forward `POST /api/v1/mcp` to the MCP gateway's
  `POST /mcp` (services/mcp-ts/api/openapi.yaml) with the user's token.
- The chat endpoint is served by the copilot BFF, which is a later slice (see
  NOT-IMPLEMENTED.md). Until both exist the pages show their error state.

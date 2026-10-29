# Copilot module (wave 47, PSG-47)

The shell's copilot page (PRD 9.47 FR-47.3) and the MCP tool catalog
(9.47.14). Four copilot modes: ask the data, explain this record, draft,
summarize. Every answer shows its citations, the tool calls it made, and a
generated-text badge. The tool catalog lists the MCP tools the signed-in user
may call, with each tool's duty, approval mode and arguments.

## Files

| File | Responsibility |
|---|---|
| `module.ts` | Module contract: `id`, `title`, routes `/copilot`, `/copilot/tools`, `/copilot/tools/:name`, duty-gated `nav` (`CopilotUse`). Route wrappers pass navigation and the URL parameter, so the pages render without a router. Auto-discovered by `modules/registry.ts` |
| `pages.tsx` | `CopilotHome`: page header and the panel |
| `CopilotPanel.tsx` | Mode group, message box (`FormField`), Send with a loading state, an empty state before the first answer, the error alert, and the answer: badge, text, Tool calls, Sources, Insert draft (draft mode only, on click) |
| `ToolCatalogPage.tsx` | Tool catalog list (`DataTable "Tools"`): tool, domain, duty, approval, access; filter by name or duty and approval mode; loading, empty, no-match and error (with Try again) states |
| `ToolDetailPage.tsx` | One tool: governance facts (duty, approval, read/write, retry safety) and its arguments table; loading, not-available and error states |
| `domain.ts` | Pure rules: mode list and labels, the generated-text badge, `canInsertDraft` (only a non-refused draft, only on click), tool-call labels, approval-mode labels |
| `api/client.ts` | `postChat` to `POST /api/v1/copilot/chat`. The response is validated with zod, and a response not marked `generated: true` is rejected |
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

- Web BFF (shell owner, wave 5): forward `POST /api/v1/mcp` to the MCP gateway's
  `POST /mcp` (services/mcp-ts/api/openapi.yaml) with the user's token.
- The chat endpoint is served by the copilot BFF, which is a later slice (see
  NOT-IMPLEMENTED.md). Until both exist the pages show their error state.

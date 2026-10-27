# lean web module (PSG-24 slices 1 and 2)

Front end for `services/lean`. Discovered by the shell through `module.ts`. Pages use the
`shell/ui` primitives and `pages.module.css` (tokens only), as in `docs/design-system.md`.

## Screens

| Route | Screen | Calls | Duty |
|---|---|---|---|
| `/lean` | Overview: links to each area | none | `lean.read` |
| `/lean/board` | Kanban board: one column per state, filter by flow or rule, WIP and overdue counts and, for a flow with a takt time, takt vs the slowest activity's cycle time (FR-24.14), a "To <next state>" button and a column picker per card; refused moves keep the card in place and show the LEAN code (AC-24.7); refreshes every 30 s | `GET /api/v1/lean/board`, `POST /kanbans/{id}:move` | `lean.kanban.work` |
| `/lean/rules` | Kanban rule list, optionally for one flow | `GET /api/v1/lean/kanban-rules` | `lean.read` |
| `/lean/rules/new` | Create a rule; fixed rules get the BR-24.7 suggestion for the number of cards, event rules an event source and exact-quantity flag | `GET /flows`, `POST /kanban-rules:fixed-count`, `POST /kanban-rules` | `lean.kanban-rule.write` |
| `/lean/rules/:id` | Rule detail: facts, its cards, generate kanbans (fixed: count; event: demand and its reference, AC-24.6) | `GET /kanban-rules/{id}`, `GET /board?ruleId`, `POST /kanbans:generate` | `lean.read` / `lean.kanban.write` |
| `/lean/flows` | Production flow list with status filter | `GET /api/v1/lean/flows` | `lean.read` |
| `/lean/flows/new` | Flow designer: activities with kind, cycle time, output location and predecessors (FR-24.8) | `POST /api/v1/lean/flows` | `lean.flow.write` |
| `/lean/flows/:id` | Flow detail: activities (missing outputs flagged), activate from a date; LEAN-0004 shown when not connected | `GET /flows/{id}`, `POST /flows/{id}:activate` | `lean.read` / `lean.flow.write` |
| `/lean/subcontracting` | Lean subcontracting preview: mode, purchase agreements and completed service jobs; the PO lines with price, amount and jobs, LEAN-0005 when no agreement covers a job (FR-24.20, BR-24.16) | `POST /api/v1/lean/subcontracting:preview` | `lean.flow.write` |

Every command sends an `Idempotency-Key`; moves send the card version the board read.

## Run and test

```
cd apps/web
npx vitest --run src/modules/lean
npx tsc --noEmit
```

`pages.test.tsx` stubs `fetch` and renders the module's routes in a MemoryRouter. It covers the
list, empty, error and detail states, flow create and activation (LEAN-0004), rule create with
the count suggestion, event generation (AC-24.6), a full fixed rule (LEAN-0002), board moves
with version, the refused move (AC-24.7), the 30-second refresh, takt vs cycle (FR-24.14) and
the subcontracting preview (AC-24.17, LEAN-0005), plus the in-repo WCAG checks.

## Needs (for the shell owner, wave 5)

- Board push refresh over SSE (FR-24.10): needs an event-stream helper in the shell; the board
  polls every 30 s meanwhile (the PRD 4.24.19 fallback).
- Drag between columns: needs a shell pointer/drag helper; the board uses buttons and a column
  picker, which are touch-friendly (44 px hit areas from `Button size="sm"`).
- Card print (GS1 DataMatrix) goes through the wave-32 print service; no print button yet.
- Item, site, value-stream and location pickers: forms take ids as UUIDs until shared lookups exist.

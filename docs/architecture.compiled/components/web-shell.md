# Web Shell and Module Remotes (apps/web)

## Purpose & Boundaries
One React 18 + TypeScript strict + Vite shell at `apps/web` (wave 5 owns `apps/web/src/shell`, `apps/web/vite.config.ts`) hosting 16 module remotes: platform, org, party, product, ledger, inventory, procurement, manufacturing, sales, warehouse, treasury, workforce, projects, integration, analytics, agents. Metadata-driven form system `@passage/forms`. Browser never holds an OAuth token. Modules live at `apps/web/src/modules/<id>/module.ts`; each has no package.json.

## Interfaces
- Module contract: default export `{ id, title, routes: RouteObject[], nav: NavItem[], slots?, i18nNamespace? }`; NavItem `{id, label, to, privilege, group?, scope?: 'legal-entity'|'tenant', featureFlag?}`; every route carries `handle: { title, privilege?, scope? }`. Discovery by `import.meta.glob('../modules/*/module.ts')`; run-time loading from `/settings/env.json → modules[<id>].remoteEntry` (`<id>-remoteEntry-nocache.js`); dev/test falls back to lazy glob.
- Routing: LE-scoped `/le/:le/...` (LE code e.g. `USMF`), tenant-level `/admin`, `/integration`, `/analytics`, `/agents`; `/le/all/...` cross-entity. Deep links `/r/:entity/:id`, `/r/doc/:documentNumber`, `/r/task/:taskId`. 108 hand-built routes + generated `entityRoutes(entity, opts)` (list, new, :id, :id/lines/:lineId). Sends header `Passage-Legal-Entity: <uuid|*>` on every API call + `filter=legalEntityId eq '<uuid>'` defense-in-depth. react-query keys start `[tenantId, leId, ...]`.
- API: browser calls `/api/v1/<context>/...` (BFF rewrite); mutations send `Idempotency-Key` (UUIDv7 per user intent) and `If-Match`; status changes only via actions.
- Runtime config `/settings/env.json` (env, bffBase `/bff`, apiBase `/api/v1`, otel, modules, featureFlags, build) rendered from Helm values; no secrets.

## Data Ownership
absent — the source document does not address this (browser storage conveniences only: last-used LE, drafts in IndexedDB; never P3, never tokens).

## Events Produced
absent — the source document does not address this

## Events Consumed
absent — the source document does not address this (SSE stream from web-bff).

## Dependencies
internal: web-bff, platform (entity-definitions, form-definitions, message-catalog, saved-views), edge-identity. external: React, react-router-dom 6.x, @tanstack/react-query 5.x, zod 3.x, i18next, react-aria-components, @tanstack/react-table + react-virtual, big.js, @module-federation/vite, vite-plugin-pwa, OTel web SDK.

## Gotchas & Constraints
- Modules add NO npm dependencies; may import only federation singletons, `@passage/*`, relative paths. CI script `apps/web/scripts/check-module-imports.ts` fails otherwise; no cross-module or shell-internal imports (except `contracts.ts`). Federation `shared` singleton + strictVersion; React/ReactDOM/router/@passage/ui eager in host.
- Workspace packages (proposed `packages/@passage/*`, Q1): ui, forms, api-client (generated per release), session, offline, gs1, i18n, telemetry. Design system in-repo `@passage/ui` assumed (Q2).
- Remote entries `Cache-Control: public, max-age=60, must-revalidate`; hashed assets immutable; `env.json` no-store; one nginx image `passage-web` serves shell+versioned remotes (N-2 retained).
- Performance budgets: shell warm FCP < 1.5 s, LCP < 2.5 s, INP < 200 ms p75, shell JS <= 220 KB gzip, remote <= 120 KB, forms <= 150 KB.
- A11y WCAG 2.1 AA: shell owns the only `<h1>`; focus to h1 on navigation; shell-owned live regions; remotes use `useAnnounce()`; custom widgets only from react-aria-based primitives; keyboard model (Ctrl/Cmd+K palette, etc.); 44 px targets in PWAs.
- Permission gating (`<Can>`) is presentational; handle 403 `sod_conflict`/`approval_required` inline. P3 reveal state-only, re-mask after 60 s.
- Languages EN, DE, FR, ES first; formats via `Intl` (DE `1.234,56`, FR `1 234,56`, IN lakh, CA YYYY-MM-DD).
- Approval inbox at `/inbox`; tasks deep-linkable from email/Slack/Teams.

## Source Sections
- Frontend (4708-5874): 1 (4712-4740), 2.1-2.2 (4744-4835), 3 (4861-5156), 4 (5158-5258), 5 (5260-5346), 6, 7 (5360-5453), 9 (5695-5784), 10 (5786-5816), 12 (5835-5840), 13-14 (5842-5874)

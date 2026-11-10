# Web shell (wave 5)

Keyboard-first host at `apps/web/src/shell`. Workspaces come from `modules/*/module.ts`. The metadata-driven form renderer consumes the wave-3 `MetadataCatalog` interface; this slice ships `fakeCatalog()` with `platform.note`.

Design system, tokens, primitives and accessibility rules: see `apps/web/docs/design-system.md`.

User-facing text follows `apps/web/docs/ux-writing.md`. Server failures render through `ProblemMessage` (`src/shell/errors`), which maps problem codes to plain messages from `src/shell/errors/catalog.ts` and keeps the code and request id under "Details".

## Needs

- `@module-federation/vite` and the OTel web SDK are still pending. Federation remotes are declared in `vite.config.ts` as `federationRemotes`.
- Playwright and `@axe-core/playwright` are now present. `e2e/` holds the end-to-end suite (`a11y.spec.ts` scans every route plus the open palette and mobile drawer for serious/critical WCAG 2.1 AA violations; `shell.spec.ts` covers skip link, landmarks and keyboard choreography; `forms.spec.ts` covers Notes validation). Run it with `make test-e2e` or `npx playwright test` in `apps/web`; first-time setup is `npx playwright install chromium`. Unit-level a11y proofs still run under vitest + the in-repo checker (`a11y.ts`).

## Workspaces and navigation (#498)

The sidebar shows one persona workspace at a time, not every module. All of it is data under `src/shell/workspaces/`:

- `workspaces.json`: the 8 business areas and the 10 persona workspaces. A workspace's `entries` are nav paths ordered by how often the persona uses them; the first five are its top tasks. Keep each workspace to 10-20 entries.
- `navAudit.json`: one row for every `modules/*/module.ts` nav entry with its `area`, a `keep`, `merge` or `advanced` decision, an optional task-language `taskLabel`, and `mergeInto` for merges. `navAudit.test.ts` fails when a module entry has no row, so classify a new entry in the same change that adds it. `keep` entries are listed under their area in All features, `advanced` and `merge` entries under the area's Advanced group; nothing is unreachable.
- `model.ts` loads and validates both files. `loadWorkspaceConfig(override)` merges a per-tenant override (replace a workspace or area by id, add new ones, `hide` ids); pass the result to `<Shell workspaceConfig={...}>`. Keys are strict and ids must be unique; an invalid override is recorded as an error and the shipped config is used. Nothing fetches an override yet.
- Duty gating runs first: the shell hands the workspace code only the modules the principal's duties allow, so a curated entry the user may not open never appears in a workspace, All features or search.
- A user holds several workspaces (every one with a visible entry, minus the ones they dropped, so workspaces gained later appear on their own) and switches with the Workspace selector. The default is the workspace with the most duty-gated entries the user can see; home shows at most four. The choice is kept in this browser only (`localStorage`).
- Labels in the sidebar, All features and search are task labels. The page title stays the module's own label.

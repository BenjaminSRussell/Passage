# Web shell (wave 5)

Keyboard-first host at `apps/web/src/shell`. Workspaces come from `modules/*/module.ts`. The metadata-driven form renderer consumes the wave-3 `MetadataCatalog` interface; this slice ships `fakeCatalog()` with `platform.note`.

Design system, tokens, primitives and accessibility rules: see `apps/web/docs/design-system.md`.

User-facing text follows `apps/web/docs/ux-writing.md`. Server failures render through `ProblemMessage` (`src/shell/errors`), which maps problem codes to plain messages from `src/shell/errors/catalog.ts` and keeps the code and request id under "Details".

## Needs

- `@module-federation/vite` and the OTel web SDK are still pending. Federation remotes are declared in `vite.config.ts` as `federationRemotes`.
- Playwright and `@axe-core/playwright` are now present. `e2e/` holds the end-to-end suite (`a11y.spec.ts` scans every route plus the open palette and mobile drawer for serious/critical WCAG 2.1 AA violations; `shell.spec.ts` covers skip link, landmarks and keyboard choreography; `forms.spec.ts` covers Notes validation). Run it with `make test-e2e` or `npx playwright test` in `apps/web`; first-time setup is `npx playwright install chromium`. Unit-level a11y proofs still run under vitest + the in-repo checker (`a11y.ts`).

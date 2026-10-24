# Web shell (wave 5)

Keyboard-first host at `apps/web/src/shell`. Workspaces come from `modules/*/module.ts`. The metadata-driven form renderer consumes the wave-3 `MetadataCatalog` interface; this slice ships `fakeCatalog()` with `platform.note`.

## Needs

- `@module-federation/vite`, Playwright, `@axe-core/playwright`, and the OTel web SDK. `apps/web` is a shared lockfile; waves cannot add npm dependencies. A11y proofs run under vitest + an in-repo WCAG 2.1 AA checker (`a11y.ts`). Federation remotes are declared in `vite.config.ts` as `federationRemotes`.

# Depot web app

React and TypeScript, built with Vite. The keyboard-first shell lives in `src/shell`; each domain is a module under `src/modules/<name>` with a `module.ts` that registers its routes.

```sh
npm ci
npm run dev          # http://localhost:5173
npx tsc --noEmit     # type check
npx vitest run       # unit and component tests
npx playwright test  # end-to-end and accessibility suite (run `npx playwright install chromium` once)
```

The repository README explains the dev proxy and how to point it at running services.

## Guardrail suite

`e2e/guardrails` holds the cross-surface checks no single story owns: duty-profile permissions, the core flow with the AI layer failing, the accessibility sweep, the write-ordering sweep and the keyboard-only journey. Per-story specs keep their own assertions.

```sh
npx playwright test --project=guardrails --project=guardrails-ap-clerk --project=guardrails-planner --project=guardrails-read-only
node scripts/guardrail-report.mjs   # writes test-results/guardrails/traceability.json and .md
```

- The guardrail projects and their dev servers (ports 4175 to 4178, one per duty set) exist only when a command line names `guardrails`; the `chromium` project ignores `e2e/guardrails`.
- CI runs the suite as the `ci / web guardrails` job when a pull request changes the home, the copilot module, the command bar or the suite. `gate` is the branch-protection check and fails when that job fails. Marking `gate` as a required status check is a repository setting, not code.
- The traceability report maps each scenario to the epic outcomes O1 to O10 and is uploaded as the `guardrail-traceability` artifact. Paths whose product slice is not built are listed as `pending <ticket>` in `e2e/guardrails/registry.json`; when a slice lands, move its entry to `present` and add its scenario to `write-guard-sweep.spec.ts`.
- Both themes (light and dark) run at 375px and 1280px. `axeAllowlist.json` must not gain a `guardrail-` row.
- Not built: the nightly run against the real stack.

## Docs

- [docs/design-system.md](docs/design-system.md): tokens, layout, primitives and accessibility rules.
- [docs/accessibility-conformance-report.md](docs/accessibility-conformance-report.md): the WCAG 2.2 AA conformance report (draft), with the evidence for each criterion and the known gaps.
- [docs/ux-writing.md](docs/ux-writing.md): voice, sentence case, task verbs, the error message pattern and the glossary.
- [src/shell/README.md](src/shell/README.md): the shell host and what it still needs.

## Contributing

- Follow the design system and the UX writing guide for anything a person can see or hear.
- Show server failures with `ProblemMessage` from `src/shell/errors`, and add catalog entries in `src/shell/errors/catalog.ts` for problem codes your screen can hit.
- Keep spec ids (FR-, BR-, AC-), waves and slices out of user-facing text; `src/shell/plainLanguage.test.ts` checks this.

Pull request checklist for changes to `apps/web`:

- [ ] `npx tsc --noEmit` and `npx vitest run` pass.
- [ ] Copy review: user-facing text follows [docs/ux-writing.md](docs/ux-writing.md) (sentence case, task verbs, glossary terms, no spec ids, waves or slices; errors say what happened and what to do).
- [ ] New screens and states are reachable by keyboard and pass the axe suite.

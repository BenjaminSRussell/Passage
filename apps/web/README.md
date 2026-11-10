# Passage web app

React and TypeScript, built with Vite. The keyboard-first shell lives in `src/shell`; each domain is a module under `src/modules/<name>` with a `module.ts` that registers its routes.

```sh
npm ci
npm run dev          # http://localhost:5173
npx tsc --noEmit     # type check
npx vitest run       # unit and component tests
npx playwright test  # end-to-end and accessibility suite (run `npx playwright install chromium` once)
```

The repository README explains the dev proxy and how to point it at running services.

## Docs

- [docs/design-system.md](docs/design-system.md): tokens, layout, primitives and accessibility rules.
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

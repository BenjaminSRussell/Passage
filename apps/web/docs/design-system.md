# Passage web design system and shell redesign

Implementation spec for `apps/web`. Follow it as written. No new npm dependencies: plain CSS (global `theme.css` + CSS Modules per component, Vite handles both), inline SVG icons, system-ui fonts. Every visual value comes from a token in section 1. No inline styles, no ID selectors, no `!important`. Use logical properties (`margin-inline-start`, `inset-inline-end`, `padding-block`) everywhere so RTL works.

**Hard rules (each one keeps an existing test or contract working):**
- R1. Pages that tests render without a router (`ProductHome`, `CatalogPage`, `WorkflowHome`, `InboxPage`, `JobMonitorPage`, `AuditViewer`, `EngineeringHome`, `WhereUsedPage`, `FormRenderer`, `MetadataForm`) must not use `Link`, `NavLink` or router hooks. When they need navigation, a `*Route` wrapper in `module.ts` passes a callback, the way `MetadataRoute` already does.
- R2. Pages keep their synchronous default props. Do not add `useQuery` to pages that tests render without a `QueryClientProvider`.
- R3. A `<label>` element contains only the field name. Put "Optional" and hints in sibling elements, because `getByLabelText("Component")` matches exactly.
- R4. `StatTile` puts a literal space between label and value (`{label}{" "}{value}`), so `"Masters 50"` still matches.
- R5. Only the shell live region and the existing page-level `role="status"`/`role="alert"` nodes carry those roles. `StatusPill`, `Skeleton` and `EmptyState` never do.
- R6. jsdom has no `scrollIntoView` or `matchMedia`. Call `el.scrollIntoView?.({ block: "nearest" })`, and do responsive behaviour in CSS only.
- R7. Keep exact accessible names: `Search`, `Find a page`, `Apply saved view`, `Skip to content`, `Find parents`, `Save Note`, `Workspaces`.

- R8. Status colours and labels come from one standard, `src/shell/ui/status.ts` (`statusTone`, `standardLabel`, used by `StatusPill`). Neutral means nothing to do, info in progress, success done or in force, warning a person should look, danger failed or refused. A page does not keep its own status tone table, and a status is never shown by colour alone.
- R9. The living component catalog is the page `/design-system` (`src/shell/pages/ComponentCatalogPage.tsx`). It renders each shared component from the real code with its usage rule. Add a new shared component there when you add it to `shell/ui`.

## 1. Design tokens (`src/shell/theme.css`)

Declare on `:root`. Dark values go under `@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) {…} }` and again under `:root[data-theme="dark"]`. Add `<meta name="color-scheme" content="light dark">` to `index.html`. `body` uses `--color-bg-app` and `--color-text`.

| Token | Light | Dark | Use |
|---|---|---|---|
| `--color-bg-app` | `#f7f8fa` | `#0f1115` | app background, sidebar |
| `--color-bg-surface` | `#ffffff` | `#181b22` | cards, top bar, tables, palette |
| `--color-bg-muted` | `#f0f2f5` | `#1c2028` | hover, zebra rows, skeleton base |
| `--color-border` | `#e3e6eb` | `#2a2f39` | dividers, card edges (decorative) |
| `--color-border-input` | `#858d9b` | `#6b7484` | input and checkbox boundaries (non-text 3:1) |
| `--color-text` | `#14171f` | `#e8eaee` | body text |
| `--color-text-muted` | `#5b6372` | `#a0a7b4` | secondary text, group labels, hints |
| `--color-accent` | `#4f46e5` | `#818cf8` | primary button bg, links, active indicator |
| `--color-accent-hover` | `#4338ca` | `#a5b4fc` | primary hover |
| `--color-on-accent` | `#ffffff` | `#0f1115` | text on accent |
| `--color-accent-subtle` | `#eef0ff` | `#22263a` | active nav bg, selected option |
| `--color-accent-strong` | `#3730a3` | `#c7d2fe` | text on accent-subtle |
| `--color-success` / `-bg` | `#15803d` / `#ecfdf3` | `#4ade80` / `#0f2a1a` | |
| `--color-warning` / `-bg` | `#a15c07` / `#fff8eb` | `#fbbf24` / `#2e2210` | |
| `--color-danger` / `-bg` | `#b42318` / `#fef3f2` | `#f87171` / `#2d1414` | danger button bg uses `--color-danger`, text `--color-on-danger` (`#ffffff` light, `#0f1115` dark) |
| `--color-info` / `-bg` | `#175cd3` / `#eff6ff` | `#60a5fa` / `#0f1f38` | |
| `--color-scrim` | `rgb(15 17 21 / 0.48)` | `rgb(0 0 0 / 0.6)` | drawer/palette backdrop |

**Type.** `--font-sans: system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;` `--font-mono: ui-monospace, "SF Mono", Menlo, Consolas, monospace;`
| Token | Size / line-height | Weight | Use |
|---|---|---|---|
| `--text-xs` | 12px / 16px | 500 | badges, kbd, table meta |
| `--text-sm` | 13px / 20px | 400 | nav, table cells, hints |
| `--text-md` | 14px / 22px | 400 | body default, inputs, buttons (600) |
| `--text-lg` | 16px / 24px | 600 | top bar `h1`, card titles |
| `--text-xl` | 20px / 28px | 600 | page header `h2` |
| `--text-2xl` | 28px / 36px | 600 | stat tile values (`font-variant-numeric: tabular-nums`) |
Define as `--text-sm-size`/`--text-sm-leading` pairs. Weights: `--weight-regular: 400; --weight-medium: 500; --weight-semibold: 600`. Headings use `letter-spacing: -0.01em`.

**Spacing** (4px base): `--space-1: 4px; --space-2: 8px; --space-3: 12px; --space-4: 16px; --space-5: 20px; --space-6: 24px; --space-8: 32px; --space-10: 40px; --space-12: 48px`.
**Radii:** `--radius-sm: 6px` (inputs, buttons, nav items), `--radius-md: 8px` (cards, tables), `--radius-lg: 12px` (palette, drawer), `--radius-pill: 999px`.
**Shadows:** `--shadow-sm: 0 1px 2px rgb(16 24 40 / 0.06)`; `--shadow-md: 0 4px 12px rgb(16 24 40 / 0.08)`; `--shadow-lg: 0 16px 48px rgb(16 24 40 / 0.18)`. In dark mode, all three become `0 0 0 1px var(--color-border)` plus the same blur at 0.5 alpha.
**Focus:** `--focus-ring: 0 0 0 2px var(--color-bg-surface), 0 0 0 4px var(--color-accent)`. Apply it with `:focus-visible { outline: 2px solid transparent; box-shadow: var(--focus-ring); }`. The transparent outline keeps the ring visible in Windows High Contrast mode.
**Motion:** `--duration-fast: 120ms` (hover, colour), `--duration-base: 180ms` (palette, collapse), `--duration-slow: 240ms` (drawer); `--ease-out: cubic-bezier(0.2, 0, 0, 1)`.
**Layout:** `--sidebar-width: 256px; --topbar-height: 56px; --content-max: 1200px; --reading-max: 72ch; --palette-width: 640px`. The drawer breakpoint is the literal `960px`, because CSS variables do not work in media queries. It is named `nav-drawer` in comments.
**Density** (on the shell root `[data-density]`):
| Token | comfortable | compact |
|---|---|---|
| `--control-height` | 44px | 36px |
| `--nav-item-height` | 40px | 32px |
| `--row-height` | 44px | 32px |
| `--cell-padding-inline` | `var(--space-4)` | `var(--space-3)` |
| `--content-padding` | `var(--space-8)` | `var(--space-6)` |

**Global rules in theme.css:** box-sizing reset, `body{margin:0;font:var(--text-md-size)/var(--text-md-leading) var(--font-sans);-webkit-font-smoothing:antialiased}`, `.visually-hidden` (the clip pattern), `.skip-link` (section 6), `kbd` styling (`--text-xs`, mono, 1px `--color-border`, `--radius-sm`), `code` in mono, and `@media (prefers-reduced-motion: reduce)`, which sets every `--duration-*` to `0.01ms` except where section 6 names a fade.

## 2. Layout (`Shell.tsx`, `Sidebar.tsx`, `TopBar.tsx`, `Shell.module.css`)

```
<div class=root data-density>                 ← existing data-density contract
  <a class="skip-link" href="#main">Skip to content</a>
  <div class=frame>  grid: var(--sidebar-width) minmax(0,1fr); min-block-size:100dvh
    <aside id="sidebar" class=sidebar data-open>   sticky, block-size 100dvh, bg-app, border-inline-end
      brand row (56px) · search trigger · <nav aria-label="Workspaces"> · footer
    <main id="main" tabIndex={-1}>
      <div class=topbar>  sticky top 0, 56px, bg-surface, border-block-end, z 10
        [menu button <960px] [breadcrumb] <h1 tabIndex={-1}>{title}</h1> … [right slot]
      [offline Alert, when offline]
      <div class=content>  max-inline-size var(--content-max); margin-inline auto; padding var(--content-padding)
        <RouteErrorBoundary key={pathname}><Suspense fallback={<PageSkeleton/>}><Outlet/></Suspense></RouteErrorBoundary>
  <div role="status" aria-live="polite" class="visually-hidden">{announce}</div>
  {paletteOpen && <CommandPalette …/>}
```
- **`h1` placement.** The `h1` is the current page title in the top bar, and it is the only `h1` in the app. The brand becomes `<Link to="/" class=brand>` holding an `aria-hidden` logo SVG and the text "Passage". `MetadataForm`'s `<h1>` becomes `<h2>`. Remotes and pages start at `h2` (MFE rule).
- **Title resolution** (`src/shell/pageMeta.ts`, a pure function): `resolvePage(pathname, modules, shellNav) → { title, workspace?: { title, to } }`. Resolve in this order:
  1. An exact match against `shellNav` and then every module `nav` (`matchPath({ path: to, end: true })`). The title is the nav label, and the workspace is that module (title, first nav `to`).
  2. Otherwise, the first module route whose `path` matches. The title is `route.handle?.title ?? module.title`.
  3. Otherwise, the title is `Page not found`.

  Also set `document.title = \`${title} · Passage\``. Give `/product/:id` the route `handle: { title: "Product details" }`.
- **Breadcrumb.** `<nav aria-label="Breadcrumb"><ol><li><Link to={workspace.to}>{workspace.title}</Link></li></ol></nav>`, followed by an `aria-hidden` chevron and then the `h1`. Leave it out on shell pages (Home, Notes, Not found). Colour `--color-text-muted`, `--text-sm`; on hover, the text colour changes and an underline appears.
- **Right slot.** `Button variant="ghost" size="sm"` "Apply saved view" (the existing behaviour, announcing "Saved view applied"). Next to it, a `Button variant="ghost" size="sm" aria-pressed={compact}` "Compact view". It flips local `density` state, and "Apply saved view" also sets density from the saved view. The root renders `data-density={density}`.
- **Sidebar.**
  - Brand row: 28px accent logo tile, "Passage" `--text-lg`.
  - Search trigger: a full-width button styled like an input (bg-surface, 1px `--color-border-input`, `--radius-sm`, height `--control-height`). It contains a search icon, the text "Search" and `<kbd aria-hidden="true">⌘K</kbd>`, which reads `Ctrl K` when `navigator.platform` does not match `/Mac|iP/`. Attributes: `aria-label="Search"`, `aria-keyshortcuts="Control+K Meta+K"`, `aria-haspopup="dialog"`.
  - Nav: first an ungrouped list of the shell entries (Home, Notes), then one group per module in registry order. Each group header is a `<button aria-expanded aria-controls={listId}>` with a workspace icon, the module title (`--text-xs`, `--weight-medium`, `--color-text-muted`) and a chevron that rotates 90° over `--duration-base`. The `ul` has `aria-labelledby` pointing at the header button id. Groups start expanded, and collapse state is in-memory. A group that contains the active route is forced open on navigation.
  - Items: `<NavLink end>`, height `--nav-item-height`, `padding-inline var(--space-3)`, `--radius-sm`, `--text-sm`. Hover uses `--color-bg-muted`. `[aria-current="page"]` gets `--color-accent-subtle` bg, `--color-accent-strong` text, `--weight-medium`, and a 3px inline-start bar in `--color-accent`, so colour is not the only cue.
  - The nav area scrolls (`overflow-y:auto; overscroll-behavior:contain`).
- **Below 960px (drawer).**
  - The frame becomes one column. The top bar shows `<button aria-label="Open navigation" aria-expanded={drawerOpen} aria-controls="sidebar">` with a menu icon. It is `display:none` at 960px and above.
  - The sidebar becomes `position:fixed; inset-block:0; inset-inline-start:0; inline-size:min(var(--sidebar-width), 85vw)`. Closed state: `transform: translateX(-100%)` (flipped for RTL with `:dir(rtl)`) and `visibility:hidden`, so it cannot be focused. Open state: `data-open="true"` gives `transform:none; visibility:visible; box-shadow: var(--shadow-lg)`, and a scrim sibling `<div class=scrim onClick=close aria-hidden>` covers the page.
  - While open, the sidebar gets `role="dialog" aria-modal="true" aria-label="Navigation"`, shows a top `<button aria-label="Close navigation">` (hidden at 960px and above), traps focus with `useFocusTrap`, and locks body scroll.
  - Open moves focus to "Close navigation". Esc, the scrim, the close button or choosing a link closes the drawer and returns focus to "Open navigation".
  - Transition: `transform var(--duration-slow) var(--ease-out)`.
- **Route change** (`useEffect` on `pathname`, skipped on first render): focus the `h1` with `preventScroll`, scroll the content to the top, and close the drawer. Hide the `h1` focus outline (`h1:focus{outline:none}`), because it is not interactive.
- **Offline.** `useOnlineStatus()` listens to `online`/`offline`. While offline, show `Alert tone="warning"` under the top bar (copy in section 7).

## 3. Command palette (`src/shell/CommandPalette.tsx` + `.module.css`)

Props: `{ entries: PaletteEntry[]; onSelect(entry): void; onClose(): void; announce(msg: string): void }`, where `PaletteEntry = { id: string; group: string; label: string; to: string }`. The shell builds entries from `shellNav` (group "General") plus each module's nav (group `module.title`), in registry order.

```
<div class=overlay onMouseDown={e => e.target === e.currentTarget && onClose()}>     fixed inset 0, scrim
  <div role="dialog" aria-modal="true" aria-labelledby="palette-title" class=panel>  inline-size min(640px, 100vw - 32px); margin-block-start 12vh
    <h2 id="palette-title" class="visually-hidden">Search</h2>
    <div class=inputRow> (56px, border-block-end) search icon
      <label htmlFor="global-search" class="visually-hidden">Find a page</label>
      <input id="global-search" role="combobox" aria-expanded={hasResults} aria-controls="palette-listbox"
             aria-autocomplete="list" aria-activedescendant={hasResults ? `palette-option-${active}` : undefined}
             aria-describedby="palette-help" autoComplete="off" spellCheck={false}
             placeholder="Search pages and workspaces"/>
      <Button variant="ghost" size="sm" aria-label="Close search">× icon</Button>
    <p id="palette-help" class="visually-hidden">Use the up and down arrows to move through results, then press Enter to open a page.</p>
    {hasResults ? <div id="palette-listbox" role="listbox" aria-label="Pages" class=list>   max-block-size min(420px, 60vh); overflow-y auto
        per group: <div role="group" aria-labelledby={gid}><div id={gid} role="presentation" class=groupLabel>{group}</div>
          <div role="option" id={`palette-option-${i}`} aria-selected={i===active} onMouseMove={()=>setActive(i)} onClick={()=>onSelect(e)}>
            workspace icon · {label} · (when selected) aria-hidden "↵"
      : <div class=empty> search icon · <p>No pages match “{query}”.</p><p>Try a workspace name, like Admin or Tax.</p></div>}
    <footer aria-hidden="true" class=footer><kbd>↑</kbd><kbd>↓</kbd> to move · <kbd>↵</kbd> to open · <kbd>Esc</kbd> to close</footer>
```
- **Filtering.** Case-insensitive `includes` against `label` or `group` (the current rule). Groups keep their order, and empty groups are dropped. `i` is the flat index across all visible options. Every query change resets `active` to 0.
- **Keys on the input.**
  - `ArrowDown`/`ArrowUp` move `active` and wrap. Call `preventDefault` so the caret stays put.
  - `Enter` calls `onSelect(visible[active])` and does nothing when there are no results.
  - `Escape` calls `stopPropagation` and then `onClose()`.
  - `Tab`/`Shift+Tab` cycle only between the input and "Close search" (`useFocusTrap`).
  - After `active` changes, run `document.getElementById(optionId)?.scrollIntoView?.({ block: "nearest" })`.
- **Selected option.** `--color-accent-subtle` bg, `--color-accent-strong` text, `--radius-sm`, height `--nav-item-height`. Group label: `--text-xs`, `--color-text-muted`, `padding-inline var(--space-3)`.
- **Announcements.** Debounce 250ms, then call `announce("{n} results")`, `announce("1 result")` or `announce("No results")`. Clear the timer on unmount so it cannot overwrite "Opened {label}".
- **Shell side.** Ctrl/Cmd+K (`preventDefault`) opens the palette, or selects the input text if it is already open, and announces "Search opened". Keep the `clickTrace("search")` call on the trigger, but do not read the traceparent aloud. On open, store `document.activeElement`. `onClose` closes and restores focus to the stored element, falling back to the search trigger. `onSelect` calls `navigate(to)`, closes the palette and announces `Opened {label}`. In that case focus moves to the new page `h1` (route-change rule) instead of the stored element. Lock body scroll while the palette is open.
- **Motion.** The overlay fades in over `--duration-base`. The panel animates from `opacity 0; transform: translateY(-4px) scale(0.98)` with `--ease-out`. Under reduced motion, use opacity only at `--duration-fast`. Input text uses `--text-lg`. The panel uses `--radius-lg` and `--shadow-lg`.

## 4. Primitives (`src/shell/ui/`, each a `.tsx` + `.module.css`, re-exported from `ui/index.ts`)

`src/shell/ui/index.ts` is the design-system boundary, and the only shell path that modules may import. It is the seam for extracting `@moonaan`/`@passage/ui` later. Every prop interface is exported.

| Component | Props | Spec |
|---|---|---|
| `Icon` | `name: IconName; size?: 16\|20` | Inline SVG, 24 viewBox, `stroke="currentColor"`, `stroke-width 1.75`, `fill="none"`, always `aria-hidden="true" focusable="false"`. Set: `search menu close chevronRight chevronDown home note shield calendarCheck cog database receipt box percent workflow inbox alertTriangle checkCircle info xCircle lock grid`. `src/shell/workspaceIcons.ts` maps module id → icon (`admin→shield, close→calendarCheck, engineering→cog, metadata→database, payables→receipt, product→box, tax→percent, workflow→workflow`, fallback `grid`). |
| `Button` | `variant?: "primary"\|"secondary"\|"ghost"\|"danger"` (default secondary); `size?: "sm"\|"md"`; `loading?: boolean`; `icon?: IconName`; plus native button props; `type` defaults to `"button"` | Height `--control-height` (md) / 32px (sm, with a transparent `::after` hit area that reaches 44px tall). `--radius-sm`, `--text-md` 600, `gap var(--space-2)`. Primary: accent/on-accent. Secondary: surface, 1px `--color-border-input`. Ghost: transparent, hover `--color-bg-muted`. Danger: danger/on-danger. `loading` sets `aria-disabled="true"` + `aria-busy="true"` (never native `disabled`, so focus stays on the button) and ignores clicks and submits, swaps the icon for a 14px spinner (a rotating border; reduced motion shows a static ring), and keeps the label text so the accessible name does not change. Disabled: opacity 0.55, `cursor:not-allowed`. |
| `PageHeader` | `title: string; description?: ReactNode; actions?: ReactNode; meta?: ReactNode` | `<header>` flex, wraps. `<h2>` holds exactly `title` at `--text-xl`. Description `--color-text-muted`, max `--reading-max`. `margin-block-end var(--space-6)`. |
| `Card` / `CardGrid` | `Card: { title?: string; headingLevel?: 2\|3; actions?; children; padded?: boolean }`; `CardGrid: { children; label?: string; as?: "ul"\|"div" }` | Surface, 1px border, `--radius-md`, `--shadow-sm`, padding `--space-5`. `CardGrid` is `grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap var(--space-4)`. |
| `StatGroup` / `StatTile` | `StatGroup: { label: string; children }` renders `<ul aria-label={label}>` as a grid `repeat(auto-fit, minmax(180px,1fr))`. `StatTile: { label: string; value: ReactNode; tone?: "neutral"\|"danger"\|"warning"; hint?: string }` renders `<li>` | Card styling. DOM order: label (`--text-sm` muted), then `" "`, then value (`--text-2xl`, tabular-nums). R4. A non-neutral tone adds a 6px dot plus the value colour, and only when the value is above 0. |
| `DataTable<T>` | `label: string; columns: { key: string; header: string; align?: "start"\|"end"; mono?: boolean; render?(row: T): ReactNode }[]; rows: T[]; getRowKey(row: T, i: number): string; loading?: boolean; empty?: { title: string; description?: string }; onRowOpen?(row: T): void` | Wrapper `<div class=scroll>` (horizontal scroll, `--radius-md` border); only when it actually overflows it becomes `role="region" aria-label="{label} table" tabIndex={0}`. Inside, `<table aria-label={label}>` with `<th scope="col">`. The `thead` (bg-muted, `--text-xs` 600 muted) is not sticky. Rows use `--row-height`, zebra `tbody tr:nth-child(even)` uses bg-muted at 50% (`color-mix`), hover uses bg-muted, and cell padding is `--cell-padding-inline`. `align:"end"` sets `text-align:end` + tabular-nums. `loading`: 5 rows of `Skeleton` cells with `aria-busy="true"` on the table. Empty: one `<td colSpan>` holding `EmptyState` (compact, no icon tile). With `onRowOpen`, the first column renders `<button class=rowLink>` (text = cell value, accent colour, underline on hover/focus), and clicking the row delegates to it. Rows themselves never get a tabindex. |
| `Badge` / `StatusPill` | `Badge: { tone: "neutral"\|"success"\|"warning"\|"danger"\|"info"; children }`; `StatusPill: { status: string }` | Pill, `--text-xs` 500, `padding 2px var(--space-2)`, tone `-bg` with tone text, plus a 6px dot in tone colour. `StatusPill` map → label/tone: `active, released, preferred → "Active"/"Released"/"Preferred" success`; `ended → "Completed" success`; `executing, running → "Running" info`; `pending, draft, open → sentence-case, neutral`; `escalated, overdue → warning`; `failed, error → "Failed" danger`. Unknown values show sentence case in neutral. Text is always present (R5: no role). |
| `FormField` | `id: string; label: string; hint?: string; error?: string; optional?: boolean; children: (a11y: { id; "aria-describedby"?; "aria-invalid"? }) => ReactNode` | Column, gap `--space-1`. `<label htmlFor={id}>` holds the label text only (R3). An "Optional" span (muted) is a sibling inside a row div. Hint: `<p id={id+"-hint"}>`. Error: `<p id={id+"-error"} role="alert">` with the `xCircle` icon, in danger colour. `aria-describedby` joins hint and error ids. |
| `Input` / `Select` / `Textarea` | native props + `invalid?: boolean` | Height `--control-height` (textarea min 96px, `resize: vertical`), 1px `--color-border-input`, `--radius-sm`, bg-surface, `padding-inline var(--space-3)`. Hover: text-muted border. Invalid: danger border + `aria-invalid`. `Select` uses `appearance:none` with a chevron as an inline SVG data URI background. A checkbox `Input` is 18px with `accent-color: var(--color-accent)`. |
| `EmptyState` | `icon?: IconName; title: string; description?: string; action?: ReactNode; headingLevel?: 2\|3; compact?: boolean` | Centered, `padding-block var(--space-12)` (compact: `--space-8`). 40px icon tile (accent-subtle bg, accent-strong icon). Title is the heading at `--text-lg`. Description is muted, max 48ch. Action below with `--space-4` gap. |
| `Skeleton` | `width?: "full"\|"3/4"\|"1/2"\|"1/4"; height?: "text"\|"control"\|"tile"` | `aria-hidden="true"`, bg-muted, `--radius-sm`, shimmer (`background-position` loop over 1.2s). Reduced motion: a static block. The loading container gets `aria-busy="true"` plus visually hidden "Loading…" text. |
| `Alert` | `tone: "info"\|"success"\|"warning"\|"danger"; title?: string; children?; action?: ReactNode; live?: "alert"\|"status"\|"off"` (default `"off"`) | Tone bg, 1px tone border at 40% (`color-mix`), `--radius-md`, a tone icon, padding `--space-3 --space-4`. `live` sets `role`. The node stays mounted, and with no children it collapses (`:empty`, so no padding or border, and not `display:none`) so live regions announce reliably. |
| `WorkspacePlaceholder` | `title: string; description: string; icon: IconName` | `PageHeader` (title) + `Card` holding `EmptyState` (headingLevel 3, title "This workspace isn't available yet", description, `Badge tone="neutral"` "Coming soon"). Action: `<Link to="/">` styled as a secondary button, "Browse workspaces". Router-only use. |

Shared hooks live in `src/shell/hooks/`. `useFocusTrap(ref, active)`: on Tab, cycle through the tabbable elements inside `ref`, and focus the first element on activate. `useOnlineStatus()`: returns a boolean.

## 5. Page inventory

Every module page uses `PageHeader` (h2). The state matrix applies: **Offline** is handled at shell level (banner). **Partial** cannot happen, because each page has one data source. **No permission** cannot happen yet, because the client gets no duties and nav is not filtered. When duties arrive, hide the nav entries the user lacks, and send direct hits to `EmptyState icon="lock"` "You don't have access to this page" / "Ask your workspace administrator for access."

| Route | Polished page | Primitives | Loading / empty / error |
|---|---|---|---|
| `/` (new, shell `HomePage`) | h1 "Home". `PageHeader` "Workspaces", description "Open a workspace, or press ⌘K to jump to any page." `CardGrid as="ul" label="Workspaces"`: one `Card` (h3 = module title, workspace icon tile, `module.description`, `ul` of `Link`s for every nav entry with a `chevronRight`) per module, in registry order. | PageHeader, CardGrid, Card, Icon | Static, no fetch. Empty (no modules): `EmptyState` "No workspaces yet" / "Workspaces appear here once they're installed." |
| `/notes` (new, shell `NotesPage`) | h1 "Notes". `PageHeader` "Note form", description "A preview of a form generated from entity metadata." `Card` wrapping `<FormRenderer entity={catalog.get(demoNote.id) ?? demoNote}/>`, which moves here from `Shell`. | PageHeader, Card | Synchronous |
| `*` (shell `NotFoundPage`) | `EmptyState` (h2) "We couldn't find that page" / "Check the address, or search for the page you need." Action: link "Go to home". | EmptyState | — |
| `/admin` AdminHome | `PageHeader` "Admin workspace" + the existing copy. `CardGrid` of 6 `Card`s (h3), each linking to its sub-page with a one-line description (section 7). | PageHeader, CardGrid, Card | Static |
| `/admin/audit` AuditViewer | `DataTable label="Audit entries"`. Columns: Field, User, Time (`<time dateTime>`, mono, raw ISO kept for the test), Old, New, Trace (mono, muted). | DataTable | Empty (the default `items=[]`): "No audit entries yet" / "Changes to records appear here with who made them and when." |
| `/admin/sequences`, `/admin/documents`, `/admin/texts` | `PageHeader` with the existing copy as description (Texts: "Language versions with placeholder tokens, used by templates and notes."). `Card` → `EmptyState` with the copy in section 7. | PageHeader, Card, EmptyState | Empty by design |
| `/admin/alerts` AlertInbox | `ul` of `Alert tone="warning"` items. | Alert, EmptyState | Empty (default): "No alerts right now" / "When a batch run fails or an approval is overdue, you'll see it here." |
| `/admin/import` ImportExportPage | `Card` holding `FormField id="csv" label="CSV data" hint="Paste comma-separated rows. The first row names the columns."` + `Textarea` (mono, 8 rows). | Card, FormField, Textarea | — |
| `/close`, `/payables`, `/tax` | New `pages.tsx` per module exporting `CloseHome`/`PayablesHome`/`TaxHome` = `WorkspacePlaceholder` (copy in section 7). `module.ts` uses `createElement(…)` in place of `null`. | WorkspacePlaceholder | Static |
| `/engineering` EngineeringHome | `PageHeader` "Engineering data" (exact) + description. `Card` (h3 "Where-used") explaining the report. No links (R1). | PageHeader, Card | Static |
| `/engineering/where-used` WhereUsedPage | `Card` holding the form in a row (wraps below `md`): `FormField` "Component" + `Input required`, `FormField` "As of date" `optional` + `Input type=date`, `Button variant="primary" loading={searching}` "Find parents". Below: `Alert tone="danger" live="alert"` (always mounted, holds `error`); `<p role="status">` results summary (muted `--text-sm`); results `DataTable label="Where-used results"` (Product mono, Level end-aligned + indent `level-1` × `--space-4` in the Product cell, BOM version mono, Site). | Card, FormField, Input, Button, Alert, DataTable, EmptyState | Loading: button loading + `DataTable loading` (keep the existing `!searching` table guard; skeleton rows are a table, so they only show while `searching`). Empty: no table. `EmptyState compact` "No parents found" / "Try another component or a different date." Error: Alert text unchanged and no table. Input values survive errors. |
| `/metadata/:entity?` MetadataForm | `PageHeader` = schema title (h2), description = the existing sentence. `Card` holding `FormField` per property, in a 2-column grid at `lg` (1 column below), `Button primary` "Save {title}". Success: `Alert tone="success" live="status"` "{title} saved." | PageHeader, Card, FormField, Input, Select, Button, Alert, Skeleton | Loading: 4 skeleton fields + visually hidden `aria-live="polite"` "Loading {entity} metadata…". Error: `Alert tone="danger" live="alert" title="We couldn't load this form"` with the message and a `Button` "Try again" (bumps a `reload` counter in the effect deps). Save error: inline Alert; fields keep their values. |
| `/product` ProductHome | `PageHeader` "Product information management". `StatGroup label="PIM tiles"` with 4 `StatTile`s: Products, Masters, Released, Incomplete attributes (`tone="warning"`). | PageHeader, StatGroup, StatTile | Static props |
| `/product/items` CatalogPage | `DataTable label="Products"`: Number (mono), Name, Subtype (`Badge neutral`, "Master"/"Product"), State (`StatusPill`). The optional prop `onOpen(number)` becomes `onRowOpen`. `module.ts` uses a `CatalogRoute` wrapper that calls `navigate(\`/product/${n}\`)`. | DataTable, Badge, StatusPill | Empty: "No products yet" / "Products you create or import appear here." |
| `/product/released` | `PageHeader` + `Card` → `EmptyState` "No released products yet" / existing sentence as description. | | |
| `/product/:id` ProductDetail | `PageHeader` = product.name, meta `StatusPill status="active"`. `Card` holding a `<dl>` 2-column grid: Number (mono), GTIN (mono). | PageHeader, Card, StatusPill | — |
| `/workflow` WorkflowHome | `PageHeader` "Workflow and batch". `StatGroup label="Workflow tiles"`: Pending work items, Running jobs, Failed runs (`tone="danger"`). | StatGroup, StatTile | Static |
| `/workflow/inbox` InboxPage | `DataTable label="Work items"`: Document (mono), Step, Submitted by, Due (`overdue` → `Badge warning` "Overdue"), Status (`StatusPill`). | DataTable, StatusPill, Badge | Empty: "You're all caught up" / "Approvals assigned to you appear here." |
| `/workflow/designer` | `PageHeader` + description. `ol aria-label="Approval steps"` as a vertical stepper: numbered 24px circles (accent-subtle) joined by a 1px border line, with each step in a `Card`. | PageHeader, Card | — |
| `/workflow/jobs` JobMonitorPage | `DataTable label="Batch runs"`: Job (mono), Status (`StatusPill`), Rows (end-aligned, tabular), ETA (end). | DataTable, StatusPill | Empty: "No batch runs yet" / "Scheduled and on-demand jobs appear here as they run." |
| `/workflow/templates` | `Card` holding the list `aria-label="Shipped templates"`, each item as `<code>` + `Badge neutral` "Shipped". | Card, Badge | — |

`RouteErrorBoundary` (a class component in `src/shell/`) catches render errors from any page and shows `Alert tone="danger" live="alert" title="This page couldn't load"`, "Something went wrong while showing this page. Your other workspaces still work." plus `Button` "Try again" (resets state) and a "Go to home" link. Add `description?: string` to `PassageModule` (optional, backward compatible) and fill it in for each module.

## 6. Accessibility choreography

- **Focus order:** skip link → brand → search trigger → nav (General, then each group header followed by its items) → in `main`: menu button (<960px) → breadcrumb → right slot → page content. The `h1` is reachable programmatically only.
- **Skip link:** `.skip-link` is absolutely positioned at `inset-inline-start: var(--space-4)`, `inset-block-start: -100px`. On `:focus` it moves to `inset-block-start: var(--space-4)`, with accent bg, on-accent text, `--radius-sm`, `--shadow-md` and z-index 100. It targets `main#main` (`tabIndex={-1}`, `outline:none` on focus).
- **Visible focus:** `--focus-ring` on every `:focus-visible` (buttons, links, inputs, nav items, row links, palette input). Never `outline:none` without the ring. Palette options are not focusable: the visual selection plus `aria-activedescendant` stand in for focus.
- **Live region:** one shell `role="status" aria-live="polite"` region, visually hidden, holding the messages "Search opened", "{n} results", "No results", "Opened {label}", "Saved view applied", "You're offline", "You're back online". Page-level `status`/`alert` nodes stay as they are (R5).
- **Dialogs:** the palette and the open drawer each set `aria-modal="true"`, an accessible name, a focus trap, Esc to close, and focus return (section 3, section 2). Background scroll is locked. Only one is open at a time: opening the palette from the drawer stacks the palette on top, and Esc closes the palette first.
- **Reduced motion:** drawer slide → opacity fade at `--duration-fast` (the change in visibility still communicates). Palette scale → opacity only. Chevron rotation → instant. Skeleton shimmer → static. Button spinner → static ring with the label kept. Hover colour transitions → none.
- **Targets:** comfortable controls are 44px. Compact (opt-in) controls are 36px, and nav and sm buttons are 32px with 44px hit areas wherever adjacent spacing allows. All of these meet WCAG 2.2 SC 2.5.8 (24px). This is a recorded deviation from the Moonaan 44px floor and applies only to the user-chosen compact density.
- **Contrast pairs verified** (WCAG relative-luminance formula):

| Pair | Light | Dark |
|---|---|---|
| text / bg-surface | 17.92 | 14.31 |
| text / bg-app | 16.86 | 15.69 |
| text-muted / bg-surface | 6.05 | 7.12 |
| text-muted / bg-app | 5.69 | 7.81 |
| text-muted / bg-muted (zebra, hover) | 5.39 | 6.75 |
| on-accent / accent (primary button) | 6.29 | 6.33 |
| on-accent / accent-hover | 7.90 | — |
| accent (link) / bg-surface | 6.29 | 5.78 |
| accent-strong / accent-subtle (active nav, selected option) | 8.77 | 10.01 |
| border-input / bg (non-text, 3:1) | 3.34 | 4.01 |
| focus ring accent / bg-surface (3:1) | 6.29 | 5.78 |
| success / success-bg | 4.76 | 8.82 |
| warning / warning-bg | 4.91 | 9.30 |
| danger / danger-bg | 6.05 | 6.21 |
| info / info-bg | 5.50 | 6.49 |
| on-danger / danger (button) | 6.57 | 6.83 |

  `--color-border` (1.58 light) is decorative only. Never use it as the sole boundary of an input.

## 7. Microcopy (sentence case, second person; strings live in `src/shell/copy.ts` until i18next is allowed)

The full writing rules (voice, task verbs, error messages, glossary) are in [ux-writing.md](ux-writing.md).

- **Shell nav:** "Home", "Notes". Module nav labels stay as they are, except two changes: `PIM workspace` → "Overview", and `Engineering workspace` → "Overview". This keeps "one concept, one word" with Admin's "Overview".
- **Module descriptions:**
  - Admin: "Numbering, audit, alerts, imports and documents."
  - Period close: "Close your books on time with tasks, reviews and period locks."
  - Engineering data: "Bills of material, routings and change orders."
  - Metadata: "Fields and forms your tenant uses for each record type."
  - Accounts payable: "Plan payment runs and pay vendors on schedule."
  - Product information: "Products, masters and what's released to each legal entity."
  - Tax: "Tax codes, rates and the rules that apply them."
  - Workflow and batch: "Approvals, batch jobs and message templates."
- **Placeholders** (EmptyState description):
  - Close: "Period close will bring your close checklist, journal reviews and period locks into one place."
  - Payables: "Payment runs will let you review invoices due, choose what to pay and release payments."
  - Tax: "Tax configuration will let you maintain tax codes, rates and how they apply to transactions."
- **Admin cards:**
  - Audit: "See who changed what, and when."
  - Sequences: "Set up gap-free numbering per legal entity."
  - Alerts: "Review failures and overdue approvals."
  - Import / export: "Move records in and out as CSV."
  - Documents: "Check documents in and out, track versions and apply legal holds."
  - Standard texts: "Reusable text in every language you support."
- **Empty states not listed elsewhere:**
  - Sequences: "No sequences set up yet" / "Sequences number your documents without gaps, per legal entity."
  - Documents: "No documents yet" / "Documents you attach to records appear here."
  - Texts: "No standard texts yet" / "Standard texts you create appear here for use in templates and notes."
- **Buttons:** "Search", "Close search", "Open navigation", "Close navigation", "Apply saved view", "Compact view", "Find parents", "Save {title}" (FormRenderer "Save Note" unchanged), "Add {line}", "Try again", "Browse workspaces", "Go to home".
- **Offline banner:** "You're offline. Pages you've opened still work, but searches and saves will fail until you reconnect."
- **Palette:** the placeholder, no-results and help strings exactly as in section 3.

## 8. Test contract

**Preserve (36 tests, unchanged unless noted):**
- `Shell.test.tsx`:
  - `checkAxeAA(container)` returns `[]`.
  - Exactly one `h1`.
  - `navigation` "Workspaces".
  - Link "Skip to content" with `href="#main"`.
  - Clicking button "Search" opens dialog "Search", and "Find a page" has focus.
  - Ctrl+K opens the dialog, and Escape removes it.
  - Typing "audit{Enter}" renders "Audit page".
  - "Apply saved view" leads to status "Saved view applied".
  - FormRenderer: "Save Note" → alert "Title is required"; type into "Title" → no alert; button "One"; label "Edit Title".
- `admin`: id, routes > 0, every nav starts with `/admin`, auto-discovered, table "Audit entries" contains `user-1`, `Acme Ltd`, `trace-abc`, `2026-10-24T12:00:00Z`.
- `close`/`payables`/`tax`: id, routes > 0, nav prefix, auto-discovered.
- `engineering`:
  - Nav prefix plus `duty === "engineering.read"` on every entry.
  - Heading "Engineering data".
  - Labels "Component" and "As of date"; button "Find parents".
  - Table "Where-used results" contains WHEEL and BIKE; status "2 parents found"; exact fetch URL.
  - On failure: alert "Where-used lookup failed" and no table.
  - On empty: status "No active BOM uses this component on that date." and no table.
- `MetadataForm`: label "Account manager", fetch URL + headers, option "preferred", "Code" required.
- `product`: heading "Product information management", list "PIM tiles" contains "Masters 50" and "Products 500", table "Products" contains SHIRT.
- `workflow`: heading "Workflow and batch", list "Workflow tiles" contains "Pending work items 4", table "Work items" contains PR-1042, table "Batch runs" contains rows-1m.
- `registry`: unique ids, sorted titles, routes and nav non-empty.
- `metadata`, `telemetry`, `vite-federation`: untouched.

**Update:**
1. `Shell.test.tsx`:
   - Rename test 1 to "the shell passes the in-repo WCAG 2.1 AA checks".
   - `renderShell(path = "/")` takes an initial entry and mounts the real `HomePage` at `/` and `NotesPage` at `/notes`.
   - Fix the latent no-op `expect(...).toHaveFocus;` to `toHaveFocus()`, and make FormRenderer focus the cell input when edit opens (a ref + effect).
2. `MetadataForm.tsx`: `h1` → `h2` (no test change).
3. `a11y.ts`: keep all current checks and add these:
   - `dialog-name`: every `[role=dialog]` has `aria-label`/`aria-labelledby`.
   - `aria-required-attr`: every `[role=combobox]` has `aria-controls` + `aria-expanded`.
   - `duplicate-id`.
   - `svg-img-alt`: every `svg` is `aria-hidden="true"` or has `role="img"` + a name.

**New assertions (in `Shell.test.tsx` unless noted):**
1. Home lists one `heading level 3` per module title and a link per nav entry. No "Save Note" button is on `/`; it appears at `/notes`, and `checkAxeAA` returns `[]` there.
2. At `/admin/audit`: `h1` is "Audit", navigation "Breadcrumb" has link "Admin", and link "Audit" inside "Workspaces" has `aria-current="page"`.
3. Palette: after Ctrl+K and ArrowDown, the input's `aria-activedescendant` equals the id of the 2nd `option`, which has `aria-selected="true"`. Enter navigates to that option's page.
4. Palette: options sit in a `group` named "Admin". Typing "zzz" shows "No pages match" with `aria-expanded="false"`, and Enter leaves the dialog open.
5. Palette: Escape after opening from the Search button returns focus to that button.
6. Drawer: clicking "Open navigation" sets `aria-expanded="true"`, shows dialog "Navigation", and focuses "Close navigation". Tab from the last element wraps inside the drawer. Escape closes it, and focus returns to "Open navigation".
7. Route change via the palette focuses the new `h1`.
8. "Compact view" sets `aria-pressed="true"` and `data-density="compact"` on the root.
9. `src/shell/ui/ui.test.tsx`: `WorkspacePlaceholder` (in a MemoryRouter) shows h2 title, "This workspace isn't available yet" and link "Browse workspaces" → `/`. `StatusPill status="failed"` renders "Failed". `FormField` with an error wires `aria-describedby` to the `role=alert` text. `DataTable` with `rows=[]` renders the empty title in one `colSpan` cell.
10. `src/modules/close/module.test.ts`: `close.routes[0].element` is not `null` (repeat for payables and tax).

## 9. File plan

**Create:**
- `docs/design-system.md`
- `src/shell/theme.css`, `src/shell/Shell.module.css`
- `src/shell/Sidebar.tsx`, `src/shell/TopBar.tsx`
- `src/shell/CommandPalette.tsx` + `.module.css`
- `src/shell/pageMeta.ts`, `src/shell/shellNav.ts`, `src/shell/copy.ts`, `src/shell/workspaceIcons.ts`
- `src/shell/RouteErrorBoundary.tsx`
- `src/shell/hooks/useFocusTrap.ts`, `src/shell/hooks/useOnlineStatus.ts`
- `src/shell/pages/HomePage.tsx`, `NotesPage.tsx`, `NotFoundPage.tsx`, `pages.module.css`
- `src/shell/ui/index.ts`, `Icon.tsx`, and `.tsx` + `.module.css` for each of: `Button`, `PageHeader`, `Card`, `StatTile`, `DataTable`, `StatusPill`, `FormField`, `Input`, `Select`, `Textarea` (one shared `Control.module.css`), `EmptyState`, `Skeleton`, `Alert`, `WorkspacePlaceholder`
- `src/shell/ui/ui.test.tsx`
- `src/modules/close/pages.tsx`, `src/modules/payables/pages.tsx`, `src/modules/tax/pages.tsx`

**Modify:**
- `index.html` (color-scheme meta)
- `src/main.tsx`: import `./shell/theme.css`; children = `{ index: true, element: <HomePage modules/> }`, `{ path: "notes", element: <NotesPage/> }`, module routes, `{ path: "*", element: <NotFoundPage/> }`
- `src/shell/Shell.tsx`: drop `FormRenderer`/`catalog`, compose Sidebar/TopBar/palette/drawer/live region
- `src/shell/forms.tsx`: primitives, edit-cell focus
- `src/shell/a11y.ts`
- `src/shell/Shell.test.tsx`
- `src/modules/registry.ts`: optional `description`
- every `src/modules/*/module.ts`: description; close/payables/tax elements; product `handle` + `CatalogRoute`; nav label renames
- every `src/modules/*/pages.tsx` and `MetadataForm.tsx`
- `src/modules/{close,payables,tax}/module.test.ts`: new assertion 10
- `src/shell/README.md`: one line pointing to this spec

Do not touch `metadata.ts`, `personalization.ts`, `telemetry.ts`, `federation.ts`, `vite.config.ts` or `package.json`.

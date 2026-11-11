# Passage web app: Accessibility Conformance Report (draft)

Based on the VPAT® 2.5 WCAG edition template. **Status: draft, not signed off.** The human screen-reader pass and the sign-off are tracked in #512. This draft records what the automated checks and the code review show today, so the sign-off can start from evidence rather than from a blank template.

| | |
|---|---|
| Product | Passage web app (`apps/web`), the shell and the procure-to-pay and order-to-cash pages |
| Version | `main` at the date below |
| Report date | 2026-11-10 |
| Standard | WCAG 2.2, levels A and AA |
| Contact | The Passage product team (see the repository README) |

## Evaluation methods

- **Automated, every CI run.**
  - `e2e/flows-a11y.demo.spec.ts`: axe-core with the `wcag2a`, `wcag2aa`, `wcag21a`, `wcag21aa` and `wcag22aa` tags on every route in `e2e/flowRoutes.ts` (the procure-to-pay and order-to-cash pages), against the demo build. The run fails on any serious or critical violation.
  - `e2e/a11y.spec.ts`: axe-core (WCAG 2.1 A and AA tags) on every shell route, and on the open command palette, the palette with no results and the open mobile drawer.
  - Component tests run the in-repo checks in `src/shell/a11y.ts` (one `h1`, a labelled `nav`, `main`, a skip link, labelled form controls) on the shared components and pages.
- **Keyboard.** `e2e/p2p-keyboard.demo.spec.ts` creates a requisition and sends it for approval using only Tab, Shift+Tab, Enter and Space after the first page load.
- **Design review.** Contrast pairs, focus order, dialog behaviour, target sizes and reduced motion are specified and checked in [design-system.md](design-system.md), section 6.
- **Not yet done.** Manual screen-reader passes (NVDA with Firefox, JAWS with Chrome, VoiceOver with Safari) and a manual zoom and reflow pass. Rows that depend on them say so.

## Terms

- **Supports**: the functionality meets the criterion without known defects.
- **Partially supports**: some functionality does not meet the criterion.
- **Does not support**: most functionality does not meet the criterion.
- **Not applicable**: the criterion does not apply to the product.
- **Not evaluated**: no evidence yet. Every such row needs a manual check before sign-off.

## Table 1: Level A

| Criterion | Conformance | Remarks |
|---|---|---|
| 1.1.1 Non-text content | Supports | Icons are decorative (`aria-hidden`) or carry an accessible name; icon-only buttons have `aria-label`. axe `image-alt`, `button-name` and `svg-img-alt` pass on the scanned routes. |
| 1.2.1 Audio-only and video-only (prerecorded) | Not applicable | The app has no audio or video content. |
| 1.2.2 Captions (prerecorded) | Not applicable | No video. |
| 1.2.3 Audio description or media alternative (prerecorded) | Not applicable | No video. |
| 1.3.1 Info and relationships | Supports | Landmarks (`header`, `nav`, `main`), one `h1` per page, tables with `th scope="col"`, labelled form fields through `FormField`, field groups in `fieldset` with a legend. Screen-reader confirmation pending (#512). |
| 1.3.2 Meaningful sequence | Supports | DOM order matches the visual order; the layout uses no CSS reordering of content. |
| 1.3.3 Sensory characteristics | Supports | Instructions refer to labels, not to shape, position or colour. |
| 1.4.1 Use of color | Partially supports | Status is never shown by colour alone: `StatusPill` and `Badge` always carry text (design-system R8). Required fields carry a text mark. Links in running text are told apart from body text by colour only until hover or focus (underline on hover, focus ring on focus); the accent-to-text contrast needs measuring against the 3:1 that technique G183 asks for, or links inside paragraphs should be underlined. |
| 1.4.2 Audio control | Not applicable | No audio. |
| 2.1.1 Keyboard | Supports | All controls are native buttons, links and inputs, or ARIA widgets with keyboard handlers (lookup combobox, command palette, Columns and Views menus, the org hierarchy tree, whose drag and drop has arrow-key move commands). Covered for procure to pay by the keyboard-only run. |
| 2.1.2 No keyboard trap | Supports | The palette and drawer trap focus while open by design and close with Esc, returning focus to the opener. |
| 2.1.4 Character key shortcuts | Supports | The palette shortcut uses a modifier (Ctrl/Cmd+K). No single-character shortcuts exist. |
| 2.2.1 Timing adjustable | Partially supports | The app sets no time limits of its own. The sign-in session expires with the identity provider's token lifetime; the app refreshes the token silently while the tab is open, but a person is not warned before a session ends. To confirm with the identity configuration in #512. |
| 2.2.2 Pause, stop, hide | Supports | No auto-moving content. Skeleton shimmer stops under reduced motion and lasts only while loading. Live dashboards refresh data in place without motion. |
| 2.3.1 Three flashes or below | Supports | No flashing content. |
| 2.4.1 Bypass blocks | Supports | "Skip to content" is the first focus stop and moves focus to `main`. Checked by the keyboard run and the in-repo checks. |
| 2.4.2 Page titled | Supports | Every route sets a document title from its page title. |
| 2.4.3 Focus order | Supports | Focus order is specified in design-system section 6 and exercised by the keyboard run. Dialogs move focus in and return it on close. |
| 2.4.4 Link purpose (in context) | Supports | Links name their destination; row links use the record's number or name. |
| 2.5.1 Pointer gestures | Supports | No multipoint or path-based gestures. |
| 2.5.2 Pointer cancellation | Supports | Actions fire on click (up event) through native controls. |
| 2.5.3 Label in name | Supports | Accessible names start with the visible label; icon buttons with visible text use that text. axe `label-content-name-mismatch` is not raised on the scanned routes. |
| 2.5.4 Motion actuation | Not applicable | No motion-operated functions. |
| 3.1.1 Language of page | Supports | `<html lang="en">`. The app is English only. |
| 3.2.1 On focus | Supports | Focus never submits, navigates or opens a dialog. |
| 3.2.2 On input | Supports | Changing a field does not submit or navigate. List filters apply with an explicit "Apply filters" button. |
| 3.2.6 Consistent help | Supports | The Help button sits in the same place in the top bar on every page and opens the same help panel. |
| 3.3.1 Error identification | Supports | Forms show an error summary that links to each field, and each field shows its own message tied with `aria-describedby` ([forms.md](forms.md)). Server failures show a plain-language message from the error catalog. |
| 3.3.2 Labels or instructions | Supports | Every field has a visible label, required and optional marks, and field help where the meaning is not obvious. |
| 3.3.7 Redundant entry | Supports | Forms default the legal entity and site from the work context and lookups offer recent picks. Guided flows carry the previous document forward (a receipt from a PO, an invoice from a receipt). |
| 4.1.2 Name, role, value | Supports | Native controls throughout; custom widgets follow the ARIA patterns (combobox with `aria-activedescendant`, disclosure buttons with `aria-expanded`, tree with `aria-level`). Screen-reader confirmation pending (#512). |

## Table 2: Level AA

| Criterion | Conformance | Remarks |
|---|---|---|
| 1.2.4 Captions (live) | Not applicable | No live media. |
| 1.2.5 Audio description (prerecorded) | Not applicable | No video. |
| 1.3.4 Orientation | Supports | No orientation lock. The warehouse and shop-floor screens work in both orientations. |
| 1.3.5 Identify input purpose | Partially supports | Sign-in is the identity provider's form. In the app, the HR profile change card sets `address-line1` and `postal-code`, but other personal-data fields there (name, city, phone, email) do not yet set `autocomplete` tokens. |
| 1.4.3 Contrast (minimum) | Supports | Token pairs measured at 4.5:1 or more for text in light and dark themes (design-system section 6). axe `color-contrast` passes on the scanned routes, including the open drawer. |
| 1.4.4 Resize text | Not evaluated | Sizes use `rem`, so text scales with browser settings. A manual 200% zoom pass is still needed. |
| 1.4.5 Images of text | Supports | No images of text. |
| 1.4.10 Reflow | Partially supports | The shell reflows to a single column with a drawer below 960px and 640px. Wide data tables scroll sideways inside a labelled region, which the criterion allows for data tables; some dense forms have not been checked at 320 CSS pixels. |
| 1.4.11 Non-text contrast | Supports | Input borders and the focus ring meet 3:1 against their backgrounds (design-system section 6). The plain border token is decorative only and never the sole boundary of an input. |
| 1.4.12 Text spacing | Not evaluated | No fixed-height text containers are known, but a manual check with the text-spacing bookmarklet is still needed. |
| 1.4.13 Content on hover or focus | Not evaluated | The copy-id button beside a name and the glossary terms show extra content on hover or focus. Whether each stays while hovered and can be dismissed with Esc without moving focus needs a manual check. |
| 2.4.5 Multiple ways | Supports | Sidebar navigation, the command palette (pages and records), Recent pages on the home page, and breadcrumbs. |
| 2.4.6 Headings and labels | Supports | Page and section headings describe their content; labels follow the UX writing guide. |
| 2.4.7 Focus visible | Supports | `--focus-ring` on every `:focus-visible`; the design system forbids removing the outline without the ring. The keyboard run asserts focus on each step. |
| 2.4.11 Focus not obscured (minimum) | Supports | The top bar is sticky, so the page sets `scroll-padding-block-start` to the top bar height (`theme.css`): a control that receives focus scrolls into view below the bar, not under it. Dialogs are modal. axe has no automated rule for this criterion; the keyboard run exercises it, and the manual pass in #512 should confirm. |
| 2.5.7 Dragging movements | Supports | The only drag and drop (org hierarchy) has keyboard move commands and buttons as a single-pointer alternative. |
| 2.5.8 Target size (minimum) | Supports | Controls are 44px by default and at least 32px in the opt-in compact density, above the 24px minimum (design-system section 6). axe `target-size` runs with the `wcag22aa` tag. |
| 3.1.2 Language of parts | Supports | The app is English only; record data in other languages is shown as entered. |
| 3.2.3 Consistent navigation | Supports | One shell: the same sidebar, top bar and palette on every page. |
| 3.2.4 Consistent identification | Supports | Shared components and the copy catalog give the same function the same label and icon. |
| 3.3.3 Error suggestion | Supports | Error messages say what happened and what to do ([ux-writing.md](ux-writing.md)); problem codes go in a Details disclosure for support. |
| 3.3.4 Error prevention (legal, financial, data) | Supports | Destructive and irreversible actions ask for confirmation through the shared `ConfirmDialog`; forms show an error summary before submit and guard unsaved changes; posted documents are corrected by reversing or cancelling documents rather than by editing. |
| 3.3.8 Accessible authentication (minimum) | Supports | Sign-in uses the identity provider's username and password form, which permits password managers and paste; there is no cognitive function test. To re-check against the deployed Keycloak theme in #512. |
| 4.1.3 Status messages | Supports | One polite shell live region plus page-level `status` and `alert` regions that stay mounted (design-system R5). List counts and action results are announced. Screen-reader confirmation pending (#512). |

## Known gaps and plan

1. **Screen-reader pass** on the procure-to-pay and order-to-cash flows with NVDA, JAWS and VoiceOver (#512).
2. **Zoom, reflow and text spacing** (1.4.4, 1.4.10, 1.4.12): a manual pass at 200% and 400% zoom and at 320 CSS pixels on the flow pages.
3. **Session timeout warning** (2.2.1): warn before the identity session ends and let the person extend it.
4. **Input purpose** (1.3.5): add `autocomplete` tokens to the remaining personal-data fields in HR self-service.
5. **Link distinction** (1.4.1): measure accent against body text, or underline links inside paragraphs.
6. **Hover and focus content** (1.4.13): check the copy-id button and glossary terms for hover persistence and Esc dismissal.
7. **Shell routes at WCAG 2.2**: `e2e/a11y.spec.ts` still scans with the 2.1 tags; add `wcag22aa` once the shell routes are clean under it.

## Sign-off

| Role | Name | Date |
|---|---|---|
| Accessibility reviewer | | |
| Product owner | | |

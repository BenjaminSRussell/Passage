# Form standard

Shared pieces live in `src/shell/ui` and are exported from `src/shell/ui/index.ts`. Use them for every new form; roll existing forms over in the rollout checklist (#505).

## Layout

- Required and common fields first, in the order a person fills them in.
- Optional fields that are rarely used go in `<FormSection collapse="more">` ("More options").
- Technical or expert-only fields go in `<FormSection collapse="advanced">` ("Advanced").
- A titled group of common fields is `<FormSection title="Delivery">`. It renders a labelled region with an `h3`.
- A collapsed section is a native `details` element, so it opens with Enter or Space and is reachable by Tab. `defaultOpen` is read once on mount: pass it when editing a record that already has values in the section.

## Required, optional and help

- `FormField required` shows "Required" and sets `aria-required` on the control. `optional` shows "Optional". Mark one or the other on every field, including selects that have a default.
- Help text is one line, passed as `hint`. Take the text from `copy.fieldHelp` in `src/shell/copy.ts`, not from an inline string, so it can be translated.
- Write help in plain language: say what the field does or where to find the value. Skip help on obvious fields.

## Inline validation

- Keep field errors in `FormField error` (plain message, shown under the field with `role="alert"`).
- Render `<FormErrorSummary items={...} attempt={attempt} />` at the top of the form. `items` is `{ fieldId, label, message }` for each error in form order; `attempt` is a counter the page increments each time a submit fails (client or server field errors).
- Declare each field's `{ id, label }` once per form in a map keyed by error key, in form order, and build `items` with `summaryItems(errors, fields, dynamic?)`. Entries without an `id` (a form-level problem) are listed but cannot be focused; `dynamic(key)` resolves repeated rows such as `lines[2].qty`. Do not hand-write labels at the call site.
- When `attempt` increases, focus moves to the first item that has a field (or to the summary itself when none does; it is an alert, so it is announced) and opens any collapsed section holding it. Each summary entry is a button that focuses its field. The summary is hidden when there are no items.

## Unsaved changes

```tsx
const { prompt, release } = useUnsavedChanges(useFormBaseline(values));
...
release();
navigate(nextPage);
...
return <div>...{prompt}</div>;
```

- Pass `useFormBaseline(values)` as `dirty`. It compares the form values with a baseline that follows every change until the person first edits a field inside a `<form>` (an `input` or `change` event, a click on any button or option inside the form such as Add line, Remove line or a lookup clear, or an Enter, Space or Delete key press inside it), so values the system fills in after mount, such as the top-bar legal entity from `EntityLookup` prefill or a late work-context pick, do not count as edits. A Tab press, a sidebar click or typing outside the form does not freeze it. After the first edit the baseline freezes and any difference is a real edit. The inputs must sit inside a `<form>` element. Do not hand-roll a snapshot comparison.
- The browser warns on tab close or reload while dirty. Inside a data router (`createBrowserRouter`) in-app navigation also asks "Leave without saving?" in a dialog (pass a second argument to change its wording) with "Leave without saving" and "Keep editing". Outside a data router (some tests) only the tab-close guard applies.
- Call `release()` right before the navigation that follows a successful save, or the guard blocks it.

## Examples

- `src/modules/receivables/NewPaymentPage.tsx` (record a payment).
- `src/modules/purchasing/RequisitionPages.tsx` (`RequisitionForm`).

Tests: `src/shell/ui/forms.test.tsx`.

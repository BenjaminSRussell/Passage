# Budgeting module (web)

Budget control screens for PSG-38 slices 1 to 4, wired to `services/budgeting` through the BFF at `/api/v1/budget/...`.

| Route | Page | Service calls |
|---|---|---|
| `/budgeting` | Budget control rules: list, legal entity filter | `GET /control-rules` |
| `/budgeting/rules/new` | New rule version (warn / block thresholds) | `POST /control-rules` |
| `/budgeting/rules/:id` | Rule detail: funds summary, available funds per combination, register entries, encumbrances (status filter), recorded checks | `GET /control-rules/{id}`, `/available`, `/register-entries`, `/encumbrances`, `/checks` |
| `/budgeting/rules/:id/entries/new` | Post a register entry (Original, Revision, Transfer, Carry-forward); transfers must net to zero | `POST /register-entries` |
| `/budgeting/check` | Budget check form with per-line result, step trace and encumbrance link | `GET /control-rules`, `POST /checks` |
| `/budgeting/checks/:id` | One recorded check | `GET /checks/{id}` |
| `/budgeting/encumbrances/:id` | Encumbrance history, invoice relief (reports the price excess to re-check) and release of the open remainder on PO cancellation or close, after a confirmation (FR-38.5) | `GET /encumbrances/{id}`, `POST /encumbrances/{id}:relieve`, `POST /encumbrances/{id}:release` |
| `/budgeting/reservations` | Earmarked funds list with type and status filters (`?rule=` narrows to one rule version) | `GET /reservations` |
| `/budgeting/reservations/new` | New reservation document (four types, optional expiry, lines); the creation check refuses over-budget documents | `POST /reservations` |
| `/budgeting/reservations/:id` | Reservation detail: lines, consumption history with encumbrance links, consume by a follow-on document, close | `GET /reservations/{id}`, `POST /reservations/{id}:consume`, `POST /reservations/{id}:close` |
| `/budgeting/programs` | Investment programs with legal entity filter | `GET /investment-programs` |
| `/budgeting/programs/new` | New program for an approval year | `POST /investment-programs` |
| `/budgeting/programs/:id` | Program totals, position hierarchy with rolled-up plan / actual / commitment, distribute budget (checked against the parent on the page, CO-0641), assign capex requests / assets under construction / projects, carry forward to the next year | `GET /investment-programs/{id}`, `POST /investment-programs/{id}:distribute`, `:assign`, `:carry-forward` |

Every list has loading (skeleton rows), empty and error (with "Try again") states. Pages use the shell primitives from
`src/shell/ui` and `pages.module.css`. Commands send an `Idempotency-Key` that stays the same while the form content
is unchanged, so a retried submit is a replay. Money is never a JS number: amounts stay decimal strings and sums use
BigInt ten-thousandths (`logic.ts`).

## Run and test

```sh
cd apps/web
npx vitest --run src/modules/budgeting
npx tsc --noEmit
```

## Needs (for the shell owner, wave 5)

- Nav duties `budget.view` and `budget.check` must exist in the duty catalogue (the service also uses
  `budget.configure`, `budget.maintain`, `budget.override`, `budget.reserve` and `budget.program.maintain`).
- The planning grid, the inline check inside the PO form and rule activation are later slices of PSG-38.

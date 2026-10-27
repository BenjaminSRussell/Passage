# Budgeting module (web)

Budget control screens for PSG-38 slice 1, wired to `services/budgeting` through the BFF at `/api/v1/budget/...`.

| Route | Page | Service calls |
|---|---|---|
| `/budgeting` | Budget control rules: list, legal entity filter | `GET /control-rules` |
| `/budgeting/rules/new` | New rule version (warn / block thresholds) | `POST /control-rules` |
| `/budgeting/rules/:id` | Rule detail: funds summary, available funds per combination, register entries, encumbrances (status filter), recorded checks | `GET /control-rules/{id}`, `/available`, `/register-entries`, `/encumbrances`, `/checks` |
| `/budgeting/rules/:id/entries/new` | Post a register entry (Original, Revision, Transfer, Carry-forward); transfers must net to zero | `POST /register-entries` |
| `/budgeting/check` | Budget check form with per-line result, step trace and encumbrance link | `GET /control-rules`, `POST /checks` |
| `/budgeting/checks/:id` | One recorded check | `GET /checks/{id}` |
| `/budgeting/encumbrances/:id` | Encumbrance history and invoice relief (reports the price excess to re-check) | `GET /encumbrances/{id}`, `POST /encumbrances/{id}:relieve` |

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
  `budget.configure`, `budget.maintain` and `budget.override`).
- The planning grid, the inline check inside the PO form and rule activation are later slices of PSG-38.

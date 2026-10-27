# finreport web module (PSG-15, wave 15 slice 1)

Financial statements workspace over the `services/finreport` API
(`/api/v1/reporting`). Pages use the shell primitives and `pages.module.css`.

| Route | Page | Service calls |
| --- | --- | --- |
| `/finreport` | Statement runs (report library list) and read-model lag | `GET /runs`, `GET /lag` |
| `/finreport/runs/new` | Run a published definition for a ledger and period | `GET /definitions`, `POST /runs` (Idempotency-Key) |
| `/finreport/runs/:runId` | Statement grid with the "as of" stamp; select an amount to drill to its balance keys | `GET /runs/{id}`, `GET /runs/{id}/cells`, `GET /definitions/{code}?version=n`, `GET .../balances` |
| `/finreport/definitions` | Report definitions list and JSON import | `GET /definitions`, `POST /definitions/import` |
| `/finreport/definitions/new`, `/finreport/definitions/:code` | Designer: rows, columns, validate, save draft (If-Match), publish, export | `GET/PUT /definitions/{code}`, `:validate`, `:publish`, `/export` |

The drill panel checks exactly that the keys sum to the cell value (decimal
strings summed as integer micro-units, no floats) and warns otherwise.
Computed cells (calculation rows, CALC columns) explain that they have no
balances behind them.

The run form builds the fiscal calendar from whole years (opening period 0 plus
N periods) until the ledger calendar API exists (wave 14 handoff).

Not yet here: reporting trees, live preview and drag reorder in the designer,
schedules, report library outputs, audit trail and drill level 2 (journal
lines). See `services/finreport/NOT-IMPLEMENTED.md`.

Needs: nothing outside the module. No npm dependencies were added.

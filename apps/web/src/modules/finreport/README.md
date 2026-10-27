# finreport web module (PSG-15, wave 15 slices 1-5)

Financial statements workspace over the `services/finreport` API
(`/api/v1/reporting`). Pages use the shell primitives and `pages.module.css`.

| Route | Page | Service calls |
| --- | --- | --- |
| `/finreport` | Statement runs (report library list) and read-model lag | `GET /runs`, `GET /lag` |
| `/finreport/runs/new` | Run a published definition for a ledger and period | `GET /definitions`, `POST /runs` (Idempotency-Key) |
| `/finreport/runs/:runId` | Statement grid with the "as of" stamp and the run's warnings (REPORT-0015); select an amount to drill to its balance keys, then to the journal lines of all of them or of one, with source document and trace links; Download CSV, XLSX and HTML | `GET /runs/{id}`, `GET /runs/{id}/cells`, `GET /definitions/{code}?version=n`, `GET .../balances`, `GET .../lines?key=&cursor=`, `GET /runs/{id}/outputs/{csv,xlsx,html}` (links) |
| `/finreport/definitions` | Report definitions list with status, standard definitions with install, and JSON import | `GET /definitions`, `GET /standard-definitions`, `POST /standard-definitions:install`, `POST /definitions/import` |
| `/finreport/definitions/new`, `/finreport/definitions/:code` | Designer: rows, columns, validate, save draft (If-Match), publish, export, retire (with confirmation) | `GET/PUT /definitions/{code}`, `:validate`, `:publish`, `:retire`, `/export` |
| `/finreport/hierarchies` | Derived hierarchies list: version, members, unassigned count, open draft | `GET /hierarchies` |
| `/finreport/hierarchies/new`, `/finreport/hierarchies/:code` | Hierarchy editor: nodes and leaf rules, save draft (If-Match), publish with the members to classify, membership per leaf and unassigned members | `GET/PUT /hierarchies/{code}`, `:publish`, `/members` |
| `/finreport/schedules` | Distribution schedules: definition, cron and zone, ledgers, formats, recipients, next run or paused | `GET /schedules` |
| `/finreport/schedules/new`, `/finreport/schedules/:code` | Schedule form: published definition, currency type, cron, time zone, ledgers, formats, recipients, attachment limit, enabled; replace with If-Match | `GET /definitions`, `GET/PUT /schedules/{code}` |

The drill panel checks exactly that the keys sum to the cell value (decimal
strings summed as integer micro-units, no floats) and warns otherwise.
Computed cells (calculation rows, CALC columns) explain that they have no
balances behind them.

Drill level 2 (`LinesPanel.tsx`) opens from "Show all journal lines" or a
balance's "Lines" button. It loads a page at a time ("Load more lines"), shows
the running sum and the HWM, and states whether the lines add up to the amount;
when the service reports `reconciled: false` it shows a warning with the
difference instead of the "sum to" note. Source links open a Passage route in
the app or an http(s) document in a new tab; any other link (`javascript:`,
`//host`) is shown as "None" (`sourceLink` in `drill.ts`). The trace opens
`traceUrl` when the service is configured with `REPORTING_TRACE_URL`. A 403
explains that the View ledger transactions duty is needed.

The run form builds the fiscal calendar from whole years (opening period 0 plus
N periods) until the ledger calendar API exists (wave 14 handoff).

Leaf rules are typed one per line (`100000..199999`, `61*, 6200`,
`region=EMEA`) and members one per line (`id, key, attribute=value`); see
`hierarchyText.ts`. Until the chart of accounts and dimension services feed this
service (wave 11 and wave 6 handoffs), the user pastes the members on publish.
The hierarchy navigation entry needs `reporting.definition.maintain`, the duty
the service enforces on those routes; the schedule entry needs
`reporting.statement.schedule`. The schedule form checks the fields the service
would refuse (code, UUID ledgers, email recipients, formats, limit) before
sending; the service checks the cron and time zone (REPORT-0018).

Not yet here: reporting trees, hierarchies as statement row sets, live preview and drag reorder in the designer,
PDF downloads, the schedule run history, share links, audit trail, keyboard
cell-by-cell navigation of the statement and the dimension-security
"filtered amount" warning. See `services/finreport/NOT-IMPLEMENTED.md`.

Needs: nothing outside the module. No npm dependencies were added.

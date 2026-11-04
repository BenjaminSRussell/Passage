# Not implemented after Wave 14 slice B

Slice B of #436 ships period maintain/reopen, the checklist HTTP API, allocation run/post/reverse, accrual apply with automatic reversals, and regrouping. Remaining items are follow-ups, not a second close slice.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1 / Deliverable 1: period control | built | `GET /v1/close/periods`, `PATCH .../modules/{module}`, dual-approval reopen (`:request-reopen` / `:approve` / `:reject`) and an in-process sweeper (`ExpireReopened` via `close_reopen_due_tenants()`) consume `CLOSE_REOPEN_*` and `CLOSE_LOCK_WAIT_MS`. Closed periods reopen only through that workflow. `dev.passage.close.period.statuschanged.v1` is written to the outbox with the transition. Temporal is not a close-module dependency; expiry is the sweeper. The ledger-side posting contract remains in `services/ledger`; a single harness that drives a ledger post into a close-service closed period is still a cross-service follow-up (ledger is out of this lane, see open #441). |
| AC2 / Deliverable 3: allocation run/post | built | Rules persist; `POST /v1/close/allocation-runs` proposes from the ledger trial balance and posts through `ledgerclient` with `PostClosed` false. `:post` and `:reverse` use the same path. Closed periods are refused in close (`LEDGER-0013`) before the ledger is called. |
| AC3 / Deliverable 5: year-end close | built (slice A) | Unchanged. Duties `close.yearend.run` / `close.yearend.read` stay unattached. |
| AC4 / Deliverable 2: close checklist | built | Templates, runs, the task board and start/complete/skip/reassign persist on the 0002 tables. The web status board is still a navigation stub. |
| Deliverable 4: accruals | built | Schemes persist; the amount lives on the scheme (`close.accrual.write`), `:apply` spreads it (equal / by_days / custom, month-end stepping, ledger currency only) and posts the occurrences and reversals that are due through `ledgerclient` without `PostClosed`, persisting each entry id as it posts. The in-process sweeper posts the rest as they fall due, with backoff, and parks a run on a permanent ledger refusal. Recurring journals already live in the ledger service and are not duplicated here. |
| Deliverable 6: regrouping | built | Remaining-term, debit/credit balance and account-balance-sign rules persist and post through `ledgerclient` without `PostClosed`. Each rule posts once per ledger and key date (`CLOSE-0010`), claimed before the ledger is called. Item-based rules (remaining-term, debit/credit balance) return 503 until an open-item source is wired; an empty proposal is never posted. A source with a credit balance books the reclassification with the sides flipped. Remaining-term items with no due date fall into the shortest bucket (BR-14.13). Ledger open items do not expose a due date on the current client; remaining-term in production needs that field from ledger (do not edit `services/ledger` while #441 is open). |
| Config knobs | built | Validated at startup; reopen days/approvers, lock wait and the sweeper interval are consumed. |
| `ALLOC_MAX_ITERATIONS` | later | Still a parameter of `Reciprocal` only. |
| Events through the outbox | built | Period status-changed, checklist run/task, allocation completed, accrual scheduled, regrouping completed, year-end completed. |
| Idempotency-Key | built | Required on slice-B write endpoints; stored per run (`COM-0409` on reuse with a different body). Year-end stays keyed on ledger plus fiscal year. |
| JWT validation, step-up MFA, OPA | wave 2 | the gateway validates tokens; services trust tenant headers |
| `serviceCode` on 401 and 403 bodies from kernel middleware | kernel handoff | tenant and authz middleware live in `platform/go` |
| GUC names `passage.legal_entity_ids` and `passage.actor` | kernel handoff | `dbx.WithTenantTx` sets `passage.tenant_id` and `passage.legal_entity_scopes` |
| Web pages (close workspace, status board) | later | the module is a navigation stub |
| Database cluster `passage_close`, `close-db` Secret, Argo CD application | handoff | `deploy/` belongs to wave 1 |
| Duties attached to roles | follow-up | `period.reopen`, `close.checklist.*`, `close.allocation.*`, `close.accrual.*`, `close.regrouping.*` and the year-end duties are seeded and unattached, same as slice A |
| `ledger.journal.read` for open items | ledger handoff | not granted; `OpenItemSource` is injectable. Do not add the grant while other PRs edit `policies/authz` and `platform/go/tenant` unless a remaining-term production path needs it |

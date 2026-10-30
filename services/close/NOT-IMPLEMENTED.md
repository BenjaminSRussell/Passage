# Not implemented in PSG-14 slice A

Slice A ships period control, the allocation, year-end and checklist engines, the `:check` endpoint and the service skeleton. Slice B of the same ticket carries the rest; no other ticket owns it.

| Deliverable or AC | Status | Reason |
|---|---|---|
| AC1 / Deliverable 1: period control enforced at the posting API | partial | decision logic, `FOR SHARE` read and `:check` ship; the ledger posting API (wave 11) does not exist in this tree, so the call from inside its posting transaction is its seam. Maintain, hold, close and reopen endpoints, the Temporal reopen workflow with expiry and the status-changed publisher are slice B |
| AC2 / Deliverable 3: allocation engine | partial | fixed, basis and reciprocal computation ship with fixtures; allocation rule persistence, run, post and reversal API are slice B and need the ledger |
| AC3 / Deliverable 5: year-end close | partial | closing and opening proposals with the opening-equals-closing check ship; posting them, the run record and `ledger.year-end.closed` are slice B and need the ledger |
| AC4 / Deliverable 2: close checklist | partial | template DAG, instantiation across entities and dependency enforcement ship; templates, runs, tasks HTTP API, assignees, due dates, persistence and the status board are slice B |
| Deliverable 4: accrual schemes, automatic reversals, recurring journals | slice B | line budget; needs the ledger and a scheduler |
| Deliverable 6: balance-sheet regrouping | slice B | line budget; needs balances from the ledger |
| Config knobs `CLOSE_REOPEN_DEFAULT_DAYS`, `CLOSE_REOPEN_APPROVERS`, `CLOSE_LOCK_WAIT_MS`, `CLOSE_REOPEN_SWEEP_INTERVAL` | PSG-1402 follow-up slices | validated at startup and logged; no handler or sweeper consumes them until the transition, reopen and sweeper slices |
| `ALLOC_MAX_ITERATIONS` | slice 3+ | no consumer; the iteration cap is a parameter of `Reciprocal` |
| Events through the outbox | slice B | no state-changing endpoint ships; the AsyncAPI file declares the channel |
| Idempotency-Key | PSG-1402 follow-up slices | `store.Idempotent` and the `idempotency_keys` table ship with migration `0002`; the header check and the 422/409 mapping ship with the first write endpoint |
| JWT validation, step-up MFA, OPA | wave 2 | the gateway validates tokens; services trust tenant headers |
| `serviceCode` on 401 and 403 bodies from kernel middleware | kernel handoff | tenant and authz middleware live in `platform/go` |
| GUC names `passage.legal_entity_ids` and `passage.actor` | kernel handoff | `dbx.WithTenantTx` sets `passage.tenant_id` and `passage.legal_entity_scopes`; the policies use what the kernel sets |
| Web pages (close workspace, status board) | slice B | the module is a navigation stub |
| Database cluster `passage_close`, `close-db` Secret, Argo CD application | handoff | `deploy/` belongs to wave 1 |
| Posting date to fiscal period resolver (`GET /v1/close/periods:resolve`, #96) | built | resolves one legal entity and ledger from `period_status.start_date` and `end_date` (migration `0003`, nullable); 404 `CLOSE-0005` no period, 409 `CLOSE-0006` several. Open: no endpoint writes the dates yet (slice B maintain endpoints), and org's fiscal calendar (`fiscal_period`, no id) is not read, so periods must be registered here; period types (adjustment periods that share dates) are not modelled |

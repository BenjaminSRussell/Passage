# Architecture Review: Passage ERP Platform (compiled.md, review round 4, extended final, scoped)

Reviewer: independent architecture reviewer. Date: 2026-10-23.

Inputs:
- `review-r3.md` and `edits-r3.md`, both read in full.
- `requirements.md`, read in full (lines 1–1576).
- The edited sections of `compiled.md` (7,840 lines). I located each with `grep -n`, read it with offset and limit, and grepped the identifiers it references across the whole document.

The compliance overlay applies: P2/P3 personal data, payroll, GDPR/CCPA and SOC 2 are all in scope. ADR-0001 governs over the Moonaan AWS defaults. Line numbers below refer to `compiled.md`.

## Verdict

**REQUEST CHANGES**

There is no CRITICAL finding, and no compliance finding above MINOR.

The round-3 edits closed most of what they targeted:
- the link-back payload and allow-list (R3-1);
- `gl_seq` beside every subledger journal link (R3-2);
- the expiry sweeper and quantity hold (R3-3);
- the retry and task lifecycle (R3-4);
- the projects billing data model (R3-5);
- the detection cardinality (R3-7);
- the customer portal profile (R3-8).

Three MAJOR defects remain. Each one either contradicts a stated requirement or is specified incompatibly by two sections:
1. **R4-1.** The new "cumulative cut" cannot be computed in its current form. It breaks whenever the prior fiscal year is still open, which happens every year. It double-counts the ledger's year-end opening entries. Nothing stores the subledger's "pinned" year-end totals.
2. **R4-2.** `:override-negative` allows negative stock that the item model group forbids. That contradicts NFR-D4 and the `on_hand` CHECK constraint in the data model.
3. **R4-3.** The `:cancel` precondition depends on a `shipment.reversed` event and a "reversal command", and neither exists. For shipments it also contradicts S2, which says there is no backward compensation.

Each is a one-paragraph fix.

| Status of round-3 and carried items | Count |
|---|---|
| Resolved | 8 (R3-1, R3-5, R3-7, C12, N1, N2, N4, N12) |
| Partially resolved | 8 (R3-2, R3-3, R3-4, R3-6, R3-8, N3, N6, N9) |
| Not resolved | 0 |

| New findings | Count |
|---|---|
| CRITICAL | 0 |
| MAJOR | 3 |
| MINOR | 10 |

---

## Per-finding verification

| ID | Status | Evidence (line: what it now says) | Residual |
|---|---|---|---|
| R3-1 journal link-back | **Resolved** | 4023: `inventory.transaction.posted.v1` carries `journalEntryId`, `glSeq` and `reservationId` (additive, v1). 1580: the allow-list holds `cogs_gl_seq` for shipments, plus `valuation_journal_entry_id`/`valuation_gl_seq` for the receipt and production order. 2903 and 2947–2949: EN-050 and EN-053 have the columns. 4297–4299: `procurement-receipt-valuation`, `manufacturing-raf-valuation` and `sales-shipment-cogs` (which sets `cogs_gl_seq`). 4202–4203: subscribed on topic rows 19/20. 4483: §9.1 step 3 sets status "on the reservation only". | Minor gaps in R4-5: the shipment never reaches `rejected`; S2 omits `cogs_gl_seq`; the per-RAF link is overwritten on the order header. |
| R3-2 cut not computable | **Partial** | `gl_seq` added on: vendor_invoice and payment (2907ff.); customer_invoice (3028); asset_transaction (3127); payroll_run (3154); project_transaction (3166). EN-053 has `wip_gl_seqs` and `variance_gl_seq` (2945–2946). 1580: the `gl_seqs bigint[]` rule. 1004: `reconciliation-run` runs in `platform-ts`, and `reconcile` is a Go library behind each endpoint (also in T5.1, 6174). 1004 and 6173 define the cumulative cut. | The cumulative cut is unsound (**R4-1, MAJOR**). `report_as_finished.journal_entry_id` and `tax_settlement.journal_entry_id` have no `gl_seq` (R4-5). The T5.1 example endpoints are stale (R4-8). |
| R3-3 reservation lifecycle | **Partial** | 1097, rule 5: TTL is 30 min; (a) adds to `on_hand.reserved_physical` and (c) releases it; the sweeper looks up `GET /v1/ledger/posting-requests/{key}` first and "never expires" a posted reservation; re-drive reuses the row under IX-IN-35; `:cancel` requires a reversed source or `reverseSource=true`. 2850–2857: EN-077 has `awaiting_reversal`, and IX-IN-36 includes `expired` and cancelled rows awaiting reversal. 4026: `valuationreservation.expired.v1` (row 62). | The quantity hold and post-then-expire are fixed. The `:cancel` precondition introduces R4-3 (MAJOR). A narrower race remains between the sweeper and finalize (R4-4). |
| R3-4 retry semantics | **Partial** | 701: `:retry` takes an optional `postingDate` with the `:<revision>` suffix. 1184: only 2xx is cached, and a 422 from `ledger.Post` is not. 4300: the task completes on a matching `transaction.posted`. 3990: `valuation_rejection` is in the `taskType` enum. 4440, 4520 and 4567: the `retryWithDate` wording is gone and the S3a `:retry` arrow targets inventory. 703: `:override-negative` is defined with its duty and SoD. | The override contradicts NFR-D4 (**R4-2, MAJOR**). Not caching 4xx deviates from D.5 and is not recorded as a deviation (R4-11). |
| R3-5 project/service billing | **Resolved** | 3166: `project_transaction` stays IL, with an IL child `project_transaction_billing` and a derived billed state. `invoice_proposal.invoice_id` exists. 3172: `service_work_order.billing_status` and `billing_invoice_id`. 4308: `projects-billing` filters on `sourceDocument.type = invoice_proposal`. 4317: `projects-service-billing` filters on `service_work_order`. 1901: ER line. | IX-PJ-05 still indexes the removed `project_transaction.billing_status`, and the invoice event has no `glSeq` (R4-6). |
| R3-6 stale wording | **Partial** | 331, 724, 764, 4283 and 4289 are reworded ("post, then finalize…", "reserve, post, finalize (§6.4 rule 5)"). | The same contradiction survives at 1378 (Q6), 3726, 4426 and 4481 (R4-7). |
| R3-7 detections | **Resolved** | 1315: the counters carry `{tenant,entity,reason}` with no `actor`; the `platform-ts` audit-stream job emits `passage_actor_anomaly_total{tenant,detector}` and opens a security task; cross-LE reads use OPA decision logs in Loki through LogQL. 1580: `<ctx>.audit_event` is `GRANT INSERT, SELECT` only. | Decision-log masking and retention are unstated (R4-13, overlay MINOR). |
| R3-8 small inconsistencies | **Partial** | 7754 and 7756: the Postgres and Kafka node volumes use "LUKS via Clevis/Tang (bare metal), provider KMS (cloud)". 7767: the payment processor is reachable "from `sales`". 591–592: `GET`/`PATCH /v1/party/portal/profile` in party, and the sales rows are removed. 829: `/cases` shows dispute cases until wave 45. | The Temporal, Keycloak and OPA rows still name Vault transit as the volume key owner (R4-9). The supplier portal profile is still a procurement write to party data (R4-10). |
| C12 nit | **Resolved** | 401: `/readyz` reads "own Postgres, migrations current, OPA sidecar…", which matches 1248 and 7735. | — |
| N1 valuation protocol | **Resolved** | 1094–1097: rules 2 and 5 are complete, including the sweeper, the quantity hold and re-drive. | Leftover stale wording is tracked under R3-6/R4-7. |
| N2 COGS link | **Resolved** | 4023, 4299 and 1580 (see R3-1). | R4-5 (S2 diagram and table nit). |
| N3 rejection / retry | **Partial** | The task lifecycle and retry are complete (see R3-4). | R4-2 and R4-3. |
| N4 handoffs | **Resolved** | See R3-5. | R4-6 (minor). |
| N6 reconciliation cut | **Partial** | The emitter deployable and the subledger `gl_seq` are fixed (see R3-2). | R4-1. |
| N9 encryption at rest | **Partial** | The Postgres and Kafka rows are fixed. | R4-9 (minor). |
| N12 portal | **Resolved** | 591–592 and 829. | R4-10 is the supplier-side analogue. |

---

## New findings (de-novo review of the edited sections)

### R4-1. The cumulative reconciliation cut is unsound in three ways (MAJOR)
- **Domains:** Backend §5.20 (1004); Testing T5.1 (6173); Data EN-027 `gl_seq` (2478, "scoped per tenant x legal entity x fiscal year"); `entryType` enum `closing`/`opening` (4009); year-end close (660).
- **Description.** A balance-sheet check at `(le, fy, W)` compares two parts: (a) "the closing balance of the last closed fiscal year on each side", and (b) "the current year's movements with `gl_seq <= W`".
  1. **Prior year still open.** Year-end close usually runs weeks into the next year. During that window, "last closed" is FY−2, and the open prior year FY−1 falls in neither (a) nor (b).
     - FY−1 movements are missing from both sides.
     - Late FY−1 adjustments posted during the window are therefore invisible to the daily check.
     - A break in FY−1 is not detected until the year closes. That contradicts NFR-D1 ("0 unexplained variance, daily") and goal 7 ("zero unexplained breaks, checked daily").
  2. **Opening entries double-counted.** The ledger posts `closing`/`opening` entries (`entryType`, 4009). The opening entry of FY lands in FY's `gl_seq` space, so (a)'s closing balance plus (b)'s movements counts the carried-forward balance twice on the ledger side. The subledger side has no opening document to match it.
  3. **No storage for pinned totals.** The subledger totals "pinned at year-end close" have no entity, no endpoint and no trigger. The year-end close run (660) does not ask the subledgers to pin anything.
- **Severity:** MAJOR. An implementation that follows the contract either misses breaks for part of every year or reports a variance equal to every opening balance.
- **Recommendation:**
  1. Define (a) as the closing balance of FY−1 only when FY−1 is closed. Otherwise, add FY−1's movements at its own watermark, so the contract takes one `glSeq` per open fiscal year.
  2. Exclude `entryType = opening` from (b) on the ledger side, or drop (a) and use the opening entry.
  3. Add a `<ctx>.reconciliation_snapshot (check, le, fy, key, total, pinned_at)` written by each context on `ledger.period.closed` for period 12 or year-end.
  4. Mirror the result in T5.1.

### R4-2. `:override-negative` contradicts NFR-D4 and the `on_hand` CHECK (MAJOR)
- **Domains:** Backend §5.9 (703); Events S2 (4520, 4532) and §9.0 (4440); Data EN-039 (2776, 2790, 2794) and `item_model_group` (2271); requirements NFR-D4 ("0 unless item model group allows") and §8 data integrity ("no negative inventory unless policy allows").
- **Description.**
  - 703 re-runs a reservation "rejected with `inventory_insufficient`, allowing negative stock for this item".
  - A rejection only happens when the item model group forbids negatives. With `allow_negative_physical = true`, the issue succeeds.
  - So the override exists precisely to break the item model group's policy, which is what NFR-D4 forbids.
  - It also cannot be implemented as specified. `on_hand` carries `CHECK (allow_negative OR physical_on_hand - reserved_physical - blocked_qty >= 0)` with `allow_negative` copied from the item model group, so the database rejects the override's write.
  - 4532 adds "per item model group", which only makes the two texts ambiguous.
- **Recommendation:** Pick one of two options.
  - Remove `:override-negative`. Forward recovery is then the counting or adjustment journal followed by `:retry`, as S2 already lists.
  - Or make the exception a policy the requirement recognises. Add an item-model-group flag such as `allow_negative_with_approval`, copy it to `on_hand`, extend the CHECK to admit an approved override row, and state that NFR-D4's "policy allows" includes it.

  In either case, give 703 a privilege code like its neighbours (it has only a duty name).

### R4-3. The `:cancel` precondition depends on events and commands that do not exist (MAJOR)
- **Domains:** Backend §5.9 (702) and §6.4 rule 5 (1097); Events catalog (4037 `productreceipt.reversed`, 4048 `rafreversed`; topic row 29 `passage.sales.shipment` has only `posted`, 4139); S2 (4527, 4532).
- **Description.**
  - `:cancel` is allowed when "`productreceipt.reversed`, `shipment.reversed` or `productionorder.rafreversed` [is] received". No `sales.shipment.reversed` event exists in the 62-type catalog.
  - S2 says the shipment is the pivot with "no backward compensation". A wrongly posted shipment is corrected by a return or a reversal packing slip, and neither produces `shipment.reversed`.
  - With `reverseSource=true`, `:cancel` "emits a reversal command to the owning context". No event type, consumer or API call is defined for that command in procurement, sales or manufacturing, and the event and consumer totals do not include one.
  - S2 (4527) still lists `:cancel` as a rejection outcome, which the precondition makes unreachable.
  - What `inventory-receipts` and `inventory-raf` do on `reversed` when the original reservation never finalized (`rejected`/`expired`) is not stated either.
- **Recommendation:**
  1. State that shipment reservations cannot be cancelled: forward recovery only, or a return order.
  2. Delete `shipment.reversed`.
  3. Define `reverseSource=true` as a synchronous call to the owning context's existing `POST …/product-receipts/{id}:reverse` or RAF `:reverse` (with a deterministic idempotency key), not as an undefined command.
  4. Specify that the `reversed` handler cancels a non-finalized reservation instead of reversing transactions that were never inserted.

### R4-4. A race between the sweeper and finalize remains (MINOR)
- **Domains:** Backend 630 and 1097; Data EN-029 (2569–2582), EN-077.
- **Description.**
  - `GET /v1/ledger/posting-requests/{key}` promises `in_progress`, but `ledger.posting_request` has no state column. Its row is inserted inside the uncommitted posting transaction (8.3 step 1), so a concurrent lookup returns `404` while a post is in flight.
  - The sweeper can then expire the reservation and release its layers. The post commits, and step (c) runs against an `expired` row. Step (c) is not specified as conditional on `status = 'reserved'`, and the behaviour when it is not is undefined.
  - When the sweeper itself finalizes a posted reservation, it cannot write the consumer's inbox row because it does not know the event id. A later redelivery then meets a `finalized` row, and that path is undefined.
  - Keys such as `post:inventory:shipment:<id>:cogs` in a path segment clash with the `:<method>` custom-method syntax unless they are percent-encoded.
- **Recommendation:**
  1. Make (c) an `UPDATE … WHERE status = 'reserved'`. On `expired`, re-acquire the same layers, or raise a `valuation_rejection` task.
  2. Have the sweeper skip reservations whose key the ledger reports as `in_progress`, and implement that state, or drop it from the contract.
  3. On redelivery, treat a `finalized` row as a no-op that commits the inbox row.
  4. Require the key to be percent-encoded, or pass it as `?key=`.

### R4-5. Residual link-back gaps (MINOR)
- `sales-shipment-cogs` consumes only `posted` (4299), and `transaction.rejected` (4203) has no sales consumer. `shipment.gl_posting_status` therefore never reaches `rejected`, although the CHECK allows it.
- S2 (4515, 4528) still omits `cogs_gl_seq`.
- The RAF key is per `rafId` (4289), so there is one valuation journal per RAF. EN-053 holds a single `valuation_journal_entry_id`/`valuation_gl_seq` on the order header, so partial RAFs overwrite each other. The link belongs on `report_as_finished`.
- `report_as_finished.journal_entry_id` (2952) and `tax_settlement.journal_entry_id` (3206) have no `gl_seq`, contrary to the §4.6 rule.
- The backend immutability row (1053) omits `gl_seq` and the `valuation_*`/`cogs_*` columns that §4.6 allows.

### R4-6. Project billing residuals (MINOR)
- `project_transaction_billing.gl_seq` is set from `sales.invoice.posted`, but that payload (4060) carries `journalEntryId` and no `glSeq`. Add `glSeq` to the invoice event (additive), as R3-1 did for inventory.
- IX-PJ-05 (3174) indexes `project_transaction.billing_status`, a column that no longer exists. Index `project_transaction_billing (tenant_id, project_transaction_id)` instead.
- `invoice_proposal.invoice_id` is "set when the invoice is created" (3166), but the consumer sets it on `invoice.posted` (4308). Pick one.

### R4-7. Stale "inside its own transaction" wording (MINOR)
Four places still contradict §6.4 rule 2:
- 1378 (Q6, "posts synchronously inside its own database transaction");
- 3726 (the same sentence);
- 4426 ("Valuation owner posts inside its own transaction");
- 4481 (S1 step 1, "Sync `ledger.Post` inside `:post`; outbox in the same transaction").

Reword them as in R3-6.

### R4-8. T5.1 example endpoints contradict §5.20 (MINOR)
6173 cites `GET /v1/inventory/reconciliation/value?asOf=&ledger=&groupBy=itemGroup` and `GET /v1/ledger/balances?...`. The contract (1004) is `GET /v1/<context>/reconciliation/{check}?le=&fy=&glSeq=` on both sides. Align the examples.

### R4-9. Encryption table key owners (MINOR, overlay: Technical Safeguard)
- The Temporal and Keycloak rows "follow [their] Postgres PV" yet name "Vault transit / provider KMS" as key owner. The Postgres PV row names Clevis/Tang (bare metal).
- The OPA row has LUKS on bare metal with a Vault-transit key owner.
- Apply the 7765 rule (nodes decrypt before Vault runs) to these three rows.

### R4-10. Supplier portal profile is a cross-context write (MINOR)
`PATCH /v1/procurement/portal/profile` (754) changes supplier bank details that `party` owns. This is the defect R3-8 fixed for customers. Move it to `/v1/party/portal/profile` with supplier scope, or have procurement call the party API.

### R4-11. 4xx-not-cached is an undocumented deviation from D.5 (MINOR)
- Appendix D.5 says the server "stores … the final response for 24 h". 1184 now stores only 2xx.
- The behaviour is sensible, and the ledger's own `posting_request` guarantee is unaffected. Record it in the deviation table (an ADR per the delivery model).
- Separately, 5486 maps "`idempotency_key_reuse` with the same body hash" to `done`. By D.5, that code only occurs with a different hash, so the row should be reworded.

### R4-12. No tests for the reservation lifecycle (MINOR)
The testing section (5850–6760) has no case for any of:
- post-then-crash then sweeper finalize;
- expiry and re-drive;
- the `:cancel` precondition;
- quantity held between (a) and (c) under concurrent issues (NFR-D4);
- task completion on `transaction.posted`;
- `GET /v1/ledger/posting-requests/{key}`;
- `valuationreservation.expired.v1`.

Add them to the FR-3.1/FR-5.3 rows and to the outbox/inbox chaos suite (6221).

### R4-13. OPA decision logs as an audit source (MINOR, overlay: Audit Control)
Cross-LE read detection now relies on OPA decision logs in Loki (1315). The following are not stated:
- decision-log masking of `input.resource` fields (amounts, owner);
- Loki retention for these logs relative to the detection window and SOC 2 evidence;
- whether a dropped log batch is detectable.

State the OPA `decision_logs` mask rules and the retention.

---

## Requirements coverage changes since round 3

| Requirement | Status | Notes |
|---|---|---|
| FR-3.3 GR/IR | COVERED | Receipt status and valuation link fed (R3-1); expiry no longer unbalances |
| FR-4.1 RAF/WIP | COVERED | WIP `gl_seqs`; per-RAF link minor (R4-5) |
| FR-5.3 packing slip with COGS | COVERED | COGS id and `gl_seq` fed |
| FR-8.1 project billing | COVERED | IL child table; minor R4-6 |
| FR-8.5 service billing | COVERED | |
| NFR-D1 daily reconciliation | PARTIAL | Cumulative cut unsound across year-end (R4-1) |
| NFR-D4 negative inventory | PARTIAL | Quantity hold fixed; `:override-negative` breaks policy (R4-2) |
| FR-0.5 / Auditability | COVERED | R4-13 minor |

## Moonaan standards compliance

There are no new violations:
- ADR-0001 governs, and the deviation table records it.
- The new endpoints use RFC 9457 problem details.
- `valuationreservation.expired.v1` follows the CloudEvents envelope mapping and shares topic 51.
- No API gateway product is introduced.

## Compliance overlay findings

| Requirement | Category | Description | Severity | Recommendation |
|---|---|---|---|---|
| Encryption at rest | Technical Safeguard | Key owner inconsistent for the Temporal, Keycloak and OPA volumes on bare metal (R4-9) | MINOR | Clevis/Tang for node volumes |
| Monitoring | Audit Control | OPA decision-log masking and retention unstated (R4-13) | MINOR | State the mask rules and retention |
| Minimum necessary | Sensitive Data Handling | Supplier bank details are written through procurement instead of the owning context (R4-10) | MINOR | Route to party |

There is no CRITICAL overlay finding.

## Open question assessment

- **Valid:** #38 (TTL, lock scope and sweeper cadence; @zdemanche, wave 16) and #37. Both have owners and waves.
- **Presented as resolved but contradicted by the text:**
  - #10 claims "a `:cancel` precondition" that depends on a non-existent event (R4-3).
  - #17 and testing OQ-6 claim the cut is resolved (R4-1).
- **Should be raised (or folded into #38):** the year-end reconciliation pinning, and whether negative-stock overrides exist at all. The second is a product decision for @BenjaminSRussell.

## Summary

The round-3 edits closed most of their targets:
- the posted event and allow-list now feed every link-back;
- every subledger journal link carries `gl_seq`;
- the reservation holds quantity and is never expired after a successful post;
- the retry and task loop closes;
- the billing and portal ownership defects are fixed.

Three MAJOR defects remain, and each is a short, contained fix:
- the cumulative reconciliation cut breaks across an open prior year and double-counts opening entries (R4-1);
- `:override-negative` breaks NFR-D4 and the `on_hand` CHECK (R4-2);
- `:cancel` depends on a shipment reversal and a reversal command that do not exist (R4-3).

With those and the ten minor items fixed, the document is ready for backlog generation.

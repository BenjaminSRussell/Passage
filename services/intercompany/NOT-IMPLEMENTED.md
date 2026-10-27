# NOT-IMPLEMENTED: intercompany (PSG-40, wave 40)

Slice 1 is stateless: pure domain rules, the journal preview and the
reconciliation classify endpoint, and the web workspace for both. Every
deliverable and acceptance criterion not listed as built in the README is
below, with its status and reason.

| Item | Status | Reason |
|---|---|---|
| Persistence: `intercompany_partner`, `ic_document_link`, `ic_reconciliation_item`, `ic_charge_request`, accounting matrix tables, migrations, RLS, `idempotency_key`, `outbox` | later slice of PSG-40 | store slice; needs the kernel `dbx` tenant GUC change (`passage.tenant_id`) before RLS can be tested |
| FR-40.1 Trading partners: create pairing and policies (`POST /api/v1/interco/partners`), value maps, IC pricing policy, one active pairing per direction | later slice of PSG-40 | needs persistence and `Idempotency-Key` |
| FR-40.2 Mirror saga as a Temporal workflow (Creating → Linked / Compensated / Failed), `POST /api/v1/interco/mirrors`, `IcDocumentLink` rows | later slice of PSG-40 | the Temporal worker is a TypeScript deployable (interco-svc). The Go module holds only the pure state logic; the worker and its place in the deployment are a handoff to the eventing-backbone and deployment-cicd owners |
| FR-40.2 mirror matrix (sales → purchase, packing slip → receipt, invoice → vendor invoice, RMA, FA transfer) | later slice of PSG-40 | depends on sales, procurement and inventory waves for the document APIs |
| FR-40.2 / BR-40.3 change tolerance re-approval (IC-0804) | later slice of PSG-40 | needs the mirror PO and wave 17 workflow |
| FR-40.3 Direct delivery chains (`DirectDeliveryChain`, chain view) | later slice of PSG-40 | needs IcDocumentLink persistence |
| FR-40.4 IC pricing from policy and transfer-pricing evidence export (CSV) | later slice of PSG-40 | needs pricing policy persistence |
| FR-40.5 IC journal posting (`POST /api/v1/interco/journals`, both sides atomic, FX difference to IC FX accounts, compensation on second-side failure) | later slice of PSG-40 | needs the wave-11 journal API; only the derivation is built |
| FR-40.6 Stored reconciliation runs (`POST /api/v1/interco/reconciliations/run`), resolve (`/items/{id}/resolve`, status Resolved, resolution note), `ic-reconcile` job, `ic-recon-planted-50` testdata file | later slice of PSG-40 | the classifier and a stateless classify endpoint are built and the web page calls it; runs and resolve need persistence. The 50 planted mismatches are generated in `recon_test.go`, not loaded from a testdata file |
| FR-40.12 Centralized payments setup and posting (`/api/v1/interco/centralized-payments/setup`) | later slice of PSG-40 | needs wave 19 payment proposals and wave 30 payments |
| FR-40.13 IC charge submission (`POST /api/v1/interco/charges`), Journal and Invoice settlement, IC-0821 transfer price lookup | later slice of PSG-40 | only cost-plus pricing is built; transfer price table and posting need persistence |
| FR-40.14 Intercompany on-hand read model (`/api/v1/interco/onhand`) | later slice of PSG-40 | needs the wave-16 on-hand events |
| FR-40.15 Planning groups and sequenced planning run (IC-0815, IC-0816) | later slice of PSG-40 | needs wave 22 MRP |
| Events `dev.passage.interco.mirror.created.v1`, `mirror.compensated.v1`, `journal.posted.v1`, `reconciliation.flagged.v1`, `charge.posted.v1`, `planning.completed.v1` | later slice of PSG-40 | published with the store and saga slices; `events/asyncapi.yaml` currently has no channels |
| Sweeper job `ic-saga-sweeper`, scheduled `ic-reconcile` | later slice of PSG-40 | Temporal and store dependencies |
| Message catalog codes IC-0802, IC-0803, IC-0806, IC-0810, IC-0811, IC-0825 | later slice of PSG-40 | raised by the saga, reconciliation and on-hand paths not yet built |
| AC-40.1 end-to-end (sales order in A creates a linked PO in B) | later slice of PSG-40 | needs the sales and procurement APIs; the balance property is covered by the domain test |
| AC-40.2 under planted number-sequence failures, no orphan documents | later slice of PSG-40 | needs the sequence service and saga persistence; the domain property test covers the saga state logic only |
| AC-40.3 direct-delivery chain posts receipt and packing slip in one saga | later slice of PSG-40 | depends on FR-40.3 |
| AC-40.5 planted 50 mismatches flagged | partial | `TestClassify_PlantedMismatches` flags all 50 planted (missing mirror, wrong amount, wrong period) and none of 50 clean pairs; the end-to-end run over stored items is later |
| AC-40.10 P pays S's vendor invoice, due-to / due-from in both entities with one ic_pair_id | later slice of PSG-40 | depends on FR-40.12 |
| AC-40.11 IC charge with cost-plus 10% on 8 h at 60.00 (528.00 / 480.00 / 48.00 split) | partial | the cost-plus arithmetic is built (`PriceCharge`); posting in both entities is later |
| AC-40.16 planning group sequence with lead time | later slice of PSG-40 | FR-40.15 not built |
| `GET /api/v1/interco/links`, `POST /api/v1/interco/reconciliations/run`, `/reconciliations/items/{id}/resolve` and every other PRD 7.40.5 endpoint | later slice of PSG-40 | persistence and idempotency first |
| Workspaces: pending sagas and failed mirrors, chain view with keyboard graph navigation, side-by-side reconciliation grid with resolve, partner setup | later slice of PSG-40 | the web module has the workspace landing page, the journal preview and the reconciliation classifier pages; the other screens need the stored endpoints above |
| Performance budgets (7.40.19), `IC_SAGA_TIMEOUT_S`, `ic.fxTolerancePct` parameter | later slice of PSG-40 | parameters come with persistence and config |
| Migration from AX / SAP (7.40.11) | later slice of PSG-40 | needs the migration framework (integration DB) |
| Feature flag `interco.payment-mirroring` | later slice of PSG-40 | no payment mirroring yet |
| Helm: `DATABASE_URL` from secret and a `/readyz` that checks the database | later slice of PSG-40 | the service has no database yet; the chart already has startup, liveness and readiness probes on `/healthz` and `/readyz`, the admin port and an optional ServiceMonitor for `/metrics` |
| Handoff: Temporal worker deployable (interco-svc, TypeScript) and its Argo CD application | handoff | owned by deployment-cicd and eventing-backbone; the Go module must not host the worker |
| Handoff: `ledger.Post` client for journal and charge postings | handoff | `ledgerclient` is a kernel addition; owned by the platform kernel (wave 0) |
| IC-0830 derived journal unbalanced | not in PRD catalog | raised by `Journal.Balanced` as a defect guard; listed for the catalog owner |

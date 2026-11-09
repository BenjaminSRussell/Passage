# Changelog

All notable changes to Passage are recorded in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). Until 1.0.0 any minor
release may change public APIs.

## [Unreleased]

### Added

- EDI: an inbound 850 or ORDERS becomes a sales order (#35 P4a). Partner item cross-references (BP, VP, UP or EN to product,
  site and warehouse) and partner sales defaults under `edi.admin` with legal-entity RLS (migration 0010);
  `POST /edi/v1/messages/{id}:post` places the order with sales under the new service grant `edi` -> `sales`
  `sales.channel.intake` (key = message id, externalRef = partner id and PO number), records the sales order on the
  message (POSTED, `message.posted`), and parks what it cannot map or sales refuses (FAILED with a park code in the
  message monitor).
- Batches and serials in the inventory postings (#480): the postings consumer books a receipt line's `batch` as the
  batch dimension and a serial-tracked receipt line as one registration per serial (one inventory unit each, an
  equal share of the document quantity and value, the rounding remainder on the last), each under its own origin
  that names the line (`inventory_transaction_origin.line_source_id`, inventory migrations 0036 and 0037, the index
  built concurrently). Sales' `:record-progress` lines take optional `tracking` entries on a DELIVERED report
  (batch, serial, quantity, and on a catch-weight line split over several entries each entry's `actualWeight`),
  passed through on `orderprogress.recorded`; inventory issues each entry under its batch and serial at its own
  row's running average. Receipt cancellations and cancelled packing slips offset every entry of the line. Tracking
  that cannot be booked parks with the new `INV-PST-005`. Location is still not booked: receiving names it by id and
  fulfillment by code. Receiving arrival and direct receipt lines without a purchase order line take `siteId` and
  `warehouseId`, carried on `receipt.posted`, so inventory books them instead of parking them.

- Partner AS2 certificate chain and revocation checks in edi (#483): with the new `EDI_AS2_TRUST_ROOTS` (a PEM bundle of
  CA certificates; chart `transports.trust.rootsConfigMap`) a partner's pinned AS2 certificate must also chain to a
  trust root through the intermediates now accepted after it in `partnerCertificate`, and with `EDI_AS2_CRL_DIR`
  (chart `transports.trust.crlConfigMap`) every certificate of the chain needs a current CRL from its issuer that does
  not list it. Both fail closed when a transport is saved (400) and at every use (`internal/certtrust`): an untrusted
  or revoked certificate gets 403 at the AS2 listener and fails outbound deliveries at once, a missing or expired CRL
  answers 503 and backs deliveries off until it is refreshed. The files are reread when they change; nothing is
  fetched from the network (no OCSP or CRL distribution points). `GET /partners/{id}/transport` shows
  `partnerCertificateTrust`. Pinning is unchanged: signatures are still verified against the stored certificate only.
- Partner update in edi (#483): `PATCH /edi/v1/partners/{id}` (duty `edi.admin`, seeded in `policies/authz/data.json`
  next to the existing partner read) changes a partner's name, ISA identity, ack SLA, status (inactive stops receipt,
  the AS2 listener and outbound documents) and EDIFACT identity as a JSON merge patch under `If-Match` on the partner
  version (428 `COM-0428`, 412 `conflict_version`, new ETag on 200); the environment cannot change. A partner bound
  to a legal entity by its trade profile is changed only by callers scoped to that entity (403 EDI-0018), read through
  a SECURITY DEFINER `edi_partner_in_entity_scope()` over a trigger-maintained `partner_entity_index` (migration 0009,
  allowlisted in `tools/rlscheck`); `passage_app` gets UPDATE on the changed columns only. The policy seeds also gain
  the missing `POST /edi/v1/inbound/edifact` route under `edi.operate`, the duty the service already requires for it.
- TA1 interchange acknowledgments and duplicate answers in edi (#483): the X12 codec reads and writes TA1 segments.
  An interchange an AS2 or SFTP transport brought from the authenticated partner that fails at the envelope (IEA
  missing, IEA01 or IEA02 wrong, no group, content after IEA) is answered with a TA1 R and its TA105 code (023, 021,
  001, 024) instead of a 997, and a duplicate interchange (EDI-0102) with a TA1 R 025, or for EDIFACT a CONTRL UCI 4
  with code 26 (AC-35.5). The answer is recorded as a refused interchange in its own transaction after the rolled-back
  receipt, takes the next number of the partner's gap-free counter, is shown as `ta1` on the interchange and is
  queued back over the partner's transport; the duplicate's AS2 message or file is marked DONE so a retransmission is
  not answered twice. A TA1 from the partner is matched to our outbound interchange (TA1 R rejects its messages and
  publishes `message.acked`). The operator routes still answer duplicates with 409 only.
- Inbound 820 and 214 receipt in edi (#483): the X12 receipt maps the 820 onto the canonical remittance and the 214
  onto the shipment status next to the 850, with the same 997 codes, `dev.passage.edi.message.received.v1` and
  `message.failed`; reprocess and `maps:test` (new `document` field) cover them, and party names are masked without
  `edi.pii.view`. Posting received documents into sales, transport or receivables stays a handoff and no service grant
  was added: sales' `sales.channel.intake` route needs product, site, warehouse and invoice-account ids the 850 and
  ORDERS do not carry (BR-35.1 item cross-reference), transport's tracking ingest needs a load id and stop, and
  receivables has no remittance route (`services/edi/NOT-IMPLEMENTED.md`, Handoffs).
- Outbound EDIFACT with CONTRL matching (#483): edi's `POST /edi/v1/partners/{id}/outbound` builds ORDERS, DESADV
  (held to the 856 rules) and INVOIC (held to the 810 rules), and `outbound:from-source` builds DESADV from the
  fulfillment shipment and INVOIC from the billing invoice under the existing read grant. Each goes in a UNOC syntax
  3 interchange from our UNB identity (new `EDI_EDIFACT_ID`/`EDI_EDIFACT_QUALIFIER`, default PASSAGE/ZZZ, chart
  `edifactSender`) to the partner's `edifactId` (409 EDI-0017 without one), asks for a CONTRL, takes UNB 0020 from the
  partner's gap-free EDIFACT counter (shared with the CONTRLs we send it), and is queued over the partner's AS2 or
  SFTP transport like X12. A CONTRL from the partner is now matched by UCI 0020 to that interchange the way a 997 is
  matched: messages become ACKED, or FAILED for UCI 4 or a UCM 4 naming them, the interchange takes the CONTRL status
  and `acked_at`, and `dev.passage.edi.message.acked.v1` is published per message (its `transaction_set` and
  `control_number` now also carry EDIFACT values); an unmatched CONTRL stays FAILED with EDI-0161. The outbound result
  gains `standard` and `edifact` (`x12` is omitted for EDIFACT).
- Inbound EDIFACT over the transports, with CONTRL (#483): the edi receipt tells X12 (ISA) from EDIFACT (UNA/UNB)
  by the first segment, so the AS2 listener, the SFTP poller and the new `POST /edi/v1/inbound/edifact` take EDIFACT
  interchanges. They are read with UNG/UNE functional groups and every count and reference checked; the UNB sender
  must be the partner's new `edifactId`/`edifactQualifier` (and the transport's partner); the UNB reference is
  checked against the duplicate window; ORDERS, DESADV and INVOIC are mapped onto the canonical order, ASN and
  invoice with the 850/856/810 rules and published as `dev.passage.edi.message.received.v1` (standard EDIFACT).
  Partners get a CONTRL (`edifactContrl`: REQUESTED when UNB 0031 = 1, ALWAYS, NEVER) on their own gap-free
  EDIFACT counter, queued back over their AS2 or SFTP transport like the 997: UCI 7 or 4, UCF per rejected group,
  UCM 4 with UCS and UCD placing each syntax or mapping error. A payload an authenticated transport brought and the
  receipt refused for syntax or sender is now also answered with a negative 997 (AK502/AK905 trailer codes) or
  CONTRL (UCI 4 with the 0085 code, 23 for a foreign sender) next to the error MDN or the parked file, recorded as a
  refused interchange (migration 0008: `interchange.refusal`) that never counts as a duplicate.
- AS2 edge and listener hardening (#483): trading partners reach the edi AS2 listener through Gateway `passage-as2`
  on `as2.dev.passage.dev` (`deploy/gateway/as2-*.yaml`), a Gateway of its own outside the `passage-api` JWT
  SecurityPolicy, with one HTTPRoute (`POST /as2/v1/` to the `edi-as2` Service), HTTPS only with a cert-manager
  Certificate, TLS 1.2 or newer, the identity headers stripped, an 8Mi request buffer (the listener's cap) and a
  global rate limit per client address; the edi chart's new NetworkPolicy admits only that gateway's proxies to the
  AS2 port, and `test_gateway.py` proves every API route stays behind the JWT policy and the AS2 route exposes only
  `/as2/v1/`. The listener adds a token bucket per endpoint (`EDI_AS2_RATE_PER_MINUTE`, `EDI_AS2_RATE_BURST`, 429
  with `Retry-After` before any lookup or signature work). Asynchronous MDNs we owe are stored in `transport_mdn`
  (migration 0007) in the transaction that records the message and posted by the delivery dispatcher with leases,
  back-off and FAILED after the last attempt, only to the partner's AS2 host and without following redirects.
  Deliveries waiting for an asynchronous MDN time out after `EDI_AS2_MDN_TIMEOUT_MINUTES` (default 24 hours), are
  sent again with the same Message-ID or FAILED, and every FAILED delivery publishes
  `dev.passage.edi.delivery.failed.v1`.
- Inventory postings consumer follow-ups (#480): plain (not catch-weight) receipt and delivery lines are booked in
  the product's inventory unit, converted by the product service (`GET /v1/product/uom/convert` without `to` now
  converts into the product's base unit; new service-only duty `product.uom.convert`, granted inventory -> product).
  Without `PRODUCT_URL` such a line is parked (`INV-PST-002`), and a conversion product refuses parks it
  (`INV-PST-001`). The consumer also offsets its bookings: receiving's `receipt.corrected` books a correction's delta
  or a cancellation's remaining net, and fulfillment's `packingslip.cancelled` books the exact mirror of the
  delivery. Each offset is read back from the booking it corrects (`corrects_transaction_id`), keyed so replays
  book nothing twice, and refused when the original is missing (`INV-PST-003`) or does not match (`INV-PST-004`).
  Additive event fields: `receipt.corrected` gains `legal_entity_id`, `posting_date` and, on a cancellation,
  `cw_qty_delta`; `packingslip.cancelled` gains `packing_slip_number` and `legal_entity_id`. New shared fixtures in
  `services/inventory/testdata/catchweight-flow` are checked by the receiving and fulfillment producer tests.
  `deploy/kafka/inventory-consumer.yaml` adds the consumer's KafkaUser, literal ACLs and dead-letter topic, and
  `make -C deploy test-argocd` checks consumer KafkaUsers against their charts. The consumer stays off in dev, like
  the other consumers.
- Sales and fulfillment outboxes reach Kafka in dev (#480): new `outbox-sales` and `outbox-fulfillment` Debezium
  connectors (publication and CDC role grants in sales migrations 0039 and 0040 and fulfillment migrations 0029 and
  0030, each with a re-run test; own `debezium_cdc_sales` / `debezium_cdc_fulfillment` roles and `passage_sales` /
  `passage_fulfillment` databases in the dev Postgres, init-rev 9; Vault keys `passage/dev/sales-postgres/cdc-password`
  and `passage/dev/fulfillment-postgres/cdc-password`). Both route by event type to `passage.<context>.<entity>`, the
  AsyncAPI channel, because these services write several entities under one aggregate_type: `orderprogress.recorded`
  (aggregate `order`) lands on `passage.sales.orderprogress` and `packingslip.*` (aggregate `shipment`) on
  `passage.fulfillment.packingslip`, the topics the inventory postings consumer reads. The debezium user gets a literal
  ACL per channel; `make -C deploy test-argocd` checks the route and the ACLs against each service's AsyncAPI, and the
  enricher chain test runs Debezium's EventRouter with the type route.
- Live tender trigger (#426): transport's `POST /v1/transport/loads/{id}:release` (duty `transport.load.tender`,
  migration 0010) applies the tender gates and publishes `dev.passage.tms.load.released.v1` with the releasing user
  and legal-entity scope from the verified caller; workflow-ts's new Kafka consumer (`src/eventConsumer.ts`, kafkajs)
  starts `tenderWorkflow` from it and sends `tender-response` on `tender.accepted.v1` and `tender.declined.v1`. The
  tender start is keyed on the release id, so a redelivered release never tenders a load twice. The new
  `outbox-transport` Debezium connector streams transport's outbox (publication and CDC role grants in transport
  migrations 0011 and 0012; own `debezium_cdc_transport` role and `passage_transport` database in the dev Postgres;
  one Vault key, `passage/dev/transport-postgres/cdc-password`; literal ACLs) to `passage.tms.<aggregate_type>`, so the
  release reaches `passage.tms.load` and the tender outcomes `passage.tms.tender`.
- Transport load status through the tender (#426): the release moves a PLANNED or RATED load to TENDERED and needs
  `If-Match` with the load version (428 without it, 412 `conflict_version` when stale; `GET /loads/{id}` returns the
  version as ETag); a load in another status, such as one already released, is 409 TMS-0136, so a load is released
  once. The release response carries `loadStatus` and `loadVersion`. A tender start also tenders the load (TENDERED
  loads included, for the workflow's start after a release); an accepted tender makes the load ACCEPTED and a failed
  one (every carrier declined or timed out) PLANNED again. The moves are checked in `domain.NextLoadStatus`. The web
  tender form gains "Release to the tender workflow" for holders of `transport.load.tender`: it shows the load's
  status and version, releases with `If-Match` and one `Idempotency-Key`, and shows the resulting status.
- Dev database wiring for transport and eam (#426): the ExternalSecrets `transport-db` and `eam-database` in
  `deploy/envs/dev/apps/postgres.yaml` write each chart's `url` for its own login role (`passage_transport_app`,
  `passage_eam_app`; NOSUPERUSER, NOBYPASSRLS) from the new Vault keys `passage/dev/transport-postgres/app-password`
  and `passage/dev/eam-postgres/app-password`; the init scripts create the roles with the same password (init-rev 8),
  the dev Postgres NetworkPolicy admits the transport and eam pods, and transport migrates on start in dev
  (`deploy/envs/dev/values/transport.yaml`). `make -C deploy test-argocd` checks the Secret, role, grants and policy
  and, with helm, that each chart reads that Secret.
- Argo CD dev wiring for every service chart (#106): a `services` ApplicationSet and an `observability` Application
  in `deploy/envs/dev/apps`, per-service dev values in `deploy/envs/dev/values` (batch turns on SMTP notify with its
  ESO secret), and `make -C deploy test-argocd`, which checks sync policy and chart coverage and renders every dev
  chart with helm. The analytics, eam, hr, iot-edge, projects, shopfloor and transport charts now take
  `image.digest`. Syncing still needs a live cluster (#213).
- Event backbone completion in dev Argo CD (#106): `apicurio-postgres` (the `deploy/postgres` chart gains
  `cdc.enabled: false` for a plain database) and the `apicurio-database` ExternalSecret give Apicurio Registry its
  SQL storage, and `kafka-consumer-users` syncs the receiving and scheduling consumer KafkaUsers and dead-letter
  topics. `test-argocd` now asserts the backbone and Temporal sync order, that every directory manifest is applied,
  and that Strimzi cluster labels and every connector or workload Secret reference resolve to a synced ExternalSecret.
- Keycloak CI login tests (#80, wave 02 AC3) in `services/identity/keycloak_login_test.go`: OIDC authorization code
  with PKCE and TOTP (enrolment, then the OTP form), identity's `JWTVerifier` accepting the token with the duty
  claims, deprovisioning (a disabled user loses refresh and login), and federated login through two Keycloak stand-in
  IdPs (OIDC and SAML) with the post-broker second factor. The realm export gains a FORCE hardcoded `tenant_id` mapper
  per IdP placeholder and a pinned single-use OTP policy; new realm tests check both.
- HR employee and manager self-service (#267). Employees change their own preferred name at once, and their home
  address and bank account with approval, through `/v1/hr/me/profile-changes`. The new value stays sealed until a
  manager (address changes of direct reports) or HR (`hr.worker.maintain`, any change) approves it into the PII
  vault with an audit row. Web screens: update my details, my absence, team changes, profile changes.

- carrier-gateway TypeScript service (#482, wave 28 AC4) in `services/carrier-gateway-ts`. It has a carrier adapter
  interface, a deterministic sandbox carrier with no credentials, rate shopping, idempotent labels and tracking. Its
  `/v1/carrier` API takes signed service tokens. The adapter contract is replayed against a recorded fixture. There
  is also a chart. fulfillment calls it through `:carrier-rates`, `:carrier-label` and `:carrier-track` on a
  shipment (`CARRIER_GATEWAY_URL`). New service-only duties: `carrier.rate.quote`, `carrier.label.create` and
  `carrier.tracking.read`. They are granted to fulfillment (all three) and transport (rates and tracking).
- HR compensation cycle approval with segregation of duties (#266): `POST /v1/hr/compensation/cycles/{id}/approve`
  under the new `HcmCompApprove` duty; the approver may not be the cycle's creator, a worksheet submitter, or the worker
  or proposing manager on a submitted row, and apply now needs the approval (`HCM-CMP-010`). Hire from an offer can
  create the worker in the same transaction with `newWorker`.
- Production orders against the real ledger (#478, W21 AC4): `internal/ledgertest` builds and runs the ledger service
  on a throwaway schema and production books and reconciles WIP through the real `ledgerclient` with signed service
  tokens (skipped without `PASSAGE_TEST_DATABASE_URL` or under `-short`). A demo-plant seed (`go run ./cmd/demoplant`
  in `services/production`) writes five orders across the lifecycle and can book them in a ledger; see the
  production README.
- Opt-in 50k-item MRP run through the planning API on Postgres 16 (#479, wave 22 AC2):
  `PASSAGE_PLANNING_API_SCALE_ITEMS=50000 go test -run TestRunAPI_ScalePostgres ./internal/api/` in
  `services/planning`. Measured 8 min 4 s end to end on a shared 4-vCPU VM, only without the router's 30 s
  request timeout; numbers and caveats in `services/planning/README.md`. The plan-run route now accepts
  bodies up to 128 MiB and `RunPlan` sends its rows in batches of 10,000 (one unbounded batch ran out of
  memory at 50k items). ADR-0002 (Proposed) records the frePPLe comparison decision for AC4.
- Asynchronous plan runs in planning (#479): `POST /v1/planning/plans/{planCode}:run` with
  `Prefer: respond-async` validates the request, answers 202 with a plan run and its `Location`, and plans
  and stores the version in the background, outside the router's 30 s request timeout. New routes
  `GET /v1/planning/plan-runs/{id}` (status, phase, row progress, the version once succeeded) and
  `POST /v1/planning/plan-runs/{id}:cancel`; migration `0030_plan_run`; one run per plan, at most
  `PLANNING_MAX_CONCURRENT_RUNS` per process (default 1, else 503 with Retry-After), bounded by
  `PLANNING_RUN_TIMEOUT_MINUTES` (default 60), a heartbeat that carries cancels across replicas and
  reports the run of a dead process as abandoned. Without the preference the run is synchronous as before.
  The 50k-item API scale test and the k6 S5 scenario now use the asynchronous route.
  ADR-0002 is accepted: the frePPLe comparison is dropped from the wave 22 Definition of Done and the
  native planner stays.
- Wave 25 gaps (#481): production's `consume-shopfloor` consumer (principal `production-shopfloor`) posts
  closed shop-floor job-stops as job cards at the worker cost rate, pinned to the order's legal entity, and
  re-books them on `registration.corrected`; the web kiosk's IndexedDB offline queue replays punches and job
  actions once, in order, when back online (Playwright `kiosk-offline.spec.ts`); `GET /v1/shopfloor/oee/stream`
  pushes the live OEE tile as Server-Sent Events, shown on the OEE page for a selected machine.
- Local ledger performance benchmark as a proxy for wave 11 AC4 (#476): `make bench-ledger` posts balanced
  entries and reads trial balances against a throwaway Postgres 16 cluster and reports lines/s and p50/p95/p99;
  results and the hardware caveat are in `services/ledger/README.md`. It does not meet or replace AC4.
- Per-vendor-group AP control comparison (#421): party vendor groups with a short `code`
  (`POST/GET /v1/party/vendor-groups`, `vendorGroupCode` on the vendor, `vendorgroup.created.v1`), the
  `VENDGRP` ledger dimension (`addDimensionAttributes` on `PATCH /v1/ledger/ledgers/{id}`), receiving
  posting it on the vendor-control line and `vendor_group_code` on `invoice.posted`, payables storing it
  and posting it on payment run control debits, and `aging:reconcile?group_by=vendorGroup` comparing the
  subledger with the ledger balance per code. `aging:reconcile` vendor group lines now carry
  `vendorGroupCode` instead of `vendorGroupId`. The ledger takes `optionalDimensions` on journal entry
  posts: an optional attribute the ledger does not define is dropped instead of refused, and its values are
  left out of the idempotency hash, so postings keep working on ledgers that have not added `VENDGRP`. A
  vendor group can be registered with an existing group id. The `/vendor-groups` web screen lists and adds
  vendor groups.
- Wave 27 AC-27.4: a sales order line can carry a `configurationId` (validated against the product
  service), `POST /v1/sales/orders/{id}:plan-production` creates the `SalesOrderLine` production
  order that carries it through `POST /v1/production/sales-line-orders`, idempotent per line, with
  the narrow service grants `sales -> product: product.configuration.read` and
  `sales -> production: production.order.create-from-sales`.
- `platform/go/eventing` is the shared at-least-once CloudEvents consumer (franz-go, structured and
  binary decoding, `consumer_inbox` dedupe by event id, bounded retry, dead-letter topic, metrics;
  a handler that calls other services runs outside the inbox transaction and relies on its own
  idempotency key) and `tenant.ServicePrincipal` the narrow-duty background actor
  (`service_principals` in `policies/authz/data.json`). `receiving consume` settles ERS receipts
  from `receipt.posted` as `service:receiving-ers`, fed by a new `outbox-receiving` Debezium
  connector (#97, #106).
- `scheduling consume` closes a production resource's capacity for EAM downtime: it reads
  `downtime.recorded` from `passage.eam.downtime` (new `outbox-eam` connector, `debezium_cdc_eam`
  role and `passage_eam` database in the dev Postgres) as `service:scheduling-downtime`, holding
  only `scheduling.downtime.apply`, and books a downtime capacity override idempotently (#44).
  `kafka.ConfigFromEnv` now holds the consumer's Kafka settings for both services.

### Fixed

- Inventory postings consumer (#480): replaying a sales delivery that emptied its on-hand row no longer fails as a
  reused Idempotency-Key (the row's running average had moved to zero, so the replay was a different request);
  entries already booked are skipped.
- Production GL posting sent the journal number as the ledger `documentId`, which the ledger refuses (it must be a
  UUID); it now sends the journal id (the order id for a settlement) and carries the number as line text (#478).
- Ledger posting no longer deadlocks (`40P01`) when two ledgers of one tenant post the same new dimension
  combinations at once: each new combination is inserted once, in id order (found by the #476 benchmark).

### Security

- `apps/web` upgrades `zod` from 3.23.8 to 4.6.7, which fixes the high-severity advisory
  GHSA-mrhx-c7xc-m3w5 (CVE-2023-54404). Schemas use the zod 4 two-argument `z.record(key, value)`
  and `z.ZodType<Output, Input>`; `npm audit --omit=dev` reports no zod advisory (#355).

### Changed

- `productcost` (Wave 39) serves `/v1/product-costing/...` instead of `/v1/costing/...`, so it
  no longer shares the `versions` and `reconciliations` route templates (and their duty rows)
  with `costing` (Wave 20). The web client calls `/api/v1/product-costing/...` (#342).

### Removed

- The transitional umbrella service grants kept for the 0.1.0 rolling deploy: `ledger.journal.post`
  and `ledger.journal.read` for receiving and payables to ledger, and `numbering.write` for party
  to numbering. The grant tables (`policies/authz/data.json` `service_grants` and
  `platform/go/tenant/service.go`) now hold only the narrow duties each caller signs, so a
  service token that still carries an umbrella duty is refused with 401. `ledger.journal.read`
  is no longer treated as a read-only duty for no-entity ALL-scope service calls. Person roles
  keep the umbrella duties unchanged (#341).

## [0.1.0] - 2026-10-31

First tagged release of Passage, cut from `main` at `463096ba`. It is a **source-only
pre-release**:

- GitHub Actions is blocked by the repository owner's billing, so CI did not run on the
  release commit and **no container images or Helm charts were built, signed or published**.
- Every Passage service image digest in `deploy/envs/{dev,stage,prod}/apps/*.yaml` is a
  placeholder (`sha256:0000…0000`). Only pinned third-party images, such as Postgres, carry real
  digests. Nothing in `deploy/envs` can be rolled out as-is.
- Only fx, org, receiving and hello-ledger have dev Argo CD apps, next to the platform apps
  (gateway, Kyverno, cert issuer, Postgres, outbox databases, event backbone). Stage and prod
  carry only hello-ledger and Kyverno. Every other service ships a chart but no environment app.
- The release-notes gate (`docs/ops/tools/check_release_notes.py`, PRD 9.50.15) requires real
  `sha256` image digests, so it applies from the first release that publishes images. This
  changelog does not take its place.
- Each service lists what it does not do yet in its own `NOT-IMPLEMENTED.md`
  (`services/<svc>/NOT-IMPLEMENTED.md`; platform-wide gaps are in `docs/ops/NOT-IMPLEMENTED.md`).
  Read it before relying on a module.

Most of the code came from Ben Russell's automated wave pipeline: one `PSG-NNxx` ticket per
slice of the 50-wave roadmap (`roadmap/waves.json`), 357 slices across all 50 waves. Many of the
later slices are marked "Haiku swarm draft, Opus review" in their commit subjects. Follow-up pull
requests #154 to #354 then hardened, wired and proved those slices. Each was reviewed on its own
before it was merged.

### Security

- Gateway: Envoy Gateway validates the JWT, strips caller-supplied identity headers and
  rate-limits per tenant (#82, #271). `/api` is stripped at the edge (#202).
- Tenancy: tenant JWTs are verified before OPA authorization, remote JWKS endpoints must use
  HTTPS, and a denial reports the least-privileged missing duty (Wave 2, #56). Nil and malformed
  tenant headers are rejected (#257).
- Legal-entity scope fails closed when empty (#256), through one shared helper that every
  service now uses (#315, #318). Bootstrap scope for org is in `org/0032`.
- Row-level security: tenant and legal-entity policies were added or hardened on revrec,
  purchasing, billing, pricing, finreport, planning, transport, metadata usage and sales number
  sequences (#206, #232, #243, #241, #246, #283, #292, #299, #310, #319). Identity tables use
  forced RLS (#249). There are `passage_app` cross-tenant tests for six services (#250),
  isolation tests for receivables, purchasing and revrec (#194, #201), and a repo-wide RLS
  coverage check (`tools/rlscheck`, #300).
- Database grants: explicit `passage_app` grant migrations (`NNNN_grants.sql`) for 43 services
  (#195, #204, #255, #268, #269). Unused `DELETE` grants were revoked (#221). Twelve services
  with migrations have no separate grants file and grant inside their other migrations: close,
  engineering, inventory, lean, migration, processmfg, product, production, receiving,
  scheduling, shopfloor and transport.
- `SECURITY DEFINER` functions pin `search_path` against `pg_temp` table shadowing (#311,
  #314).
- Service-to-service calls use signed Ed25519 service-principal tokens (#226, #303, #305,
  #320), with a replay cache shared through Postgres that fails closed (#321, #336). Ledger
  and numbering duties were split so a caller signs only the exact duty it uses (#333). For
  this one release the grant tables still hold the old umbrella duties
  (`ledger.journal.post` and `ledger.journal.read` for receiving and payables to ledger,
  `numbering.write` for party to numbering) so a rolling deploy keeps working. They are removed
  in #341. Payables' grant on tax covers only `Calculate withholding tax` (#350). Close calls
  ledger with a signed `ledger.ledger.lookup` to check ledger ownership before it registers a
  period (#351). Payables holds `party.vendor_bank.reveal` on party (#352).
- Vendor bank details: account and routing numbers are sealed with `fieldcrypt` (tenant and
  field as additional data). Reads return only the last four digits. The `:reveal` route needs
  the `party.vendor_bank.reveal` duty and logs every reveal with user and vendor (#352).
- Treasury bank files: the signer uses Vault transit, so private keys never leave Vault, with
  RSA-PSS pinned. Every file is recorded in an insert-only `bank_file_audit` before and after it
  is sent (#346). The sender is not yet wired to a route.
- Authorization policy: every service's duties are seeded in `policies/authz` and scanned
  (#313, #337). Duty contract tests run against the real Rego with `opa test` in CI (#78,
  #187). There is a dedicated tenant-admin duty for tenant-level writers (#298, #326), and
  the web shell gates nav entries and routes by duty (#297, #312).
- Supply chain: a Kyverno admission policy rejects unsigned or tag-referenced images, including
  through `pods/ephemeralcontainers`, and stage promotion verifies cosign signatures (#170,
  #172).
- Data protection: HR PII and one-time payee data are sealed with `platform/go/fieldcrypt`
  (#285, #98, #330). Compliance exports follow a server-side policy, and erasure pseudonyms
  are keyed (#282, #325).

### Added

Each wave line gives the number of `PSG-NNxx` slices that landed and the follow-up PRs that
extended that wave.

#### Phase 0 - Foundation & Platform Kernel

- **Wave 1 - Delivery Pipeline & Cluster Baseline** (1 slice): k3d and Argo CD baseline,
  `make deploy-local` (#58), stage digest promotion and rollback (#59). Follow-ups: Argo CD
  apps and dev databases for receiving and org (#186, #205), prod app-of-apps (#75 part a,
  #207).
- **Wave 2 - Identity, Tenancy & Authorization** (1 slice, #56): OPA authorization client and
  bundle. Follow-ups: tenant, user and legal-entity schema (#249), effective-permission engine
  with OPA parity over 100 resources (#81, #272), tenant, legal-entity and role-assignment APIs
  (#83, #288), user list, role and duty catalogue APIs and the `cmd/identity` server (#329),
  SCIM 2.0 Users and Groups provisioning (#339), admin-security web screens (#296, #338).
- **Wave 3 - Metadata Kernel** (1 slice, #54/#64): registry extensions, generated runtime,
  metadata-driven entity forms and the published entity-language schema. Follow-ups:
  `entity.published` through the outbox (#188), Debezium outbox connector (#190, #216), CDC
  publications and prerequisites (#223, #259, #331).
- **Wave 4 - Event Backbone, Workflow & Batch** (3 slices, #53/#104): Temporal, schema gate,
  partitioned batch runner, workflow API, notify and job monitor. Follow-ups: product explode
  and group-change jobs with checkpointed paging, plus a run dispatcher (#105, #280, #308),
  notify adapter credentials from ExternalSecret, and the notify distributor and event-backbone
  Argo apps (#106, #295, #328), a credit-limit approval workflow started from party change
  events (#212, #260).
- **Wave 5 - UX Shell, Number Sequences, Audit & Observability** (4 slices, #66): number
  sequence service, audit log, alerts and texts, UX shell and admin workspace, observability
  stack and CSV import/export. Follow-ups: web design system with tokens, primitives and a
  command palette (#165), Playwright and axe accessibility suite (#168).

#### Phase 1 - Enterprise Structure, Master Data & Engineering Data

- **Wave 6 - Enterprise Structure & Organization Model** (37 slices): reference data
  (countries, currencies, regions, languages, units and conversions, currency rounding),
  legal-entity lifecycle, operating units, sites, warehouses and locations, versioned postal
  addresses, fiscal calendars and period resolution, effective-dated hierarchies with closure
  table, as-of API and maker-checker, teams, name translations, and the org web module,
  reference-data screen and hierarchy designer. Follow-ups: 13-period 53-week FY2028 fixture
  (#164), demo seed package and address normalize and suggest adapter (#87, #209), editable
  hierarchy designer (#86, #211, #248).
- **Wave 7 - Global Party & Business Partner Model** (3 slices): party persistence, roles, tax
  registrations, terms and the core party API. Follow-ups: atomic merge with survivorship rules
  and a referential-integrity audit (#173), credit-limit increase approval with per-legal-entity
  configuration (#90, #208, #212, #230, #251). Proof and vendor master (#352): a VIES adapter
  replayed against six recorded VIES REST responses (Wave 7 AC3, so all four Wave 7 criteria
  are met in code), `vendor_role` and `vendor_bank_account` (`party/0008_vendor_role.sql`),
  vendor read routes, and merge that moves vendor roles.
- **Wave 8 - Product Master, Variants & Catalogs** (2 slices): product master and configurator,
  org site and warehouse check, numbering sequences.
- **Wave 9 - Engineering Data: BOMs, Routings, Resources & Change Management** (14 slices):
  resources, capabilities and work calendars, routings and route versions, BOM versions, ECOs
  with impact report, and the engineering screens.
- **Wave 10 - Data Migration Framework & Legacy Connectors** (8 slices): mapping DSL, versioned
  mapping specs, data templates with lineage, `rejectWhen` reason codes, source formats and
  code pages, signed configuration packages with diff and apply, and legacy key
  cross-reference lookup.

#### Phase 2 - Financial Core

- **Wave 11 - Universal Journal & Chart of Accounts** (10 slices): journal core, posting,
  reversal and storno, balances, account structures and dimensions, journal names and batches
  with approval, recurring journals, open-item accounts with manual and automatic clearing,
  dimension derivation, and the ledger transactions inquiry.
- **Wave 12 - Multi-Currency, Parallel Ledgers & Revaluation** (4 slices): fx service, rate
  store and providers, rate API and importer with outage fallback. Follow-ups: fx hardening and
  a deployable service with chart, OpenAPI and web screens (#92, #93, #166, #191, #196, #198,
  #220, #279, #324), atomic parallel-ledger posting, a ledger-specific posting duty with the
  20-document AC-12.4 fixture, and `ledger_lines` with per-ledger balancing (#91, #222, #214,
  #273, #287).
- **Wave 13 - Tax Engine** (1 slice): sales and use and VAT/GST calculation, provider failover
  and the settlement domain. Follow-up: a stateless `POST /v1/tax/withholding/calculate`
  endpoint with effective-dated rates, cumulative thresholds and minor-unit rounding. It sits
  behind the new duty `Calculate withholding tax` so payables can call it (#103, #350). The same
  PR audited the Wave 13 proof criteria: the 500-scenario matrix and provider failover are met,
  while settlement-to-GL and the SAF-T XSD check remain open.
- **Wave 14 - Period Close, Year-End & Allocations** (2 slices): period maintenance, reopen
  approval and the close checklist API. Follow-up: period resolver, with receiving deriving the
  fiscal period from the posting date (#96, #307). Proof (#351): `PUT
  /v1/close/periods/{fiscalPeriodId}` (duty `period.maintain`) registers a period's dates once
  the ledger service confirms that the ledger belongs to the caller's legal entity. Allocation,
  year-end and checklist criteria are met (AC2 to AC4). Closed-period rejection (AC1) is
  proved per ledger and receiving posting path, but ledger does not yet read close's period
  status.
- **Wave 15 - Financial Reporting, Statements & Audit Trail** (6 slices): statement
  definitions, runs and drill-down, derived hierarchies, cash flow statement, HTML, XLSX and CSV
  output, schedules, and the audit trail report. Proof: demo-year statements tie to the trial
  balance and the cash flow reconciles to the change in cash (Wave 15 AC1, #344).

#### Phase 3 - Inventory & Procure-to-Pay

- **Wave 16 - Inventory Ledger, Dimensions & On-Hand** (14 slices): inventory ledger and
  journals, reservations, cycle counting with freeze and review, transfer orders, daily
  inventory-to-ledger reconciliation, reports, as-of on-hand, and the inventory workspace.
- **Wave 17 - Purchasing** (9 slices): requisitions, purchase orders with versioned change
  management, agreements, RFQs and sealed bids, repricing, scheduling agreements, purchasing
  policy and PO four-eyes. Follow-up: invoicing-plan rows are marked invoiced on settlement
  (#289, #316).
- **Wave 18 - Receiving, Product Receipts & Three-Way Match** (6 slices): receipt lifecycle,
  arrival, registration and posting, GR/IR inquiry and the `receipt.posted` event. Follow-ups:
  real ledger and period-control adapters (#174), receipt and invoice routes with receiving and
  AP web workspaces (#95, #197), HTTP purchase-order adapter and registration checked against
  the PO line (#96, #215, #327), subsequent debit and credit, invoicing-plan and consignment
  settlement (#98, #286), evaluated receipt settlement with stochastic audit weighting (#97,
  #304, #334), one-time payees (#330), observability (#169).
  - #354 (#302): at posting (manual, ERS and audit release), receiving reads the vendor from
    party (`GET /v1/party/vendors/{id}`, `party.read`) and puts `vendor_group_id` on
    `invoice.posted` for payables' per-group aging.
  - The lookup fails open: if party is down, refuses the call or does not know the vendor, the
    invoice still posts without the field, and the failure is counted in
    `receiving_vendor_group_lookups_failed_total`.
- **Wave 19 - Accounts Payable, Settlement & Payments** (6 slices): ISO 20022 pain.001
  generation and pain.002 parsing with XSD conformance, NACHA ACH and returns, and payment-file
  previews. Follow-ups: metrics and settlement invariants (#167), preview and status endpoints
  (#219, #229, #242), vendor open items, FX valuation and payment proposals (#103, #254, #284),
  vendor and vendor-group aging reconciled to the AP control account (#101, #270, #301), an
  idempotent payment run through a Temporal workflow (#102, #306, #317, #323), holds, vendor
  block and proposal list routes (#332), distroless runtime image (#294).
- **Wave 20 - Inventory Costing & Supplier Collaboration** (6 slices): costing settlement,
  marking, valuation ledgers, costing versions and standard cost, cost transactions with GL
  tie-out, manual adjustments and reconciliation runs. Proof (#347): a 104-sequence reference
  fixture matches to the cent under all 7 costing methods (AC-20.1). Settle, post and report tie
  to the ledger, including a gated 1M-transaction run in 16.7 s against a 20-minute bar
  (AC-20.3). Purchase price variance posts a cost transaction on receipt and invoice
  (AC-20.4).

#### Phase 4 - Manufacturing & Planning

- **Wave 21 - Production Orders, WIP & Order-Based Costing** (16 slices): production orders,
  journals, WIP and variance, rework and genealogy, split and merge, order change management,
  co-products and by-products, subcontracting, KPIs and per-site parameters.
- **Wave 22 - Master Planning: MRP, MPS & Forecasting** (19 slices): MRP engine and pegging,
  forecasting and consensus, time fences, firming, DDMRP buffers, quota arrangements, coverage
  groups, plan version diff and simulation scenarios.
- **Wave 23 - Capacity Planning & Finite Scheduling** (13 slices): finite scheduling with a
  CP-SAT optimizer, Gantt, resource networks, setup matrices, campaigns, capacity overrides and
  backward scheduling. Proof: 30 OR-Library job-shop instances solved within a 5% gap (#23,
  #261).
- **Wave 24 - Process Manufacturing & Lean** (11 slices): formulas and batch orders, batch
  derivation and FEFO, lean flows and flow costing, kanban board and rules, and batch quality
  records.
- **Wave 25 - Shop Floor Execution, Time Registration & IoT** (12 slices): time registration
  and pay calculation, machine ingest, andon and OEE, process messages, kiosk flows,
  supervisor corrections and pay agreements.

#### Phase 5 - Order-to-Cash

- **Wave 26 - Pricing, Discounts & Trade Agreements** (10 slices): price and discount
  agreements with resolver and explain, price journals, rebates, commissions, trade funds,
  campaigns and free goods.
- **Wave 27 - Sales Orders, Quotations, ATP & Credit** (13 slices): sales orders with ATP and
  credit holds, quotations, supplementary items, allocation quotas, schedule lines and
  backorders, and per-legal-entity sales parameters.
- **Wave 28 - Fulfillment: Picking, Packing & Shipping** (11 slices): release, pick, pack and
  post, auto-pack and returnable packaging, waves, ASN and labels, the SSCC register, and
  delivered-not-invoiced and backorder reports.
- **Wave 29 - Billing, Invoicing & Revenue Recognition** (5 slices): free-text invoices,
  revenue contracts and recognition runs, credit notes, installment plans, and invoices from
  packing slips.
- **Wave 30 - Accounts Receivable, Collections, Returns & Customer Portal** (5 slices): cash
  application, collections and dunning, lockbox and bank statement import, deductions, payer
  matching and customer statements.

#### Phase 6 - Warehouse, Quality & Logistics

- **Wave 31 - Advanced Warehouse Management** (4 slices): location directive engine and
  simulator, hazmat and cross-dock checks, work execution, dock appointments. Proof (#348):
  100 outbound PICK directive scenarios (AC1), a 10k-lines/day simulation with a measured p95
  assignment of 11 ms (AC2), capacity property tests (AC3), and wave release of 500 orders
  (AC4).
  - Wave release (`domain.ReleaseWave` / `Store.ReleaseWave`) creates pick work plus min/max
    replenishment.
  - A pick that only a refill can cover stays `WAITING` (`depends_on`) until the refill
    finishes, then emits the new event `dev.passage.wms.work.released.v1`. Migration:
    `warehouse/0004_work_dependencies.sql`.
  - There is no HTTP route or wave record yet.
- **Wave 32 - Mobile Warehouse Execution** (6 slices): print service, GS1 labels and scanning,
  PDF and PCL transports, offline queue and device flows.
- **Wave 33 - Quality Management & Nonconformance** (6 slices): quality orders and sampling,
  SPC and stability studies, nonconformance and 8D, supplier certificates and certificates of
  analysis.
- **Wave 34 - Transportation Management & Freight** (5 slices): rating, freight bills, tender
  cascade, load building, gate check, screening holds and route optimization.
- **Wave 35 - B2B Integration: EDI, e-Invoicing & Integration Hub** (5 slices): X12
  interchanges (855/856/810/997), UBL and Peppol invoices, message monitor and segment viewer
  with PII masking.

#### Phase 7 - Treasury, Assets & Controlling

- **Wave 36 - Cash & Bank Management, Reconciliation & Treasury** (4 slices): bank accounts,
  BAI2 and MT940 import, cash forecast, checks, facilities and loans. Proof (#346): stricter
  reconciliation rules, a cash-forecast bucket property (AC2), an EBICS H004 upload subset
  against a fake bank (AC3, partial), and a Vault-signed, audited bank-file sender
  (`treasury/0007_bank_file_audit.sql`, AC4). AC1 is not met; see Known limitations.
- **Wave 37 - Fixed Assets & Leases** (5 slices): asset register, depreciation books and runs,
  disposal, leases and remeasurement, capex approvals, transfers and splits.
- **Wave 38 - Cost Accounting, Budgeting & Budget Control** (5 slices): budget control and
  encumbrances, allocations, earmarked funds, investment programs, cost object hierarchies.
- **Wave 39 - Product Costing, Cost Roll-Up & Variance Analysis** (5 slices): cost roll-up,
  production variances, costing sheets, profit-center valuation, stored cost components.
- **Wave 40 - Intercompany & Financial Consolidation** (4 slices): intercompany journals,
  charges and reconciliation, consolidation eliminations and translation, transfer-pricing
  evidence, stored trading partners. Proof (interim): a 3-entity group elimination fixture
  asserting gross debit and credit (#345). It is marked interim until accountant review (#349).

#### Phase 8 - Projects, People & Service

- **Wave 41 - Project Operations** (5 slices): projects and CPM scheduling, WBS, change orders,
  risk register, transactions and invoice proposals, forecast models.
- **Wave 42 - Human Resources Core** (5 slices): workers, positions and assignments, absence
  and accrual, benefits, skills and certificates, courses. Proof: 36-case accrual fixture for
  AC2 (#42, #258).
- **Wave 43 - Payroll Engine & Expense Management** (5 slices): payroll and expense engines,
  per diem, cash advances, travel requisitions and card statement matching.
- **Wave 44 - Enterprise Asset Management** (5 slices): maintenance assets, plans and work
  orders, fault catalogs, work permits and lockout/tagout, functional locations, rounds and
  KPIs.
- **Wave 45 - Service Management & Field Service** (5 slices): service cases, agreements and
  SLA clocks, installed base, generic case management, warranty claims and email-to-case.

#### Phase 9 - Intelligence, Compliance & Go-Live

- **Wave 46 - Data Platform, Analytics & Embedded BI** (4 slices): metric catalog, scoped Cube
  queries, data quality, archive, dashboards and lineage. Follow-up: a read-classified POST
  allowlist so analytics can serve all-scope reads (#293, #322).
- **Wave 47 - AI Copilots, Agents & Automation** (5 slices): MCP tool gateway, agent run
  engine with governance and an eval gate, model adapters with failover, Bedrock adapter, kill
  switch and copilot screens.
- **Wave 48 - Globalization, Localization & Compliance Controls** (5 slices): segregation of
  duties, country packs, breach clocks, e-signature, sanctions screening, privacy subject
  requests and access certification.
- **Wave 49 - Migration Factory** (5 slices): pack runs with control totals, cutover runbooks,
  quarantine release, rollback re-keying export, GL opening balances pack.
- **Wave 50 - Scale Hardening, Multi-Region & Production Go-Live** (11 slices): multi-region
  chart with fencing and MirrorMaker 2, k6 load and soak campaigns, expand/contract migration
  linter, OpenAPI and AsyncAPI compatibility gates, game-day and chaos tooling, the
  release-notes gate, cost and capacity calculators, runbooks, and implementer, administrator
  and functional guides.

### Changed

- CI: GitHub Actions run volume was reduced (#150). Postgres-backed store tests run in CI, with
  an org idempotency-key cap and NetworkPolicy (#88, #193). A golangci-lint matrix covers the
  clean Go modules (#169), xmllint is installed so the pain.001 XSD tests run (#171), and the
  configurator's Python suite runs in CI (#154).
- `services/ledger` serves its API under `/v1/ledger` (#199, #202).
- The `warehouse-mobile` toolchain matches `apps/web`, with a lockfile and a CI build (#281,
  #309).
- Docs: README getting-started guide for newcomers (#192).

### Fixed

- The configurator's CP-SAT `OnlyEnforceIf` works with OR-Tools 9.11+ (#154).
- The dev DB secret is recreated when `APP_PASSWORD` is missing (#156).
- Migration catalog checks are scoped by schema (#224, #233).
- Org seed and postal replay gaps (#235, #253).
- Ledger sets enforce their constraints and take locks in a consistent order (#237, #245).
- Payables classifies XSD validator interruptions and has a preview timeout and 503 contract
  (#239, #242).
- Smaller fx, org-web and receiving follow-ups: #227/#228, #234, #236, #238, #240, #244, #247
  and #252.

### Dependencies

- Web: React 19 and react-router 7 (#189, supersedes #162 and #163). Bundled Dependabot bumps
  for Go modules, GitHub Actions and web test tooling (#185), and for `apps/web` (#274).
- Dependabot is configured for Go modules, npm, pip and GitHub Actions (#155).
- Configurator: OR-Tools 9.11+ supported (#154).

### Upgrade notes / operator actions

There is no earlier release, so this is a first install rather than an upgrade. Before any
environment runs 0.1.0:

1. **Protect `main` before prod.** A repo admin must enable branch protection on `main`
   (required review plus code-owner review) before the prod cluster is registered in Argo CD.
   Until then any push to `main` auto-deploys prod (`docs/runbooks/delivery/deploy-local.md`).
2. **Build, sign and pin images.** Once Actions billing is restored, re-run CI on the tag, then
   build every service image and sign it with cosign. Replace each placeholder digest in
   `deploy/envs/{dev,stage,prod}/apps/*.yaml` with the real digest. The Kyverno admission
   policy (#170, #172) rejects unsigned or tag-referenced images.
3. **External Secrets.** The charts read their Secrets through External Secrets Operator from a
   `ClusterSecretStore` named `passage` (the `storeName` default in `deploy/postgres` and the
   batch chart; also `deploy/envs/dev/kafka-secrets`). Install ESO and create that store,
   backed by Vault, before syncing.
4. **Apply each service's migrations in filename order** (`services/<svc>/migrations`). Pay
   particular attention to these:
   - Identity schema, operator and SCIM: `identity/0001_identity.sql`,
     `identity/0002_grants.sql`, `identity/0004_tenant_provisioning.sql`,
     `identity/0006_operator_single_row.sql`, `identity/0007_scim.sql`.
   - Grants: the `NNNN_grants.sql` file in each of the 43 services that has one (for example
     `pricing/0008_grants.sql`). The other twelve grant inside their other migrations.
   - `payables/0010_vendor_block_per_legal_entity.sql` must run as a superuser or a `BYPASSRLS`
     role. It rewrites every tenant's existing vendor blocks into per-legal-entity rows, so a
     role that is subject to RLS cannot see the rows it has to move.
   - Vendor master: `party/0008_vendor_role.sql`. Treasury bank-file audit:
     `treasury/0007_bank_file_audit.sql`. Warehouse work dependencies:
     `warehouse/0004_work_dependencies.sql`.
   - Legal-entity RLS: `org/0032_legal_entity_bootstrap_scope.sql`,
     `billing/0006_child_legal_entity_scope.sql`, `pricing/0007_child_legal_entity_scope.sql`,
     `purchasing/0010_child_legal_entity_scope.sql`, `revrec/0004_outbox_and_child_scope.sql`,
     `revrec/0005_revenue_schedule_direct_scope.sql`, `finreport/0008_legal_entity_scope.sql`,
     `planning/0012_legal_entity_scope.sql`,
     `planning/0013_legal_entity_scope_buffers_coverage.sql`,
     `transport/0006_legal_entity_scope.sql`, `transport/0007_legal_entity_scope_children.sql`,
     `transport/0008_legal_entity_scope_voyages_routes.sql`,
     `metadata/0005_usage_legal_entity_scope.sql`,
     `sales/0010_number_sequence_legal_entity_scope.sql`.
   - `SECURITY DEFINER` search path: `close/0004_definer_search_path.sql`.
   - Service-token replay tables. Apply these before deploying a target build, because a target
     refuses to start without its table: `close/0005`, `fx/0006`, `inventory/0014`,
     `ledger/0017`, `numbering/0003`, `payables/0011`, `product/0002` and `purchasing/0013`,
     each named `*_service_token_replay.sql`.
   - CDC: `metadata/0004_cdc_publication.sql`, `metadata/0006_cdc_role_grants.sql`,
     `batch/0004_cdc_publication.sql`, `batch/0011_cdc_role_grants.sql`. The role-grant
     migrations grant to `debezium_cdc` only if that role already exists. If a service migrated
     before its database had the role, delete that `schema_migrations` row and restart (see
     `deploy/postgres/README.md`).
   - One-time payees: `receiving/0010_one_time_payee.sql`.
   - Receiving `posted_by`: `receiving/0009_ers_audit_block.sql` and
     `receiving/0013_ers_price_confirmation.sql` add `posted_by` with default `''`. Receipts
     posted before 0013 have no recorded poster, so ERS price confirmation refuses them
     (`AP-ERS-004`) until `posted_by` is backfilled from the posting audit trail.
5. **Service-token keys and rollout order** (`deploy/gateway/README.md`, #321).
   - Callers and their targets:
     - receiving to ledger, close, purchasing and inventory;
     - payables to ledger and fx;
     - party to numbering;
     - batch to product;
     - workflow-ts to payables;
     - close to ledger. Close also needs `ledgerUrl` (`LEDGER_URL`), and without it period
       registration returns 503.
     - receiving to party (#354). The grant exists, but party cannot verify the token yet; see
       Known limitations.
   - Give each caller an Ed25519 seed Secret (`serviceIdentity.signingKeySecret`, key
     `signing-key`) and a `serviceIdentity.keyId`. Give each target its callers' public keys in
     `serviceIdentity.publicKeys`.
   - Order, per target:
     1. Set its public keys and apply its `service_token_replay` migration.
     2. Deploy the target build.
     3. Deploy the callers.
   - Payables and close are both targets and callers. Deploy them after ledger and fx, and
     before workflow-ts and receiving.
   - Roll back in reverse order, callers first.
6. **Field-encryption keys** (`FIELDCRYPT_KEYS` or `FIELDCRYPT_KEYS_FILE`, plus
   `FIELDCRYPT_CURRENT_KEY_ID`):
   - hr: refuses to start without them. Keep the legacy `HR_PII_KEY` until every HR row is
     re-sealed.
   - receiving and payables (they share a service key): without it, one-time payees, payables
     proposals with bank details and payment-run starts return 503.
   - party: without it, vendor bank writes and reveals return 503
     `PTY-BANK-ENCRYPTION-UNAVAILABLE`. The chart reads the keys from `existingSecret`.
   - compliance: without it, the erasure preview returns 503 `PRV-1211`. Do not rotate its
     current key while erasure requests are open.
7. **Party VIES.** Set `PARTY_VIES_URL` (the VIES REST base,
   `https://ec.europa.eu/taxation_customs/vies/rest-api`) and optionally
   `PARTY_VIES_TIMEOUT_MS` (default 5000). With an empty URL, VAT numbers stay `unverified`.
8. **Tenant administrators and the platform operator** (`services/identity/README.md`).
   - Before deploying, add `tenant.administer` to the duties claim of existing tenant
     administrators in the Keycloak duties mapper. Tenant-level writes need it.
   - Seed the operator tenant in `identity_tenants` and the single `identity_operator` row with
     its id. Do this as the migration owner, because `passage_app` can only read
     `identity_operator`. Then set `IDENTITY_OPERATOR_TENANT_ID` on identity to the same id.
     Without it, the tenant routes and `Maintain tenants` grants are refused.
   - The operator then bootstraps each tenant's first administrator, through `adminUserId` /
     `adminIdpSubject` on `POST /v1/tenants` or through `POST /v1/tenants/{id}/admins`.
9. **Gateway** (`deploy/gateway/README.md`).
   - Install Envoy Gateway v1.4 or newer, which provides the CRDs, `localJWKS`/`remoteJWKS` and
     shared rate limits.
   - Replace the dev placeholder `localJWKS` with `remoteJWKS` pointing at the IdP realm's JWKS,
     and set `issuer`.
   - Emit `duties` as a single comma-joined string claim, not a multivalued array.
   - Provide the TLS Secret `passage-api-tls` for the `https` listener.
   - Remove the `http-dev` listener in every non-dev environment.
10. **CDC credentials.** Each CDC database needs Vault entries
    `<prefix>/<release>-postgres/{postgres-password,app-password,cdc-password}` and a matching
    `debezium-cdc-<release>` Secret in the `kafka` namespace (`deploy/envs/<env>/kafka-secrets`).
11. **Notify adapters.** Enabling `NOTIFY_SMTP_ENABLED` or `NOTIFY_WEBHOOK_ENABLED` needs that
    adapter's credentials, from the batch chart's ExternalSecret or `NOTIFY_SECRETS_DIR`.
    Startup fails if they are missing.
12. **SCIM (optional).** SCIM is mounted only when `IDENTITY_SCIM_ISSUER_BASE`,
    `IDENTITY_SCIM_AUDIENCE` and `IDENTITY_OPA_URL` are all set. Assign the duty `Provision
    users via SCIM` to the IdP's service account.
13. **Receiving `PARTY_URL`** (#354). Receiving reads vendor groups from party through
    `PARTY_URL`. The chart sets it from `serviceUrls.party`, defaulting to
    `http://party.<namespace>.svc:8080`, so set `serviceUrls.party` wherever party runs
    elsewhere. With `PARTY_URL` empty, `vendor_group_id` is never sent and payables' per-group
    aging gets no group.
14. **`RECEIVING_ALLOW_LEGACY_PO_LINE` is for dev only.** Chart value `allowLegacyPoLine`
    defaults to false. When it is set, receiving trusts client-sent `poLineId`,
    `orderedRemaining` and `unitPrice` instead of resolving `poLineNo` against the purchase
    order. Never set it in stage or prod; it is removed in #340.

### Known limitations

- **No published artifacts.** No images, charts, SBOMs or signatures exist for 0.1.0, and the
  digests in `deploy/envs` are placeholders. CI did not run on the release commit because
  Actions is billing-blocked.
- **Per-service gaps.** Each service's `NOT-IMPLEMENTED.md` lists what is missing. Many slices
  are partial implementations of their wave's functional requirements.
- **Identity is incomplete.** The Keycloak realm and push (#80), OIDC/SAML federation and MFA,
  OPA bundle build and distribution, and gateway host-to-tenant mapping (#213) are not
  implemented. mTLS between pods is a follow-up to the service tokens.
- **No live-cluster proof.** Nothing has been proved on a live cluster: Actions runs, the
  gateway, the CDC relay, the EBICS bank sandbox, or the repository admin steps. All of these
  are tracked in #213.
- **Wave proof criteria are partly open.** The proofs that landed in code are Wave 7 (#352),
  Wave 12 AC-12.4 (#273), Wave 13 (two of four, #350), Wave 14 (AC2 to AC4, AC1 partial, #351),
  Wave 15 AC1 (#344), Wave 20 (AC-20.1, 20.3 and 20.4, #347), Wave 23 (#261), Wave 31 (AC1 to
  AC4, #348), Wave 36 (AC2 and AC4, AC3 partial, #346), Wave 42 AC2 (#258) and the interim Wave
  40 fixture (#345). Human steps are still open: accountant workbooks and sign-offs, recorded
  walkthroughs, and every phase-exit sign-off.
- **Wave 36 AC1 is not met.** On the adversarial 1,000-line reconciliation fixture, 78.0% of
  all lines auto-match correctly (91.8% of the lines that have a book counterpart, 0 wrong),
  against the 95% bar (#346).
- **Transitional umbrella grants.** The service grant tables keep `ledger.journal.post`,
  `ledger.journal.read` and `numbering.write` for this one release, until #341 removes them.
- **Tax is not deployable yet.** The tax service has no Helm chart, Dockerfile or store (slice
  B in `services/tax/NOT-IMPLEMENTED.md`). Tax does not verify service tokens yet, so the
  seeded payables to tax grant is not exercised.
- **Party does not verify service tokens yet.** No service-token verifier, public keys or
  replay table are configured for party. A receiving build with a signing key therefore signs
  its vendor lookup (#354), party rejects it, and every posted invoice goes out without
  `vendor_group_id`; watch `receiving_vendor_group_lookups_failed_total`. The lookup only works
  where receiving runs without a signing key and forwards the user's headers. Payables' vendor
  bank reveal (#352) is not callable as a service for the same reason.
- **Known vulnerability.** `apps/web` depends on `zod` 3.23.8 in production, which is affected
  by the high-severity advisory GHSA-mrhx-c7xc-m3w5 (CVE-2023-54404: memory exhaustion when
  validating oversized constrained collections). It is fixed in zod 4.6.6+, but that is a major
  version bump, tracked in #355. The code runs client-side, in API response validation in the
  browser.
- **Go and Python dependencies were not audited.** No `govulncheck` or `pip-audit` run was made
  for this release. Only the npm advisory above was checked.
- **Not merged into 0.1.0:** #262 (platform-health release persistence and promote/rollback
  recording, #75).
- **Open follow-up issues:**
  - #335: fieldcrypt follow-ups from #330 (hr legacy fallback, receiving idempotency hash).
  - #340: receiving web must send `poLineNo`, then `RECEIVING_ALLOW_LEGACY_PO_LINE` is dropped.
  - #341: remove the transitional umbrella service grants.
  - #342: costing and productcost mount the same `/v1/costing` routes with different duties.
  - #343: SCIM follow-ups from the #339 review (admin-end deadlock, silent cap on group members).
  - #349: the IAS 21 FX treatment in the FR-40.9 consolidation eliminations needs accountant
    review. Until then the Wave 40 fixture (#345) stays interim.
  - #355: upgrade `apps/web` to zod 4 for GHSA-mrhx-c7xc-m3w5.

[0.1.0]: https://github.com/BenjaminSRussell/Passage/releases/tag/v0.1.0

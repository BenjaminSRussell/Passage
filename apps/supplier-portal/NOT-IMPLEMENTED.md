# NOT-IMPLEMENTED — apps/supplier-portal (PSG-20, wave 20, slice 1)

| Deliverable or AC | Status | Reason |
|---|---|---|
| D5 Portal: PO acknowledgement (accept, reject, propose changes; `ack_due_days` SLA) | later slice of PSG-20 | Needs wave 17 PO versions and the portal API; FR-20.9 |
| D5 Portal: ASN (create from PO lines, packages, SSCC, batches, serials, submit; pre-fills receiving) | later slice of PSG-20 | Needs wave 18 receiving and the ASN API; FR-20.10 |
| D5 Portal: invoice submission against received lines (source PORTAL to wave 18 `:capture`) | later slice of PSG-20 | Needs wave 18 capture; FR-20.11 and BR-20.11 |
| D5 Portal: payment status and advices | later slice of PSG-20 | Needs wave 19 payment read model; FR-20.11 |
| D5 Portal: document exchange (virus scan, 25 MB limit) | later slice of PSG-20 | Needs scan service and object store; FR-20.13 |
| FR-20.12 Keycloak realm `suppliers`, vendor-admin invitations, MFA option, OPA vendor scoping, rate limits | handoff | The realm is provisioned by the deploy/ Helm hook and edge-identity; OPA policies live in policies/ (outside this wave) |
| FR-20.12 / AC-20.9 cross-vendor 404 and OPA decision log | handoff | Depends on the OPA policy bundle in policies/ |
| FR-20.19 vendor catalog upload (wave 17 staging) | later slice of PSG-20 | Needs wave 17 staging API |
| FR-20.20 service entry sheets and timesheets | later slice of PSG-20 | Needs wave 18 service entry sheets; BR-20.17 limits |
| FR-20.18 prospective-vendor registration and questionnaires (public page) | later slice of PSG-20 | Needs wave 4 approval workflow and wave 7 parties; the public page also needs CAPTCHA at the edge |
| FR-20.21 controlled document distribution and acknowledgement | later slice of PSG-20 | Needs wave 5 attachments and wave 4 timers |
| FR-20.22 portal consignment view | later slice of PSG-20 | Needs wave 16 and 18 consignment projections |
| Portal BFF `services/supplier-portal-ts` (Fastify, cookie session, token handler) | handoff | Outside this wave's owned paths; the BFF is a TypeScript service and needs an owner |
| Chart `passage-supplier-portal`, Envoy route with rate limiting and WAF | handoff | deploy/ and edge-identity are outside this wave's owned paths |
| Demo vendor login and `portal-demo-vendor` fixture | later slice of PSG-20 | Needs the realm and the seeded vendor |
| WCAG 2.1 AA (axe) tests, four-language strings | later slice of PSG-20 | No screens yet |
| AC-20.10 phase-exit walkthrough | later slice of PSG-20 | Needs all portal features and waves 17–20 on stage |
| Vite/React app shell for the portal (`package.json`, `vite.config`, tests) | later slice of PSG-20 | No dependency lockfile is added in slice 1; the app is scoped with its first screen |

# NOT-IMPLEMENTED — apps/supplier-portal (PSG-20, #511)

| Deliverable or AC | Status | Reason |
|---|---|---|
| PO acknowledgement, ASN, invoice document submission, vendor requests and onboarding questionnaire, document exchange, controlled distribution with acknowledgement | built | see README; services: purchasing, receiving, party |
| Vite/React app shell, tests, axe checks on the main pages | built | English only |
| Four-language strings, full WCAG audit with a screen reader | not built | strings are inline English; axe covers the automatable part |
| Invoice submission against received lines (BR-20.11), structured line entry | not built | the portal sends a document that AP reviews; there is no endpoint that takes lines |
| Payment status and advices | not built | payables has no vendor-facing read model (wave 19) |
| ASN packages, SSCC, serials entry, cancel, partial receipt per line | not built | receiving's notice has no packages and no cancel (see services/receiving/NOT-IMPLEMENTED.md); serial numbers are accepted by the API but the form has no serial field |
| Catalog upload (FR-20.19), service entry sheets and timesheets (FR-20.20), consignment view (FR-20.22) | not built | each needs its own vendor-scoped service endpoint |
| Prospective-vendor registration on a public page (FR-20.18) | not built | needs CAPTCHA at the edge and an anonymous principal; the built flow starts with a buyer inviting a vendor |
| Users page: vendor-admin invitations, MFA option | not built | Keycloak user administration (live infra) |
| Demo vendor login and fixture, phase-exit walkthrough | not built | needs the realm and a seeded vendor |
| Installable PWA | not built | no manifest or service worker; online only |
| Document virus scan, 25 MB limit | not built | no scan service or object store (decision on #512); documents are stored in Postgres, 5 MB |

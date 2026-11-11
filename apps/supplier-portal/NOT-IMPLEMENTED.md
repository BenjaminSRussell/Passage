# NOT-IMPLEMENTED — apps/supplier-portal (PSG-20, #511)

| Deliverable or AC | Status | Reason |
|---|---|---|
| PO acknowledgement, ASN, invoice document submission, vendor requests and onboarding questionnaire, document exchange, controlled distribution with acknowledgement | built | see README; services: purchasing, receiving, party |
| Vite/React app shell, tests, axe checks on the main pages | built | English only |
| Four-language strings, full WCAG audit with a screen reader | not built | strings are inline English; axe covers the automatable part |
| Invoice submission against received lines (BR-20.11), structured line entry | built | `/invoices/received`: pick received lines (item and order names from the purchase order), quantity and price, send; see and withdraw submissions. Open: no tolerance check against the order price at submission (AP matches it), service lines and consignment are not offered |
| Payment status | built | `/payments`: each invoice with open, scheduled, part paid, paid or cancelled and the relevant date. Open: remittance advices (no document channel) |
| ASN packages, SSCC, serials entry, cancel, partial receipt per line | not built | receiving's notice has no packages and no cancel (see services/receiving/NOT-IMPLEMENTED.md); serial numbers are accepted by the API but the form has no serial field |
| Catalog upload (FR-20.19), service entry sheets and timesheets (FR-20.20), consignment view (FR-20.22) | not built | each needs its own vendor-scoped service endpoint |
| Prospective-vendor registration on a public page (FR-20.18) | built | `/register` (no session; honeypot, size and rate limits in the BFF; no CAPTCHA). A buyer reviews it at `/party/vendor-prospects` in `apps/web` (accept as a blocked vendor with an onboarding questionnaire, or reject with a note) |
| Users page: vendor-admin invitations | built | `/users` for the `supplier-admin` role (link shown once) and `/accept-invite`. The invited user is created in Keycloak by an administrator until the Keycloak adapter exists (live infra) |
| Users page: MFA option | not built | Keycloak user administration (live infra) |
| Company details change request (PROFILE_CHANGE) with structured fields | built | `/requests`: legal name, trade name, registration number, industry code; approval writes them to the buyer's record and the request shows when |
| Demo vendor login and fixture, phase-exit walkthrough | not built | needs the realm and a seeded vendor |
| Installable PWA | not built | no manifest or service worker; online only |
| Document virus scan | built in the BFF | uploads are refused when infected or when a configured scanner cannot answer; the page says so. Deploying clamd is live infra |
| 25 MB document limit | not built | no object store (decision on #512); documents are stored in Postgres, 5 MB |

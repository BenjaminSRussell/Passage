# NOT-IMPLEMENTED: apps/customer-portal (PSG-30, #30)

| Deliverable or AC | Status | Reason |
|---|---|---|
| Read-only portal: orders, invoices and credit notes, open items, payments, statement, returns | built | see README; services: sales, billing, receivables |
| Vite/React app, tests, axe checks on every page | built | English only |
| Placing orders from the portal (FR-30.14) | not built | the portal reads only; it needs a customer-scoped write route in sales (pricing, credit check) and its own duty |
| Requesting a return (FR-30.14) | not built | sales' return order needs product, site, warehouse and disposition; a customer-facing request that staff authorize is a separate route |
| Paying by card or ACH (FR-30.25) | not built | needs the payments gateway (a separate service); open items are shown, payment is not taken |
| Invoice and statement PDF download, shipment tracking, documents (FR-30.26) | not built | need the document store (decision on #512) and a carrier read API |
| Product names and images on order and return lines | not built | sales lines hold a product id only; the page shows line numbers until a customer-facing product read exists |
| Customer names in the company selector | not built | companies are named "Company 1, 2"; a customer-facing legal-entity name read is not built (the shell lookups are not available to this standalone app) |
| Contacts of bill-to or ship-to accounts of the same customer group | not built | a contact sees only the customer in the sign-in claim |
| Full WCAG audit with a screen reader, mobile-first visual check | not built | axe covers the automatable part; layout is one column on small screens but has no device review |
| Installable PWA | not built | no manifest or service worker; online only |
| Sign-in realm, contact invitation, MFA | not built | Keycloak user administration (live infra, #213) |

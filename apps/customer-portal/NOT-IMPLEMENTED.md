# NOT-IMPLEMENTED: apps/customer-portal (PSG-30, #30)

| Deliverable or AC | Status | Reason |
|---|---|---|
| Read-only portal: orders, invoices and credit notes, open items, payments, statement, returns | built | see README; services: sales, billing, receivables |
| Vite/React app, tests, axe checks on every page | built | English only |
| Placing orders from the portal (FR-30.14) | built | reorder form: only products the customer has bought, at the price, unit and warehouse of its last order; sales confirms it through the credit check. A first order for a product is entered by staff |
| Requesting a return (FR-30.14) | built | pick one of the customer's invoices, the lines and a reason; staff authorize, receive and inspect it as any return order |
| Product names on order and return lines | built | from the product service through sales; images are not built |
| Customer names in the company selector | built | identity's customer-facing legal-entity name; falls back to a numbered name |
| Contacts of bill-to or ship-to accounts of the same customer group | built | an "Acting for" selector when party lists more than one account for the contact |
| Installable PWA | built | manifest, icons, offline page and a service worker that keeps only static files and never data. Needs `sw.js` served from the root with `Cache-Control: no-cache` by the edge |
| Logout ends the Keycloak session | built | the browser is sent to the end-session URL |
| Sign-in realm, contact invitation, MFA | built in identity, not applied | a live Keycloak is needed (#213) |
| Paying by card or ACH (FR-30.25) | not built | needs the payments gateway (a separate service) that does not exist |
| Invoice and statement PDF download (FR-30.26) | built | Download buttons on the invoice and statement pages; the BFF renders them through print |
| Shipment tracking, stored documents (FR-30.26) | not built | need the document store (decision on #512) and a carrier read API |
| Full WCAG audit with a screen reader, mobile-first visual check | not built | axe covers the automatable part; the screen-reader pass and device review go to #512 |

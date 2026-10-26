# supplier-portal

Vendor-facing portal for wave 20 (ticket PSG-20, "Inventory Costing & Supplier Collaboration"),
served at `supplier.<env>.passage.dev`.

**Status: not built in slice 1.** This directory holds the scope record only. The PRD (`docs/prd/phase-3-procure-to-pay.md` §3.20) defines the screens, APIs and realm:

- Portal APIs under `/portal/v1` (purchase orders and acknowledgements, ASNs, invoices, payments, users invitation). Built on the wave 17, 18 and 19 APIs with their own Keycloak realm `suppliers`, its own database `supplier-db`, and a TypeScript BFF.
- Screens: home, PO acknowledgement, ASN, invoices and payments, users, plus the FR-20.18 to FR-20.22 pages.
- Intended stack, per ADR-0001: a Vite + React app served by the shell, with a Fastify/zod BFF in `services/supplier-portal-ts` (that BFF is outside this wave's owned paths and needs its own wave or an owner decision).

See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md) for each deliverable and its reason.

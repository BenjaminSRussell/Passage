# customer-portal (PSG-30, not yet built)

The customer portal at `customer.<env>.passage.dev` is a PSG-30 deliverable
(FR-30.14, FR-30.25, FR-30.26): invited contacts place orders, view invoices and
documents, pay by card or ACH through the payments gateway, request returns and
track shipments. WCAG 2.1 AA, mobile-first.

**Status: not started in slice 1.** This directory holds only this note. There is
no Vite app, no package.json and no BFF yet, so nothing here is buildable.

Why it is deferred: the portal needs the receivables read APIs (open items,
payments, returns) that this slice exposes only partly. It also needs the
`portal-bff` (cookie session, token handler), the Keycloak `portal` realm and
OPA scoping to the contact's accounts (edge-identity), and the payments gateway
(`payments-gateway`, a separate TypeScript service outside this owned path).

Planned layout when built (React 18 + Vite + TS, no new npm dependencies beyond
the shared lockfile without review):

```
apps/customer-portal/
  src/             pages: orders, invoices, payments, returns, shipments, documents
  src/api/         fetch clients for /portal/api/v1/*
  e2e/             Playwright + axe (portal-e2e fixture, 0 serious a11y violations)
```

The full list of what is missing, with status and reason, is in
[services/receivables/NOT-IMPLEMENTED.md](../../services/receivables/NOT-IMPLEMENTED.md).

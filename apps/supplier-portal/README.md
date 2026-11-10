# supplier-portal

Vendor-facing portal for wave 20 (PSG-20, issue #511), served at `supplier.<env>.passage.dev`. A standalone Vite + React 19 app
(like `apps/warehouse-mobile`), not a shell module: suppliers are external users who must never reach the buyer shell. It talks only
to its BFF, `services/supplier-portal-ts`.

## Screens

| Route | What a supplier does |
|---|---|
| `/purchase-orders`, `/purchase-orders/:id` | Read the orders sent to them; accept, reject with a reason, or propose quantity, price or delivery date changes per line (sends the version on screen, so a stale answer is refused) |
| `/shipments`, `/shipments/new`, `/shipments/:id` | Advance ship notices built from the open lines of an order (carrier, tracking, dates, quantity, batch, expiry) |
| `/invoices` | Send an invoice document (UBL XML, or PDF or image for OCR); see documents waiting for review and posted invoices with a vendor status (accepted, in review, rejected) |
| `/requests`, `/requests/:id` | Ask the buyer for a change; complete the onboarding questionnaire the buyer opened; withdraw a request |
| `/documents` | Download controlled documents the buyer distributed and acknowledge them; exchange files with the buyer (up to 5 MB) |

The vendor is never in a request: the BFF takes it from the session. The page holds no token, only a CSRF token from `/bff/me`.
A legal-entity selector appears when the user belongs to several buyer entities (sent as `X-Legal-Entity`).

## Run and test

```
npm ci && npm run dev        # :5175, proxies /bff and /api to the BFF on :8080 (run it with COOKIE_SECURE=false)
npx tsc --noEmit && npx vitest --run && npx vite build
```

Tests cover the answer, notice, invoice, document and questionnaire flows (payloads, client-side checks, CSRF and legal entity
headers) and run axe on the main pages. Authorization (a supplier sees only its own rows) is tested in the services and the BFF.

See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

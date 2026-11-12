# customer-portal

Customer-facing portal for wave 30 (PSG-30, issue #30; FR-30.14, FR-30.25), served at `customer.<env>.passage.dev`. A standalone Vite +
React 19 app (like `apps/supplier-portal`), not a shell module: customers are external users who must never reach the internal
shell. It talks only to its BFF, `services/customer-portal-ts`. It reads, and makes two writes for the customer itself: an order and a return.

## Screens

| Route | What a customer sees |
|---|---|
| `/` | What they owe, by currency, and how much of it is overdue |
| `/orders`, `/orders/:id` | Their orders (not drafts) with their own reference, and each order's lines (product names, ship dates, delivered and invoiced quantities); "Order these again" |
| `/orders/new` | Place an order from products they have bought before |
| `/invoices`, `/invoices/:id` | Their posted invoices and credit notes with lines and totals |
| `/open-items` | Their open items and the balance |
| `/payments` | Payments received from them |
| `/statement` | The account statement as of a day, by currency, with the closing balance |
| `/returns`, `/returns/:id` | Their return orders, the credit expected and the lines |
| `/returns/new` | Request a return of goods from one of their invoices |

The customer is never in a request: the BFF takes it from the session and signs it into the service token; the services read it only
from that verified token. The page holds no token. A selector appears when the contact belongs to several of the seller's companies
(sent as `X-Legal-Entity`, named by identity, or "Company 2" when it cannot), and another when party lists bill-to or ship-to accounts of
the contact's customer group (sent as `X-Customer`; the BFF signs one only when party lists it). Sign out sends the browser to Keycloak's
end-session endpoint. The app is an installable shell (`public/manifest.webmanifest`, `public/sw.js`): the worker keeps static files and
an offline page and never stores an API response or a page; serve `sw.js` from the root with `Cache-Control: no-cache`.

## Run and test

```
npm ci && npm run dev        # :5176, proxies /bff and /api to the BFF on :8080 (run it with COOKIE_SECURE=false)
npx tsc --noEmit && npx vitest --run && npx vite build
```

Tests cover each page (rendering, no ids on screen, problems shown), the balance arithmetic, the as-of statement query, the order and
return forms (one key per submit, nothing sent for an invalid form, no customer, price or site in a body), the account and company
selectors, sign-out at Keycloak, the manifest and the service worker's caching rules (never data), and run axe on every page. That a customer can never read another customer's data
is tested where it is enforced: in the BFF (`services/customer-portal-ts/test`) and in receivables, billing and sales (their
`customer_portal_test.go` and store tests).

See [NOT-IMPLEMENTED.md](NOT-IMPLEMENTED.md).

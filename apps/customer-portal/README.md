# customer-portal

Customer-facing portal for wave 30 (DPT-30, issue #30; FR-30.14), served at `customer.<env>.depot.dev`. A standalone Vite +
React 19 app (like `apps/supplier-portal`), not a shell module: customers are external users who must never reach the internal
shell. It talks only to its BFF, `services/customer-portal-ts`. It reads, and makes three writes for the customer itself: an order, a return and a card or bank account payment.

## Screens

| Route | What a customer sees |
|---|---|
| `/` | What they owe, by currency, and how much of it is overdue |
| `/orders`, `/orders/:id` | Their orders (not drafts) with their own reference, and each order's lines (product names, ship dates, delivered and invoiced quantities); "Order these again" |
| `/orders/new` | Place an order from products they have bought before |
| `/invoices`, `/invoices/:id` | Their posted invoices and credit notes with lines and totals |
| `/open-items` | Their open items and the balance |
| `/payments` | Payments received from them, and their card and bank account payments made here with the status |
| `/pay?items=` or `/pay?invoices=` | Pay one or several open invoices: the amount of each (part is fine, more than open is refused), card or US bank account (ACH), then Stripe's Payment Element |
| `/pay/:id` | Where a card or bank payment stands: waiting, processing (ACH takes days), paid, failed (in plain language), refunded or returned |
| `/statement` | The account statement as of a day, by currency, with the closing balance |
| `/returns`, `/returns/:id` | Their return orders, the credit expected and the lines |
| `/returns/new` | Request a return of goods from one of their invoices |

The customer is never in a request: the BFF takes it from the session and signs it into the service token; the services read it only
from that verified token. The page holds no token. A selector appears when the contact belongs to several of the seller's companies
(sent as `X-Legal-Entity`, named by identity, or "Company 2" when it cannot), and another when party lists bill-to or ship-to accounts of
the contact's customer group (sent as `X-Customer`; the BFF signs one only when party lists it). Sign out sends the browser to Keycloak's
end-session endpoint. The app is an installable shell (`public/manifest.webmanifest`, `public/sw.js`): the worker keeps static files and
an offline page and never stores an API response or a page; serve `sw.js` from the root with `Cache-Control: no-cache`.

## Card and bank account payments

The customer picks open invoices and amounts; `POST /api/v1/payment-intents` (one Idempotency-Key per submit) has receivables create a
Stripe PaymentIntent and answer its client secret. Only then is Stripe.js loaded, from `https://js.stripe.com/v3/` by a script tag
(`src/stripe.ts`, no npm dependency), and the Payment Element is mounted in Stripe's iframe: the card number, CVC and bank details go
from the browser to Stripe and never reach Depot (PCI SAQ A). The serving edge's Content-Security-Policy, when one is added, must allow
`https://js.stripe.com` for scripts and frames and `https://api.stripe.com` for connections. After `confirmPayment` (or a 3-D Secure
redirect back to `/pay/:id`) the page reads the payment from receivables, which changes it only on Stripe's signature-verified webhook.
While a payment waits for the customer it holds its invoices; "Change the amounts" cancels it (`POST /api/v1/payment-intents/:id/cancel`)
so they can be paid again. When the payment form cannot be shown (no publishable key) the page says so.

The demo build (`npx vite build --mode demo`) answers the BFF in the browser (`src/demo.ts`, Harlan Agricultural Co-op buying from
Cedar Ridge Pumps) and replaces the Payment Element with a stub: a card payment is paid at once and a bank account payment stays processing,
without Stripe.js. Check it with `node apps/web/scripts/demo-check.mjs --base "" --port <p> /pay?items=... /pay/<id>` against `vite preview --mode demo`.

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

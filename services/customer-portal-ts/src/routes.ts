// The routes the customer portal app may call and the one upstream call each becomes. Nothing else is proxied. Reads are GET; the
// two writes a customer makes for itself (FR-30.14) are POST /orders and POST /returns, each one call to sales with its own duty
// and an Idempotency-Key. The customer is never a parameter or a body field: it is in the signed service token. The legal entity is
// the session's, never the client's.

import type { Audience } from "./serviceToken.ts";

export interface Route {
  method: "GET" | "POST";
  /** Path with :name segments. */
  path: string;
  audience: Audience;
  /** The one duty of the audience's grant this call carries. */
  duty: string;
  /** Upstream path; :name segments are filled from the matched path and must be UUIDs. */
  upstream: string;
  /**
   * The route always acts for the contact's own customer, whatever X-Customer says. The accounts list is such a route: it is how the
   * other accounts are found, so it cannot depend on one of them.
   */
  homeOnly?: true;
}

const AR = "/v1/receivables/customer-portal";
const BILL = "/v1/billing/customer-portal";
const SALES = "/v1/sales/customer-portal";
const PARTY = "/v1/party/customer-portal";

const AR_READ = "receivables.customer-portal.read";
const BILL_READ = "billing.customer-portal.read";
const SALES_READ = "sales.customer-portal.read";

export const ROUTES: readonly Route[] = [
  { method: "GET", path: "/api/v1/invoices", audience: "billing", duty: BILL_READ, upstream: `${BILL}/invoices` },
  { method: "GET", path: "/api/v1/invoices/:id", audience: "billing", duty: BILL_READ, upstream: `${BILL}/invoices/:id` },

  { method: "GET", path: "/api/v1/open-items", audience: "receivables", duty: AR_READ, upstream: `${AR}/open-items` },
  { method: "GET", path: "/api/v1/open-items/:id", audience: "receivables", duty: AR_READ, upstream: `${AR}/open-items/:id` },
  { method: "GET", path: "/api/v1/payments", audience: "receivables", duty: AR_READ, upstream: `${AR}/payments` },
  { method: "GET", path: "/api/v1/statement", audience: "receivables", duty: AR_READ, upstream: `${AR}/statement` },

  { method: "GET", path: "/api/v1/orders", audience: "sales", duty: SALES_READ, upstream: `${SALES}/orders` },
  { method: "GET", path: "/api/v1/orders/:id", audience: "sales", duty: SALES_READ, upstream: `${SALES}/orders/:id` },
  { method: "GET", path: "/api/v1/returns", audience: "sales", duty: SALES_READ, upstream: `${SALES}/returns` },
  { method: "GET", path: "/api/v1/returns/:id", audience: "sales", duty: SALES_READ, upstream: `${SALES}/returns/:id` },
  { method: "GET", path: "/api/v1/products", audience: "sales", duty: SALES_READ, upstream: `${SALES}/products` },

  { method: "POST", path: "/api/v1/orders", audience: "sales", duty: "sales.customer-portal.order", upstream: `${SALES}/orders` },
  { method: "POST", path: "/api/v1/returns", audience: "sales", duty: "sales.customer-portal.return", upstream: `${SALES}/returns` },

  { method: "GET", path: "/api/v1/accounts", audience: "party", duty: "party.customer-portal.read", upstream: `${PARTY}/accounts`, homeOnly: true },
];

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Matches a request path against the table. A path parameter that is not a UUID matches nothing. */
export function matchRoute(method: string, pathname: string): { route: Route; upstreamPath: string } | undefined {
  const segments = pathname.split("/");
  for (const route of ROUTES) {
    if (route.method !== method) continue;
    const pattern = route.path.split("/");
    if (pattern.length !== segments.length) continue;
    const params = new Map<string, string>();
    const ok = pattern.every((p, i) => {
      const s = segments[i] as string;
      if (!p.startsWith(":")) return p === s;
      if (!UUID.test(s)) return false;
      params.set(p.slice(1), s.toLowerCase());
      return true;
    });
    if (ok) return { route, upstreamPath: route.upstream.replace(/:([a-z]+)/g, (m, name: string) => params.get(name) ?? m) };
  }
  return undefined;
}

/**
 * Query parameters that pass through to a list. `customer`, `customerId`, `legalEntityId` and `tenantId` are not among them,
 * and the owning services ignore them on these routes anyway: who the caller is comes from the signed token.
 */
export const QUERY_ALLOWLIST = ["limit", "cursor", "status", "asOf"] as const;

/** Body fields a write may never carry: who the call is for is the token's. The owning service refuses unknown fields as well. */
export const FORBIDDEN_BODY_FIELDS = ["customer", "customerId", "customer_id", "legalEntityId", "legalEntity", "tenantId", "tenant_id", "invoiceAccountId", "unitPrice"] as const;

export const IDEMPOTENCY_KEY = /^[A-Za-z0-9_-]{8,80}$/;

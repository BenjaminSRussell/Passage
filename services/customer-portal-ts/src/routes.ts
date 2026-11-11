// The routes the customer portal app may call and the one upstream call each becomes. Nothing else is proxied, and every route is
// a read: there is no POST. The customer is never a parameter: it is in the signed service token. The legal entity is the
// session's, never the client's.

import type { Audience } from "./serviceToken.ts";

export interface Route {
  method: "GET";
  /** Path with :name segments. */
  path: string;
  audience: Audience;
  /** Upstream path; :name segments are filled from the matched path and must be UUIDs. */
  upstream: string;
}

const AR = "/v1/receivables/customer-portal";
const BILL = "/v1/billing/customer-portal";
const SALES = "/v1/sales/customer-portal";

export const ROUTES: readonly Route[] = [
  { method: "GET", path: "/api/v1/invoices", audience: "billing", upstream: `${BILL}/invoices` },
  { method: "GET", path: "/api/v1/invoices/:id", audience: "billing", upstream: `${BILL}/invoices/:id` },

  { method: "GET", path: "/api/v1/open-items", audience: "receivables", upstream: `${AR}/open-items` },
  { method: "GET", path: "/api/v1/open-items/:id", audience: "receivables", upstream: `${AR}/open-items/:id` },
  { method: "GET", path: "/api/v1/payments", audience: "receivables", upstream: `${AR}/payments` },
  { method: "GET", path: "/api/v1/statement", audience: "receivables", upstream: `${AR}/statement` },

  { method: "GET", path: "/api/v1/orders", audience: "sales", upstream: `${SALES}/orders` },
  { method: "GET", path: "/api/v1/orders/:id", audience: "sales", upstream: `${SALES}/orders/:id` },
  { method: "GET", path: "/api/v1/returns", audience: "sales", upstream: `${SALES}/returns` },
  { method: "GET", path: "/api/v1/returns/:id", audience: "sales", upstream: `${SALES}/returns/:id` },
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

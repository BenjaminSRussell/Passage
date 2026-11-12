// The routes the supplier portal app may call and the one upstream call each becomes. Nothing else is proxied. The vendor
// is never a parameter: it is in the signed service token. The legal entity is the session's, never the client's.

import type { Audience } from "./serviceToken.ts";

export interface Route {
  method: "GET" | "POST";
  /** Path with :name segments. */
  path: string;
  audience: Audience;
  /** Upstream path; :name segments are filled from the matched path and must be UUIDs. */
  upstream: string;
  /** JSON body forwarded (a POST), or an uploaded document. */
  body?: "json";
  /** The upstream body names the legal entity (receiving does); the session's replaces whatever the client sent. */
  injectLegalEntity?: boolean;
  /** The answer is a file, streamed with its headers. */
  binary?: boolean;
  /** Only a vendor administrator (the `supplier-admin` role) may call it. */
  adminOnly?: boolean;
  /** The body field holding a base64 upload; it is scanned for viruses before the request goes on. */
  scan?: string;
  /** The answer carries an invitation token; the portal adds the link to accept it. */
  inviteLink?: boolean;
}

const P = "/v1/procurement/vendor";
const R = "/v1/receiving/vendor";
const Y = "/v1/party/vendor-portal";
const A = "/v1/payables/vendor";

export const ROUTES: readonly Route[] = [
  { method: "GET", path: "/api/v1/purchase-orders", audience: "purchasing", upstream: `${P}/purchase-orders` },
  { method: "GET", path: "/api/v1/purchase-orders/:id", audience: "purchasing", upstream: `${P}/purchase-orders/:id` },
  { method: "POST", path: "/api/v1/purchase-orders/:id/acknowledgements", audience: "purchasing", upstream: `${P}/purchase-orders/:id/acknowledgements`, body: "json" },

  { method: "GET", path: "/api/v1/asns", audience: "receiving", upstream: `${R}/asns` },
  { method: "GET", path: "/api/v1/asns/:id", audience: "receiving", upstream: `${R}/asns/:id` },
  { method: "POST", path: "/api/v1/asns", audience: "receiving", upstream: `${R}/asns`, body: "json", injectLegalEntity: true },
  { method: "GET", path: "/api/v1/invoices", audience: "receiving", upstream: `${R}/invoices` },
  { method: "GET", path: "/api/v1/invoices/:id", audience: "receiving", upstream: `${R}/invoices/:id` },
  { method: "GET", path: "/api/v1/invoiceable-lines", audience: "receiving", upstream: `${R}/invoiceable-lines` },
  { method: "GET", path: "/api/v1/invoice-submissions", audience: "receiving", upstream: `${R}/invoice-submissions` },
  { method: "GET", path: "/api/v1/invoice-submissions/:id", audience: "receiving", upstream: `${R}/invoice-submissions/:id` },
  { method: "POST", path: "/api/v1/invoice-submissions", audience: "receiving", upstream: `${R}/invoice-submissions`, body: "json", injectLegalEntity: true },
  { method: "POST", path: "/api/v1/invoice-submissions/:id/withdraw", audience: "receiving", upstream: `${R}/invoice-submissions/:id:withdraw` },
  { method: "GET", path: "/api/v1/payments", audience: "payables", upstream: `${A}/payments` },
  { method: "GET", path: "/api/v1/invoice-documents", audience: "receiving", upstream: `${R}/invoice-captures` },
  { method: "GET", path: "/api/v1/invoice-documents/:id", audience: "receiving", upstream: `${R}/invoice-captures/:id` },
  { method: "POST", path: "/api/v1/invoice-documents", audience: "receiving", upstream: `${R}/invoice-captures`, body: "json", injectLegalEntity: true, scan: "content" },

  { method: "GET", path: "/api/v1/requests", audience: "party", upstream: `${Y}/requests` },
  { method: "GET", path: "/api/v1/requests/:id", audience: "party", upstream: `${Y}/requests/:id` },
  { method: "POST", path: "/api/v1/requests", audience: "party", upstream: `${Y}/requests`, body: "json" },
  { method: "POST", path: "/api/v1/requests/:id/submit", audience: "party", upstream: `${Y}/requests/:id:submit`, body: "json" },
  { method: "POST", path: "/api/v1/requests/:id/withdraw", audience: "party", upstream: `${Y}/requests/:id:withdraw` },
  { method: "GET", path: "/api/v1/documents", audience: "party", upstream: `${Y}/documents` },
  { method: "GET", path: "/api/v1/documents/:id/content", audience: "party", upstream: `${Y}/documents/:id/content`, binary: true },
  { method: "POST", path: "/api/v1/documents", audience: "party", upstream: `${Y}/documents`, body: "json", scan: "contentBase64" },
  { method: "GET", path: "/api/v1/distributions", audience: "party", upstream: `${Y}/distributions` },
  { method: "GET", path: "/api/v1/distributions/:id/content", audience: "party", upstream: `${Y}/distributions/:id/content`, binary: true },
  { method: "POST", path: "/api/v1/distributions/:id/acknowledge", audience: "party", upstream: `${Y}/distributions/:id:acknowledge` },

  { method: "GET", path: "/api/v1/invites", audience: "party", upstream: `${Y}/invites`, adminOnly: true },
  { method: "POST", path: "/api/v1/invites", audience: "party", upstream: `${Y}/invites`, body: "json", adminOnly: true, inviteLink: true },
  { method: "POST", path: "/api/v1/invites/:id/revoke", audience: "party", upstream: `${Y}/invites/:id:revoke`, adminOnly: true },
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

/** Query parameters that pass through to a list. `vendorId` and `legalEntityId` are not among them. */
export const QUERY_ALLOWLIST = ["limit", "cursor", "status", "purchaseOrderId"] as const;

/** Body keys the client may never set: who the caller is comes from the session. */
export const STRIPPED_BODY_KEYS = ["vendorId", "legalEntityId", "tenantId"] as const;

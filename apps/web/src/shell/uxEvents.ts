import { apiFetch } from "./api/http";
import { resolveHelp } from "./help/routeHelp";
import { localPreferenceStorage, type PreferenceStorage } from "./preferenceStorage";

/**
 * UX telemetry (#510): where people stop in a flow. Events go to the analytics service in small batches and
 * hold a random per-tab session id and the release, never a user id, a record id or a field value. Nothing is
 * sent when the person has opted out, when the browser asks not to be tracked, or when the person does not hold
 * the record duty. A failed send is dropped: telemetry never gets in the way of work.
 */

export const UX_EVENTS_PATH = "/api/v1/analytics/ux-events";
export const UX_RECORD_DUTY = "analytics.ux.record";
export const UX_OPT_OUT_KEY = "passage.ux-telemetry.optout.v1";

export type UxFlow = "procure_to_pay" | "onboarding";

export type UxEvent =
  | { type: "funnel_step"; flow: UxFlow; step: string }
  | { type: "validation_error"; route: string; field: string }
  | { type: "search_no_results"; query: string }
  | { type: "form_abandoned"; route: string };

type Sent = UxEvent & { sessionId: string; release: string; occurredAt: string };

export function isOptedOut(storage: PreferenceStorage = localPreferenceStorage): boolean {
  const dnt = typeof navigator !== "undefined" && (navigator.doNotTrack === "1" || (globalThis as { doNotTrack?: string }).doNotTrack === "1");
  return dnt || storage.read(UX_OPT_OUT_KEY) === "1";
}

export function setOptOut(optOut: boolean, storage: PreferenceStorage = localPreferenceStorage): void {
  if (optOut) storage.write(UX_OPT_OUT_KEY, "1");
  else storage.remove(UX_OPT_OUT_KEY);
}

/** A page template for an address: the route help pattern when there is one, else ids replaced by :id. */
export function routeTemplate(pathname: string): string {
  const base = (import.meta.env.BASE_URL ?? "/").replace(/\/$/, "");
  const path = base && pathname.startsWith(base) ? pathname.slice(base.length) || "/" : pathname;
  const known = resolveHelp(path)?.pattern;
  if (known) return known;
  return path
    .split("/")
    .map((segment) => (/\d/.test(segment) ? ":id" : segment))
    .join("/")
    .replace(/[^A-Za-z0-9/_:.{}*-]/g, "")
    .slice(0, 199) || "/";
}

const FIELD = /^[A-Za-z][A-Za-z0-9._[\]-]{0,63}$/;
const MAX_QUEUE = 200;
const BATCH = 20;
const FLUSH_MS = 5000;

export interface UxClientOptions {
  fetchImpl?: typeof fetch;
  storage?: PreferenceStorage;
  now?: () => Date;
}

export class UxEventClient {
  private queue: Sent[] = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private enabled = false;
  private release = "dev";
  private identity = "";
  private session = "";

  constructor(private readonly options: UxClientOptions = {}) {}

  private get storage(): PreferenceStorage {
    return this.options.storage ?? localPreferenceStorage;
  }

  /** Called by the shell when the principal is known. */
  configure({ duties, identity, release }: { duties: readonly string[]; identity: string; release?: string }): void {
    this.enabled = duties.includes(UX_RECORD_DUTY);
    this.identity = identity;
    if (release) this.release = release.replace(/[^A-Za-z0-9._+-]/g, "").slice(0, 32) || "dev";
  }

  private sessionId(): string {
    if (this.session) return this.session;
    let id = "";
    try {
      id = window.sessionStorage.getItem("passage.ux-session.v1") ?? "";
      if (!id) {
        id = crypto.randomUUID();
        window.sessionStorage.setItem("passage.ux-session.v1", id);
      }
    } catch {
      id = id || crypto.randomUUID();
    }
    this.session = id;
    return id;
  }

  track(event: UxEvent): void {
    if (!this.enabled || isOptedOut(this.storage)) return;
    if (event.type === "validation_error" && !FIELD.test(event.field)) return;
    if (this.queue.length >= MAX_QUEUE) this.queue.shift();
    this.queue.push({ ...event, sessionId: this.sessionId(), release: this.release, occurredAt: (this.options.now?.() ?? new Date()).toISOString() });
    if (this.queue.length >= BATCH) void this.flush();
    else if (this.timer === undefined) this.timer = setTimeout(() => void this.flush(), FLUSH_MS);
  }

  funnelStep(flow: UxFlow, step: string): void {
    this.track({ type: "funnel_step", flow, step });
  }

  /** The first transaction of a tenant's user is also the last step of the onboarding funnel. Reported once per browser. */
  firstTransaction(): void {
    const key = `passage.ux.first-transaction.v1.${this.identity}`;
    if (this.identity === "" || this.storage.read(key) === "1") return;
    this.funnelStep("onboarding", "first_transaction");
    this.storage.write(key, "1");
  }

  async flush(keepalive = false): Promise<void> {
    if (this.timer !== undefined) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
    const events = this.queue.splice(0, 50);
    if (events.length === 0) return;
    try {
      await (this.options.fetchImpl ?? apiFetch)(UX_EVENTS_PATH, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ events }),
        keepalive,
      });
    } catch {
      // Dropped on purpose.
    }
    if (this.queue.length > 0) void this.flush(keepalive);
  }
}

export const ux = new UxEventClient();

interface FunnelRule {
  method: string;
  path: RegExp;
  flow: UxFlow;
  step: string;
  /** A real business transaction, so also the end of the onboarding funnel. */
  transaction?: boolean;
}

/** Successful writes that mark a step. Matching the request keeps the pages free of tracking calls. */
export const FUNNEL_RULES: readonly FunnelRule[] = [
  { method: "POST", path: /^\/api\/v1\/procurement\/requisitions$/, flow: "procure_to_pay", step: "requisition_created" },
  { method: "POST", path: /^\/api\/v1\/procurement\/requisitions\/[^/]+:submit$/, flow: "procure_to_pay", step: "requisition_submitted" },
  { method: "POST", path: /^\/api\/v1\/procurement\/purchase-orders$/, flow: "procure_to_pay", step: "purchase_order_created", transaction: true },
  { method: "POST", path: /^\/api\/v1\/receiving\/receipts\/[^/]+:post$/, flow: "procure_to_pay", step: "receipt_posted", transaction: true },
  { method: "POST", path: /^\/api\/v1\/receiving\/invoices:post$/, flow: "procure_to_pay", step: "vendor_invoice_posted", transaction: true },
  { method: "POST", path: /^\/api\/v1\/payables\/payment-proposals$/, flow: "procure_to_pay", step: "payment_proposal_created", transaction: true },
  { method: "POST", path: /^\/api\/v1\/org\/legal-entities$/, flow: "onboarding", step: "legal_entity_created" },
  { method: "POST", path: /^\/api\/v1\/org\/fiscal-calendars$/, flow: "onboarding", step: "fiscal_calendar_set" },
  { method: "POST", path: /^\/api\/v1\/org\/.*\/?sites$/, flow: "onboarding", step: "site_created" },
  { method: "POST", path: /^\/api\/v1\/party\/(parties|vendors|customers)$/, flow: "onboarding", step: "party_added" },
  { method: "POST", path: /^\/api\/v1\/product\/products$/, flow: "onboarding", step: "product_created" },
  { method: "POST", path: /^\/api\/v1\/sales\/orders$/, flow: "onboarding", step: "first_transaction" },
];

export function matchFunnelRule(method: string, pathname: string): FunnelRule | undefined {
  return FUNNEL_RULES.find((rule) => rule.method === method.toUpperCase() && rule.path.test(pathname));
}

/** Field names from a refused write's problem body (`errors` maps field name to message). */
export function problemFields(body: unknown): string[] {
  const errors = (body as { errors?: unknown } | null)?.errors;
  return errors && typeof errors === "object" && !Array.isArray(errors) ? Object.keys(errors).slice(0, 10) : [];
}

function urlPath(input: RequestInfo | URL): string {
  try {
    const raw = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    return new URL(raw, "http://local").pathname;
  } catch {
    return "";
  }
}

/** Wraps fetch so successful funnel writes and refused writes are reported. Returns the function that removes it. */
export function observeFetch(client: UxEventClient = ux, getPathname: () => string = () => window.location.pathname): () => void {
  if (typeof window.fetch !== "function") return () => undefined;
  const original = window.fetch;
  const observed: typeof fetch = async (input, init) => {
    const res = await original.call(window, input, init);
    try {
      const method = init?.method ?? (input instanceof Request ? input.method : "GET");
      const path = urlPath(input);
      if (method !== "GET" && path !== UX_EVENTS_PATH) {
        if (res.ok) {
          const rule = matchFunnelRule(method, path);
          if (rule) {
            client.funnelStep(rule.flow, rule.step);
            if (rule.transaction) client.firstTransaction();
          }
        } else if (res.status === 400 || res.status === 422) {
          const fields = problemFields(await res.clone().json().catch(() => null));
          const route = routeTemplate(getPathname());
          for (const field of fields) client.track({ type: "validation_error", route, field });
        }
      }
    } catch {
      // Never let reporting change the answer a page gets.
    }
    return res;
  };
  window.fetch = observed;
  return () => {
    if (window.fetch === observed) window.fetch = original;
  };
}

export const trackSearchNoResults = (query: string): void => {
  if (query.trim().length >= 3) ux.track({ type: "search_no_results", query });
};

export const trackFormAbandoned = (): void => {
  ux.track({ type: "form_abandoned", route: routeTemplate(window.location.pathname) });
};

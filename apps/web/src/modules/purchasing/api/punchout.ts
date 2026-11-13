import { apiFetch } from "../../../shell/api/http";
import { ProcurementApiError, type Problem } from "./client";
// Punchout (#35, 35-3) lives in the EDI engine: the browser calls /api/v1/edi/punchout/..., which the web BFF rewrites to
// /edi/v1/punchout/... on the edi service. Every route needs the requester's duty edi.requisition.create.

export const EDI_BASE = "/api/v1/edi";

export type PunchoutFormat = "CXML" | "OCI";
export type PunchoutStatus = "OPEN" | "RETURNED" | "REJECTED" | "CANCELLED";

/** A supplier whose punchout catalog the requester can open. */
export interface PunchoutSupplier {
  partnerId: string;
  name: string;
  legalEntityId: string;
  currencies: string[];
}

/** One line of a returned cart, as validated against BR-35.13. */
export interface CartLine {
  line: number;
  supplierPartId: string;
  description: string;
  quantity: string;
  unitCode: string;
  unitPrice: string;
  currency: string;
  unspsc: string;
  leadTimeDays: number;
  catalog: boolean;
  needsApproval: boolean;
}

export interface CartIssue {
  code: string;
  detail: string;
  reject: boolean;
}

export interface Cart {
  lines: CartLine[];
  issues: CartIssue[];
  cartRejected: boolean;
  computedTotal: string;
  statedTotal?: string;
}

export interface PunchoutSession {
  id: string;
  partnerId: string;
  legalEntityId: string;
  requesterId: string;
  format: PunchoutFormat;
  status: PunchoutStatus;
  startUrl?: string;
  expiresAt: string;
  returnedAt?: string;
  cart?: Cart;
  requisitionId?: string;
  createdAt: string;
}

async function send<T>(method: "GET" | "POST", path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await apiFetch(EDI_BASE + path, {
    method,
    headers,
    credentials: "same-origin",
    signal,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    let problem: Problem;
    try {
      problem = (await res.json()) as Problem;
    } catch {
      problem = { type: "", title: res.statusText || "Request failed", status: res.status };
    }
    throw new ProcurementApiError({ ...problem, status: problem.status ?? res.status });
  }
  return (await res.json()) as T;
}

export async function listPunchoutSuppliers(signal?: AbortSignal): Promise<PunchoutSupplier[]> {
  return (await send<{ items: PunchoutSupplier[] }>("GET", "/punchout/suppliers", undefined, signal)).items ?? [];
}

export async function listPunchoutSessions(signal?: AbortSignal): Promise<PunchoutSession[]> {
  return (await send<{ items: PunchoutSession[] }>("GET", "/punchout/sessions", undefined, signal)).items ?? [];
}

export function openPunchoutSession(partnerId: string, format: PunchoutFormat): Promise<PunchoutSession> {
  return send<PunchoutSession>("POST", "/punchout/sessions", { partnerId, format });
}

export function cancelPunchoutSession(id: string): Promise<PunchoutSession> {
  return send<PunchoutSession>("POST", `/punchout/sessions/${encodeURIComponent(id)}:cancel`);
}

export function createRequisitionFromCart(id: string): Promise<{ sessionId: string; requisitionId: string }> {
  return send<{ sessionId: string; requisitionId: string }>("POST", `/punchout/sessions/${encodeURIComponent(id)}:create-requisition`);
}

/** The cart total in its currency, or the computed total alone when the lines mix currencies or there are none. */
export function cartTotal(cart: Cart | undefined): string {
  if (!cart || cart.lines.length === 0) return "—";
  const currency = cart.lines[0]!.currency;
  return cart.lines.every((l) => l.currency === currency) ? `${cart.computedTotal} ${currency}` : cart.computedTotal;
}

/** What the requester can do with a session next. */
export function nextStep(s: PunchoutSession): "shop" | "create" | "open" | "none" {
  if (s.status === "OPEN") return "shop";
  if (s.status === "RETURNED") return s.requisitionId ? "open" : "create";
  return "none";
}

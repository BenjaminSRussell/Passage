import { Context } from "@temporalio/activity";
import type { TenderCarrier, TenderRef, TenderView } from "./tenderWorkflow.js";
import { callFailure, mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

export interface TenderActivities {
  startTender(ref: TenderRef & { loadId: string; carriers: TenderCarrier[]; currency?: string; timeoutMin?: number; idempotencyKey: string }): Promise<TenderView>;
  getTender(ref: TenderRef & { tenderId: string }): Promise<TenderView>;
  sweepExpiredTenders(ref: TenderRef): Promise<void>;
}

export function createTenderActivities(baseUrl: string, signer: ServiceTokenSigner, fetchImpl: typeof fetch = fetch): TenderActivities {
  async function send(ref: TenderRef, label: string, path: string, init: { method: string; headers?: Record<string, string>; body?: unknown }): Promise<Response> {
    const response = await fetchImpl(`${baseUrl}/v1/transport${path}`, {
      method: init.method,
      headers: {
        "X-Passage-Service-Token": mintOrReject(() => signer.mintForTransport({ tenantId: ref.tenantId, legalEntityId: ref.legalEntityId, actor: ref.actor })),
        ...(init.body === undefined ? {} : { "Content-Type": "application/json" }),
        ...init.headers,
      },
      ...(init.body === undefined ? {} : { body: JSON.stringify(init.body) }),
      signal: Context.current().cancellationSignal,
    });
    if (!response.ok) throw await callFailure(`transport ${label}`, response);
    return response;
  }

  return {
    async startTender({ loadId, carriers, currency, timeoutMin, idempotencyKey, ...ref }) {
      const response = await send(ref, "tender start", `/loads/${encodeURIComponent(loadId)}:tender`, {
        method: "POST",
        headers: { "Idempotency-Key": idempotencyKey },
        body: { carriers, ...(currency ? { currency } : {}), ...(timeoutMin ? { timeoutMin } : {}) },
      });
      return (await response.json()) as TenderView;
    },
    async getTender({ tenderId, ...ref }) {
      const response = await send(ref, "tender read", `/tenders/${encodeURIComponent(tenderId)}`, { method: "GET" });
      return (await response.json()) as TenderView;
    },
    async sweepExpiredTenders(ref) {
      await send(ref, "tender expiry sweep", "/tenders:expire", { method: "POST" });
    },
  };
}

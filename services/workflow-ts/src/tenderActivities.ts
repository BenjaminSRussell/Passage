import { Context } from "@temporalio/activity";
import type { TenderCarrier, TenderRef, TenderView } from "./tenderWorkflow.js";
import { callFailure, mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

export interface TenderActivities {
  startTender(ref: TenderRef & { loadId: string; carriers: TenderCarrier[]; currency?: string; timeoutMin?: number; idempotencyKey: string }): Promise<TenderView>;
  getTender(ref: TenderRef & { tenderId: string }): Promise<TenderView>;
  sweepExpiredTenders(ref: TenderRef): Promise<void>;
}

const TENDER_OPEN = "TMS-0114";

export function createTenderActivities(baseUrl: string, signer: ServiceTokenSigner, fetchImpl: typeof fetch = fetch): TenderActivities {
  async function send(
    ref: TenderRef,
    label: string,
    path: string,
    init: { method: string; headers?: Record<string, string>; body?: unknown },
    passCodes: readonly string[] = [],
  ): Promise<Response> {
    const response = await fetchImpl(`${baseUrl}/v1/transport${path}`, {
      method: init.method,
      headers: {
        "X-Passage-Service-Token": mintOrReject(() => signer.mintForTransport({ tenantId: ref.tenantId, legalEntityId: ref.legalEntityId, actor: ref.actor, scope: ref.scope })),
        ...(init.body === undefined ? {} : { "Content-Type": "application/json" }),
        ...init.headers,
      },
      ...(init.body === undefined ? {} : { body: JSON.stringify(init.body) }),
      signal: Context.current().cancellationSignal,
    });
    if (!response.ok && !(passCodes.length > 0 && passCodes.includes((await problemCode(response)) ?? ""))) throw await callFailure(`transport ${label}`, response);
    return response;
  }

  // A load whose tender is already open (started by a planner, or by an earlier run of the workflow under another key:
  // a release event replayed after that run ended) answers TMS-0114; the workflow follows that tender instead of failing.
  async function adoptOpen(ref: TenderRef, loadId: string): Promise<TenderView> {
    const response = await send(ref, "tender lookup", `/tenders?loadId=${encodeURIComponent(loadId)}`, { method: "GET" });
    const open = ((await response.json()) as { items?: TenderView[] }).items?.find((tender) => tender.status === "PENDING" || tender.status === "ACCEPTED");
    if (!open) throw new Error(`transport refused the tender of load ${loadId} as open (${TENDER_OPEN}) but lists no open tender; retrying`);
    return open;
  }

  return {
    async startTender({ loadId, carriers, currency, timeoutMin, idempotencyKey, ...ref }) {
      const response = await send(
        ref,
        "tender start",
        `/loads/${encodeURIComponent(loadId)}:tender`,
        { method: "POST", headers: { "Idempotency-Key": idempotencyKey }, body: { carriers, ...(currency ? { currency } : {}), ...(timeoutMin ? { timeoutMin } : {}) } },
        [TENDER_OPEN],
      );
      if (!response.ok) return adoptOpen(ref, loadId);
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

async function problemCode(response: Response): Promise<string | undefined> {
  try {
    return ((await response.clone().json()) as { code?: string }).code;
  } catch {
    return undefined;
  }
}

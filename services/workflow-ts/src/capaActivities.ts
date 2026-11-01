import { Context } from "@temporalio/activity";
import type { CapaNcr, CapaRef, EffectivenessResult } from "./capaWorkflow.js";
import { callFailure, mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

export interface CapaActivities {
  startCorrection(ref: CapaRef & { effectivenessCheckDays?: number }): Promise<CapaNcr>;
  completeStep(ref: CapaRef & { step: number; notes: string }): Promise<CapaNcr>;
  checkEffectiveness(ref: CapaRef & { result: EffectivenessResult; note: string }): Promise<CapaNcr>;
}

const EFFECTIVENESS_NOT_DUE = "QM-0174";

export function createCapaActivities(baseUrl: string, signer: ServiceTokenSigner, fetchImpl: typeof fetch = fetch): CapaActivities {
  async function send(ref: CapaRef, path: string, init: { method: string; headers?: Record<string, string>; body?: unknown }, retryableCodes: string[] = []) {
    const response = await fetchImpl(`${baseUrl}/v1/quality/nonconformances/${encodeURIComponent(ref.ncrId)}${path}`, {
      method: init.method,
      headers: {
        "X-Passage-Service-Token": mintOrReject(() => signer.mintForQuality({ tenantId: ref.tenantId, legalEntityId: ref.legalEntityId, actor: ref.actor, scope: ref.scope })),
        ...(init.body === undefined ? {} : { "Content-Type": "application/json" }),
        ...init.headers,
      },
      ...(init.body === undefined ? {} : { body: JSON.stringify(init.body) }),
      signal: Context.current().cancellationSignal,
    });
    if (!response.ok) throw await callFailure(`quality nonconformance${path || " read"}`, response, retryableCodes);
    return response;
  }

  async function load(ref: CapaRef): Promise<{ ncr: CapaNcr; ifMatch: string }> {
    const response = await send(ref, "", { method: "GET" });
    return { ncr: (await response.json()) as CapaNcr, ifMatch: response.headers.get("ETag") ?? "" };
  }

  async function change(ref: CapaRef, path: string, body: unknown, ifMatch: string, key?: string): Promise<CapaNcr> {
    const response = await send(ref, path, { method: "POST", headers: { "If-Match": ifMatch, ...(key ? { "Idempotency-Key": key } : {}) }, body }, [EFFECTIVENESS_NOT_DUE]);
    return (await response.json()) as CapaNcr;
  }

  return {
    async startCorrection(ref) {
      const { ncr, ifMatch } = await load(ref);
      if (ncr.correction) return ncr;
      return change(ref, "/corrections", ref.effectivenessCheckDays ? { effectivenessCheckDays: ref.effectivenessCheckDays } : {}, ifMatch, `capa:${ref.ncrId}:start`);
    },
    async completeStep(ref) {
      const { ncr, ifMatch } = await load(ref);
      if (ncr.correction && ncr.correction.currentStep > ref.step) return ncr;
      return change(ref, ":complete-step", { step: ref.step, notes: ref.notes }, ifMatch);
    },
    async checkEffectiveness(ref) {
      const { ncr, ifMatch } = await load(ref);
      if (ncr.correction && ncr.correction.status !== "EFFECTIVENESS_PENDING") return ncr;
      return change(ref, ":check-effectiveness", { result: ref.result, note: ref.note }, ifMatch);
    },
  };
}

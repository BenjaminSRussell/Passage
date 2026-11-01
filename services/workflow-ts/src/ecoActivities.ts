import { Context } from "@temporalio/activity";
import type { EcoRef, EcoSnapshot } from "./ecoApprovalWorkflow.js";
import { callFailure } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

export interface EcoActivities {
  approveEco(ref: EcoRef): Promise<EcoSnapshot>;
  rejectEco(ref: EcoRef): Promise<EcoSnapshot>;
  cancelEco(ref: EcoRef): Promise<EcoSnapshot>;
  releaseEco(ref: EcoRef): Promise<EcoSnapshot>;
}

export function createEcoActivities(baseUrl: string, signer: ServiceTokenSigner, fetchImpl: typeof fetch = fetch): EcoActivities {
  const post =
    (action: "approve" | "reject" | "cancel" | "release") =>
    async (ref: EcoRef): Promise<EcoSnapshot> => {
      const response = await fetchImpl(`${baseUrl}/v1/engineering/ecos/${encodeURIComponent(ref.ecoId)}:${action}`, {
        method: "POST",
        headers: {
          "X-Passage-Service-Token": signer.mintForEngineering({ tenantId: ref.tenantId, legalEntityId: ref.legalEntityId, actor: ref.actor }),
          ...(ref.reason ? { "Content-Type": "application/json" } : {}),
        },
        ...(ref.reason ? { body: JSON.stringify({ reason: ref.reason }) } : {}),
        signal: Context.current().cancellationSignal,
      });
      if (!response.ok) throw await callFailure(`engineering eco ${action}`, response);
      return (await response.json()) as EcoSnapshot;
    };

  return { approveEco: post("approve"), rejectEco: post("reject"), cancelEco: post("cancel"), releaseEco: post("release") };
}

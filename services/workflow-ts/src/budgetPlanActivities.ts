import { Context } from "@temporalio/activity";
import type { BudgetPlanRejectRef, BudgetPlanSignOffRef, BudgetPlanSnapshot } from "./budgetPlanWorkflow.js";
import { callFailure, mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

export interface BudgetPlanActivities {
  signOffPlan(ref: BudgetPlanSignOffRef): Promise<BudgetPlanSnapshot>;
  rejectPlan(ref: BudgetPlanRejectRef): Promise<BudgetPlanSnapshot>;
}

/**
 * The idempotency key names the decision, so a retry after a lost reply replays budgeting's stored answer instead of
 * signing twice, and a different person's decision on the same stage is a different key.
 */
export function signOffKey(ref: BudgetPlanSignOffRef): string {
  return ["budget-plan", ref.planId, "sign-off", ref.stage, ref.orgUnitId, ref.submission, ref.actor].join(":");
}

export function rejectKey(ref: BudgetPlanRejectRef): string {
  return ["budget-plan", ref.planId, "reject", ref.submission, ref.actor].join(":");
}

export function createBudgetPlanActivities(baseUrl: string, signer: ServiceTokenSigner, fetchImpl: typeof fetch = fetch): BudgetPlanActivities {
  const call = async (
    label: string,
    path: string,
    ref: BudgetPlanSignOffRef | BudgetPlanRejectRef,
    key: string,
    body: Record<string, unknown>,
  ): Promise<BudgetPlanSnapshot> => {
    const response = await fetchImpl(`${baseUrl}/v1/budget/plans/${encodeURIComponent(ref.planId)}${path}`, {
      method: "POST",
      headers: {
        "X-Passage-Service-Token": mintOrReject(() =>
          signer.mintForBudgeting({
            tenantId: ref.tenantId,
            legalEntityId: ref.legalEntityId,
            actor: ref.actor,
            scope: ref.scope,
          }),
        ),
        "Content-Type": "application/json",
        "Idempotency-Key": key,
      },
      body: JSON.stringify(body),
      signal: Context.current().cancellationSignal,
    });
    if (!response.ok) throw await callFailure(`budgeting plan ${label}`, response);
    return (await response.json()) as BudgetPlanSnapshot;
  };

  return {
    signOffPlan: (ref) =>
      call("sign-off", `/stages/${ref.stage}:sign-off`, ref, signOffKey(ref), {
        orgUnitId: ref.orgUnitId,
        submission: ref.submission,
        ...(ref.scenarioId ? { scenarioId: ref.scenarioId } : {}),
      }),
    rejectPlan: (ref) =>
      call("reject", ":reject", ref, rejectKey(ref), {
        reason: ref.reason,
        submission: ref.submission,
      }),
  };
}

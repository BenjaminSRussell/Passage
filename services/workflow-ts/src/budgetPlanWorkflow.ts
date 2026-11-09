import { ActivityFailure, condition, defineQuery, defineSignal, metricMeter, proxyActivities, setHandler } from "@temporalio/workflow";
import type { BudgetPlanActivities } from "./budgetPlanActivities.js";

export interface BudgetPlanInput {
  tenantId: string;
  legalEntityId: string;
  planId: string;
  submission: number;
  requester: string;
}

export type BudgetPlanStage = "review" | "approve";

export interface BudgetPlanRef {
  tenantId: string;
  legalEntityId: string;
  planId: string;
  submission: number;
  actor: string;
  scope: string[];
}

export interface BudgetPlanSignOffRef extends BudgetPlanRef {
  stage: BudgetPlanStage;
  orgUnitId: string;
  scenarioId?: string;
}

export interface BudgetPlanRejectRef extends BudgetPlanRef {
  reason: string;
}

export interface BudgetPlanSnapshot {
  id: string;
  status: "Draft" | "Submitted" | "Reviewed" | "Approved";
  submission: number;
  approvedScenarioId?: string;
  registerEntryId?: string;
}

export type BudgetPlanDecision =
  | {
      decision: "sign-off";
      stage: BudgetPlanStage;
      orgUnitId: string;
      scenarioId?: string;
      actor: string;
      scope: string[];
    }
  | { decision: "reject"; reason: string; actor: string; scope: string[] };

export interface BudgetPlanState {
  planId: string;
  tenantId: string;
  legalEntityId: string;
  submission: number;
  requester: string;
  status: "awaiting_review" | "awaiting_approval" | "approved" | "rejected";
  refusedDecisions: number;
  approvedScenarioId?: string;
  registerEntryId?: string;
  decidedBy?: string;
}

export const budgetPlanDecisionSignal = defineSignal<[decision: BudgetPlanDecision]>("budget-plan-decision");
export const budgetPlanStateQuery = defineQuery<BudgetPlanState>("budget-plan-state");

const activities = proxyActivities<BudgetPlanActivities>({
  startToCloseTimeout: "1 minute",
  scheduleToCloseTimeout: "1 hour",
  retry: {
    initialInterval: "1 second",
    maximumInterval: "1 minute",
    nonRetryableErrorTypes: ["ServiceCallRejected"],
  },
});

/** One run per submission: a plan sent back and submitted again is a new workflow, and a signal for the old round finds none. */
export function budgetPlanWorkflowId(planId: string, submission: number): string {
  return `budget-plan:${planId}:${submission}`;
}

export function validateBudgetPlanInput(input: BudgetPlanInput): void {
  if (!input.tenantId || !input.legalEntityId || !input.planId || !input.requester) {
    throw new TypeError("tenantId, legalEntityId, planId and requester are required");
  }
  if (!Number.isInteger(input.submission) || input.submission < 1) throw new TypeError("submission must be a positive integer");
}

const STATUS = {
  Submitted: "awaiting_review",
  Reviewed: "awaiting_approval",
  Approved: "approved",
  Draft: "rejected",
} as const;

/**
 * Carries one submission of a budget plan through its review and approval stages. Every decision is a signal from the
 * decision route (src/decisionApi.ts) with the verified actor; budgeting alone judges it (stage owner, segregation of
 * duties, submission), so a refused decision is counted and the workflow keeps waiting. It ends when budgeting reports the
 * plan Approved (the approved scenario is then the budget register) or back in Draft after a rejection.
 */
export async function budgetPlanWorkflow(input: BudgetPlanInput): Promise<BudgetPlanState> {
  validateBudgetPlanInput(input);

  const state: BudgetPlanState = {
    planId: input.planId,
    tenantId: input.tenantId,
    legalEntityId: input.legalEntityId,
    submission: input.submission,
    requester: input.requester,
    status: "awaiting_review",
    refusedDecisions: 0,
  };
  const decisions: BudgetPlanDecision[] = [];
  setHandler(budgetPlanStateQuery, () => ({ ...state }));
  setHandler(budgetPlanDecisionSignal, (decision) => void decisions.push(decision));

  const ref = (actor: string, scope: string[]): BudgetPlanRef => ({
    tenantId: input.tenantId,
    legalEntityId: input.legalEntityId,
    planId: input.planId,
    submission: input.submission,
    actor,
    scope,
  });

  for (;;) {
    await condition(() => decisions.length > 0);
    const decision = decisions.shift()!;
    try {
      const plan =
        decision.decision === "reject"
          ? await activities.rejectPlan({
              ...ref(decision.actor, decision.scope),
              reason: decision.reason,
            })
          : await activities.signOffPlan({
              ...ref(decision.actor, decision.scope),
              stage: decision.stage,
              orgUnitId: decision.orgUnitId,
              ...(decision.scenarioId ? { scenarioId: decision.scenarioId } : {}),
            });
      state.status = STATUS[plan.status];
      state.decidedBy = decision.actor;
      if (plan.approvedScenarioId && plan.status === "Approved") state.approvedScenarioId = plan.approvedScenarioId;
      if (plan.registerEntryId) state.registerEntryId = plan.registerEntryId;
      if (state.status === "approved" || state.status === "rejected") {
        metricMeter.createCounter("passage_budget_plan_decided", "1", "Budget plan submissions that ended").add(1, { outcome: state.status });
        return { ...state };
      }
    } catch (error) {
      if (!(error instanceof ActivityFailure)) throw error;
      state.refusedDecisions += 1;
    }
  }
}

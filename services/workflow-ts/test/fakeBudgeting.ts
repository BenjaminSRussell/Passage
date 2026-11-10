import { decodeClaims, problem, type ServiceClaims } from "./fakePayables.js";

type Action = "sign-off" | "reject";
type Hook = (signal: AbortSignal | null | undefined) => Promise<void>;

export const PLAN_ID = "00000000-0000-0000-0000-0000000000b1";
export const SCENARIO_ID = "00000000-0000-0000-0000-0000000000b2";
export const UNIT_A = "00000000-0000-0000-0000-0000000000c1";
export const UNIT_B = "00000000-0000-0000-0000-0000000000c2";
export const UNIT_APPROVE = "00000000-0000-0000-0000-0000000000c3";

export interface FakeOwner {
  stage: "review" | "approve";
  orgUnitId: string;
  owner: string;
  signedBy?: string;
}

export interface FakePlan {
  id: string;
  status: "Draft" | "Submitted" | "Reviewed" | "Approved";
  submission: number;
  submittedBy: string;
  approvedScenarioId?: string;
  registerEntryId?: string;
}

/** Mirrors the rules budgeting enforces on a sign-off and a reject (services/budgeting/internal/store/plan.go). */
export class FakeBudgeting {
  plan: FakePlan = {
    id: PLAN_ID,
    status: "Submitted",
    submission: 1,
    submittedBy: "pat",
  };
  owners: FakeOwner[] = [
    { stage: "review", orgUnitId: UNIT_A, owner: "rev-a" },
    { stage: "review", orgUnitId: UNIT_B, owner: "rev-b" },
    { stage: "approve", orgUnitId: UNIT_APPROVE, owner: "boss" },
  ];
  readonly calls: Record<Action, number> = { "sign-off": 0, reject: 0 };
  readonly keys: string[] = [];
  readonly tokens: ServiceClaims[] = [];
  effects = 0;
  private readonly replies = new Map<string, Response>();
  private readonly refusals = new Map<Action, { status: number; code: string; times: number }>();
  private readonly afterEffect = new Map<Action, Hook>();

  readonly fetch: typeof fetch = async (input, init) => {
    const claims = decodeClaims(new Headers(init?.headers).get("X-Passage-Service-Token"));
    if (!claims || claims.iss !== "workflow-ts" || claims.aud !== "budgeting" || claims.duties.join() !== "budget.plan.workflow" || !claims.act) {
      return problem(401, "missing or wrong service token");
    }
    this.tokens.push(claims);
    const key = new Headers(init?.headers).get("Idempotency-Key") ?? "";
    this.keys.push(key);
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const signOff = /^\/v1\/budget\/plans\/([^/]+)\/stages\/(review|approve):sign-off$/.exec(url.pathname);
    const reject = /^\/v1\/budget\/plans\/([^/:]+):reject$/.exec(url.pathname);
    const action: Action | undefined = signOff ? "sign-off" : reject ? "reject" : undefined;
    if (!action) return problem(404, "unknown route");
    this.calls[action] += 1;
    const refusal = this.refusals.get(action);
    if (refusal && refusal.times > 0) {
      refusal.times -= 1;
      return problem(refusal.status, `refused ${action}`, refusal.code);
    }
    const replay = this.replies.get(key);
    if (replay) return replay.clone();
    const body = JSON.parse(String(init?.body)) as {
      orgUnitId?: string;
      scenarioId?: string;
      submission: number;
      reason?: string;
    };
    const response = signOff ? this.signOff(claims.act, signOff[2] as "review" | "approve", body) : this.reject(claims.act, body);
    if (response.ok) {
      this.replies.set(key, response.clone());
      await this.afterEffect.get(action)?.(init?.signal);
    }
    return response;
  };

  refuse(action: Action, status: number, code: string, times = Number.POSITIVE_INFINITY): void {
    this.refusals.set(action, { status, code, times });
  }

  after(action: Action, hook: Hook): void {
    this.afterEffect.set(action, hook);
  }

  private signOff(actor: string, stage: "review" | "approve", body: { orgUnitId?: string; scenarioId?: string; submission: number }): Response {
    const plan = this.plan;
    if (plan.submission !== body.submission) return problem(409, "stale submission", "BUD-0046");
    if (plan.status !== (stage === "review" ? "Submitted" : "Reviewed")) return problem(409, "stage not open", "BUD-0047");
    const owner = this.owners.find((o) => o.stage === stage && o.orgUnitId === body.orgUnitId);
    if (!owner || owner.owner !== actor) return problem(403, "not the stage owner", "BUD-0043");
    if (actor === plan.submittedBy) return problem(403, "the submitter cannot sign", "BUD-0044");
    if (stage === "approve" && this.owners.some((o) => o.stage === "review" && o.signedBy === actor))
      return problem(403, "the reviewer cannot approve", "BUD-0044");
    if (stage === "approve" && plan.approvedScenarioId && plan.approvedScenarioId !== body.scenarioId) return problem(409, "another scenario", "BUD-0048");
    if (!owner.signedBy) {
      owner.signedBy = actor;
      if (stage === "approve") plan.approvedScenarioId = body.scenarioId;
    }
    if (this.owners.filter((o) => o.stage === stage).every((o) => o.signedBy)) {
      if (stage === "review") plan.status = "Reviewed";
      else {
        plan.status = "Approved";
        plan.registerEntryId = "00000000-0000-0000-0000-0000000000d1";
        this.effects += 1;
      }
    }
    return ok(plan);
  }

  private reject(actor: string, body: { submission: number }): Response {
    const plan = this.plan;
    if (plan.submission !== body.submission) return problem(409, "stale submission", "BUD-0046");
    const stage = plan.status === "Submitted" ? "review" : plan.status === "Reviewed" ? "approve" : undefined;
    if (!stage) return problem(409, "no open stage", "BUD-0047");
    if (!this.owners.some((o) => o.stage === stage && o.owner === actor)) return problem(403, "not the stage owner", "BUD-0043");
    for (const o of this.owners) delete o.signedBy;
    plan.status = "Draft";
    delete plan.approvedScenarioId;
    return ok(plan);
  }
}

function ok(plan: FakePlan): Response {
  return new Response(JSON.stringify(plan), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

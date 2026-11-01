import { decodeClaims, problem, type ServiceClaims } from "./fakePayables.js";

type Action = "read" | "start" | "complete-step" | "check-effectiveness";
type Hook = (signal: AbortSignal | null | undefined) => Promise<void>;

interface Step {
  step: number;
  completedBy?: string;
  notes?: string;
}

export interface FakeNcr {
  id: string;
  status: "APPROVED" | "IN_CORRECTION" | "CLOSED";
  version: number;
  correction?: {
    status: "OPEN" | "EFFECTIVENESS_PENDING" | "CLOSED";
    currentStep: number;
    effectivenessDays: number;
    effectivenessDueAt?: string;
    effectivenessResult?: string;
    reopened: number;
    steps: Step[];
  };
}

export const NCR_ID = "00000000-0000-0000-0000-0000000000c1";
const DAY_MS = 24 * 60 * 60 * 1000;

export class FakeQuality {
  ncr: FakeNcr = { id: NCR_ID, status: "APPROVED", version: 2 };
  readonly calls: Record<Action, number> = { read: 0, start: 0, "complete-step": 0, "check-effectiveness": 0 };
  readonly effects = { corrections: 0, stepWrites: 0, effectivenessChecks: 0 };
  readonly tokens: ServiceClaims[] = [];
  private readonly refusals = new Map<Action, { status: number; code: string; times: number }>();
  private readonly afterEffect = new Map<Action, Hook>();
  private readonly startKeys = new Set<string>();
  private bumpBeforeNext = false;

  readonly fetch: typeof fetch = async (input, init) => {
    const claims = decodeClaims(new Headers(init?.headers).get("X-Passage-Service-Token"));
    if (!claims || claims.iss !== "workflow-ts" || claims.aud !== "quality" || claims.duties.join() !== "quality.capa.execute" || !claims.act) {
      return problem(401, "missing or wrong service token");
    }
    this.tokens.push(claims);
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const [, id, suffix] = /^\/v1\/quality\/nonconformances\/([^/:]+)(.*)$/.exec(url.pathname) ?? [];
    const action = ({ "": "read", "/corrections": "start", ":complete-step": "complete-step", ":check-effectiveness": "check-effectiveness" } as Record<string, Action>)[suffix ?? "?"];
    if (!action) return problem(404, "unknown route");
    this.calls[action] += 1;
    const refusal = this.refusals.get(action);
    if (refusal && refusal.times > 0) {
      refusal.times -= 1;
      return problem(refusal.status, `refused ${action}`, refusal.code);
    }
    if (id !== this.ncr.id) return problem(404, "nonconformance not found", "QM-0170");
    const headers = new Headers(init?.headers);
    const body = init?.body ? (JSON.parse(String(init.body)) as Record<string, unknown>) : {};
    if (action !== "read") {
      if (this.bumpBeforeNext) {
        this.bumpBeforeNext = false;
        this.ncr.version += 1;
      }
      if (headers.get("If-Match") !== `"${this.ncr.version}"`) return problem(412, "stale If-Match", "COM-0412");
    }
    const response = this.apply(action, claims.act, body, headers.get("Idempotency-Key"));
    if (response.ok) await this.afterEffect.get(action)?.(init?.signal);
    return response;
  };

  refuse(action: Action, status: number, code: string, times = Number.POSITIVE_INFINITY): void {
    this.refusals.set(action, { status, code, times });
  }

  after(action: Action, hook: Hook): void {
    this.afterEffect.set(action, hook);
  }

  staleOnce(): void {
    this.bumpBeforeNext = true;
  }

  private apply(action: Action, actor: string, body: Record<string, unknown>, key: string | null): Response {
    const ncr = this.ncr;
    const correction = ncr.correction;
    if (action === "read") return reply(ncr);
    if (action === "start") {
      if (ncr.status !== "APPROVED" || !key || this.startKeys.has(key)) return problem(409, "no approved NCR to correct", "QM-0171");
      this.startKeys.add(key);
      const days = Number(body.effectivenessCheckDays ?? 90);
      ncr.correction = { status: "OPEN", currentStep: 1, effectivenessDays: days, reopened: 0, steps: Array.from({ length: 8 }, (_, i) => ({ step: i + 1 })) };
      ncr.status = "IN_CORRECTION";
      ncr.version += 1;
      this.effects.corrections += 1;
      return reply(ncr, 201);
    }
    if (!correction) return problem(409, "no open correction", "QM-0173");
    if (action === "complete-step") {
      if (correction.status !== "OPEN" || body.step !== correction.currentStep) return problem(409, "not the current step", "QM-0173");
      Object.assign(correction.steps[correction.currentStep - 1]!, { completedBy: actor, notes: String(body.notes) });
      correction.currentStep += 1;
      if (body.step === 7) {
        correction.status = "EFFECTIVENESS_PENDING";
        correction.effectivenessDueAt = new Date(Date.now() + correction.effectivenessDays * DAY_MS).toISOString();
      }
      ncr.version += 1;
      this.effects.stepWrites += 1;
      return reply(ncr);
    }
    if (correction.status !== "EFFECTIVENESS_PENDING") return problem(409, "the effectiveness check follows D7", "QM-0173");
    if (actor === correction.steps[6]!.completedBy) return problem(422, "checker completed D7", "QM-0175");
    this.effects.effectivenessChecks += 1;
    correction.effectivenessResult = String(body.result);
    if (body.result === "EFFECTIVE") {
      correction.status = "CLOSED";
      ncr.status = "CLOSED";
    } else {
      for (const s of correction.steps.slice(3, 7)) Object.assign(s, { completedBy: undefined, notes: undefined });
      Object.assign(correction, { status: "OPEN", currentStep: 4, effectivenessDueAt: undefined, reopened: correction.reopened + 1 });
    }
    ncr.version += 1;
    return reply(ncr);
  }
}

function reply(ncr: FakeNcr, status = 200): Response {
  return new Response(JSON.stringify(ncr), { status, headers: { "Content-Type": "application/json", ETag: `"${ncr.version}"` } });
}

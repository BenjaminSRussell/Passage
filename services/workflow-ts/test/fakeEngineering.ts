import { decodeClaims, problem, type ServiceClaims } from "./fakePayables.js";

type Action = "approve" | "reject" | "cancel" | "release";
type Hook = (signal: AbortSignal | null | undefined) => Promise<void>;

export interface FakeEco {
  id: string;
  number: string;
  state: "submitted" | "approved" | "scheduled" | "released" | "rejected" | "cancelled";
  releaseDate?: string;
  decidedBy?: string;
  createdBy?: string;
  decisionReason?: string;
}

export const ECO_ID = "00000000-0000-0000-0000-0000000000e1";

export class FakeEngineering {
  eco: FakeEco = { id: ECO_ID, number: "ECO-1", state: "submitted" };
  oldVersion: "active" | "expired" = "active";
  newVersion: "draft" | "active" = "draft";
  readonly calls: Record<Action, number> = { approve: 0, reject: 0, cancel: 0, release: 0 };
  readonly tokens: ServiceClaims[] = [];
  private readonly refusals = new Map<Action, { status: number; code: string; times: number }>();
  private readonly afterEffect = new Map<Action, Hook>();

  readonly fetch: typeof fetch = async (input, init) => {
    const claims = decodeClaims(new Headers(init?.headers).get("X-Passage-Service-Token"));
    if (!claims || claims.iss !== "workflow-ts" || claims.aud !== "engineering" || claims.duties.join() !== "engineering.eco.workflow" || !claims.act) {
      return problem(401, "missing or wrong service token");
    }
    this.tokens.push(claims);
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const [, id, action] = /^\/v1\/engineering\/ecos\/([^:]+):(approve|reject|cancel|release)$/.exec(url.pathname) ?? [];
    if (!action) return problem(404, "unknown route");
    const name = action as Action;
    this.calls[name] += 1;
    const refusal = this.refusals.get(name);
    if (refusal && refusal.times > 0) {
      refusal.times -= 1;
      return problem(refusal.status, `refused ${name}`, refusal.code);
    }
    if (id !== this.eco.id) return problem(404, "eco not found", "ENG-ECO-NOTFOUND");
    const reason = init?.body ? (JSON.parse(String(init.body)) as { reason?: string }).reason : undefined;
    const response = this.apply(name, claims.act, reason);
    if (response.ok) await this.afterEffect.get(name)?.(init?.signal);
    return response;
  };

  refuse(action: Action, status: number, code: string, times = Number.POSITIVE_INFINITY): void {
    this.refusals.set(action, { status, code, times });
  }

  after(action: Action, hook: Hook): void {
    this.afterEffect.set(action, hook);
  }

  private apply(action: Action, actor: string, reason?: string): Response {
    const eco = this.eco;
    if (action === "release") return this.release();
    const target = ({ approve: "approved", reject: "rejected", cancel: "cancelled" } as const)[action];
    if (eco.state === target) return ok(eco);
    if (eco.state !== "submitted") return problem(409, `ECO is ${eco.state}`, "ENG-ECO-STATE");
    if (actor === eco.createdBy) return problem(403, "the creator cannot decide", "ENG-ECO-SELF-APPROVAL");
    eco.state = target;
    eco.decidedBy = actor;
    eco.decisionReason = reason;
    return ok(eco);
  }

  private release(): Response {
    const eco = this.eco;
    if (eco.state === "released") return ok(eco);
    if (eco.state === "submitted") return problem(503, "approval service unavailable", "ENG-ECO-APPROVER-UNAVAILABLE");
    if (eco.state !== "approved" && eco.state !== "scheduled") return problem(422, `ECO ${eco.number} must be approved before release.`, "ENG-ECO-NOT-APPROVED");
    if (eco.state === "approved" && eco.releaseDate) {
      eco.state = "scheduled";
      return ok(eco);
    }
    eco.state = "released";
    this.oldVersion = "expired";
    this.newVersion = "active";
    return ok(eco);
  }
}

function ok(eco: FakeEco): Response {
  return new Response(JSON.stringify(eco), { status: 200, headers: { "Content-Type": "application/json" } });
}

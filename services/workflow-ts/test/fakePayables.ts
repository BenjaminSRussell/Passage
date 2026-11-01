const GROUPS = 2;
const ITEMS = 3;

type Action = "start" | "generate-file" | "post-journal" | "settle" | "complete" | "fail";
type Hook = (signal: AbortSignal | null | undefined) => Promise<void>;

interface Run {
  id: string;
  status: "RUNNING" | "COMPLETED" | "FAILED";
  fileGenerated: boolean;
  journalEntryId?: string;
  settled: boolean;
  failureReason?: string;
}

export interface Effects {
  runs: number;
  files: number;
  payments: number;
  ledgerEntries: number;
  settleCounts: number[];
  proposal: "APPROVED" | "EXECUTING" | "EXECUTED" | "FAILED";
  reservedLines: number;
  calls: Record<Action, number>;
}

export class FakePayables {
  private run?: Run;
  private files = 0;
  private payments = 0;
  private readonly ledger = new Set<string>();
  private readonly settleCounts = new Array<number>(ITEMS).fill(0);
  private proposal: Effects["proposal"] = "APPROVED";
  private reservedLines = ITEMS;
  private readonly calls: Effects["calls"] = { start: 0, "generate-file": 0, "post-journal": 0, settle: 0, complete: 0, fail: 0 };
  private readonly afterEffect = new Map<Action, Hook>();
  private readonly refusals = new Map<Action, { status: number; times: number }>();
  startsFailed = false;

  readonly tokens: ServiceClaims[] = [];

  readonly fetch: typeof fetch = async (input, init) => {
    const claims = decodeClaims(new Headers(init?.headers).get("X-Passage-Service-Token"));
    if (!claims || claims.iss !== "workflow-ts" || claims.aud !== "payables" || claims.duties.join() !== "payment-run.execute" || !claims.act) {
      return problem(401, "missing or wrong service token");
    }
    this.tokens.push(claims);
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const [, runId, action] = /^\/v1\/payables\/payment-runs(?:\/([^:]+):(.+))?$/.exec(url.pathname) ?? [];
    const name = (action ?? "start") as Action;
    this.calls[name] += 1;
    const refusal = this.refusals.get(name);
    if (refusal && refusal.times > 0) {
      refusal.times -= 1;
      return problem(refusal.status, `refused ${name}`);
    }
    const response = this.apply(name, runId, init?.body ? (JSON.parse(String(init.body)) as { reason?: string }) : {});
    if (response.ok) await this.afterEffect.get(name)?.(init?.signal);
    return response;
  };

  refuse(action: Action, status: number, times = Number.POSITIVE_INFINITY): void {
    this.refusals.set(action, { status, times });
  }

  after(action: Action, hook: Hook): void {
    this.afterEffect.set(action, hook);
  }

  effects(): Effects {
    return {
      runs: this.run ? 1 : 0,
      files: this.files,
      payments: this.payments,
      ledgerEntries: this.ledger.size,
      settleCounts: [...this.settleCounts],
      proposal: this.proposal,
      reservedLines: this.reservedLines,
      calls: { ...this.calls },
    };
  }

  private apply(action: Action, runId: string | undefined, body: { reason?: string }): Response {
    if (action === "start") return this.start();
    const run = this.run;
    if (!run || run.id !== runId) return problem(404, "payment run not found");
    switch (action) {
      case "generate-file":
        if (run.status === "FAILED") return problem(409, "payment run is no longer running");
        if (!run.fileGenerated) {
          run.fileGenerated = true;
          this.files += 1;
          this.payments += GROUPS;
        }
        return json(run);
      case "post-journal":
        if (run.status !== "RUNNING" && !run.journalEntryId) return problem(409, "payment run is no longer running");
        if (!run.fileGenerated) return problem(409, "payment run step needs an earlier step to finish first");
        if (!run.journalEntryId) {
          this.ledger.add(`payables:payment-run:${run.id}`);
          run.journalEntryId = "entry-1";
        }
        return json(run);
      case "settle":
        if (!run.journalEntryId) return problem(409, "payment run step needs an earlier step to finish first");
        if (!run.settled) {
          this.settleCounts.forEach((_, i) => (this.settleCounts[i] = (this.settleCounts[i] ?? 0) + 1));
          run.settled = true;
        }
        return json(run);
      case "complete":
        if (!run.settled) return problem(409, "payment run step needs an earlier step to finish first");
        run.status = "COMPLETED";
        this.proposal = "EXECUTED";
        return json(run);
      case "fail":
        if (run.status === "FAILED") return json(run);
        if (run.journalEntryId) return problem(409, "the payment journal is posted; the run can only complete");
        run.status = "FAILED";
        run.failureReason = body.reason;
        this.proposal = "FAILED";
        this.reservedLines = 0;
        return json(run);
      default:
        return problem(404, "unknown action");
    }
  }

  private start(): Response {
    if (!this.run) {
      this.run = { id: "00000000-0000-0000-0000-0000000000r1", status: "RUNNING", fileGenerated: false, settled: false };
      this.proposal = "EXECUTING";
      if (this.startsFailed) {
        this.run.status = "FAILED";
        this.run.failureReason = "open items changed";
        this.proposal = "FAILED";
        this.reservedLines = 0;
      }
    }
    return json(this.run, 201);
  }
}

function json(run: Run, status = 200): Response {
  return new Response(JSON.stringify({ ...run }), { status, headers: { "Content-Type": "application/json" } });
}

export function problem(status: number, detail: string, code?: string): Response {
  return new Response(JSON.stringify({ status, detail, ...(code ? { code } : {}) }), { status, headers: { "Content-Type": "application/problem+json" } });
}

export interface ServiceClaims {
  iss: string;
  aud: string;
  tid: string;
  le: string;
  act: string;
  duties: string[];
  iat: number;
  exp: number;
  jti: string;
}

export function decodeClaims(token: string | null): ServiceClaims | undefined {
  const body = token?.split(".")[0];
  if (!body) return undefined;
  return JSON.parse(Buffer.from(body, "base64url").toString("utf8")) as ServiceClaims;
}

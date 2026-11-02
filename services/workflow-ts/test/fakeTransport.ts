import { problem } from "./fakePayables.js";

type Response0 = "PENDING" | "ACCEPTED" | "DECLINED" | "EXPIRED";

interface Attempt {
  rank: number;
  carrierId: string;
  expiresAt: number;
  response: Response0;
}

export const LOAD_ID = "00000000-0000-0000-0000-0000000000d1";
export const TENDER_ID = "00000000-0000-0000-0000-0000000000e1";
export const CARRIERS = ["00000000-0000-0000-0000-0000000000f1", "00000000-0000-0000-0000-0000000000f2", "00000000-0000-0000-0000-0000000000f3"];

export class FakeTransport {
  status: "PENDING" | "ACCEPTED" | "TENDER_FAILED" = "PENDING";
  attempts: Attempt[] = [];
  carriers: string[] = [];
  timeoutMs = 0;
  starts = 0;
  sweeps = 0;
  readonly identities: Record<string, string | null>[] = [];
  skewMs = 0;
  frozenSweeps = false;
  private startKey?: string;

  private now(): number {
    return Date.now() + this.skewMs;
  }

  readonly fetch: typeof fetch = async (input, init) => {
    const headers = new Headers(init?.headers);
    if (headers.get("X-Passage-Duties") !== "TMS_TENDER" || !headers.get("X-Passage-Tenant") || !headers.get("X-Passage-User")) return problem(401, "missing identity", "AUTH-0001");
    this.identities.push({ tenant: headers.get("X-Passage-Tenant"), legalEntity: headers.get("X-Passage-Legal-Entity"), user: headers.get("X-Passage-User") });
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url);
    const method = init?.method ?? "GET";

    if (method === "POST" && url.pathname === `/v1/transport/loads/${LOAD_ID}:tender`) {
      const key = headers.get("Idempotency-Key");
      if (this.startKey && this.startKey !== key) return problem(409, "load already has an open tender", "TMS-0114");
      const body = JSON.parse(String(init?.body)) as { carriers: { carrierId: string }[]; timeoutMin?: number };
      if (!this.startKey) {
        this.startKey = key ?? "";
        this.starts += 1;
        this.carriers = body.carriers.map((carrier) => carrier.carrierId);
        this.timeoutMs = (body.timeoutMin ?? 120) * 60_000;
        this.offerNext(this.now());
      }
      return this.reply(201);
    }
    if (method === "POST" && url.pathname === "/v1/transport/tenders:expire") {
      this.sweeps += 1;
      if (!this.frozenSweeps) this.tick(this.now());
      return new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (method === "GET" && url.pathname === `/v1/transport/tenders/${TENDER_ID}`) return this.reply(200);
    return problem(404, "unknown route");
  };

  respond(carrierId: string, accept: boolean): void {
    this.tick(this.now());
    const pending = this.attempts.at(-1);
    if (this.status !== "PENDING" || pending?.carrierId !== carrierId) throw new Error("carrier is not the pending recipient");
    if (accept) {
      pending.response = "ACCEPTED";
      this.status = "ACCEPTED";
      return;
    }
    pending.response = "DECLINED";
    this.offerNext(this.now());
  }

  private tick(now: number): void {
    for (let pending = this.attempts.at(-1); this.status === "PENDING" && pending?.response === "PENDING" && now >= pending.expiresAt; pending = this.attempts.at(-1)) {
      pending.response = "EXPIRED";
      this.offerNext(pending.expiresAt);
    }
  }

  private offerNext(at: number): void {
    const carrierId = this.carriers[this.attempts.length];
    if (!carrierId) {
      this.status = "TENDER_FAILED";
      return;
    }
    this.attempts.push({ rank: this.attempts.length + 1, carrierId, expiresAt: at + this.timeoutMs, response: "PENDING" });
  }

  private reply(status: number): Response {
    const pending = this.status === "PENDING" ? this.attempts.at(-1) : undefined;
    const view = {
      id: TENDER_ID,
      loadId: LOAD_ID,
      status: this.status,
      attempts: this.attempts.map((attempt) => ({ rank: attempt.rank, carrierId: attempt.carrierId, response: attempt.response })),
      acceptedCarrierId: this.attempts.find((attempt) => attempt.response === "ACCEPTED")?.carrierId,
      plannerTask: this.status === "TENDER_FAILED",
      pendingCarrierId: pending?.carrierId,
      pendingExpiresAt: pending ? new Date(pending.expiresAt).toISOString() : undefined,
      overdue: !!pending && this.now() >= pending.expiresAt,
    };
    return new Response(JSON.stringify(view), { status, headers: { "Content-Type": "application/json" } });
  }
}

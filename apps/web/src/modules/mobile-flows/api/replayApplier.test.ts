import { afterEach, describe, expect, it, vi } from "vitest";
import type { QueuedTx } from "../domain/offlineQueue";
import { makeApplier, outcomeFromReplay, type ReplayStop } from "./replayApply";

const tx = (payload: Record<string, unknown> = {}): QueuedTx => ({
  idempotencyKey: "d1:7",
  deviceId: "d1",
  seq: 7,
  command: "ConfirmWorkLine",
  payload,
  capturedAt: Date.UTC(2026, 10, 10, 9, 30, 0),
  replayStatus: "PENDING",
});

const problem = (status: number, code: string, detail = "x") =>
  vi.fn(async () => new Response(JSON.stringify({ code, detail }), { status, headers: { "Content-Type": "application/problem+json" } }));

afterEach(() => vi.unstubAllGlobals());

describe("replay body", () => {
  it("sends the capture time as RFC 3339, the queuing user and the session", async () => {
    const fetcher = vi.fn(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    const apply = makeApplier({ userId: "signed-in", sessionId: () => "sess-1" });
    expect(await apply(tx({ userId: "queued-by" }))).toEqual({ status: "APPLIED" });
    const [, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBe("d1:7");
    expect(JSON.parse(String(init.body))).toMatchObject({
      idempotencyKey: "d1:7",
      command: "ConfirmWorkLine",
      capturedAt: "2026-11-10T09:30:00.000Z",
      userId: "queued-by",
      sessionId: "sess-1",
    });
  });

  it("falls back to the signed-in user and leaves the session out when there is none", async () => {
    const fetcher = vi.fn(async () => new Response("{}", { status: 200 }));
    vi.stubGlobal("fetch", fetcher);
    await makeApplier({ userId: "signed-in" })(tx());
    const [, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    const body = JSON.parse(String(init.body));
    expect(body.userId).toBe("signed-in");
    expect(body).not.toHaveProperty("sessionId");
  });
});

describe("replay answers", () => {
  it("pauses with a reason, rejecting nothing, when another user queued the entry (MOB-NOT-OWNER)", async () => {
    vi.stubGlobal("fetch", problem(403, "MOB-NOT-OWNER"));
    const stops: ReplayStop[] = [];
    await expect(makeApplier({ onStop: (s) => stops.push(s) })(tx())).rejects.toThrow("queued by another user");
    expect(stops).toEqual([{ code: "MOB-NOT-OWNER", message: expect.stringContaining("another user") }]);
  });

  it("pauses on a sequence gap (MOB-0108) and on clock skew (MOB-0114)", async () => {
    expect(outcomeFromReplay(422, { code: "MOB-0108" })).toMatchObject({ code: "MOB-0108" });
    expect(outcomeFromReplay(503, { code: "MOB-0114" })).toMatchObject({ code: "MOB-0114", message: expect.stringContaining("clock") });
    vi.stubGlobal("fetch", problem(503, "MOB-0114"));
    await expect(makeApplier()(tx())).rejects.toThrow("clock");
  });

  it("carries the server's reason for a conflict and a rejection", async () => {
    vi.stubGlobal("fetch", problem(409, "MOB-0107", "line was CANCELLED by alice"));
    expect(await makeApplier()(tx())).toEqual({ status: "CONFLICT", reason: "line was CANCELLED by alice" });
    vi.stubGlobal("fetch", problem(422, "MOB-REJECTED", "AUTH-0003: missing duty"));
    expect(await makeApplier()(tx())).toEqual({ status: "REJECTED", reason: "AUTH-0003: missing duty" });
  });

  it("leaves the transaction pending behind an unresolved one (429) and when the service is down", async () => {
    for (const status of [429, 503]) {
      vi.stubGlobal("fetch", problem(status, "X"));
      await expect(makeApplier()(tx())).rejects.toThrow("server unavailable");
    }
  });
});

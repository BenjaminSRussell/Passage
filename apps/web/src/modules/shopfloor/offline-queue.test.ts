import { describe, expect, it, vi } from "vitest";
import { MAX_BATCH, ShopfloorApiError, type RegistrationInput, type RegistrationResult } from "./api/client";
import { isNetworkFailure, MemoryQueueStore, OfflineQueue, OfflineQueueFullError, submitOrQueue } from "./offlineQueue";

const WORKER = "0195f0a0-0000-7000-8000-000000000001";
const t0 = Date.parse("2026-10-21T06:00:00Z");

function action(i: number, kind: RegistrationInput["kind"] = "job-start"): RegistrationInput {
  return { id: `00000000-0000-7000-8000-${String(i).padStart(12, "0")}`, workerId: WORKER, kind, startedAt: new Date(t0 + i * 45_000).toISOString(), deviceId: "web-kiosk" };
}

const accepted = (items: RegistrationInput[]): RegistrationResult[] => items.map((it) => ({ id: it.id, status: "accepted", flags: [], autoStopped: [], warnings: [] }));

function sender() {
  const batches: { items: RegistrationInput[]; deviceSentAt?: string }[] = [];
  let fail: unknown = null;
  const seen = new Set<string>();
  const send = vi.fn(async (items: RegistrationInput[], deviceSentAt?: string) => {
    if (fail) throw fail;
    batches.push({ items, deviceSentAt });
    return items.map((it) => {
      const dup = seen.has(it.id);
      seen.add(it.id);
      return { id: it.id, status: dup ? "duplicate" : "accepted", flags: [], autoStopped: [], warnings: [] } as RegistrationResult;
    });
  });
  return { send, batches, failWith: (e: unknown) => (fail = e) };
}

describe("OfflineQueue", () => {
  // AC3 / AC-25.5: 40 actions queued over 30 minutes replay once, in the order recorded.
  it("replays 40 queued actions once, in recording order, marked offline with the device clock", async () => {
    const s = sender();
    const clock = new Date(t0 + 30 * 60_000);
    const q = new OfflineQueue(new MemoryQueueStore(), s.send, () => clock);
    const recorded = Array.from({ length: 40 }, (_, i) => action(i, i % 2 === 0 ? "job-start" : "job-stop"));
    // Recorded one at a time, out of id order, as a worker taps through 30 minutes.
    for (const a of [...recorded].reverse()) await q.enqueue([a]);
    expect(await q.size()).toBe(40);

    const res = await q.flush();
    expect(res).toEqual({ sent: 40, duplicates: 0, remaining: 0 });
    expect(s.batches).toHaveLength(1);
    expect(s.batches[0].deviceSentAt).toBe(clock.toISOString());
    expect(s.batches[0].items.map((it) => it.id)).toEqual([...recorded].reverse().map((a) => a.id));
    expect(s.batches[0].items.every((it) => it.offline === true)).toBe(true);

    expect(await q.flush()).toEqual({ sent: 0, duplicates: 0, remaining: 0 });
    expect(s.send).toHaveBeenCalledTimes(1);
  });

  it("sends at most MAX_BATCH per request and keeps what a dropped network did not take", async () => {
    const s = sender();
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    await q.enqueue(Array.from({ length: MAX_BATCH + 20 }, (_, i) => action(i)));
    let calls = 0;
    s.send.mockImplementation(async (items: RegistrationInput[]) => {
      calls += 1;
      if (calls === 2) throw new TypeError("Failed to fetch");
      return accepted(items);
    });
    expect(await q.flush()).toEqual({ sent: MAX_BATCH, duplicates: 0, remaining: 20 });
    const left = await q.list();
    expect(left.map((a) => a.item.id)).toEqual(Array.from({ length: 20 }, (_, i) => action(MAX_BATCH + i).id));
    expect(await q.flush()).toEqual({ sent: 20, duplicates: 0, remaining: 0 });
  });

  it("counts a resent id as a duplicate and drops it", async () => {
    const s = sender();
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    await q.enqueue([action(1)]);
    await s.send([action(1)]);
    expect(await q.flush()).toEqual({ sent: 0, duplicates: 1, remaining: 0 });
  });

  it("keeps an id already queued once, and refuses a set that would pass the capacity (SF-0010)", async () => {
    const q = new OfflineQueue(new MemoryQueueStore(), sender().send, undefined, 3);
    await q.enqueue([action(1), action(2)]);
    expect(await q.enqueue([action(2)])).toBe(2);
    await expect(q.enqueue([action(3), action(4)])).rejects.toBeInstanceOf(OfflineQueueFullError);
    expect(await q.size()).toBe(2);
    await expect(q.enqueue([action(3), action(4)])).rejects.toMatchObject({ code: "SF-0010" });
  });

  it("stops at a batch the service refuses and keeps it queued", async () => {
    const s = sender();
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    await q.enqueue([action(1)]);
    s.failWith(new ShopfloorApiError(422, "COM-0422", "Validation failed"));
    expect(await q.flush()).toEqual({ sent: 0, duplicates: 0, remaining: 1, refused: { code: "COM-0422", message: "Validation failed" } });
    expect(await q.size()).toBe(1);
  });

  it("shares one flush between concurrent callers and tells subscribers the size", async () => {
    const s = sender();
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    const sizes: number[] = [];
    q.subscribe((n) => sizes.push(n));
    await q.enqueue([action(1), action(2)]);
    const [a, b] = await Promise.all([q.flush(), q.flush()]);
    expect(a).toBe(b);
    expect(s.send).toHaveBeenCalledTimes(1);
    expect(sizes).toContain(2);
    expect(sizes.at(-1)).toBe(0);
  });
});

describe("submitOrQueue", () => {
  it("sends online, queues on a network failure, and queues behind older actions", async () => {
    const s = sender();
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    expect(await submitOrQueue(q, [action(1)])).toMatchObject({ queued: false });

    s.failWith(new TypeError("Failed to fetch"));
    expect(await submitOrQueue(q, [action(2)])).toEqual({ queued: true, size: 1 });

    // Still down: the next action goes behind the first instead of being tried on its own.
    expect(await submitOrQueue(q, [action(3)])).toEqual({ queued: true, size: 2 });
    expect((await q.list()).map((a) => a.item.id)).toEqual([action(2).id, action(3).id]);

    s.failWith(null);
    await q.flush(); // joins the background flush the queued submit started, if still running
    await q.flush();
    expect(s.batches.at(-1)?.items.map((it) => it.id)).toEqual([action(2).id, action(3).id]);
  });

  it("does not queue a problem the service answered", async () => {
    const s = sender();
    s.failWith(new ShopfloorApiError(503, "COM-0503", "Service unavailable"));
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    await expect(submitOrQueue(q, [action(1)])).rejects.toBeInstanceOf(ShopfloorApiError);
    expect(await q.size()).toBe(0);
  });

  it("queues without trying while the browser is offline", async () => {
    const s = sender();
    s.failWith(new TypeError("Failed to fetch"));
    const q = new OfflineQueue(new MemoryQueueStore(), s.send);
    const online = vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    try {
      expect(await submitOrQueue(q, [action(1)])).toEqual({ queued: true, size: 1 });
      expect(isNetworkFailure(new Error("x"))).toBe(true);
    } finally {
      online.mockRestore();
    }
    expect(isNetworkFailure(new Error("x"))).toBe(false);
    expect(isNetworkFailure(new DOMException("aborted", "AbortError"))).toBe(false);
  });
});

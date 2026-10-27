import { describe, expect, it } from "vitest";
import { type ApplyOutcome, DEFAULT_LIMITS, OfflineQueue, QueueFullError, type QueuedTx } from "./offlineQueue";

const applied: ApplyOutcome = { status: "APPLIED" };

function clock(start = 1_700_000_000_000) {
  let t = start;
  return { now: () => t, advance: (ms: number) => (t += ms) };
}

function fill(q: OfflineQueue, n: number) {
  for (let i = 0; i < n; i++) q.enqueue("ConfirmWorkLine", { line: i + 1, qty: 1 });
}

describe("OfflineQueue basics", () => {
  it("assigns monotonic seq and device:seq idempotency keys", () => {
    const q = new OfflineQueue("dev-7", DEFAULT_LIMITS, clock().now);
    const a = q.enqueue("ConfirmWorkLine", {});
    const b = q.enqueue("ConfirmWorkLine", {});
    expect([a.seq, b.seq]).toEqual([1, 2]);
    expect([a.idempotencyKey, b.idempotencyKey]).toEqual(["dev-7:1", "dev-7:2"]);
  });

  it("rejects the 501st transaction with MOB-0106", () => {
    const q = new OfflineQueue("d", DEFAULT_LIMITS, clock().now);
    fill(q, 500);
    expect(() => q.enqueue("ConfirmWorkLine", {})).toThrow(QueueFullError);
    try {
      q.enqueue("ConfirmWorkLine", {});
    } catch (e) {
      expect((e as QueueFullError).code).toBe("MOB-0106");
    }
  });

  it("refuses new work when the oldest pending transaction is older than 24 h", () => {
    const c = clock();
    const q = new OfflineQueue("d", DEFAULT_LIMITS, c.now);
    q.enqueue("ConfirmWorkLine", {});
    c.advance(24 * 60 * 60 * 1000 + 1);
    expect(() => q.enqueue("ConfirmWorkLine", {})).toThrow(QueueFullError);
  });
});

describe("OfflineQueue replay (AC-32.3)", () => {
  it("applies 200 queued confirmations once, in order, and a second replay changes nothing", async () => {
    const q = new OfflineQueue("dev-1", DEFAULT_LIMITS, clock().now);
    fill(q, 200);
    const server = new Map<string, number>();
    const order: number[] = [];
    const apply = async (tx: QueuedTx): Promise<ApplyOutcome> => {
      if (!server.has(tx.idempotencyKey)) {
        server.set(tx.idempotencyKey, tx.seq);
        order.push(tx.seq);
      }
      return applied;
    };
    const first = await q.replay(apply);
    expect(first).toMatchObject({ applied: 200, pending: 0, stoppedBecause: "drained" });
    expect(order).toEqual(Array.from({ length: 200 }, (_, i) => i + 1));

    let calls = 0;
    const second = await q.replay(async (tx) => {
      calls++;
      return apply(tx);
    });
    expect(calls).toBe(0);
    expect(second.applied).toBe(0);
    expect(server.size).toBe(200);
  });

  it("stops at the first conflict and keeps later transactions PENDING (BR-32.3, AC-32.6)", async () => {
    const q = new OfflineQueue("dev-2", DEFAULT_LIMITS, clock().now);
    fill(q, 5);
    const sent: number[] = [];
    const outcome = await q.replay(async (tx) => {
      sent.push(tx.seq);
      return tx.seq === 3 ? { status: "CONFLICT", reason: "line was CANCELLED by alice" } : applied;
    });
    expect(sent).toEqual([1, 2, 3]);
    expect(outcome).toMatchObject({ applied: 2, conflicts: 1, pending: 2, stoppedBecause: "conflict" });
    const snap = q.snapshot();
    expect(snap[2]).toMatchObject({ replayStatus: "CONFLICT", conflictReason: "line was CANCELLED by alice" });
    expect(snap[3].replayStatus).toBe("PENDING");
  });

  it("does not send anything past an unresolved conflict, even on a later replay", async () => {
    const q = new OfflineQueue("dev-3", DEFAULT_LIMITS, clock().now);
    fill(q, 3);
    await q.replay(async (tx) => (tx.seq === 1 ? { status: "CONFLICT", reason: "x" } : applied));
    const sent: number[] = [];
    await q.replay(async (tx) => {
      sent.push(tx.seq);
      return applied;
    });
    expect(sent).toEqual([]);
    q.resolve(1, "reapply");
    const again = await q.replay(async (tx) => {
      sent.push(tx.seq);
      return applied;
    });
    expect(sent).toEqual([1, 2, 3]);
    expect(again.pending).toBe(0);
  });

  it("lets replay continue after a supervisor rejects the conflicting transaction (BR-32.4)", async () => {
    const q = new OfflineQueue("dev-3b", DEFAULT_LIMITS, clock().now);
    fill(q, 3);
    await q.replay(async (tx) => (tx.seq === 2 ? { status: "CONFLICT", reason: "line was COMPLETED by bob" } : applied));
    q.resolve(2, "reject");
    const sent: number[] = [];
    const summary = await q.replay(async (tx) => {
      sent.push(tx.seq);
      return applied;
    });
    expect(sent).toEqual([3]);
    expect(summary).toMatchObject({ applied: 1, pending: 0, stoppedBecause: "drained" });
    expect(q.snapshot()[1]).toMatchObject({ replayStatus: "REJECTED", resolved: true, conflictReason: "line was COMPLETED by bob" });
    expect(q.prune()).toBe(3);
  });

  it("treats a transport failure as a pause, not a loss", async () => {
    const q = new OfflineQueue("dev-4", DEFAULT_LIMITS, clock().now);
    fill(q, 4);
    let fail = true;
    const apply = async (tx: QueuedTx) => {
      if (fail && tx.seq === 3) throw new Error("network down");
      return applied;
    };
    const first = await q.replay(apply);
    expect(first).toMatchObject({ applied: 2, stoppedBecause: "transport", pending: 2 });
    fail = false;
    const second = await q.replay(apply);
    expect(second).toMatchObject({ applied: 2, pending: 0 });
  });

  it("prunes applied transactions without losing pending ones", async () => {
    const q = new OfflineQueue("dev-5", DEFAULT_LIMITS, clock().now);
    fill(q, 3);
    await q.replay(async (tx) => (tx.seq === 2 ? { status: "REJECTED", reason: "bad" } : applied));
    expect(q.prune()).toBe(1);
    expect(q.snapshot().map((t) => t.seq)).toEqual([2, 3]);
  });
});

describe("OfflineQueue exactly-once under random disconnects (property)", () => {
  // Seeded LCG so any failure replays exactly.
  function rng(seed: number) {
    let s = seed >>> 0;
    return () => {
      s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
      return s / 0x100000000;
    };
  }

  for (const seed of [1, 2, 3, 4, 5, 99, 1234]) {
    it(`seed ${seed}: every transaction reaches the server exactly once and in seq order`, async () => {
      const rand = rng(seed);
      const n = 50 + Math.floor(rand() * 150);
      const q = new OfflineQueue(`dev-${seed}`, DEFAULT_LIMITS, clock().now);
      fill(q, n);
      const server = new Map<string, number>(); // idempotency key -> times applied
      const serverOrder: number[] = [];
      let rounds = 0;
      while (q.pending().length > 0) {
        if (++rounds > 10_000) throw new Error("replay did not converge");
        // A disconnect may cut the connection before any work is sent, or after the server
        // applied a transaction but before the device saw the ack (the duplicate case).
        await q.replay(async (tx) => {
          const r = rand();
          if (r < 0.15) throw new Error("disconnected before send");
          const prior = server.get(tx.idempotencyKey) ?? 0;
          server.set(tx.idempotencyKey, prior + 1);
          if (prior === 0) serverOrder.push(tx.seq);
          if (r < 0.3) throw new Error("ack lost");
          return applied;
        });
      }
      expect(server.size).toBe(n);
      for (const count of server.values()) expect(count).toBeGreaterThanOrEqual(1);
      expect(serverOrder).toEqual(Array.from({ length: n }, (_, i) => i + 1));
      expect(q.snapshot().every((t) => t.replayStatus === "APPLIED")).toBe(true);
    });
  }
});

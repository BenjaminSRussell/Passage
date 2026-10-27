// Offline confirmation queue (FR-32.5, BR-32.3, BR-32.4). Pure: persistence and
// transport are injected. The device app stores the queue encrypted in IndexedDB.

export type ReplayStatus = "PENDING" | "APPLIED" | "CONFLICT" | "REJECTED";

export interface QueuedTx {
  /** `device_id:seq`, the Idempotency-Key sent on replay. */
  idempotencyKey: string;
  deviceId: string;
  seq: number;
  command: string;
  payload: Record<string, unknown>;
  capturedAt: number; // epoch ms, device clock corrected by measured server skew
  replayStatus: ReplayStatus;
  conflictReason?: string;
  /** Set when a supervisor rejected the transaction: it is closed and no longer blocks replay. */
  resolved?: boolean;
}

export type ApplyOutcome = { status: "APPLIED" } | { status: "CONFLICT" | "REJECTED"; reason: string };

export interface QueueLimits {
  maxTx: number; // MOB-0106 when reached
  maxAgeMs: number; // oldest pending transaction older than this blocks new work
}

export const DEFAULT_LIMITS: QueueLimits = { maxTx: 500, maxAgeMs: 24 * 60 * 60 * 1000 };

export class QueueFullError extends Error {
  readonly code = "MOB-0106";
  constructor(message: string) {
    super(message);
    this.name = "QueueFullError";
  }
}

export interface ReplaySummary {
  applied: number;
  conflicts: number;
  rejected: number;
  /** Transactions left PENDING (after a conflict, a rejection, or a transport failure). */
  pending: number;
  stoppedBecause: "drained" | "conflict" | "rejected" | "transport";
}

/** Still counts against the queue limits: not applied and not closed by a supervisor. */
function open(t: QueuedTx): boolean {
  return t.replayStatus !== "APPLIED" && !t.resolved;
}

/** In-memory queue for one device. Seq is assigned locally and therefore never has gaps. */
export class OfflineQueue {
  private readonly txs: QueuedTx[] = [];
  private nextSeq = 1;

  constructor(
    readonly deviceId: string,
    private readonly limits: QueueLimits = DEFAULT_LIMITS,
    private readonly now: () => number = Date.now,
  ) {}

  /** Appends a confirmation. Throws QueueFullError (MOB-0106) at the count or age limit. */
  enqueue(command: string, payload: Record<string, unknown>): QueuedTx {
    const pending = this.txs.filter((t) => open(t));
    if (pending.length >= this.limits.maxTx) {
      throw new QueueFullError(`Offline queue is full; reconnect to continue.`);
    }
    const oldest = pending[0];
    if (oldest && this.now() - oldest.capturedAt > this.limits.maxAgeMs) {
      throw new QueueFullError(`Offline queue holds transactions older than 24 h; reconnect to continue.`);
    }
    const seq = this.nextSeq++;
    const tx: QueuedTx = {
      idempotencyKey: `${this.deviceId}:${seq}`,
      deviceId: this.deviceId,
      seq,
      command,
      payload,
      capturedAt: this.now(),
      replayStatus: "PENDING",
    };
    this.txs.push(tx);
    return tx;
  }

  /** The seq the next enqueue will take. Persist it with the queue: pruning drops applied
   * transactions, so the stored transactions alone cannot tell which seqs were already used. */
  get nextSequence(): number {
    return this.nextSeq;
  }

  /**
   * Reloads a persisted queue (FR-32.5). Refuses anything that would break the invariants:
   * seqs are consecutive (a pruned queue starts after 1, but never skips one), every key is
   * `device:seq` of this device, and nextSeq is past the last stored seq so a key is never
   * issued twice (a reused key would make the server treat a new confirmation as a replay).
   * Returns the number of transactions restored.
   */
  restore(txs: QueuedTx[], nextSeq?: number): number {
    const first = txs[0]?.seq ?? 1;
    if (!Number.isInteger(first) || first < 1) throw new RangeError(`restore: seq ${first} is not a positive integer`);
    txs.forEach((t, i) => {
      if (t.seq !== first + i) throw new RangeError(`restore: sequence gap at ${first + i}, found seq ${t.seq}`);
      if (t.deviceId !== this.deviceId || t.idempotencyKey !== `${this.deviceId}:${t.seq}`) {
        throw new RangeError(`restore: key ${t.idempotencyKey} does not belong to device ${this.deviceId}`);
      }
    });
    const after = txs.length > 0 ? txs[txs.length - 1].seq + 1 : 1;
    const next = nextSeq ?? after;
    if (!Number.isInteger(next) || next < after) {
      throw new RangeError(`restore: next seq ${next} would reuse a stored seq (last stored is ${after - 1})`);
    }
    this.txs.splice(0, this.txs.length, ...txs.map((t) => ({ ...t })));
    this.nextSeq = next;
    return txs.length;
  }

  /** All transactions in seq order, including applied ones until pruned. */
  snapshot(): QueuedTx[] {
    return this.txs.map((t) => ({ ...t }));
  }

  /** Transactions still waiting for a server answer, in seq order. */
  pending(): QueuedTx[] {
    return this.txs.filter((t) => t.replayStatus === "PENDING").map((t) => ({ ...t }));
  }

  /** Drops APPLIED and supervisor-rejected transactions from the local queue. */
  prune(): number {
    const before = this.txs.length;
    const keep = this.txs.filter((t) => open(t));
    this.txs.splice(0, this.txs.length, ...keep);
    return before - keep.length;
  }

  /** Supervisor resolution of a CONFLICT: re-queue it as PENDING, or reject it. */
  resolve(seq: number, action: "reapply" | "reject"): void {
    const tx = this.txs.find((t) => t.seq === seq);
    if (!tx || !open(tx)) return;
    if (action === "reapply") {
      tx.replayStatus = "PENDING";
      return;
    }
    tx.replayStatus = "REJECTED";
    tx.resolved = true;
    tx.conflictReason = tx.conflictReason ?? "rejected by supervisor";
  }

  /**
   * Replays pending transactions in seq order (BR-32.3). The first CONFLICT, REJECTED or
   * transport error stops the run; later transactions stay PENDING. Applied transactions
   * are never sent again, so replaying the same batch changes nothing.
   */
  async replay(apply: (tx: QueuedTx) => Promise<ApplyOutcome>): Promise<ReplaySummary> {
    const summary: ReplaySummary = { applied: 0, conflicts: 0, rejected: 0, pending: 0, stoppedBecause: "drained" };
    // An unresolved CONFLICT or REJECTED blocks everything after it until a supervisor resolves it.
    const blocker = this.txs.find((t) => t.replayStatus === "CONFLICT" || (t.replayStatus === "REJECTED" && !t.resolved));
    if (blocker) {
      summary.stoppedBecause = blocker.replayStatus === "CONFLICT" ? "conflict" : "rejected";
      summary.pending = this.txs.filter((t) => t.replayStatus === "PENDING").length;
      return summary;
    }
    for (const tx of this.txs) {
      if (tx.replayStatus !== "PENDING") continue;
      let outcome: ApplyOutcome;
      try {
        outcome = await apply({ ...tx });
      } catch {
        summary.stoppedBecause = "transport";
        break;
      }
      if (outcome.status === "APPLIED") {
        tx.replayStatus = "APPLIED";
        summary.applied++;
        continue;
      }
      tx.replayStatus = outcome.status;
      tx.conflictReason = outcome.reason;
      if (outcome.status === "CONFLICT") {
        summary.conflicts++;
        summary.stoppedBecause = "conflict";
      } else {
        summary.rejected++;
        summary.stoppedBecause = "rejected";
      }
      break;
    }
    summary.pending = this.txs.filter((t) => t.replayStatus === "PENDING").length;
    return summary;
  }
}

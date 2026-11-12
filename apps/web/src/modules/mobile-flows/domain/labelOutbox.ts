// Offline label printing (#32, FR-32.7). A label that cannot be printed because the device is offline, or the print service
// is unreachable, waits in the device's label outbox and prints when the connection is back. The outbox is a small list kept
// in the device's storage: one entry per pallet (the SSCC), because the print service deduplicates by that same key, so a
// flush that is repeated or interrupted never prints a label twice. Pure of the network: the caller gives it the two calls.

import type { LpLabelRequest } from "./receiveLabel";

export interface OutboxEntry {
  sscc: string;
  /** The label request, once the pallet's label data was read; absent when the device went offline before it could. */
  request?: LpLabelRequest;
  queuedAt: number;
  attempts: number;
  /** Why the last try did not print; set only for a permanent refusal that needs a person. */
  blocked?: string;
}

export interface OutboxStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

const KEY = "passage.labelOutbox";
/** The most labels a device keeps waiting: past it the oldest is dropped with its SSCC reported. */
export const MAX_WAITING = 200;

export class LabelOutbox {
  constructor(private readonly storage: OutboxStorage) {}

  list(): OutboxEntry[] {
    try {
      const raw = this.storage.getItem(KEY);
      const parsed: unknown = raw ? JSON.parse(raw) : [];
      return Array.isArray(parsed) ? (parsed as OutboxEntry[]).filter((e) => typeof e?.sscc === "string") : [];
    } catch {
      return [];
    }
  }

  private save(entries: OutboxEntry[]): void {
    try {
      this.storage.setItem(KEY, JSON.stringify(entries));
    } catch {
      // storage full or blocked: the label is lost to the outbox, and the worker was already told it would print
    }
  }

  /** Adds a pallet's label; a pallet already waiting keeps its place and gains the request if it had none. Returns the SSCC dropped for room, if any. */
  enqueue(sscc: string, request: LpLabelRequest | undefined, now: number, attempts = 0): { dropped?: string } {
    const entries = this.list();
    const at = entries.findIndex((e) => e.sscc === sscc);
    if (at >= 0) {
      entries[at] = { ...entries[at]!, request: entries[at]!.request ?? request, blocked: undefined };
      this.save(entries);
      return {};
    }
    entries.push({ sscc, request, queuedAt: now, attempts });
    const dropped = entries.length > MAX_WAITING ? entries.shift()?.sscc : undefined;
    this.save(entries);
    return dropped ? { dropped } : {};
  }

  remove(sscc: string): void {
    this.save(this.list().filter((e) => e.sscc !== sscc));
  }

  waiting(): number {
    return this.list().filter((e) => !e.blocked).length;
  }

  update(sscc: string, change: Partial<OutboxEntry>): void {
    this.save(this.list().map((e) => (e.sscc === sscc ? { ...e, ...change } : e)));
  }
}

/** jobFailed: the print service made a job and it failed, so the retry needs a new key (a repeated key returns the failed job). */
export type PrintResult = { printed: true } | { printed: false; transient: boolean; reason: string; jobFailed?: boolean };

export interface FlushDeps {
  /** Reads the pallet's label request (a pallet queued before its data was read). */
  resolve(sscc: string): Promise<{ ok: true; request: LpLabelRequest } | { ok: false; reason: string; transient: boolean }>;
  /** Prints it; the print service answers a repeated key with the stored job. */
  print(sscc: string, request: LpLabelRequest, attempt: number): Promise<PrintResult>;
}

export interface FlushReport {
  printed: string[];
  /** Entries that need a person: their reasons. */
  blocked: { sscc: string; reason: string }[];
  /** True when it stopped on a transient failure, with entries still waiting. */
  stopped: boolean;
}

/** Prints the waiting labels in order. A transient failure (still offline, the print service down) stops the flush and keeps the rest; a permanent refusal blocks that one entry and goes on. */
export async function flushOutbox(outbox: LabelOutbox, deps: FlushDeps): Promise<FlushReport> {
  const report: FlushReport = { printed: [], blocked: [], stopped: false };
  for (const entry of outbox.list()) {
    if (entry.blocked) continue;
    let request = entry.request;
    if (!request) {
      const resolved = await deps.resolve(entry.sscc);
      if (!resolved.ok) {
        if (resolved.transient) {
          report.stopped = true;
          return report;
        }
        outbox.update(entry.sscc, { blocked: resolved.reason });
        report.blocked.push({ sscc: entry.sscc, reason: resolved.reason });
        continue;
      }
      request = resolved.request;
      outbox.update(entry.sscc, { request });
    }
    const result = await deps.print(entry.sscc, request, entry.attempts);
    if (result.printed) {
      outbox.remove(entry.sscc);
      report.printed.push(entry.sscc);
      continue;
    }
    if (result.transient) {
      outbox.update(entry.sscc, { attempts: entry.attempts + (result.jobFailed ? 1 : 0) });
      report.stopped = true;
      return report;
    }
    outbox.update(entry.sscc, { blocked: result.reason });
    report.blocked.push({ sscc: entry.sscc, reason: result.reason });
  }
  return report;
}

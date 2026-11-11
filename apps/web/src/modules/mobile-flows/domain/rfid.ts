// RFID read integration point (FR-32.9). The native shell (a Zebra RFD handheld or a fixed reader
// gateway) forwards each inventory round as a "passage:rfid" CustomEvent. This module validates the
// detail, drops the repeated reads of one tag inside a round and across a short window, and turns
// each EPC into the scan the flow runner already understands. Pure apart from the EventTarget the
// caller passes in.

import { decodeEpc, epcToScan } from "./epc";

export const RFID_EVENT = "passage:rfid";

/** One tag seen by the reader. */
export interface RfidRead {
  /** 24 hexadecimal characters. */
  epc: string;
  /** Signal strength in dBm (negative); the strongest read of a tag in a round is kept. */
  rssi?: number;
  antenna?: number;
}

export interface RfidRound {
  reads: RfidRead[];
}

export type RfidResult = { ok: true; reads: RfidRead[] } | { ok: false; reason: string };

/** Most tags accepted in one forwarded round; a larger one is a fault in the capture profile. */
export const MAX_READS_PER_ROUND = 500;

export function parseRound(detail: unknown): RfidResult {
  if (typeof detail !== "object" || detail === null) return { ok: false, reason: "RFID round is not an object" };
  const reads = (detail as Partial<RfidRound>).reads;
  if (!Array.isArray(reads)) return { ok: false, reason: "RFID round has no reads" };
  if (reads.length > MAX_READS_PER_ROUND) return { ok: false, reason: `RFID round has more than ${MAX_READS_PER_ROUND} reads` };
  const out: RfidRead[] = [];
  for (const r of reads) {
    if (typeof r !== "object" || r === null || typeof (r as RfidRead).epc !== "string") return { ok: false, reason: "RFID read has no EPC" };
    const { epc, rssi, antenna } = r as RfidRead;
    out.push({ epc: epc.trim().toUpperCase(), ...(typeof rssi === "number" ? { rssi } : {}), ...(typeof antenna === "number" ? { antenna } : {}) });
  }
  return { ok: true, reads: out };
}

/** A tag read the device acts on: the scan string for the runner and what it decoded to. */
export interface TagScan {
  epc: string;
  raw: string;
  rssi?: number;
}

export interface RoundOutcome {
  scans: TagScan[];
  /** EPCs that are not SGTIN-96 or SSCC-96, with the reason. They are logged, never scanned. */
  unreadable: { epc: string; reason: string }[];
}

/**
 * Drops duplicates within a round (keeping the strongest read) and tags already passed on within
 * `windowMs`, so a tag that stays in the field is not scanned again every round. Unreadable tags
 * are reported once per window too.
 */
export class RfidAggregator {
  private readonly lastSeen = new Map<string, number>();

  constructor(
    private readonly windowMs = 5000,
    private readonly now: () => number = Date.now,
  ) {}

  accept(reads: readonly RfidRead[]): RoundOutcome {
    const t = this.now();
    for (const [epc, at] of this.lastSeen) if (t - at >= this.windowMs) this.lastSeen.delete(epc);
    const strongest = new Map<string, RfidRead>();
    for (const r of reads) {
      const prior = strongest.get(r.epc);
      if (!prior || (r.rssi ?? -Infinity) > (prior.rssi ?? -Infinity)) strongest.set(r.epc, r);
    }
    const outcome: RoundOutcome = { scans: [], unreadable: [] };
    for (const r of [...strongest.values()].sort((a, b) => (b.rssi ?? -Infinity) - (a.rssi ?? -Infinity))) {
      if (this.lastSeen.has(r.epc)) continue;
      this.lastSeen.set(r.epc, t);
      try {
        decodeEpc(r.epc);
        outcome.scans.push({ epc: r.epc, raw: epcToScan(r.epc), ...(r.rssi !== undefined ? { rssi: r.rssi } : {}) });
      } catch (e) {
        outcome.unreadable.push({ epc: r.epc, reason: e instanceof Error ? e.message : "unreadable EPC" });
      }
    }
    return outcome;
  }
}

/** Subscribes to forwarded rounds. Returns the unsubscribe function. */
export function listenForRfid(target: EventTarget, handler: (result: RfidResult) => void): () => void {
  const listener = (event: Event) => handler(parseRound((event as CustomEvent).detail));
  target.addEventListener(RFID_EVENT, listener);
  return () => target.removeEventListener(RFID_EVENT, listener);
}

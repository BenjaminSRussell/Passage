import type { ActionBody, ActionOutcome, CaptureBody } from "./types";
import type { KeyValueStore, SealCipher } from "../../mobile-flows/domain/offlineStore";

/** One thing waiting to reach the server: an action of the log, or an image to upload before the action that names it. */
export type QueueEntry =
  | { seq: number; kind: "action"; workOrderId: string; technicianId: string; action: ActionBody }
  | { seq: number; kind: "capture"; workOrderId: string; technicianId: string; capture: Omit<CaptureBody, "data"> };

/** What the server said about an entry that did not go through, kept for the technician to read. */
export interface Attention {
  workOrderId: string;
  kind: string;
  code?: string;
  reason: string;
  at: string;
}

export const MAX_QUEUE = 500;

interface Persisted {
  v: 1;
  nextSeq: number;
  entries: QueueEntry[];
  attention: Attention[];
}

const QUEUE_KEY = "technician-queue";
const blobKey = (captureId: string) => `blob:${captureId}`;

/**
 * The device's append-only outbox (BR-45.6). Entries are sent in the order they were made. The queue is sealed
 * under the device key before it reaches storage, with each image under its own key so an action does not rewrite
 * a photo. A full queue refuses new entries rather than dropping old ones.
 */
export class TechQueue {
  private entries: QueueEntry[] = [];
  private seq = 1;
  private notes: Attention[] = [];
  private blobs = new Map<string, string>();

  constructor(private readonly store: KeyValueStore<string>, private readonly cipher: SealCipher) {}

  get pending(): readonly QueueEntry[] {
    return this.entries;
  }
  get attention(): readonly Attention[] {
    return this.notes;
  }
  get size(): number {
    return this.entries.length;
  }

  async load(): Promise<void> {
    const sealed = await this.store.get(QUEUE_KEY);
    if (sealed === null) return;
    const doc = JSON.parse(await this.cipher.open(sealed)) as Persisted;
    if (doc.v !== 1 || !Array.isArray(doc.entries)) throw new Error("the technician queue is not in a format this app reads");
    this.entries = doc.entries;
    this.seq = doc.nextSeq;
    this.notes = doc.attention ?? [];
  }

  private async save(): Promise<void> {
    const doc: Persisted = { v: 1, nextSeq: this.seq, entries: this.entries, attention: this.notes };
    await this.store.put(QUEUE_KEY, await this.cipher.seal(JSON.stringify(doc)));
  }

  private assertRoom(extra: number): void {
    if (this.entries.length + extra > MAX_QUEUE) throw new Error(`The queue holds ${MAX_QUEUE} entries and is full. Connect and send it before adding more.`);
  }

  async addAction(workOrderId: string, technicianId: string, action: ActionBody): Promise<void> {
    this.assertRoom(1);
    this.entries.push({ seq: this.seq++, kind: "action", workOrderId, technicianId, action });
    await this.save();
  }

  /** Queues an image upload (kept ahead of the action that names it) and the action itself, together. */
  async addCapture(workOrderId: string, technicianId: string, capture: CaptureBody, action: ActionBody): Promise<void> {
    this.assertRoom(2);
    const { data, ...meta } = capture;
    await this.store.put(blobKey(capture.captureId), await this.cipher.seal(data));
    this.blobs.set(capture.captureId, data);
    this.entries.push({ seq: this.seq++, kind: "capture", workOrderId, technicianId, capture: meta });
    this.entries.push({ seq: this.seq++, kind: "action", workOrderId, technicianId, action });
    await this.save();
  }

  async blob(captureId: string): Promise<string | null> {
    const kept = this.blobs.get(captureId);
    if (kept !== undefined) return kept;
    const sealed = await this.store.get(blobKey(captureId));
    return sealed === null ? null : await this.cipher.open(sealed);
  }

  /** Removes sent entries (and the images among them). */
  async remove(seqs: readonly number[]): Promise<void> {
    const gone = new Set(seqs);
    for (const e of this.entries) {
      if (gone.has(e.seq) && e.kind === "capture") {
        this.blobs.delete(e.capture.captureId);
        await this.store.delete(blobKey(e.capture.captureId));
      }
    }
    this.entries = this.entries.filter((e) => !gone.has(e.seq));
    await this.save();
  }

  async note(a: Attention): Promise<void> {
    this.notes = [...this.notes, a].slice(-50);
    await this.save();
  }

  async dismiss(index: number): Promise<void> {
    this.notes = this.notes.filter((_, i) => i !== index);
    await this.save();
  }
}

/** Turns a server outcome into a sentence a technician can act on. */
export function outcomeNote(o: ActionOutcome): string {
  switch (o.code) {
    case "SVC-WO-001":
      return "The customer's signature was missing, so the job was not completed. Get the signature, then complete it again.";
    case "SVC-WO-003":
      return "A required checklist item was open, so the job was not completed. Tick it, then complete the job again.";
    case "SVC-CAP-002":
      return "A photo or signature had not reached the server. Take it again.";
    case "SVC-SYNC-002":
      return o.reason ?? "The job is in a different state on the server.";
    case "SVC-SYNC-003":
      return "This job is not assigned to you any more.";
    case "SVC-SYNC-004":
      return o.reason ?? "That part could not be recorded.";
    case "SVC-SYNC-007":
      return "That checklist item is no longer on the job.";
    default:
      return o.reason ?? "The server did not accept this entry.";
  }
}

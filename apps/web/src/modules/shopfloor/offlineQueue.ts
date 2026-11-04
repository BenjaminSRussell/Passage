// Offline queue of kiosk registrations (D5, AC3, AC-25.5). A punch or job action the kiosk
// cannot send because the network is down is kept on the device (IndexedDB) under its client
// UUID and replayed, in the order it was recorded, once the kiosk is back online. The server
// stores each id once (a resent id is a duplicate), so a replay interrupted half-way is resent
// safely. Each replay batch carries the kiosk's clock as deviceSentAt so the server corrects a
// device clock more than 5 minutes off (BR-25.7).

import { chunk, createRegistrations, MAX_BATCH, ShopfloorApiError, type RegistrationInput, type RegistrationResult } from "./api/client";

/** Most actions a kiosk keeps offline (PRD site setting "Offline max actions", default 5,000; SF-0010). */
export const OFFLINE_MAX_ACTIONS = 5000;

export interface QueuedAction {
  /** Recording order on this device; replay follows it. */
  seq: number;
  item: RegistrationInput;
  queuedAt: string;
}

/** Where the queue lives. IndexedDbQueueStore in the browser, MemoryQueueStore in tests and as a fallback. */
export interface QueueStore {
  all(): Promise<QueuedAction[]>;
  put(actions: QueuedAction[]): Promise<void>;
  remove(ids: string[]): Promise<void>;
}

export class MemoryQueueStore implements QueueStore {
  private readonly rows = new Map<string, QueuedAction>();

  async all(): Promise<QueuedAction[]> {
    return [...this.rows.values()];
  }

  async put(actions: QueuedAction[]): Promise<void> {
    for (const a of actions) this.rows.set(a.item.id, a);
  }

  async remove(ids: string[]): Promise<void> {
    for (const id of ids) this.rows.delete(id);
  }
}

/** One object store keyed by the registration's client UUID. */
export class IndexedDbQueueStore implements QueueStore {
  private db: Promise<IDBDatabase> | null = null;

  constructor(
    private readonly dbName = "passage-kiosk",
    private readonly storeName = "registrations",
  ) {}

  private open(): Promise<IDBDatabase> {
    this.db ??= new Promise<IDBDatabase>((resolve, reject) => {
      const req = indexedDB.open(this.dbName, 1);
      req.onupgradeneeded = () => req.result.createObjectStore(this.storeName, { keyPath: "item.id" });
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    return this.db;
  }

  private async tx(mode: IDBTransactionMode, op: (s: IDBObjectStore) => IDBRequest | void): Promise<unknown> {
    const db = await this.open();
    return new Promise((resolve, reject) => {
      const t = db.transaction(this.storeName, mode);
      const req = op(t.objectStore(this.storeName));
      t.oncomplete = () => resolve(req ? req.result : undefined);
      t.onerror = () => reject(t.error);
      t.onabort = () => reject(t.error);
    });
  }

  async all(): Promise<QueuedAction[]> {
    return ((await this.tx("readonly", (s) => s.getAll())) as QueuedAction[] | undefined) ?? [];
  }

  async put(actions: QueuedAction[]): Promise<void> {
    await this.tx("readwrite", (s) => {
      for (const a of actions) s.put(a);
    });
  }

  async remove(ids: string[]): Promise<void> {
    await this.tx("readwrite", (s) => {
      for (const id of ids) s.delete(id);
    });
  }
}

/** The queue is full: the action is not recorded (SF-0010). */
export class OfflineQueueFullError extends Error {
  readonly code = "SF-0010";
  constructor(readonly capacity: number) {
    super(`The kiosk holds at most ${capacity} offline actions. Reconnect the kiosk so they can sync.`);
    this.name = "OfflineQueueFullError";
  }
}

/**
 * True for a failure that means the request never reached the service: fetch rejects with a
 * TypeError when the network is down, or the browser says it is offline. A problem response
 * from the service (any HTTP status) is not one: the kiosk shows it and the worker retries.
 */
export function isNetworkFailure(error: unknown): boolean {
  if (error instanceof ShopfloorApiError) return false;
  if (error instanceof DOMException && error.name === "AbortError") return false;
  if (typeof navigator !== "undefined" && navigator.onLine === false) return true;
  return error instanceof TypeError;
}

export interface FlushResult {
  sent: number;
  duplicates: number;
  /** Actions still queued (the network failed again, or the service refused a batch). */
  remaining: number;
  /** A batch the service refused with a problem; it stays queued for a supervisor to see. */
  refused?: { code: string; message: string };
}

type Send = (batch: RegistrationInput[], deviceSentAt?: string) => Promise<RegistrationResult[]>;
type Listener = (size: number) => void;

export class OfflineQueue {
  private nextSeq = 0;
  private flushing: Promise<FlushResult> | null = null;
  private readonly listeners = new Set<Listener>();
  private known = 0;

  constructor(
    private readonly store: QueueStore,
    private readonly send: Send = createRegistrations,
    private readonly now: () => Date = () => new Date(),
    readonly capacity: number = OFFLINE_MAX_ACTIONS,
  ) {}

  /** Calls listener with the queue size now and after every change; returns the unsubscribe. */
  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    void this.size().then(listener);
    return () => this.listeners.delete(listener);
  }

  private notify(size: number) {
    this.known = size;
    for (const l of this.listeners) l(size);
  }

  /** The last size the queue reported to its listeners. */
  get lastKnownSize(): number {
    return this.known;
  }

  /** Sends actions straight to the service, bypassing the queue. */
  sendNow(items: RegistrationInput[]): Promise<RegistrationResult[]> {
    return this.send(items);
  }

  async size(): Promise<number> {
    return (await this.store.all()).length;
  }

  /** The queued actions in recording order. */
  async list(): Promise<QueuedAction[]> {
    return (await this.store.all()).sort((a, b) => a.seq - b.seq);
  }

  /**
   * Keeps actions on the device, marked offline. An id already queued keeps its place. The
   * whole set is refused, and nothing is kept, when it would take the queue past capacity.
   */
  async enqueue(items: RegistrationInput[]): Promise<number> {
    const existing = await this.store.all();
    const have = new Set(existing.map((a) => a.item.id));
    const fresh = items.filter((it) => !have.has(it.id));
    if (existing.length + fresh.length > this.capacity) throw new OfflineQueueFullError(this.capacity);
    this.nextSeq = Math.max(this.nextSeq, ...existing.map((a) => a.seq + 1));
    const queuedAt = this.now().toISOString();
    await this.store.put(fresh.map((item) => ({ seq: this.nextSeq++, item: { ...item, offline: true }, queuedAt })));
    const size = existing.length + fresh.length;
    this.notify(size);
    return size;
  }

  /**
   * Sends the queue in recording order, at most MAX_BATCH per request, each batch with the
   * kiosk's clock as deviceSentAt. A batch the service answers is removed (accepted and
   * duplicate alike). On a network failure the rest stays queued for the next flush; a batch
   * the service refuses stops the flush and stays queued. Concurrent calls share one flush.
   */
  flush(): Promise<FlushResult> {
    this.flushing ??= this.doFlush().finally(() => {
      this.flushing = null;
    });
    return this.flushing;
  }

  private async doFlush(): Promise<FlushResult> {
    const queued = await this.list();
    const out: FlushResult = { sent: 0, duplicates: 0, remaining: queued.length };
    for (const batch of chunk(queued, MAX_BATCH)) {
      const items = batch.map((a) => a.item);
      let results: RegistrationResult[];
      try {
        results = await this.send(items, this.now().toISOString());
      } catch (error) {
        if (!isNetworkFailure(error)) {
          out.refused =
            error instanceof ShopfloorApiError
              ? { code: error.code, message: error.message }
              : { code: "COM-0001", message: error instanceof Error ? error.message : "Sync failed" };
        }
        break;
      }
      await this.store.remove(items.map((it) => it.id));
      out.sent += results.filter((r) => r.status === "accepted").length;
      out.duplicates += results.filter((r) => r.status === "duplicate").length;
      out.remaining -= items.length;
      this.notify(out.remaining);
    }
    return out;
  }
}

export type SubmitOutcome = { queued: false; results: RegistrationResult[] } | { queued: true; size: number };

/**
 * Sends the actions, or queues them when the network is down (or when older actions are still
 * queued, so a new action never overtakes them). The caller tells the worker which happened.
 */
export async function submitOrQueue(queue: OfflineQueue, items: RegistrationInput[]): Promise<SubmitOutcome> {
  if ((typeof navigator !== "undefined" && navigator.onLine === false) || (await queue.size()) > 0) {
    const size = await queue.enqueue(items);
    void queue.flush();
    return { queued: true, size };
  }
  try {
    return { queued: false, results: await queue.sendNow(items) };
  } catch (error) {
    if (!isNetworkFailure(error)) throw error;
    return { queued: true, size: await queue.enqueue(items) };
  }
}

let shared: OfflineQueue | null = null;

/** The kiosk's queue: IndexedDB when the browser has it, else memory (lost on reload). */
export function kioskQueue(): OfflineQueue {
  shared ??= new OfflineQueue(typeof indexedDB === "undefined" ? new MemoryQueueStore() : new IndexedDbQueueStore());
  return shared;
}

/** Replaces the shared queue (tests). */
export function setKioskQueue(queue: OfflineQueue | null): void {
  shared = queue;
}

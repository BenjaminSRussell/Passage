// Encrypted persistence for the offline queue (FR-32.5). The queue is sealed with AES-GCM
// under a non-extractable device key before it reaches IndexedDB, so a lost handheld does
// not leak pending confirmations. Storage and crypto are injected so the logic runs under
// vitest without a browser.

import { type QueueLimits, type QueuedTx, OfflineQueue } from "./offlineQueue";

/** Minimal async key-value store. IndexedDbStore is the browser implementation. */
export interface KeyValueStore<V> {
  get(key: string): Promise<V | null>;
  put(key: string, value: V): Promise<void>;
  delete(key: string): Promise<void>;
}

/** Seals and opens a UTF-8 string. Opening a value sealed under another key or tampered with must throw. */
export interface SealCipher {
  seal(plain: string): Promise<string>;
  open(sealed: string): Promise<string>;
}

const QUEUE_KEY = "offline-queue";
const DEVICE_KEY = "device-key";
const FORMAT_VERSION = 1;

interface PersistedQueue {
  v: number;
  deviceId: string;
  /** Next seq to issue. Kept because prune() drops applied transactions from txs. */
  nextSeq: number;
  txs: QueuedTx[];
}

/** Writes the whole queue as one sealed document. Called after every enqueue, replay and resolve. */
export async function saveQueue(store: KeyValueStore<string>, cipher: SealCipher, queue: OfflineQueue): Promise<void> {
  const doc: PersistedQueue = { v: FORMAT_VERSION, deviceId: queue.deviceId, nextSeq: queue.nextSequence, txs: queue.snapshot() };
  await store.put(QUEUE_KEY, await cipher.seal(JSON.stringify(doc)));
}

/**
 * Loads the device's queue. An empty store yields an empty queue. A document that belongs to
 * another device or a newer format is refused rather than guessed at.
 */
export async function loadQueue(
  store: KeyValueStore<string>,
  cipher: SealCipher,
  deviceId: string,
  limits: QueueLimits,
  now: () => number,
): Promise<OfflineQueue> {
  const queue = new OfflineQueue(deviceId, limits, now);
  const sealed = await store.get(QUEUE_KEY);
  if (sealed === null) return queue;
  const doc = JSON.parse(await cipher.open(sealed)) as PersistedQueue;
  if (doc.v !== FORMAT_VERSION) throw new Error(`offline store format ${doc.v} is not supported`);
  if (doc.deviceId !== deviceId) throw new Error("offline store belongs to another device");
  if (!Array.isArray(doc.txs) || typeof doc.nextSeq !== "number") throw new Error("offline store document is malformed");
  queue.restore(doc.txs, doc.nextSeq);
  return queue;
}

/** Returns the device's sealing key, creating it on first use. The key is never exported. */
export async function loadOrCreateDeviceKey(store: KeyValueStore<CryptoKey>, subtle: SubtleCrypto): Promise<CryptoKey> {
  const existing = await store.get(DEVICE_KEY);
  if (existing) return existing;
  const key = await AesGcmSealer.generateKey(subtle);
  await store.put(DEVICE_KEY, key);
  return key;
}

function toBase64(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) bin += String.fromCharCode(b);
  return btoa(bin);
}

function fromBase64(text: string): Uint8Array {
  const bin = atob(text);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

/** AES-256-GCM sealer. Output is base64(iv[12] || ciphertext+tag). */
export class AesGcmSealer implements SealCipher {
  constructor(
    private readonly subtle: SubtleCrypto,
    private readonly key: CryptoKey,
    private readonly random: (n: number) => Uint8Array = (n) => globalThis.crypto.getRandomValues(new Uint8Array(n)),
  ) {}

  static generateKey(subtle: SubtleCrypto): Promise<CryptoKey> {
    return subtle.generateKey({ name: "AES-GCM", length: 256 }, false, ["encrypt", "decrypt"]);
  }

  async seal(plain: string): Promise<string> {
    const iv = this.random(12);
    const data = new TextEncoder().encode(plain);
    const ct = new Uint8Array(await this.subtle.encrypt({ name: "AES-GCM", iv }, this.key, data));
    const out = new Uint8Array(iv.length + ct.length);
    out.set(iv, 0);
    out.set(ct, iv.length);
    return toBase64(out);
  }

  async open(sealed: string): Promise<string> {
    const bytes = fromBase64(sealed);
    if (bytes.length <= 12) throw new Error("sealed value is too short");
    const plain = await this.subtle.decrypt({ name: "AES-GCM", iv: bytes.slice(0, 12) }, this.key, bytes.slice(12));
    return new TextDecoder().decode(plain);
  }
}

/** IndexedDB-backed store for the browser. Values are structured-cloned, so CryptoKey objects fit. */
export class IndexedDbStore<V> implements KeyValueStore<V> {
  private db: Promise<IDBDatabase> | null = null;

  constructor(
    private readonly dbName = "passage-warehouse-mobile",
    private readonly storeName = "kv",
  ) {}

  private open(): Promise<IDBDatabase> {
    this.db ??= new Promise<IDBDatabase>((resolve, reject) => {
      const req = indexedDB.open(this.dbName, 1);
      req.onupgradeneeded = () => req.result.createObjectStore(this.storeName);
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
    return this.db;
  }

  private async run<T>(mode: IDBTransactionMode, op: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
    const db = await this.open();
    return new Promise<T>((resolve, reject) => {
      const tx = db.transaction(this.storeName, mode);
      const req = op(tx.objectStore(this.storeName));
      tx.oncomplete = () => resolve(req.result);
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
    });
  }

  async get(key: string): Promise<V | null> {
    const v = await this.run("readonly", (s) => s.get(key));
    return (v ?? null) as V | null;
  }

  async put(key: string, value: V): Promise<void> {
    await this.run("readwrite", (s) => s.put(value, key));
  }

  async delete(key: string): Promise<void> {
    await this.run("readwrite", (s) => s.delete(key));
  }
}

import { describe, expect, it } from "vitest";
import { DEFAULT_LIMITS, OfflineQueue } from "./offlineQueue";
import { AesGcmSealer, type KeyValueStore, loadOrCreateDeviceKey, loadQueue, type SealCipher, saveQueue } from "./offlineStore";

class MemoryStore<V> implements KeyValueStore<V> {
  readonly data = new Map<string, V>();
  async get(key: string) {
    return this.data.get(key) ?? null;
  }
  async put(key: string, value: V) {
    this.data.set(key, value);
  }
  async delete(key: string) {
    this.data.delete(key);
  }
}

/** Reversible, non-secure cipher for logic tests. The AES tests below cover the real primitive. */
const xorCipher = (key: number): SealCipher => ({
  async seal(plain) {
    return btoa([...plain].map((c) => String.fromCharCode(c.charCodeAt(0) ^ key)).join(""));
  },
  async open(sealed) {
    return [...atob(sealed)].map((c) => String.fromCharCode(c.charCodeAt(0) ^ key)).join("");
  },
});

const clock = () => 1_700_000_000_000;

function sampleQueue(deviceId: string, n: number): OfflineQueue {
  const q = new OfflineQueue(deviceId, DEFAULT_LIMITS, clock);
  for (let i = 0; i < n; i++) q.enqueue("ConfirmWorkLine", { line: i + 1, qty: 1 });
  return q;
}

describe("offline queue persistence (FR-32.5)", () => {
  it("round-trips 200 transactions with their statuses and continues the seq without a gap", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(0x5a);
    const q = sampleQueue("dev-p1", 200);
    await q.replay(async (tx) => (tx.seq === 150 ? { status: "CONFLICT", reason: "line cancelled" } : { status: "APPLIED" }));
    await saveQueue(store, cipher, q);

    const loaded = await loadQueue(store, cipher, "dev-p1", DEFAULT_LIMITS, clock);
    expect(loaded.snapshot()).toEqual(q.snapshot());
    const next = loaded.enqueue("ConfirmWorkLine", {});
    expect(next.seq).toBe(201);
    expect(next.idempotencyKey).toBe("dev-p1:201");
  });

  it("returns an empty queue when nothing was saved", async () => {
    const loaded = await loadQueue(new MemoryStore<string>(), xorCipher(1), "dev-x", DEFAULT_LIMITS, clock);
    expect(loaded.snapshot()).toEqual([]);
    expect(loaded.enqueue("ConfirmWorkLine", {}).seq).toBe(1);
  });

  it("refuses a store that belongs to another device", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(7);
    await saveQueue(store, cipher, sampleQueue("dev-a", 2));
    await expect(loadQueue(store, cipher, "dev-b", DEFAULT_LIMITS, clock)).rejects.toThrow("another device");
  });

  it("does not write the command or payload in clear text", async () => {
    const store = new MemoryStore<string>();
    const q = new OfflineQueue("dev-c", DEFAULT_LIMITS, clock);
    q.enqueue("ConfirmWorkLine", { lot: "SECRET-LOT-42" });
    await saveQueue(store, xorCipher(3), q);
    const stored = [...store.data.values()][0];
    expect(stored).not.toContain("SECRET-LOT-42");
    expect(stored).not.toContain("ConfirmWorkLine");
  });

  it("never reissues a key after applied transactions were pruned (AC-32.3)", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(0x21);
    const q = sampleQueue("dev-pr", 5);
    await q.replay(async (tx) => (tx.seq === 4 ? { status: "CONFLICT", reason: "line cancelled" } : { status: "APPLIED" }));
    expect(q.prune()).toBe(3);
    await saveQueue(store, cipher, q);

    const loaded = await loadQueue(store, cipher, "dev-pr", DEFAULT_LIMITS, clock);
    expect(loaded.snapshot().map((t) => t.seq)).toEqual([4, 5]);
    expect(loaded.enqueue("ConfirmWorkLine", {}).idempotencyKey).toBe("dev-pr:6");
  });

  it("keeps the next seq when every transaction was applied and pruned", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(0x33);
    const q = sampleQueue("dev-e", 3);
    await q.replay(async () => ({ status: "APPLIED" }));
    q.prune();
    await saveQueue(store, cipher, q);

    const loaded = await loadQueue(store, cipher, "dev-e", DEFAULT_LIMITS, clock);
    expect(loaded.snapshot()).toEqual([]);
    expect(loaded.enqueue("ConfirmWorkLine", {}).seq).toBe(4);
  });

  it("refuses a document whose next seq would reuse a stored seq", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(0x44);
    const q = sampleQueue("dev-n", 3);
    const doc = { v: 1, deviceId: "dev-n", nextSeq: 3, txs: q.snapshot() };
    await store.put("offline-queue", await cipher.seal(JSON.stringify(doc)));
    await expect(loadQueue(store, cipher, "dev-n", DEFAULT_LIMITS, clock)).rejects.toThrow("reuse a stored seq");
  });

  it("refuses a document without a next seq or with a transaction of another device", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(0x55);
    const q = sampleQueue("dev-m", 2);
    await store.put("offline-queue", await cipher.seal(JSON.stringify({ v: 1, deviceId: "dev-m", txs: q.snapshot() })));
    await expect(loadQueue(store, cipher, "dev-m", DEFAULT_LIMITS, clock)).rejects.toThrow("malformed");

    const foreign = q.snapshot().map((t) => ({ ...t, deviceId: "dev-z" }));
    await store.put("offline-queue", await cipher.seal(JSON.stringify({ v: 1, deviceId: "dev-m", nextSeq: 3, txs: foreign })));
    await expect(loadQueue(store, cipher, "dev-m", DEFAULT_LIMITS, clock)).rejects.toThrow("does not belong");
  });

  it("refuses a newer format instead of guessing", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(0x66);
    await store.put("offline-queue", await cipher.seal(JSON.stringify({ v: 2, deviceId: "dev-v", nextSeq: 1, txs: [] })));
    await expect(loadQueue(store, cipher, "dev-v", DEFAULT_LIMITS, clock)).rejects.toThrow("format 2");
  });

  it("rejects a restored document whose sequence has a gap", async () => {
    const store = new MemoryStore<string>();
    const cipher = xorCipher(9);
    const q = sampleQueue("dev-g", 3);
    const doc = { v: 1, deviceId: "dev-g", nextSeq: 4, txs: q.snapshot().filter((t) => t.seq !== 2) };
    await store.put("offline-queue", await cipher.seal(JSON.stringify(doc)));
    await expect(loadQueue(store, cipher, "dev-g", DEFAULT_LIMITS, clock)).rejects.toThrow("sequence gap");
  });
});

const hasSubtle = typeof globalThis.crypto?.subtle !== "undefined";

describe("AES-GCM sealer", () => {
  it.skipIf(!hasSubtle)("opens what it sealed and rejects a tampered value", async () => {
    const subtle = globalThis.crypto.subtle;
    const key = await AesGcmSealer.generateKey(subtle);
    const sealer = new AesGcmSealer(subtle, key);
    const sealed = await sealer.seal("hello warehouse");
    expect(await sealer.open(sealed)).toBe("hello warehouse");

    const bytes = Uint8Array.from(atob(sealed), (c) => c.charCodeAt(0));
    bytes[bytes.length - 1] ^= 0xff;
    const tampered = btoa(String.fromCharCode(...bytes));
    await expect(sealer.open(tampered)).rejects.toThrow();
  });

  it.skipIf(!hasSubtle)("cannot open a queue sealed under another device key", async () => {
    const subtle = globalThis.crypto.subtle;
    const store = new MemoryStore<string>();
    const a = new AesGcmSealer(subtle, await AesGcmSealer.generateKey(subtle));
    const b = new AesGcmSealer(subtle, await AesGcmSealer.generateKey(subtle));
    await saveQueue(store, a, sampleQueue("dev-k", 1));
    await expect(loadQueue(store, b, "dev-k", DEFAULT_LIMITS, clock)).rejects.toThrow();
  });

  it.skipIf(!hasSubtle)("creates the device key once and reuses it", async () => {
    const subtle = globalThis.crypto.subtle;
    const keys = new MemoryStore<CryptoKey>();
    const first = await loadOrCreateDeviceKey(keys, subtle);
    const second = await loadOrCreateDeviceKey(keys, subtle);
    expect(second).toBe(first);
    expect(first.extractable).toBe(false);
  });
});

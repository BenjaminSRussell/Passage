import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { formatOption, resolveOption } from "../../shell/lookup/entities";
import { AesGcmSealer, IndexedDbStore, loadOrCreateDeviceKey, type KeyValueStore, type SealCipher } from "../mobile-flows/domain/offlineStore";
import { fetchDay, fetchTruckStock, serverClient } from "./api/client";
import { loadSnapshot, saveSnapshot, type DaySnapshot } from "./domain/dayCache";
import { project, type PendingAction } from "./domain/projection";
import { TechQueue, type Attention } from "./domain/queue";
import { syncQueue, type SyncClient } from "./domain/sync";
import type { ActionBody, CaptureBody, DayVisit, TechnicianDay } from "./domain/types";

/** Everything the app talks to, so tests can supply a store, a clock and a server. */
export interface TechnicianDeps {
  store: KeyValueStore<string>;
  cipher: SealCipher;
  client: SyncClient;
  loadDay: (date?: string) => Promise<TechnicianDay>;
  loadTruck: typeof fetchTruckStock;
  resolveName: (entity: "party" | "product", id: string) => Promise<string | undefined>;
  now: () => Date;
  isOnline: () => boolean;
}

async function lookupName(entity: "party" | "product", id: string): Promise<string | undefined> {
  try {
    const found = await resolveOption(entity, id);
    return found ? formatOption(found) : undefined;
  } catch {
    return undefined;
  }
}

/** The browser's dependencies: IndexedDB sealed under a non-extractable device key, the real API. */
export async function browserDeps(): Promise<TechnicianDeps> {
  // One database, one object store: the device key and the sealed documents sit under different keys.
  const db = new IndexedDbStore<CryptoKey>("passage-technician", "kv");
  const key = await loadOrCreateDeviceKey(db, globalThis.crypto.subtle);
  return {
    store: new IndexedDbStore<string>("passage-technician", "kv"),
    cipher: new AesGcmSealer(globalThis.crypto.subtle, key),
    client: serverClient,
    loadDay: fetchDay,
    loadTruck: fetchTruckStock,
    resolveName: lookupName,
    now: () => new Date(),
    isOnline: () => (typeof navigator === "undefined" ? true : navigator.onLine),
  };
}

const SYNC_EVERY_MS = 60_000;

export interface TechnicianState {
  ready: boolean;
  snapshot: DaySnapshot | null;
  visits: DayVisit[];
  /** Entries still to be sent: what the offline indicator and queue screen show. */
  waiting: number;
  attention: readonly Attention[];
  online: boolean;
  busy: boolean;
  /** A plain sentence when the day could not be loaded and none is cached. */
  problem: string | null;
  refresh: () => Promise<void>;
  syncNow: () => Promise<void>;
  act: (visit: DayVisit, action: ActionBody) => Promise<void>;
  capture: (visit: DayVisit, capture: CaptureBody, action: ActionBody) => Promise<void>;
  dismiss: (index: number) => Promise<void>;
}

/**
 * The technician's device state (FR-45.8): the cached day, the outbox, and the loop that sends it. Actions are
 * applied to the cached day at once (projection) and queued; they leave the queue when the server has answered.
 */
export function useTechnician(deps: TechnicianDeps | null, date?: string): TechnicianState {
  const queue = useRef<TechQueue | null>(null);
  const [ready, setReady] = useState(false);
  const [snapshot, setSnapshot] = useState<DaySnapshot | null>(null);
  const [pending, setPending] = useState<PendingAction[]>([]);
  const [attention, setAttention] = useState<readonly Attention[]>([]);
  const [online, setOnline] = useState(true);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const running = useRef(false);

  const mirror = useCallback(() => {
    const q = queue.current;
    if (!q) return;
    setPending(q.pending.flatMap((e) => (e.kind === "action" ? [{ workOrderId: e.workOrderId, action: e.action }] : [])));
    setAttention([...q.attention]);
  }, []);

  const snapshotRef = useRef<DaySnapshot | null>(null);
  snapshotRef.current = snapshot;

  /** Reads the day from the server into the cache. Without a connection the cached day stays. */
  const reload = useCallback(async () => {
    if (!deps) return;
    const before = snapshotRef.current;
    try {
      const day = await deps.loadDay(date);
      const names: Record<string, string> = { ...(before?.names ?? {}) };
      const products: Record<string, string> = { ...(before?.products ?? {}) };
      await Promise.all([
        ...[...new Set(day.visits.map((v) => v.customerPartyId))].map(async (id) => {
          const n = await deps.resolveName("party", id);
          if (n) names[id] = n;
        }),
        ...[...new Set(day.visits.flatMap((v) => v.lines.flatMap((l) => (l.productId ? [l.productId] : []))))].map(async (id) => {
          const n = await deps.resolveName("product", id);
          if (n) products[id] = n;
        }),
      ]);
      const t = day.technician;
      const truck = t.truck && t.legalEntityId ? await deps.loadTruck(t.legalEntityId, t.truck.siteId, t.truck.warehouseId) : (before?.truck ?? []);
      const next: DaySnapshot = { day, names, products, truck, savedAt: deps.now().toISOString() };
      await saveSnapshot(deps.store, deps.cipher, next);
      setSnapshot(next);
      setProblem(null);
      setOnline(true);
    } catch (e) {
      setOnline(false);
      if (!before) setProblem(e instanceof Error && e.message !== "offline" ? e.message : "There is no connection and nothing is saved on this device yet. Connect once to load your day.");
    }
  }, [deps, date]);

  /** Sends the queue; once the server has taken entries, the day is read again so it shows the server's state. */
  const syncNow = useCallback(async () => {
    const q = queue.current;
    if (!deps || !q || running.current) return;
    running.current = true;
    setBusy(true);
    try {
      if (q.size > 0) {
        const report = await syncQueue(q, deps.client, deps.now);
        setOnline(!report.offline);
        mirror();
        if (report.sent + report.rejected > 0 && !report.offline) await reload();
      }
    } finally {
      running.current = false;
      setBusy(false);
      mirror();
    }
  }, [deps, mirror, reload]);

  const refresh = useCallback(async () => {
    // Send what is waiting first, so the day the server returns already holds it.
    await syncNow();
    await reload();
  }, [syncNow, reload]);

  useEffect(() => {
    if (!deps) return;
    let live = true;
    void (async () => {
      const q = new TechQueue(deps.store, deps.cipher);
      await q.load();
      queue.current = q;
      const cached = await loadSnapshot(deps.store, deps.cipher);
      if (!live) return;
      setSnapshot(cached);
      setOnline(deps.isOnline());
      mirror();
      setReady(true);
    })();
    return () => {
      live = false;
    };
  }, [deps, mirror]);

  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;
  useEffect(() => {
    if (!ready) return;
    void refreshRef.current();
    const timer = setInterval(() => void refreshRef.current(), SYNC_EVERY_MS);
    const onOnline = () => void refreshRef.current();
    window.addEventListener("online", onOnline);
    return () => {
      clearInterval(timer);
      window.removeEventListener("online", onOnline);
    };
  }, [ready]);

  const techId = snapshot?.day.technician.id ?? "";
  const act = useCallback(
    async (visit: DayVisit, action: ActionBody) => {
      await queue.current?.addAction(visit.id, techId, action);
      mirror();
      void syncNow();
    },
    [techId, mirror, syncNow],
  );
  const capture = useCallback(
    async (visit: DayVisit, c: CaptureBody, action: ActionBody) => {
      await queue.current?.addCapture(visit.id, techId, c, action);
      mirror();
      void syncNow();
    },
    [techId, mirror, syncNow],
  );
  const dismiss = useCallback(
    async (index: number) => {
      await queue.current?.dismiss(index);
      mirror();
    },
    [mirror],
  );

  const visits = useMemo(() => (snapshot ? snapshot.day.visits.map((v) => project(v, pending)) : []), [snapshot, pending]);
  return { ready, snapshot, visits, waiting: pending.length + (queue.current ? queue.current.pending.filter((e) => e.kind === "capture").length : 0), attention, online, busy, problem, refresh, syncNow, act, capture, dismiss };
}

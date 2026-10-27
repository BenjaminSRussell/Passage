import { useEffect, useRef, useState, type CSSProperties, type FormEvent } from "react";
import "./app.css";
import { completionFor, CONFIRM_WORK_LINE, confirmationPayload, CORE_FLOWS } from "../../web/src/modules/mobile-flows/domain/coreFlows";
import type { FlowDraft } from "../../web/src/modules/mobile-flows/domain/flowGraph";
import { goBack, startFlow, submitStep, type RunnerResult, type RunnerState } from "../../web/src/modules/mobile-flows/domain/flowRunner";
import { CLASS_LABEL } from "../../web/src/modules/mobile-flows/domain/gs1";
import { fontSizePx, targetSizePx, textDirection } from "../../web/src/modules/mobile-flows/domain/layout";
import { type ApplyOutcome, DEFAULT_LIMITS, OfflineQueue, type QueuedTx } from "../../web/src/modules/mobile-flows/domain/offlineQueue";
import {
  AesGcmSealer,
  IndexedDbStore,
  loadOrCreateDeviceKey,
  loadQueue,
  saveQueue,
  type KeyValueStore,
  type SealCipher,
} from "../../web/src/modules/mobile-flows/domain/offlineStore";
import { listenForScans } from "../../web/src/modules/mobile-flows/domain/scanBridge";
import type { ScanSource } from "../../web/src/modules/mobile-flows/domain/scanRouting";
import { appendTiming, type StepTiming } from "../../web/src/modules/mobile-flows/domain/telemetry";

const REPLAY_URL = "/api/v1/mobile/offline:replay";

/** Sends one queued confirmation. The server enforces ordering and idempotency per key. */
async function applyOne(tx: QueuedTx): Promise<ApplyOutcome> {
  const res = await fetch(REPLAY_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": tx.idempotencyKey },
    body: JSON.stringify({ idempotencyKey: tx.idempotencyKey, command: tx.command, payload: tx.payload, capturedAt: tx.capturedAt }),
  });
  if (res.ok) return { status: "APPLIED" };
  if (res.status === 409) return { status: "CONFLICT", reason: "conflict reported by the server" };
  if (res.status >= 500) throw new Error("server unavailable");
  return { status: "REJECTED", reason: `rejected with ${res.status}` };
}

interface DeviceStorage {
  store: KeyValueStore<string>;
  cipher: SealCipher;
}

interface Notice {
  tone: "error" | "info";
  text: string;
}

/** A scan that arrived while no scan step had focus (AC-32.7): kept on the device for support. */
interface IgnoredScan {
  at: number;
  source: ScanSource;
  stepId: string | null;
  reason: string;
}

function readFlag(key: string): boolean {
  try {
    return localStorage.getItem(key) === "true";
  } catch {
    return false;
  }
}

export interface AppProps {
  deviceId: string;
  userId: string;
  /** The flows on the device menu, in menu order. Defaults to the bundled core flows (FR-32.4). */
  flows?: readonly FlowDraft[];
}

/** Menu label for a flow code, e.g. PICK_CLUSTER -> "pick cluster". */
const flowLabel = (code: string) => code.replace(/_/g, " ").toLowerCase();

/**
 * Device shell (FR-32.1, FR-32.4). The worker picks a flow from the menu; the shared runner then
 * decides every step: one input per screen, Enter or a hardware scan advances, Back returns one
 * step (§6.32.6). A finished flow is queued as ConfirmWorkLine in the offline queue, which is
 * sealed with the device key in IndexedDB after every change (FR-32.5) and replayed in order on
 * Sync (BR-32.3). An inquiry queues nothing, and a count is not queued while offline (§6.32.13).
 */
export function App({ deviceId, userId, flows = CORE_FLOWS }: AppProps) {
  const [flow, setFlow] = useState<FlowDraft | null>(flows.length === 1 ? flows[0] : null);
  const [storage, setStorage] = useState<DeviceStorage | null>(null);
  const [queue, setQueue] = useState<OfflineQueue | null>(null);
  const [runner, setRunner] = useState<RunnerState | null>(null);
  const [timings, setTimings] = useState<StepTiming[]>([]);
  const [ignored, setIgnored] = useState<IgnoredScan[]>([]);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [fatal, setFatal] = useState<string | null>(null);
  const [typed, setTyped] = useState("");
  const [syncing, setSyncing] = useState(false);
  const [, bump] = useState(0);
  const onScanRef = useRef<(raw: string, source: ScanSource) => void>(() => undefined);

  // Open the sealed queue and the device key once per device.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const key = await loadOrCreateDeviceKey(new IndexedDbStore<CryptoKey>(), crypto.subtle);
      const cipher = new AesGcmSealer(crypto.subtle, key);
      const store = new IndexedDbStore<string>();
      const loaded = await loadQueue(store, cipher, deviceId, DEFAULT_LIMITS, Date.now);
      if (cancelled) return;
      setStorage({ store, cipher });
      setQueue(loaded);
    })().catch((e: unknown) => {
      if (!cancelled) setFatal(e instanceof Error ? `Device storage could not be opened: ${e.message}` : "Device storage could not be opened.");
    });
    return () => {
      cancelled = true;
    };
  }, [deviceId]);

  // Start the chosen flow at its entry step. A flow that fails validation does not run.
  useEffect(() => {
    if (!flow) {
      setRunner(null);
      return;
    }
    const started = startFlow(flow, Date.now());
    if (!started.ok) {
      setNotice({ tone: "error", text: started.issues.map((i) => `${i.code} ${i.message}`).join("; ") });
      setFlow(null);
      return;
    }
    setRunner(started.state);
  }, [flow]);

  // Hardware scans forwarded by the native shell's intent bridge (FR-32.10).
  useEffect(
    () =>
      listenForScans(window, (r) => {
        if (r.ok) onScanRef.current(r.raw, r.source);
        else setNotice({ tone: "error", text: `Scanner: ${r.reason}` });
      }),
    [],
  );

  async function persist(q: OfflineQueue) {
    if (storage) await saveQueue(storage.store, storage.cipher, q);
  }

  function restart() {
    if (!flow) return;
    const started = startFlow(flow, Date.now());
    if (started.ok) setRunner(started.state);
  }

  function openMenu() {
    setFlow(null);
    setNotice(null);
    setTyped("");
  }

  async function queueConfirmation(done: RunnerState) {
    if (!queue) return;
    try {
      queue.enqueue(CONFIRM_WORK_LINE, confirmationPayload(done));
      await persist(queue);
      restart();
      setNotice({ tone: "info", text: "Confirmation queued." });
    } catch (e) {
      // MOB-0106: the queue is full or too old; the worker must reconnect and sync first.
      setNotice({ tone: "error", text: e instanceof Error ? e.message : "The confirmation could not be queued." });
    }
    bump((n) => n + 1);
  }

  function apply(result: RunnerResult, source?: ScanSource) {
    if (!result.ok && result.ignored) {
      const entry: IgnoredScan = { at: Date.now(), source: source ?? "KEYBOARD_WEDGE", stepId: result.state.stepId, reason: result.message };
      console.info("scan ignored", entry);
      setIgnored((log) => [...log.slice(-49), entry]);
      return;
    }
    const t = result.timing;
    if (t) {
      setTimings((log) => {
        const next = appendTiming(log, t, Date.now());
        return "log" in next ? next.log : log;
      });
    }
    if (!result.ok) {
      setNotice({ tone: "error", text: result.code ? `${result.code} ${result.message}` : result.message });
      return;
    }
    if (result.finished && flow) {
      const completion = completionFor(flow.code, navigator.onLine);
      if (completion.kind === "blocked") {
        // Stay on the last step so the worker can confirm again once connected.
        setNotice({ tone: "error", text: completion.message });
        return;
      }
      setRunner(result.state);
      if (completion.kind === "queue") void queueConfirmation(result.state);
      else {
        restart();
        setNotice({ tone: "info", text: "Done." });
      }
      return;
    }
    setNotice(null);
    setRunner(result.state);
  }

  onScanRef.current = (raw, source) => {
    if (runner && flow) apply(submitStep(flow, runner, { kind: "scan", raw, source }, { deviceId, userId }, Date.now()), source);
  };

  function onKeyboardScan(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const value = typed.trim();
    setTyped("");
    if (value) onScanRef.current(value, "KEYBOARD_WEDGE");
  }

  function onValue(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (runner && flow) apply(submitStep(flow, runner, { kind: "value", value: typed }, { deviceId, userId }, Date.now()));
    setTyped("");
  }

  function onOutcome(outcome: string) {
    if (runner && flow) apply(submitStep(flow, runner, { kind: "value", value: outcome }, { deviceId, userId }, Date.now()));
  }

  function onConfirm() {
    if (runner && flow) apply(submitStep(flow, runner, { kind: "confirm" }, { deviceId, userId }, Date.now()));
  }

  function onBack() {
    setRunner((r) => (r ? goBack(r, Date.now()) : r));
    setNotice(null);
  }

  async function onSync() {
    if (!queue) return;
    setSyncing(true);
    try {
      // replay() never throws on transport errors: they stop the run and leave the rest PENDING.
      const summary = await queue.replay(applyOne);
      queue.prune();
      await persist(queue);
      setNotice(
        summary.stoppedBecause === "transport"
          ? { tone: "error", text: "Offline: sync paused, it will retry." }
          : { tone: summary.stoppedBecause === "drained" ? "info" : "error", text: `Synced ${summary.applied}; ${summary.pending} waiting (${summary.stoppedBecause}).` },
      );
    } catch (e) {
      setNotice({ tone: "error", text: e instanceof Error ? e.message : "Sync failed." });
    } finally {
      setSyncing(false);
      bump((n) => n + 1);
    }
  }

  const step = flow && runner?.stepId ? flow.steps.find((s) => s.id === runner.stepId) : undefined;
  const waiting = queue ? queue.snapshot().filter((t) => t.replayStatus !== "APPLIED" && !t.resolved).length : 0;
  const largeTargets = readFlag("passage.largeTargets");
  const layout = { "--font-base": `${fontSizePx(18, largeTargets)}px`, "--target": `${targetSizePx(largeTargets)}px` } as CSSProperties;

  return (
    <main className="app" dir={textDirection(navigator.language)} style={layout}>
      <header className="app__header">
        <h1>{flow ? flowLabel(flow.code) : "menu"}</h1>
        <span className="app__status">
          {waiting} queued, {DEFAULT_LIMITS.maxTx - waiting} left
        </span>
      </header>

      {fatal ? (
        <p role="alert" className="app__alert">
          {fatal}
        </p>
      ) : null}
      {!queue && !fatal ? <p>Opening device storage…</p> : null}

      {queue && !flow ? (
        <nav className="app__step" aria-label="Flows">
          {flows.map((f) => (
            <button key={f.code} type="button" className="app__button" onClick={() => setFlow(f)}>
              {flowLabel(f.code)}
            </button>
          ))}
        </nav>
      ) : null}

      {queue && runner && step ? (
        <section className="app__step" aria-label="Current step">
          <p className="app__prompt">{step.prompt}</p>
          {step.type === "scan" ? (
            <form onSubmit={onKeyboardScan}>
              <label htmlFor="scan-input">Scan {(step.expectedScan ?? ["ANY"]).map((c) => CLASS_LABEL[c]).join(" or ")}</label>
              <input id="scan-input" autoFocus autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />
            </form>
          ) : null}
          {step.type === "input" || step.type === "select" ? (
            <form onSubmit={onValue}>
              <label htmlFor="value-input">Value</label>
              <input id="value-input" autoFocus autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} />
            </form>
          ) : null}
          {step.type === "branch"
            ? Object.keys(step.next).map((outcome) => (
                <button key={outcome} type="button" className="app__button" onClick={() => onOutcome(outcome)}>
                  {outcome}
                </button>
              ))
            : null}
          {step.type === "confirm" || step.type === "display" || step.type === "call" ? (
            <button type="button" className="app__button" onClick={onConfirm}>
              Confirm
            </button>
          ) : null}
          <button type="button" className="app__button app__button--secondary" onClick={onBack} disabled={runner.history.length === 0}>
            Back
          </button>
          {flows.length > 1 ? (
            <button type="button" className="app__button app__button--secondary" onClick={openMenu}>
              Menu
            </button>
          ) : null}
        </section>
      ) : null}

      {notice ? (
        <p role={notice.tone === "error" ? "alert" : "status"} className={notice.tone === "error" ? "app__alert" : "app__message"}>
          {notice.text}
        </p>
      ) : null}

      {queue ? (
        <footer className="app__footer">
          <button type="button" className="app__button" onClick={onSync} disabled={syncing}>
            {syncing ? "Syncing…" : `Sync ${waiting} queued`}
          </button>
          <span className="app__status">
            {timings.length} steps timed{ignored.length > 0 ? `, ${ignored.length} scans ignored` : ""}
          </span>
        </footer>
      ) : null}
    </main>
  );
}

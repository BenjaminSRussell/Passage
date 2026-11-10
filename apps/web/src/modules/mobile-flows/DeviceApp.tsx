import { useEffect, useMemo, useRef, useState, type CSSProperties, type FormEvent } from "react";
import { DEFAULT_MENU_ID } from "./api/client";
import { makeApplier, type ReplayStop } from "./api/replayApply";
import { endSession, heartbeat, startSession } from "./api/session";
import { ClusterPanel } from "./ClusterPanel";
import { ExceptionsPanel } from "./ExceptionsPanel";
import { completionFor, CONFIRM_WORK_LINE, confirmationPayload, CORE_FLOWS } from "./domain/coreFlows";
import type { FlowDraft } from "./domain/flowGraph";
import { goBack, startFlow, submitStep, type RunnerResult, type RunnerState } from "./domain/flowRunner";
import { CLASS_LABEL } from "./domain/gs1";
import { fontSizePx, targetSizePx, textDirection } from "./domain/layout";
import { latestGraph } from "./domain/menuSync";
import { DEFAULT_LIMITS, OfflineQueue } from "./domain/offlineQueue";
import {
  AesGcmSealer,
  IndexedDbStore,
  loadOrCreateDeviceKey,
  loadQueue,
  saveQueue,
  type KeyValueStore,
  type SealCipher,
} from "./domain/offlineStore";
import { labelOffer } from "./domain/receiveLabel";
import { RfidAggregator, listenForRfid } from "./domain/rfid";
import { listenForScans } from "./domain/scanBridge";
import type { ScanSource } from "./domain/scanRouting";
import { SessionKeeper, type KeeperState } from "./domain/sessionKeeper";
import { appendTiming, type StepTiming } from "./domain/telemetry";
import { promptFor, routeUtterance, type VoiceAdapter } from "./domain/voice";
import { ReceiveLabelPanel } from "./ReceiveLabelPanel";
import { useDeviceFlows } from "./useDeviceFlows";
import "./deviceApp.css";

/** Seconds between session heartbeats; well inside the service's idle timeout (5 to 240 min). */
const HEARTBEAT_MS = 60_000;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Sends one queued confirmation. The server enforces ordering and idempotency per key. */
export const applyOne = makeApplier();

function readSetting(key: string): string {
  try {
    return localStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
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
  /** The flows on the device menu, in menu order. Defaults to the published menu, then the bundled core flows. */
  flows?: readonly FlowDraft[];
  /** Device menu id (GET /mobile/v1/menus/{id}). Defaults to `default`. */
  menuId?: string;
  /** The warehouse the device is bound to (a UUID). Defaults to `localStorage["passage.warehouseId"]`; without one the device runs without a service session. */
  warehouseId?: string;
  /** A headset's speech engine (FR-32.9): the step prompt is spoken and utterances answer the step. */
  voice?: VoiceAdapter;
}

/** Device screens that sit beside the flows on the menu: exceptions and override (FR-32.8), cart positions (BR-32.5). */
type DeviceTool = "exceptions" | "cluster";

const TOOL_LABEL: Record<DeviceTool, string> = { exceptions: "exceptions", cluster: "cluster cart" };

/** Menu label for a flow code, e.g. PICK_CLUSTER -> "pick cluster". */
const flowLabel = (code: string) => code.replace(/_/g, " ").toLowerCase();

/**
 * Device shell (FR-32.1, FR-32.4). The worker picks a flow from the menu; the shared runner then
 * decides every step: one input per screen, Enter or a hardware scan advances, Back returns one
 * step (§6.32.6). A finished flow is queued as ConfirmWorkLine in the offline queue, which is
 * sealed with the device key in IndexedDB after every change (FR-32.5) and replayed in order on
 * Sync (BR-32.3). An inquiry queues nothing, and a count is not queued while offline (§6.32.13).
 */
export function DeviceApp({ deviceId, userId, flows, menuId = DEFAULT_MENU_ID, warehouseId, voice }: AppProps) {
  const loaded = useDeviceFlows({ menuId, fallback: CORE_FLOWS });
  const menu = flows ?? loaded.flows;
  const [flow, setFlow] = useState<FlowDraft | null>(menu.length === 1 ? menu[0] : null);
  const [tool, setTool] = useState<DeviceTool | null>(null);
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
  const [session, setSession] = useState<KeeperState>({ status: "idle" });
  const [labelFor, setLabelFor] = useState<string | null>(null);
  const keeperRef = useRef<SessionKeeper | null>(null);
  const stopRef = useRef<ReplayStop | null>(null);
  const rfid = useRef(new RfidAggregator());
  const onScanRef = useRef<(raw: string, source: ScanSource) => void>(() => undefined);
  const onVoiceRef = useRef<(text: string) => void>(() => undefined);
  const warehouse = warehouseId ?? readSetting("passage.warehouseId");
  const applier = useMemo(
    () => makeApplier({ userId, sessionId: () => keeperRef.current?.sessionId, onStop: (stop) => (stopRef.current = stop) }),
    [userId],
  );
  const menuRef = useRef(menu);
  menuRef.current = menu;

  // The service session (FR-32.1, FR-32.6): start it, beat while the device is open, sign out on close.
  // Without a provisioned warehouse, or with the service out of reach, the device works from its queue.
  useEffect(() => {
    if (!UUID.test(warehouse)) return;
    const keeper = new SessionKeeper(
      {
        start: () => startSession({ deviceId, warehouseId: warehouse, menuKey: menuId }),
        heartbeat: (id, at) => heartbeat(id, at),
        end: (id) => endSession(id),
      },
      setSession,
    );
    keeperRef.current = keeper;
    void keeper.start();
    const timer = setInterval(() => void keeper.beat(), HEARTBEAT_MS);
    return () => {
      clearInterval(timer);
      keeperRef.current = null;
      void keeper.stop();
    };
  }, [deviceId, warehouse, menuId]);

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

  // Tag reads forwarded by the shell (FR-32.9): the strongest new tag of a round is a scan.
  useEffect(
    () =>
      listenForRfid(window, (r) => {
        if (!r.ok) {
          setNotice({ tone: "error", text: `RFID: ${r.reason}` });
          return;
        }
        const round = rfid.current.accept(r.reads);
        if (round.scans.length > 0) onScanRef.current(round.scans[0].raw, "RFID");
        else if (round.unreadable.length > 0) setNotice({ tone: "error", text: `MOB-0118 ${round.unreadable[0].reason}` });
        if (round.scans.length > 1) console.info("rfid: tags left for the next read", round.scans.length - 1);
      }),
    [],
  );

  // Voice (FR-32.9): speak each prompt, and let spoken digits and commands answer the step.
  useEffect(() => (voice ? voice.onUtterance((text) => onVoiceRef.current(text)) : undefined), [voice]);

  async function persist(q: OfflineQueue) {
    if (storage) await saveQueue(storage.store, storage.cipher, q);
  }

  function restart() {
    if (!flow) return;
    const next = latestGraph(menuRef.current, flow);
    if (next !== flow) {
      setFlow(next);
      return;
    }
    const started = startFlow(flow, Date.now());
    if (started.ok) setRunner(started.state);
  }

  function openMenu() {
    setLabelFor(null);
    setFlow(null);
    setTool(null);
    setNotice(null);
    setTyped("");
  }

  async function queueConfirmation(done: RunnerState) {
    if (!queue) return;
    try {
      // userId is the worker who captured it: after a takeover the server refuses another user's entries (BR-32.4).
      queue.enqueue(CONFIRM_WORK_LINE, { ...confirmationPayload(done), userId });
      await persist(queue);
      const offer = labelOffer(done);
      restart();
      setNotice({ tone: "info", text: "Confirmation queued." });
      if (offer && navigator.onLine) setLabelFor(offer.sscc);
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

  onVoiceRef.current = (text) => {
    const current = flow && runner?.stepId ? flow.steps.find((s) => s.id === runner.stepId) : undefined;
    const decision = routeUtterance(text, current);
    const ctx = { deviceId, userId };
    switch (decision.do) {
      case "scan":
        onScanRef.current(decision.raw, "VOICE");
        break;
      case "value":
      case "outcome":
        if (runner && flow) apply(submitStep(flow, runner, { kind: "value", value: decision.do === "value" ? decision.value : decision.value }, ctx, Date.now()));
        break;
      case "confirm":
        onConfirm();
        break;
      case "back":
        onBack();
        break;
      default:
        voice?.speak(decision.text);
    }
  };

  async function onSync() {
    if (!queue) return;
    setSyncing(true);
    try {
      // replay() never throws on transport errors: they stop the run and leave the rest PENDING.
      stopRef.current = null;
      const summary = await queue.replay(applier);
      const stop = stopRef.current as ReplayStop | null;
      queue.prune();
      await persist(queue);
      setNotice(
        summary.stoppedBecause === "transport"
          ? { tone: "error", text: stop ? stop.message : "Offline: sync paused, it will retry." }
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
  const stepId = step?.id;
  useEffect(() => {
    if (voice && step) voice.speak(promptFor(step));
    // Speak when the step changes, not on every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [voice, flow?.code, stepId]);
  const waiting = queue ? queue.snapshot().filter((t) => t.replayStatus !== "APPLIED" && !t.resolved).length : 0;
  const largeTargets = readFlag("passage.largeTargets");
  const layout = { "--font-base": `${fontSizePx(18, largeTargets)}px`, "--target": `${targetSizePx(largeTargets)}px` } as CSSProperties;

  return (
    <main className="app" dir={textDirection(navigator.language)} style={layout}>
      <header className="app__header">
        <h1>{flow ? flowLabel(flow.code) : tool ? TOOL_LABEL[tool] : "menu"}</h1>
        <span className="app__status">
          {waiting} queued, {DEFAULT_LIMITS.maxTx - waiting} left
        </span>
      </header>

      {session.status === "blocked" ? (
        <p role="alert" className="app__alert">
          {session.message}
        </p>
      ) : null}
      {session.status === "active" && session.message ? (
        <p role="status" className="app__message">
          {session.message}
        </p>
      ) : null}

      {labelFor ? <ReceiveLabelPanel sscc={labelFor} warehouseId={UUID.test(warehouse) ? warehouse : undefined} onClose={() => setLabelFor(null)} /> : null}

      {fatal ? (
        <p role="alert" className="app__alert">
          {fatal}
        </p>
      ) : null}
      {!queue && !fatal ? <p>Opening device storage…</p> : null}

      {queue && !flow && !tool ? (
        <nav className="app__step" aria-label="Flows">
          {menu.map((f) => (
            <button key={f.code} type="button" className="app__button" onClick={() => setFlow(f)}>
              {flowLabel(f.code)}
            </button>
          ))}
          {(Object.keys(TOOL_LABEL) as DeviceTool[]).map((t) => (
            <button key={t} type="button" className="app__button app__button--secondary" onClick={() => setTool(t)}>
              {TOOL_LABEL[t]}
            </button>
          ))}
        </nav>
      ) : null}

      {/* No verifyPin is wired yet, so overrides stay disabled until the server PIN check exists
          (handoff to the mobile service, see NOT-IMPLEMENTED.md). */}
      {queue && tool === "exceptions" ? <ExceptionsPanel deviceId={deviceId} workerId={userId} /> : null}
      {queue && tool === "cluster" ? <ClusterPanel /> : null}
      {queue && tool ? (
        <button type="button" className="app__button app__button--secondary" onClick={openMenu}>
          Menu
        </button>
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
          {/* The menu also holds the exception and cart screens, so it is reachable with one flow too. */}
          <button type="button" className="app__button app__button--secondary" onClick={openMenu}>
            Menu
          </button>
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
            {session.status === "offline" ? ", service offline" : ""}
          </span>
        </footer>
      ) : null}
    </main>
  );
}

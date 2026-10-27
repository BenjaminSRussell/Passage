import { useState, type FormEvent } from "react";
import {
  consumeOverride,
  issueOverride,
  logException,
  validateException,
  type ExceptionEntry,
  type ExceptionInput,
  type ExceptionKind,
  type OverrideGrant,
} from "./domain/exceptions";

/**
 * On-device exceptions and supervisor override (FR-32.8, BR-32.7). Device UI: it uses the
 * warehouse-mobile shell classes (app.css), not the admin design system, and is rendered by
 * apps/warehouse-mobile. The worker fills in one exception (short pick, skip or damage) and
 * either logs it, or asks a supervisor to authorize it with a PIN. The override authorizes
 * exactly that exception, once, within 60 s, and only for this worker; using it logs the
 * exception. The log lives in component state; upload to the server is a handoff to the mobile
 * service (see apps/warehouse-mobile/NOT-IMPLEMENTED.md).
 */

/** Override reasons for the warehouse. Replace with the server list when the mobile service is connected. */
export const DEFAULT_OVERRIDE_REASONS: readonly string[] = [
  "Damaged stock",
  "Wrong item located",
  "Count discrepancy",
  "Customer hold",
];

type WorkerKind = Exclude<ExceptionKind, "OVERRIDE">;

const KIND_LABEL: Record<ExceptionKind, string> = {
  SHORT_PICK: "Short pick",
  SKIP: "Skip",
  DAMAGED: "Report damage",
  OVERRIDE: "Supervisor override",
};

const WORKER_KINDS: readonly WorkerKind[] = ["SHORT_PICK", "SKIP", "DAMAGED"];

export interface ExceptionsPanelProps {
  deviceId: string;
  workerId: string;
  overrideReasons?: readonly string[];
  /** Checks a supervisor PIN with the server. Without it no override can be issued (BR-32.7). */
  verifyPin?: (supervisorId: string, pin: string) => Promise<boolean>;
  now?: () => number;
  newId?: () => string;
}

interface Notice {
  tone: "error" | "info";
  text: string;
}

/** An issued override and the one exception it authorizes. */
interface PendingOverride {
  grant: OverrideGrant;
  input: ExceptionInput;
}

function defaultId(): string {
  return crypto.randomUUID();
}

/** The action string an override names: the exception kind and its work line. */
export function overrideAction(input: Pick<ExceptionInput, "kind" | "workLineId">): string {
  return `${input.kind}:${input.workLineId}`;
}

function describeEntry(e: ExceptionInput): string {
  const detail =
    e.kind === "SHORT_PICK" ? ` · ${e.shortQuantity} short` : e.kind === "DAMAGED" || e.kind === "OVERRIDE" ? ` · ${e.reason}` : "";
  return `${KIND_LABEL[e.kind]} · ${e.workLineId} · ${e.locationCode}${detail}`;
}

export function ExceptionsPanel({
  deviceId,
  workerId,
  overrideReasons = DEFAULT_OVERRIDE_REASONS,
  verifyPin,
  now = Date.now,
  newId = defaultId,
}: ExceptionsPanelProps) {
  const [kind, setKind] = useState<WorkerKind>("SHORT_PICK");
  const [locationCode, setLocationCode] = useState("");
  const [workLineId, setWorkLineId] = useState("");
  const [shortQuantity, setShortQuantity] = useState("");
  const [damageReason, setDamageReason] = useState("");
  const [log, setLog] = useState<ExceptionEntry[]>([]);
  const [notice, setNotice] = useState<Notice | null>(null);

  const [supervisorId, setSupervisorId] = useState("");
  const [pin, setPin] = useState("");
  const [overrideReason, setOverrideReason] = useState("");
  const [pending, setPending] = useState<PendingOverride | null>(null);
  const [checking, setChecking] = useState(false);

  /** The worker's exception as currently filled in. */
  function currentInput(): ExceptionInput {
    return {
      kind,
      userId: workerId,
      deviceId,
      locationCode: locationCode.trim(),
      workLineId: workLineId.trim(),
      ...(kind === "SHORT_PICK" ? { shortQuantity: Number(shortQuantity) } : {}),
      ...(kind === "DAMAGED" ? { reason: damageReason.trim() } : {}),
    };
  }

  function clearException() {
    setShortQuantity("");
    setDamageReason("");
  }

  function onLogException(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const r = logException(log, currentInput(), newId(), now(), overrideReasons);
    if (r.error || !r.entry) {
      setNotice({ tone: "error", text: r.error ?? "The exception could not be logged." });
      return;
    }
    setLog(r.log);
    clearException();
    setNotice({ tone: "info", text: `${describeEntry(r.entry)} logged.` });
  }

  async function onRequestOverride(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!verifyPin) return;
    const input = currentInput();
    const valid = validateException(input, overrideReasons);
    if (!valid.ok) {
      setNotice({ tone: "error", text: `Fill in the exception to authorize first. ${valid.message}` });
      return;
    }
    const supervisor = supervisorId.trim();
    if (!supervisor || !pin) {
      setNotice({ tone: "error", text: "Supervisor ID and PIN are required." });
      return;
    }
    if (!overrideReasons.includes(overrideReason)) {
      setNotice({ tone: "error", text: "Override needs a configured reason." });
      return;
    }
    const action = overrideAction(input);
    // Self-override is refused before the PIN leaves the device (BR-32.7, §6.32.17).
    const issued = issueOverride(newId(), supervisor, workerId, action, now());
    if (issued.error || !issued.grant) {
      setNotice({ tone: "error", text: issued.error ?? "The override could not be issued." });
      return;
    }
    setChecking(true);
    const accepted = await verifyPin(supervisor, pin).catch(() => false);
    setChecking(false);
    setPin("");
    if (!accepted) {
      setNotice({ tone: "error", text: "Supervisor PIN was not accepted." });
      return;
    }
    // The override is logged with the supervisor's subject when it is granted (BR-32.7).
    const grant: OverrideGrant = { ...issued.grant, issuedAt: now() };
    const logged = logException(
      log,
      { kind: "OVERRIDE", userId: supervisor, deviceId, locationCode: input.locationCode, workLineId: input.workLineId, reason: overrideReason },
      newId(),
      grant.issuedAt,
      overrideReasons,
    );
    if (logged.error) {
      setNotice({ tone: "error", text: logged.error });
      return;
    }
    setLog(logged.log);
    setPending({ grant, input });
    setNotice({ tone: "info", text: `Override granted by ${supervisor} for ${describeEntry(input)}. Use it within 60 seconds.` });
  }

  function onUseOverride() {
    if (!pending) return;
    const at = now();
    const used = consumeOverride(pending.grant, workerId, overrideAction(pending.input), at);
    if (used.error || !used.grant) {
      const text = used.code ? `${used.code} ${used.error}` : (used.error ?? "The override could not be used.");
      setNotice({ tone: "error", text });
      setPending(null);
      return;
    }
    const r = logException(log, pending.input, newId(), at, overrideReasons);
    setPending(null);
    if (r.error || !r.entry) {
      setNotice({ tone: "error", text: r.error ?? "The exception could not be logged." });
      return;
    }
    setLog(r.log);
    clearException();
    setNotice({ tone: "info", text: `${describeEntry(r.entry)} logged with the override by ${used.grant.supervisorId}.` });
  }

  return (
    <section className="app__panel" aria-labelledby="exceptions-heading">
      <h2 id="exceptions-heading">Exceptions</h2>

      <form onSubmit={onLogException} aria-label="Log exception">
        <label className="app__field">
          Exception
          <select value={kind} onChange={(e) => setKind(e.target.value as WorkerKind)}>
            {WORKER_KINDS.map((k) => (
              <option key={k} value={k}>
                {KIND_LABEL[k]}
              </option>
            ))}
          </select>
        </label>
        <label className="app__field">
          Location
          <input autoComplete="off" value={locationCode} onChange={(e) => setLocationCode(e.target.value)} />
        </label>
        <label className="app__field">
          Work line
          <input autoComplete="off" value={workLineId} onChange={(e) => setWorkLineId(e.target.value)} />
        </label>
        {kind === "SHORT_PICK" ? (
          <label className="app__field">
            Units short
            <input inputMode="numeric" autoComplete="off" value={shortQuantity} onChange={(e) => setShortQuantity(e.target.value)} />
          </label>
        ) : null}
        {kind === "DAMAGED" ? (
          <label className="app__field">
            Damage reason
            <input autoComplete="off" value={damageReason} onChange={(e) => setDamageReason(e.target.value)} />
          </label>
        ) : null}
        <button type="submit" className="app__button">
          Log exception
        </button>
      </form>

      <form onSubmit={onRequestOverride} aria-label="Supervisor override">
        <h3>Supervisor override</h3>
        <p>A supervisor authorizes the exception above with their PIN. It can be used once, within 60 seconds.</p>
        {verifyPin ? null : (
          <p className="app__alert">The supervisor PIN check is not connected on this device, so no override can be issued.</p>
        )}
        <label className="app__field">
          Supervisor ID
          <input autoComplete="off" value={supervisorId} onChange={(e) => setSupervisorId(e.target.value)} />
        </label>
        <label className="app__field">
          Supervisor PIN
          <input type="password" inputMode="numeric" autoComplete="off" value={pin} onChange={(e) => setPin(e.target.value)} />
        </label>
        <label className="app__field">
          Override reason
          <select value={overrideReason} onChange={(e) => setOverrideReason(e.target.value)}>
            <option value="">Choose a reason</option>
            {overrideReasons.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>
        <button type="submit" className="app__button" disabled={!verifyPin || checking}>
          {checking ? "Checking PIN…" : "Request override"}
        </button>
      </form>

      {pending ? (
        <button type="button" className="app__button" onClick={onUseOverride}>
          Use override: {describeEntry(pending.input)}
        </button>
      ) : null}

      {notice ? (
        <p role={notice.tone === "error" ? "alert" : "status"} className={notice.tone === "error" ? "app__alert" : "app__message"}>
          {notice.text}
        </p>
      ) : null}

      <h3>Exception log</h3>
      {log.length === 0 ? (
        <p>No exceptions logged on this device.</p>
      ) : (
        <ol aria-label="Exception log" className="app__log">
          {log.map((e) => (
            <li key={e.id}>
              {describeEntry(e)}
              {e.kind === "OVERRIDE" ? ` · by ${e.userId}` : ""} · {new Date(e.occurredAt).toLocaleTimeString()}
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}

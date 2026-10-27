import { useRef, useState, type KeyboardEvent } from "react";
import { checkScan, classifyScan, type ScanClass } from "../../web/src/modules/mobile-flows/domain/gs1";
import { type ApplyOutcome, OfflineQueue, type QueuedTx, type ReplaySummary } from "../../web/src/modules/mobile-flows/domain/offlineQueue";

const REPLAY_URL = "/api/v1/mobile/offline:replay";

/** Sends one queued confirmation. The server enforces ordering and idempotency per key. */
async function applyOne(tx: QueuedTx): Promise<ApplyOutcome> {
  const res = await fetch(REPLAY_URL, {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": tx.idempotencyKey },
    body: JSON.stringify({ idempotencyKey: tx.idempotencyKey, command: tx.command, payload: tx.payload }),
  });
  if (res.ok) return { status: "APPLIED" };
  if (res.status === 409) return { status: "CONFLICT", reason: "conflict reported by the server" };
  if (res.status >= 500) throw new Error("server unavailable");
  return { status: "REJECTED", reason: `rejected with ${res.status}` };
}

/** One scan step: one input per screen, Enter or scan advances (§6.32.6). */
export function App({ deviceId }: { deviceId: string }) {
  const [queue] = useState(() => new OfflineQueue(deviceId));
  const [expected] = useState<ScanClass>("LP");
  const [value, setValue] = useState("");
  const [notice, setNotice] = useState("Scan the LP.");
  const [, bump] = useState(0);
  const input = useRef<HTMLInputElement>(null);

  function onScan(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key !== "Enter") return;
    e.preventDefault();
    const raw = value.trim();
    setValue("");
    if (raw === "") return;
    // Scans are GS1 when they carry a symbology identifier or start with a known AI on a step expecting GS1.
    const classified = classifyScan(raw, { expectGs1: true });
    // A label that fails GS1 parsing (MOB-0102..0104) is reported before a class mismatch (MOB-0101).
    const verdict = checkScan(expected, classified);
    if (verdict) {
      setNotice(`${verdict.code} ${verdict.message}`);
      return;
    }
    try {
      queue.enqueue("ConfirmWorkLine", { scan: raw, fields: classified.gs1?.fields ?? { plain: raw } });
      setNotice(`Queued. ${queue.pending().length} waiting to sync.`);
    } catch (err) {
      setNotice((err as Error).message);
    }
    bump((n) => n + 1);
    input.current?.focus();
  }

  async function onSync() {
    // replay() never throws: transport failures stop the run and leave the rest PENDING.
    const summary: ReplaySummary = await queue.replay(applyOne);
    setNotice(
      summary.stoppedBecause === "transport"
        ? "Offline: sync paused, will retry."
        : `Synced ${summary.applied}; ${summary.pending} pending (${summary.stoppedBecause}).`,
    );
    bump((n) => n + 1);
  }

  return (
    <main style={{ fontSize: 18, padding: 16, maxWidth: 480, margin: "0 auto" }}>
      <h1 style={{ fontSize: 22 }}>Receive / put-away</h1>
      <p role="status" aria-live="polite">{notice}</p>
      <label htmlFor="scan">Scan</label>
      <input
        id="scan"
        ref={input}
        autoFocus
        autoComplete="off"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={onScan}
        style={{ fontSize: 20, width: "100%", minHeight: 48 }}
      />
      <button type="button" onClick={onSync} style={{ fontSize: 18, minHeight: 48, marginTop: 16, width: "100%" }}>
        Sync {queue.pending().length} queued
      </button>
    </main>
  );
}

import { useRef, useState, type FormEvent } from "react";
import { problemSummary } from "../../shell/errors";
import { createJob, describeProblem, type PrintJob } from "./api/print";
import { labelKey, lpLabelRequest } from "./domain/receiveLabel";

export interface ReceiveLabelPanelProps {
  /** The SSCC of the plate just received; it prints as the license plate id. */
  sscc: string;
  warehouseId?: string;
  zone?: string;
  onClose(): void;
}

/**
 * Label on receive (FR-32.7, AC-32.5). After a put-away the worker scans the item's GS1 label and
 * the license plate label prints on the printer the routing rules pick for LP labels. The print
 * key names the plate, so scanning twice returns the first job instead of printing twice; a failed
 * job is retried under a new key.
 */
export function ReceiveLabelPanel({ sscc, warehouseId, zone, onClose }: ReceiveLabelPanelProps) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ tone: "error" | "info"; text: string } | null>(null);
  const [printed, setPrinted] = useState<PrintJob | null>(null);
  const attempt = useRef(0);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const raw = typed.trim();
    if (!raw) return;
    const built = lpLabelRequest(sscc, raw, { warehouseId, zone });
    if (!built.ok) {
      setMessage({ tone: "error", text: built.reason });
      return;
    }
    setBusy(true);
    try {
      const job = await createJob(built.request, labelKey(sscc, attempt.current));
      if (job.status === "PRINTED") {
        setPrinted(job);
        setMessage({ tone: "info", text: `Label printed${job.printerCode ? ` on ${job.printerCode}` : ""}.` });
      } else {
        // A failed job is stored under its key, so the retry uses the next one.
        attempt.current += 1;
        setMessage({ tone: "error", text: `The label did not print${job.lastError ? ` (${job.lastError})` : ""}. Scan again to retry.` });
      }
      setTyped("");
    } catch (error) {
      const problem = describeProblem(error);
      setMessage({ tone: "error", text: problem.code === "MOB-0112" ? "No printer is set up for LP labels. Ask a supervisor to add a routing rule." : problemSummary(problem) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="app__step" aria-label="License plate label">
      <p className="app__prompt">Print the license plate label for {sscc}</p>
      {printed ? null : (
        <form onSubmit={submit}>
          <label htmlFor="label-item-scan">Item label (GTIN, lot, expiry)</label>
          <input id="label-item-scan" autoFocus autoComplete="off" value={typed} onChange={(e) => setTyped(e.target.value)} disabled={busy} />
        </form>
      )}
      {message ? (
        <p role={message.tone === "error" ? "alert" : "status"} className={message.tone === "error" ? "app__alert" : "app__message"}>
          {message.text}
        </p>
      ) : null}
      <button type="button" className="app__button app__button--secondary" onClick={onClose}>
        {printed ? "Done" : "Skip label"}
      </button>
    </section>
  );
}

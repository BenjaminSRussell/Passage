import { apiFetch } from "../../../shell/api/http";
import { labelFromPallet, labelKey, type LpLabelRequest, type PalletLabel } from "../domain/receiveLabel";
import { LabelOutbox, flushOutbox, type FlushDeps, type FlushReport, type PrintResult } from "../domain/labelOutbox";
import { createJob, describeProblem, PrintApiError } from "./print";

/** Reads what the warehouse holds for a pallet's label. A 404 means the SSCC is not a pallet the warehouse received. */
export async function getPalletLabel(sscc: string, signal?: AbortSignal): Promise<PalletLabel> {
  const res = await apiFetch(`/api/v1/warehouse/pallets/${encodeURIComponent(sscc)}/label`, { headers: { Accept: "application/json" }, credentials: "same-origin", signal });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { code?: string; detail?: string } | null;
    throw new PrintApiError(res.status, body?.code ?? `HTTP-${res.status}`, body?.detail ?? `Request failed (HTTP ${res.status})`, {});
  }
  return (await res.json()) as PalletLabel;
}

/** A failure that another try can cure: no network, a timeout, a busy or broken service. */
export function isTransient(error: unknown): boolean {
  if (error instanceof PrintApiError) return error.status === 0 || error.status === 401 || error.status === 408 || error.status === 429 || error.status >= 500;
  return true; // a network error is a TypeError from fetch
}

const where = (warehouseId?: string) => (warehouseId ? { warehouseId } : {});

async function resolveRequest(sscc: string, warehouseId?: string): ReturnType<FlushDeps["resolve"]> {
  try {
    const built = labelFromPallet(await getPalletLabel(sscc), where(warehouseId));
    return built.ok ? { ok: true, request: built.request } : { ok: false, reason: built.reason, transient: false };
  } catch (error) {
    if (isTransient(error)) return { ok: false, reason: "The warehouse could not be reached.", transient: true };
    const problem = describeProblem(error);
    return { ok: false, reason: problem.code === "WMS-PALLET-NOT-FOUND" ? "The warehouse has no pallet with this SSCC." : problem.message, transient: false };
  }
}

async function printRequest(sscc: string, request: LpLabelRequest, attempt: number): Promise<PrintResult> {
  try {
    const job = await createJob(request, labelKey(sscc, attempt));
    if (job.status === "PRINTED") return { printed: true };
    return { printed: false, transient: true, jobFailed: true, reason: job.lastError ?? "The label did not print." };
  } catch (error) {
    const problem = describeProblem(error);
    if (isTransient(error)) return { printed: false, transient: true, reason: problem.message };
    return { printed: false, transient: false, reason: problem.code === "MOB-0112" ? "No printer is set up for LP labels." : problem.message };
  }
}

export function labelFlushDeps(warehouseId?: string): FlushDeps {
  return { resolve: (sscc) => resolveRequest(sscc, warehouseId), print: printRequest };
}

export type AutoLabelOutcome =
  | { kind: "printed" }
  /** Saved on the device; it prints when the connection is back. */
  | { kind: "queued" }
  /** The warehouse cannot give the label's data: the worker scans the item label instead. */
  | { kind: "manual"; reason: string };

/**
 * Prints the label of a pallet just received, from the warehouse's own data (#32): the SSCC the worker scanned names the
 * pallet, and the receipt holds its item, lot and expiry. Offline, or with the print service unreachable, the label waits in the
 * device's outbox and prints later; a pallet the warehouse has no label data for goes back to the item scan.
 */
export async function printPalletLabel(sscc: string, outbox: LabelOutbox, warehouseId?: string, now: () => number = Date.now): Promise<AutoLabelOutcome> {
  if (typeof navigator !== "undefined" && navigator.onLine === false) {
    outbox.enqueue(sscc, undefined, now());
    return { kind: "queued" };
  }
  const resolved = await resolveRequest(sscc, warehouseId);
  if (!resolved.ok) {
    if (resolved.transient) {
      outbox.enqueue(sscc, undefined, now());
      return { kind: "queued" };
    }
    return { kind: "manual", reason: resolved.reason };
  }
  const result = await printRequest(sscc, resolved.request, 0);
  if (result.printed) return { kind: "printed" };
  if (result.transient) {
    outbox.enqueue(sscc, resolved.request, now(), result.jobFailed ? 1 : 0);
    return { kind: "queued" };
  }
  return { kind: "manual", reason: result.reason };
}

/** Prints what waits in the outbox; call when the device is back online and on start. */
export function flushLabelOutbox(outbox: LabelOutbox, warehouseId?: string): Promise<FlushReport> {
  return flushOutbox(outbox, labelFlushDeps(warehouseId));
}

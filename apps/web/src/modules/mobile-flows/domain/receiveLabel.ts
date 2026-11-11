// Label on receive (FR-32.7, AC-32.5). After a license plate is received and put away, the device
// prints its LP label through the print service's routing rules. Pure: the label request is built
// from what the worker scanned; the HTTP call is the caller's. The LP's SSCC comes from the GS1
// scan of the plate (AI 00); lot and expiry come from the GS1 label of the item (AI 01, 10, 17).

import { classifyScan } from "./gs1";
import type { RunnerState } from "./flowRunner";

export const RECEIVE_FLOW_CODE = "RECEIVE_PUTAWAY";

/** The LP label job the print service takes (POST /print/v1/jobs). */
export interface LpLabelRequest {
  templateCode: "LP_LABEL";
  documentType: "LP_LABEL";
  warehouseId?: string;
  zone?: string;
  data: { lp: string; item: string; lot: string; expiry: string; sscc: string };
}

/** The SSCC the worker scanned as the license plate, or null when the plate is not a GS1 SSCC. */
export function receivedSscc(state: RunnerState): string | null {
  const sscc = state.fields["scan_lp"]?.["00"];
  return sscc && /^\d{18}$/.test(sscc) ? sscc : null;
}

/** Whether a finished flow should offer a label: a receive whose plate carries an SSCC. */
export function labelOffer(state: RunnerState): { sscc: string } | null {
  if (state.flowCode !== RECEIVE_FLOW_CODE) return null;
  const sscc = receivedSscc(state);
  return sscc ? { sscc } : null;
}

export type LabelResult = { ok: true; request: LpLabelRequest } | { ok: false; reason: string };

/**
 * Builds the LP label request from the plate's SSCC and a scan of the item's GS1 label. The label
 * prints the SSCC as its license plate id. A label without a GTIN, lot or expiry date cannot be
 * printed, and the reason says which is missing.
 */
export function lpLabelRequest(sscc: string, itemScan: string, where: { warehouseId?: string; zone?: string } = {}): LabelResult {
  const classified = classifyScan(itemScan, { expectGs1: true });
  const gs1 = classified.gs1;
  if (classified.kind !== "gs1" || !gs1) return { ok: false, reason: "That is not a GS1 item label. Scan the label with the item, lot and expiry date." };
  if (!gs1.ok) return { ok: false, reason: gs1.errors[0]?.message ?? "The item label could not be read." };
  const missing = [
    ["01", "item (GTIN)"],
    ["10", "lot"],
    ["17", "expiry date"],
  ]
    .filter(([ai]) => !gs1.fields[ai])
    .map(([, name]) => name);
  if (missing.length > 0) return { ok: false, reason: `The item label has no ${missing.join(" or ")}, so the label cannot be printed.` };
  return {
    ok: true,
    request: {
      templateCode: "LP_LABEL",
      documentType: "LP_LABEL",
      ...(where.warehouseId ? { warehouseId: where.warehouseId } : {}),
      ...(where.zone ? { zone: where.zone } : {}),
      data: { lp: sscc, item: gs1.fields["01"], lot: gs1.fields["10"], expiry: gs1.fields["17"], sscc },
    },
  };
}

/** One print per plate: a retried or repeated request for the same SSCC returns the stored job. */
export function labelKey(sscc: string, attempt = 0): string {
  return attempt === 0 ? `lp-label:${sscc}` : `lp-label:${sscc}:${attempt}`;
}

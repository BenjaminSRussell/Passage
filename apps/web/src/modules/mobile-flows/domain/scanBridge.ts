// Scanner intent bridge (FR-32.10, AC-32.7). The native shell (Zebra DataWedge, Honeywell Data
// Collection) receives each decoded intent and forwards it to the page as a "passage:scan"
// CustomEvent whose detail is the ScanIntent below. This module validates the detail and
// returns the raw value; the runner decides whether the focused step accepts it. Pure apart
// from the EventTarget the caller passes in.

import type { ScanSource } from "./scanRouting";

export const SCAN_EVENT = "passage:scan";

/** Longest raw scan accepted. Longer data is a fault in the capture profile, not a barcode. */
export const MAX_SCAN_LENGTH = 512;

/**
 * Intent extra that carries the decoded data, per capture bridge: DataWedge's Intent output
 * plug-in sends `com.symbol.datawedge.data_string`; the Honeywell Data Collection data intent
 * sends `data`. Both must be confirmed against the profiles provisioned on the zebra-tc52 and
 * honeywell-ct45 devices (apps/warehouse-mobile/NOT-IMPLEMENTED.md, Android shell).
 */
export const INTENT_DATA_KEYS = {
  DATAWEDGE: "com.symbol.datawedge.data_string",
  HONEYWELL_DC: "data",
} as const;

/** What the shell forwards. KEYBOARD_WEDGE carries the text directly in raw. */
export interface ScanIntent {
  source: ScanSource;
  raw?: string;
  extras?: Record<string, unknown>;
}

export type IntentResult = { ok: true; raw: string; source: ScanSource } | { ok: false; reason: string };

function fail(reason: string): IntentResult {
  return { ok: false, reason };
}

function checkRaw(value: unknown, source: ScanSource): IntentResult {
  if (typeof value !== "string" || value.length === 0) return fail("scan carries no data");
  if (value.length > MAX_SCAN_LENGTH) return fail(`scan longer than ${MAX_SCAN_LENGTH} characters`);
  return { ok: true, raw: value, source };
}

/**
 * Validates a forwarded intent. The symbology prefix (for example "]C1") is kept as sent:
 * the scan rule reads it (BR-32.1), so stripping it here would hide GS1 data.
 */
export function parseIntent(detail: unknown): IntentResult {
  if (typeof detail !== "object" || detail === null) return fail("intent is not an object");
  const d = detail as Partial<ScanIntent>;
  if (d.source === "KEYBOARD_WEDGE") return checkRaw(d.raw, "KEYBOARD_WEDGE");
  if (d.source !== "DATAWEDGE" && d.source !== "HONEYWELL_DC") {
    return fail(`unknown capture source ${String(d.source)}`);
  }
  if (typeof d.extras !== "object" || d.extras === null) return fail("intent has no extras");
  return checkRaw(d.extras[INTENT_DATA_KEYS[d.source]], d.source);
}

/** Subscribes to forwarded intents. Returns the unsubscribe function. */
export function listenForScans(target: EventTarget, handler: (result: IntentResult) => void): () => void {
  const listener = (event: Event) => handler(parseIntent((event as CustomEvent).detail));
  target.addEventListener(SCAN_EVENT, listener);
  return () => target.removeEventListener(SCAN_EVENT, listener);
}

// Scan acceptance on the device (FR-32.10, BR-32.1, BR-32.2, AC-32.7). Pure: no I/O, no React.
// A scan counts only on the focused scan step; anywhere else it is ignored, and the caller logs
// it. An accepted scan is classified (GS1 or plain) and must satisfy one of the step's classes.

import type { StepType } from "./flowGraph";
import { checkScan, CLASS_LABEL, classifyScan, describeScan, type Classified, type ParseOptions, type ScanClass } from "./gs1";

/** Capture bridge that produced the scan (§6.32.13 scanner_profile). */
export type ScanSource = "DATAWEDGE" | "HONEYWELL_DC" | "KEYBOARD_WEDGE";

/** The step that has focus when the scan arrives. */
export interface FocusedStep {
  type: StepType;
  expectedScan?: readonly ScanClass[];
}

/** Warehouse scan settings (§6.32.13 gs1_require_symbology_id, gs1_fnc1_substitute). */
export interface ScanOptions extends ParseOptions {
  requireSymbologyId?: boolean;
}

export type ScanDecision =
  | { kind: "accepted"; classified: Classified; scanClass: ScanClass }
  | { kind: "rejected"; code: string; message: string; classified: Classified }
  | { kind: "ignored"; reason: string };

/**
 * Decides one scan:
 * 1. No focused step, or a step that is not a scan step: ignored (AC-32.7). The caller logs it.
 * 2. The raw value is classified. Unprefixed text is read as GS1 only when one of the step's
 *    classes can carry GS1 data (LP, GTIN, ANY), so a location such as "10-01-02" is never AI 10.
 * 3. The scan is accepted by the first expected class it satisfies. Otherwise a GS1 parse error
 *    (MOB-0102..0104) is reported before a class mismatch (MOB-0101), as in checkScan.
 */
export function routeScan(raw: string, step: FocusedStep | null, opts: ScanOptions = {}): ScanDecision {
  if (!step || step.type !== "scan") {
    return { kind: "ignored", reason: `Scan ignored: the focused step is ${step ? `a ${step.type} step` : "missing"}, not a scan step.` };
  }
  const expected: readonly ScanClass[] = step.expectedScan && step.expectedScan.length > 0 ? step.expectedScan : ["ANY"];
  const expectGs1 = expected.some((c) => c !== "LOCATION");
  const classified = classifyScan(raw, { ...opts, expectGs1 });
  const verdicts = expected.map((c) => ({ scanClass: c, verdict: checkScan(c, classified) }));
  const match = verdicts.find((v) => v.verdict === null);
  if (match) return { kind: "accepted", classified, scanClass: match.scanClass };
  const first = verdicts[0].verdict as { code: string; message: string };
  if (first.code !== "MOB-0101" || expected.length === 1) return { kind: "rejected", code: first.code, message: first.message, classified };
  const wanted = expected.map((c) => CLASS_LABEL[c]).join(" or ");
  return { kind: "rejected", code: "MOB-0101", message: `Expected ${wanted}, scanned ${describeScan(classified)}.`, classified };
}

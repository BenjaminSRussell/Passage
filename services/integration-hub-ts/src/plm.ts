// PLM import precedence (FR-35.15, BR-35.16). Pure: the connector reads the PLM
// change feed, asks decideEcoImport what to do with each engineering change order,
// and performs the write (create ECR, create task, or nothing). No I/O here.

/** Revision schemes a PLM system may use (ordering per scheme). */
export type RevisionScheme = "alpha" | "numeric";

/** Scheduled sync default and range (§6.35.13 plm_sync_interval_min). */
export const PLM_SYNC_DEFAULT_MIN = 15;
export const PLM_SYNC_MIN = 1;
export const PLM_SYNC_MAX = 1440;

/** EDI-0139: an ECO effectivity earlier than today is moved to today. */
export const CODE_EFFECTIVITY_MOVED = "EDI-0139";

const ISO_DAY = /^\d{4}-\d{2}-\d{2}$/;

/** True for a real calendar day written YYYY-MM-DD (rejects 2026-02-30). */
function isIsoDay(s: string): boolean {
  if (!ISO_DAY.test(s)) return false;
  const d = new Date(`${s}T00:00:00Z`);
  return !Number.isNaN(d.getTime()) && d.toISOString().slice(0, 10) === s;
}

/** Throws RangeError when the sync interval is outside 1..1440 minutes. */
export function validateSyncInterval(minutes: number): void {
  if (!Number.isInteger(minutes) || minutes < PLM_SYNC_MIN || minutes > PLM_SYNC_MAX) {
    throw new RangeError(`plm_sync_interval_min must be an integer in ${PLM_SYNC_MIN}..${PLM_SYNC_MAX}, got ${minutes}`);
  }
}

/** Throws RangeError when the revision does not follow the scheme's format. */
export function validateRevision(revision: string, scheme: RevisionScheme): void {
  const ok = scheme === "alpha" ? /^[A-Z]+$/.test(revision) : /^\d+$/.test(revision);
  if (!ok) {
    throw new RangeError(`revision ${JSON.stringify(revision)} is not a valid ${scheme} revision`);
  }
}

/**
 * Orders two revisions of one scheme. Alpha revisions order by length, then
 * letter (A < B < Z < AA < AB). Numeric revisions order by integer value.
 * Returns a negative number, zero or a positive number like a comparator.
 */
export function compareRevisions(a: string, b: string, scheme: RevisionScheme): number {
  validateRevision(a, scheme);
  validateRevision(b, scheme);
  if (scheme === "numeric") {
    return Number(BigInt(a) - BigInt(b));
  }
  if (a.length !== b.length) {
    return a.length - b.length;
  }
  return a < b ? -1 : a > b ? 1 : 0;
}

/** One engineering change order as read from the PLM system. */
export interface PlmEco {
  ecoId: string;
  partNumber: string;
  revision: string;
  bomVersion: string;
  effectivityDate: string; // YYYY-MM-DD
}

/** What Passage already holds for the part when the ECO arrives. */
export interface ImportState {
  /** Keys "partNumber|revision" already imported. */
  importedKeys: ReadonlySet<string>;
  /** Latest imported revision of the part, if any. */
  latestRevision?: string;
  /** BOM versions of the part already released in Passage; these are never overwritten. */
  releasedBomVersions: ReadonlySet<string>;
}

export type ImportAction = "CREATE_ECR" | "DUPLICATE" | "STALE" | "TASK";

export interface ImportDecision {
  action: ImportAction;
  /** Effectivity to use when action is CREATE_ECR (today or the original date). */
  effectivityDate: string;
  /** Codes raised while deciding (EDI-0139 for a moved effectivity). */
  issues: string[];
  /** Human-readable reason for TASK, STALE and DUPLICATE. */
  reason?: string;
}

/** Key used for idempotent import of one part revision. */
export function importKey(partNumber: string, revision: string): string {
  return `${partNumber}|${revision}`;
}

/**
 * Applies BR-35.16 to one ECO. Order of checks:
 *  1. The same part and revision already imported: DUPLICATE, nothing changes.
 *  2. A revision older than the latest imported one: STALE, nothing changes.
 *  3. A BOM version already released in Passage: TASK, never an overwrite.
 *  4. Effectivity before today: moved to today with EDI-0139.
 * `today` is a YYYY-MM-DD date in the tenant's business calendar.
 */
export function decideEcoImport(
  eco: PlmEco,
  today: string,
  state: ImportState,
  scheme: RevisionScheme,
): ImportDecision {
  if (!isIsoDay(eco.effectivityDate) || !isIsoDay(today)) {
    throw new RangeError("effectivity and today must be YYYY-MM-DD");
  }
  validateRevision(eco.revision, scheme);
  if (state.importedKeys.has(importKey(eco.partNumber, eco.revision))) {
    return { action: "DUPLICATE", effectivityDate: eco.effectivityDate, issues: [], reason: "already imported" };
  }
  if (state.latestRevision !== undefined && compareRevisions(eco.revision, state.latestRevision, scheme) < 0) {
    return {
      action: "STALE",
      effectivityDate: eco.effectivityDate,
      issues: [],
      reason: `revision ${eco.revision} is older than imported revision ${state.latestRevision}`,
    };
  }
  if (state.releasedBomVersions.has(eco.bomVersion)) {
    return {
      action: "TASK",
      effectivityDate: eco.effectivityDate,
      issues: [],
      reason: `BOM version ${eco.bomVersion} is already released in Passage`,
    };
  }
  if (eco.effectivityDate < today) {
    return { action: "CREATE_ECR", effectivityDate: today, issues: [CODE_EFFECTIVITY_MOVED] };
  }
  return { action: "CREATE_ECR", effectivityDate: eco.effectivityDate, issues: [] };
}

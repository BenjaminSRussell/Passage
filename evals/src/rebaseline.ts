/**
 * Dataset versioning and re-baselining (BR-47.8). A change to an eval dataset
 * invalidates every agent version's earlier result: each version must be
 * evaluated again on the new dataset version before it can be promoted.
 */

export interface DatasetRef {
  datasetId: string;
  datasetVersion: number;
}

export interface AgentVersionRef {
  agentId: string;
  agentVersion: number;
}

/** One persisted eval_run (9.47.4), reduced to what promotion needs. */
export interface EvalRecord extends AgentVersionRef, DatasetRef {
  passed: boolean;
}

export interface PromotionCheck {
  allowed: boolean;
  reasons: string[];
}

/** True when the dataset id is unchanged but its version moved (a re-baseline trigger). */
export function datasetChanged(previous: DatasetRef, next: DatasetRef): boolean {
  if (previous.datasetId !== next.datasetId) {
    throw new Error(`dataset ids differ: ${previous.datasetId} vs ${next.datasetId}`);
  }
  return previous.datasetVersion !== next.datasetVersion;
}

function hasPassingRecord(records: readonly EvalRecord[], v: AgentVersionRef, dataset: DatasetRef): boolean {
  return records.some(
    (r) =>
      r.passed &&
      r.agentId === v.agentId &&
      r.agentVersion === v.agentVersion &&
      r.datasetId === dataset.datasetId &&
      r.datasetVersion === dataset.datasetVersion,
  );
}

/** Agent versions that still need a passing eval on the current dataset version. */
export function rebaselineQueue(
  versions: readonly AgentVersionRef[],
  dataset: DatasetRef,
  records: readonly EvalRecord[],
): AgentVersionRef[] {
  return versions.filter((v) => !hasPassingRecord(records, v, dataset));
}

/**
 * Promotion gate: the candidate may move to production only with a passing
 * eval on the dataset version in force now (AGT-0204 otherwise).
 */
export function canPromote(candidate: AgentVersionRef, dataset: DatasetRef, records: readonly EvalRecord[]): PromotionCheck {
  if (hasPassingRecord(records, candidate, dataset)) return { allowed: true, reasons: [] };
  return {
    allowed: false,
    reasons: [
      `AGT-0204: ${candidate.agentId} v${candidate.agentVersion} has no passing evaluation on ${dataset.datasetId} v${dataset.datasetVersion}`,
    ],
  };
}

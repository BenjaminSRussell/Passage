import { createHash } from "node:crypto";
import { assertNoPii } from "./redaction.js";

/** 9.47.4 agent_action.output_json cap. */
export const OUTPUT_CAP_BYTES = 64 * 1024;

/** APPROVED and AUTO commit; REJECTED and DENIED never do. */
export type Decision = "APPROVED" | "REJECTED" | "AUTO" | "DENIED";

export interface ActionRecord {
  runId: string;
  seq: number;
  toolName: string;
  inputJson: string;
  outputHash: string;
  outputJson: string;
  outputTruncated: boolean;
  approverUserId: string | null;
  decision: Decision;
  decidedAt: string;
  committedEntityRef: string | null;
  reason: string | null;
}

export interface ActionInput {
  runId: string;
  seq: number;
  toolName: string;
  /** Already pseudonymized by the run worker. */
  inputRedacted: string;
  /** Already pseudonymized by the run worker. */
  outputRedacted: string;
  approverUserId: string | null;
  decision: Decision;
  decidedAt: string;
  committedEntityRef?: string | null;
  reason?: string;
}

/**
 * Builds one audit row. The write-time scanner rejects raw PII (AGT-0210), and
 * a committed reference is allowed only for APPROVED or AUTO decisions.
 */
export function buildActionRecord(a: ActionInput): ActionRecord {
  assertNoPii(a.inputRedacted);
  assertNoPii(a.outputRedacted);
  const committed = a.committedEntityRef ?? null;
  if (committed !== null && a.decision !== "APPROVED" && a.decision !== "AUTO") {
    throw new Error(`committed_entity_ref requires an APPROVED or AUTO decision, got ${a.decision}`);
  }
  const bytes = Buffer.from(a.outputRedacted, "utf8");
  const truncated = bytes.byteLength > OUTPUT_CAP_BYTES;
  return {
    runId: a.runId,
    seq: a.seq,
    toolName: a.toolName,
    inputJson: a.inputRedacted,
    outputHash: createHash("sha256").update(a.outputRedacted).digest("hex"),
    outputJson: truncated ? bytes.subarray(0, OUTPUT_CAP_BYTES).toString("utf8") : a.outputRedacted,
    outputTruncated: truncated,
    approverUserId: a.approverUserId,
    decision: a.decision,
    decidedAt: a.decidedAt,
    committedEntityRef: committed,
    reason: a.reason ?? null,
  };
}

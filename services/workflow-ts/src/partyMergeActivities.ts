import { ApplicationFailure, Context } from "@temporalio/activity";
import { mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";
import type { PartyMergeRef } from "./workflows.js";

export interface PartyMergeDecision extends PartyMergeRef {
  decision: "approved" | "rejected";
  /** The approver the decision route authenticated; party records it as the decider and refuses the requester. */
  decidedBy: string;
}

export interface PartyMergeClient {
  decideMerge(decision: PartyMergeDecision, signal?: AbortSignal): Promise<void>;
}

export interface PartyMergeActivities {
  decidePartyMerge(decision: PartyMergeDecision): Promise<void>;
}

type Log = (message: string, attributes: Record<string, string>) => void;

const CODE_MERGE_STALE = "PTY-MERGE-STALE";
const CODE_MERGE_DECIDED = "PTY-MERGE-DECIDED";
export const STALE_MERGE_REASON = "stale: parties changed since request";

const stdoutLog: Log = (message, attributes) => {
  process.stdout.write(`${JSON.stringify({ level: "warn", message, ...attributes })}\n`);
};

export function createPartyMergeActivities(party: PartyMergeClient): PartyMergeActivities {
  return { decidePartyMerge: (decision) => party.decideMerge(decision, Context.current().cancellationSignal) };
}

/**
 * Calls party `POST /v1/party/merge-requests/{id}:approve|:reject` under a tenant-level workflow-ts token
 * (`party.merge.execute`, no legal entity, act = the approver). Approval executes the merge in party; a stale
 * request (the parties changed since it was made) is rejected instead, as the credit-limit client does.
 */
export function createPartyMergeHttpClient(
  baseUrl: string,
  signer: ServiceTokenSigner,
  fetchImpl: typeof fetch = fetch,
  log: Log = stdoutLog,
): PartyMergeClient {
  async function post(decision: PartyMergeDecision, action: "approve" | "reject", signal?: AbortSignal): Promise<Response> {
    const token = mintOrReject(() => signer.mintForPartyMerge({ tenantId: decision.tenantId, actor: decision.decidedBy }));
    const reject = action === "reject" && decision.decision === "approved";
    return fetchImpl(`${baseUrl}/v1/party/merge-requests/${encodeURIComponent(decision.mergeRequestId)}:${action}`, {
      method: "POST",
      headers: { "X-Passage-Service-Token": token, ...(reject ? { "Content-Type": "application/json" } : {}) },
      ...(reject ? { body: JSON.stringify({ reason: STALE_MERGE_REASON }) } : {}),
      signal,
    });
  }

  async function problemCode(response: Response): Promise<string | undefined> {
    try {
      return ((await response.json()) as { code?: string }).code;
    } catch {
      return undefined;
    }
  }

  // As for party changes: a refused service token (401, or 403 with an AUTH- code) is retried; any other 4xx, such as
  // the requester deciding their own merge, is final.
  function failure(action: string, decision: PartyMergeDecision, status: number, code?: string): Error {
    const message = `party ${action} of merge request ${decision.mergeRequestId} failed with ${status}${code ? ` ${code}` : ""}`;
    const authFault = (status === 401 || status === 403) && (!code || code.startsWith("AUTH-"));
    if (status >= 400 && status < 500 && status !== 429 && !authFault) {
      return ApplicationFailure.nonRetryable(message, "PartyMergeDecisionRejected");
    }
    return new Error(message);
  }

  return {
    async decideMerge(decision, signal) {
      const action = decision.decision === "approved" ? "approve" : "reject";
      const response = await post(decision, action, signal);
      if (response.ok) return;
      const code = response.status >= 400 && response.status < 500 ? await problemCode(response) : undefined;
      if (code === CODE_MERGE_DECIDED) {
        log("party merge already decided", { mergeRequestId: decision.mergeRequestId, action });
        return;
      }
      if (code === CODE_MERGE_STALE && action === "approve") {
        const rejected = await post(decision, "reject", signal);
        if (rejected.ok) {
          log("party merge stale, rejected instead of approved", { mergeRequestId: decision.mergeRequestId });
          return;
        }
        const rejectCode = rejected.status >= 400 && rejected.status < 500 ? await problemCode(rejected) : undefined;
        if (rejectCode === CODE_MERGE_DECIDED) {
          log("party merge already decided", { mergeRequestId: decision.mergeRequestId, action: "reject" });
          return;
        }
        throw failure("reject", decision, rejected.status, rejectCode);
      }
      throw failure(action, decision, response.status, code);
    },
  };
}

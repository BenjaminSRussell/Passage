import { ApplicationFailure, Context } from "@temporalio/activity";
import { mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";
import type { PartyChangeRef } from "./workflows.js";

export interface PartyDecision extends PartyChangeRef {
  decision: "approved" | "rejected";
  /** The approver the decision route authenticated; party records it as the decider and refuses the requester. */
  decidedBy: string;
  /** That approver's legal-entity scope; the service token names the change's legal entity only when it is inside it. */
  scope: string[];
}

export interface PartyClient {
  decide(decision: PartyDecision, signal?: AbortSignal): Promise<void>;
}

export interface PartyDecisionActivities {
  decidePartyChange(decision: PartyDecision): Promise<void>;
}

type Log = (message: string, attributes: Record<string, string>) => void;

const CODE_CHANGE_STALE = "PTY-PENDING-STALE";
const CODE_CHANGE_DECIDED = "PTY-CHANGE-DECIDED";
export const STALE_REJECT_REASON = "stale: limit changed since request";

const stdoutLog: Log = (message, attributes) => {
  process.stdout.write(`${JSON.stringify({ level: "warn", message, ...attributes })}\n`);
};

export function createActivities(party: PartyClient): PartyDecisionActivities {
  return { decidePartyChange: (decision) => party.decide(decision, Context.current().cancellationSignal) };
}

export function createPartyHttpClient(
  baseUrl: string,
  signer: ServiceTokenSigner,
  fetchImpl: typeof fetch = fetch,
  log: Log = stdoutLog,
): PartyClient {
  async function post(decision: PartyDecision, action: "approve" | "reject", signal?: AbortSignal): Promise<Response> {
    const token = mintOrReject(() =>
      signer.mintForParty({ tenantId: decision.tenantId, legalEntityId: decision.legalEntityId, actor: decision.decidedBy, scope: decision.scope }),
    );
    return fetchImpl(`${baseUrl}/v1/party/pending-changes/${encodeURIComponent(decision.changeId)}:${action}`, {
      method: "POST",
      headers: {
        "X-Passage-Service-Token": token,
        ...(action === "reject" ? { "Content-Type": "application/json" } : {}),
      },
      ...(action === "reject" ? { body: JSON.stringify({ reason: STALE_REJECT_REASON }) } : {}),
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

  // A refused service token (401, or 403 with an AUTH- code: a key or grant not deployed yet) is retried like the other
  // signed calls; any other 4xx, such as the requester deciding their own change, is final.
  function failure(action: string, decision: PartyDecision, status: number, code?: string): Error {
    const message = `party ${action} of change ${decision.changeId} failed with ${status}${code ? ` ${code}` : ""}`;
    const authFault = (status === 401 || status === 403) && (!code || code.startsWith("AUTH-"));
    if (status >= 400 && status < 500 && status !== 429 && !authFault) {
      return ApplicationFailure.nonRetryable(message, "PartyDecisionRejected");
    }
    return new Error(message);
  }

  return {
    async decide(decision, signal) {
      const action = decision.decision === "approved" ? "approve" : "reject";
      const response = await post(decision, action, signal);
      if (response.ok) return;
      const code = response.status >= 400 && response.status < 500 ? await problemCode(response) : undefined;
      if (code === CODE_CHANGE_DECIDED) {
        log("party change already decided", { changeId: decision.changeId, action });
        return;
      }
      if (code === CODE_CHANGE_STALE && action === "approve") {
        const rejected = await post(decision, "reject", signal);
        if (rejected.ok) {
          log("party change stale, rejected instead of approved", { changeId: decision.changeId });
          return;
        }
        if (rejected.status === 409 && (await problemCode(rejected)) === CODE_CHANGE_DECIDED) {
          log("party change already decided", { changeId: decision.changeId, action: "reject" });
          return;
        }
        throw failure("reject", decision, rejected.status);
      }
      throw failure(action, decision, response.status, code);
    },
  };
}

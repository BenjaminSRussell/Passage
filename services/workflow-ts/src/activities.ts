import { ApplicationFailure } from "@temporalio/client";
import type { PartyChangeRef } from "./workflows.js";

export interface PartyDecision extends PartyChangeRef {
  decision: "approved" | "rejected";
  decidedBy: string;
}

export interface PartyClient {
  decide(decision: PartyDecision): Promise<void>;
}

export interface PartyDecisionActivities {
  decidePartyChange(decision: PartyDecision): Promise<void>;
}

export function createActivities(party: PartyClient): PartyDecisionActivities {
  return { decidePartyChange: (decision) => party.decide(decision) };
}

export function createPartyHttpClient(baseUrl: string, fetchImpl: typeof fetch = fetch): PartyClient {
  return {
    async decide(decision) {
      const action = decision.decision === "approved" ? "approve" : "reject";
      const response = await fetchImpl(
        `${baseUrl}/v1/party/pending-changes/${encodeURIComponent(decision.changeId)}:${action}`,
        {
          method: "POST",
          headers: {
            "X-Passage-Tenant": decision.tenantId,
            "X-Passage-Legal-Entity": decision.legalEntityId,
            "X-Passage-User": decision.decidedBy,
            "X-Passage-Duties": "party.approve",
          },
        },
      );
      if (response.ok) return;
      const message = `party ${action} of change ${decision.changeId} failed with ${response.status}`;
      if (response.status >= 400 && response.status < 500 && response.status !== 429) {
        throw ApplicationFailure.nonRetryable(message, "PartyDecisionRejected");
      }
      throw new Error(message);
    },
  };
}

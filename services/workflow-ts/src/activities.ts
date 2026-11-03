import { Context } from "@temporalio/activity";
import type { PartyChangeRef } from "./workflows.js";

export interface PartyDecision extends PartyChangeRef {
  decision: "approved" | "rejected";
  decidedBy: string;
}

export interface PartyClient {
  decide(decision: PartyDecision, signal?: AbortSignal): Promise<void>;
}

export interface PartyDecisionActivities {
  decidePartyChange(decision: PartyDecision): Promise<void>;
}

export function createActivities(party: PartyClient): PartyDecisionActivities {
  return { decidePartyChange: (decision) => party.decide(decision, Context.current().cancellationSignal) };
}

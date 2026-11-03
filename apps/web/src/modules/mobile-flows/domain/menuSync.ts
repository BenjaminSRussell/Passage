// Device menu updates (FR-32.2, AC-32.4). Pure: a published graph replaces the bundled one
// with the same code. A runner that already started a flow keeps the graph it started
// (BR-32.9); only the next menu load uses the new version.

import type { SavedFlow } from "../api/client";
import type { FlowDraft } from "./flowGraph";

/** Poll interval: devices see a publish within 60 s even if SSE drops (FR-32.2). */
export const MENU_POLL_MS = 30_000;

export function asDraft(flow: SavedFlow): FlowDraft {
  return { code: flow.code, entryStepId: flow.entryStepId, steps: flow.steps };
}

/** Published graphs in the order the menu returned them. */
export function publishedDrafts(flows: readonly SavedFlow[]): FlowDraft[] {
  return flows.filter((f) => f.status === "PUBLISHED").map(asDraft);
}

/**
 * Replaces the device's menu with the published set. Codes not in `incoming` are dropped so a
 * retired flow disappears; the caller must not swap the in-flight graph (BR-32.9).
 */
export function applyPublishedFlows(_current: readonly FlowDraft[], incoming: readonly SavedFlow[]): FlowDraft[] {
  return publishedDrafts(incoming);
}
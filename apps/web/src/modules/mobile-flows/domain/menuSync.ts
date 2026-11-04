// Device menu updates (FR-32.2, AC-32.4). Pure: a published graph replaces the bundled one
// with the same code. A runner that already started a flow keeps the graph it started
// (BR-32.9); only the next start (menu pick or restart after confirm) uses the new version.

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

/** Replaces the device's menu with the published set. Codes not in `incoming` are dropped. */
export function applyPublishedFlows(incoming: readonly SavedFlow[]): FlowDraft[] {
  return publishedDrafts(incoming);
}

/**
 * Graph to start after a confirm (or a single-flow auto-restart). The in-flight runner keeps
 * `current` until this is called (BR-32.9). The next run takes the latest published code.
 */
export function graphForRestart(menu: readonly FlowDraft[], current: FlowDraft): FlowDraft {
  return menu.find((f) => f.code === current.code) ?? current;
}

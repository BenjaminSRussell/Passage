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
 * The graph the next run of `running` should use: the newest menu entry with the same code.
 * The run in progress keeps its own graph (BR-32.9); only a restart switches.
 */
export function latestGraph(menu: readonly FlowDraft[], running: FlowDraft): FlowDraft {
  return menu.find((f) => f.code === running.code) ?? running;
}

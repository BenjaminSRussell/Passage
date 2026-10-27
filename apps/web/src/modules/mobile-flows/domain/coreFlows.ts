// Core flow definitions bundled with the device app (§6.32.9 flows-core-v1). Published flows
// will come from the mobile service (a handoff); until then the device runs this bundled
// graph, and the designer opens it as its starting point. Only receive/put-away is bundled.

import type { FlowDraft } from "./flowGraph";
import type { RunnerState } from "./flowRunner";

/** Receive and put away one license plate to a location (FR-32.4). */
export const RECEIVE_PUTAWAY_FLOW: FlowDraft = {
  code: "RECEIVE_PUTAWAY",
  entryStepId: "scan_lp",
  steps: [
    { id: "scan_lp", type: "scan", prompt: "Scan the license plate", expectedScan: ["LP"], next: { default: "scan_location" } },
    { id: "scan_location", type: "scan", prompt: "Scan the put-away location", expectedScan: ["LOCATION"], next: { default: "confirm_putaway" } },
    { id: "confirm_putaway", type: "confirm", prompt: "Confirm put-away", next: { default: null } },
  ],
};

/** Work line confirmation command queued for replay (FR-32.5, BR-32.3, BR-32.4). */
export const CONFIRM_WORK_LINE = "ConfirmWorkLine";

/** The ConfirmWorkLine payload for a finished flow: what was captured at each step. */
export function confirmationPayload(state: RunnerState): Record<string, unknown> {
  return { flowCode: state.flowCode, steps: state.history, values: { ...state.values }, fields: { ...state.fields } };
}

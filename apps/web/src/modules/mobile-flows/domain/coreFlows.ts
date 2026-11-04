// Core flow definitions bundled with the device app (§6.32.9 flows-core-v1, FR-32.4): receive
// and put-away, pick, cluster pick, pack, move, count and inquiry. Published flows will come
// from the mobile service (a handoff); until then the device menu offers these bundled graphs,
// and the designer opens any of them as its starting point. Each passes validateFlow (tested).
// The flows capture what the worker scans and enters; they do not yet read the expected work
// from the wave-31 work API, so a scan is checked against its class, not against the work line.

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

/**
 * Work-directed pick of one work line (FR-32.4, BR-32.4). A short pick takes the short-reason
 * path (FR-32.8) and still completes the line.
 */
export const PICK_FLOW: FlowDraft = {
  code: "PICK",
  entryStepId: "scan_location",
  steps: [
    { id: "scan_location", type: "scan", prompt: "Scan the pick location", expectedScan: ["LOCATION"], next: { default: "scan_item" } },
    { id: "scan_item", type: "scan", prompt: "Scan the item", expectedScan: ["GTIN"], next: { default: "enter_qty" } },
    { id: "enter_qty", type: "input", prompt: "Quantity picked", next: { default: "qty_check" } },
    { id: "qty_check", type: "branch", prompt: "Was the full quantity picked?", next: { full: "confirm_pick", short: "short_reason" } },
    { id: "short_reason", type: "select", prompt: "Short pick reason", next: { default: "confirm_pick" } },
    { id: "confirm_pick", type: "confirm", prompt: "Confirm pick", next: { default: null } },
  ],
};

/**
 * Cluster pick of up to eight orders onto cart positions (FR-32.4, BR-32.5). Each position is
 * bound to one order's LP; the flow loops until the worker is done with the cart.
 */
export const CLUSTER_PICK_FLOW: FlowDraft = {
  code: "PICK_CLUSTER",
  entryStepId: "scan_cart_lp",
  steps: [
    { id: "scan_cart_lp", type: "scan", prompt: "Scan the cart position LP", expectedScan: ["LP"], next: { default: "scan_item" } },
    { id: "scan_item", type: "scan", prompt: "Scan the item", expectedScan: ["GTIN"], next: { default: "enter_qty" } },
    { id: "enter_qty", type: "input", prompt: "Quantity for this position", next: { default: "confirm_position" } },
    { id: "confirm_position", type: "confirm", prompt: "Confirm the position", next: { default: "more_positions" } },
    { id: "more_positions", type: "branch", prompt: "Another cart position?", next: { more: "scan_cart_lp", done: null } },
  ],
};

/** Pack one item into a shipping container (FR-32.4, §6.32.2 packing station). */
export const PACK_FLOW: FlowDraft = {
  code: "PACK",
  entryStepId: "scan_container",
  steps: [
    { id: "scan_container", type: "scan", prompt: "Scan the shipping container", expectedScan: ["LP"], next: { default: "scan_item" } },
    { id: "scan_item", type: "scan", prompt: "Scan the item to pack", expectedScan: ["GTIN"], next: { default: "enter_qty" } },
    { id: "enter_qty", type: "input", prompt: "Quantity packed", next: { default: "confirm_pack" } },
    { id: "confirm_pack", type: "confirm", prompt: "Confirm the pack", next: { default: null } },
  ],
};

/** Move a license plate, or part of it, to another location (FR-32.4). */
export const MOVE_FLOW: FlowDraft = {
  code: "MOVE",
  entryStepId: "scan_source_lp",
  steps: [
    { id: "scan_source_lp", type: "scan", prompt: "Scan the license plate to move", expectedScan: ["LP"], next: { default: "enter_qty" } },
    { id: "enter_qty", type: "input", prompt: "Quantity to move", next: { default: "scan_destination" } },
    { id: "scan_destination", type: "scan", prompt: "Scan the destination location", expectedScan: ["LOCATION"], next: { default: "confirm_move" } },
    { id: "confirm_move", type: "confirm", prompt: "Confirm the move", next: { default: null } },
  ],
};

/** Count a location's stock, planned or spot (FR-32.4). Offline is off by default (§6.32.13). */
export const COUNT_FLOW: FlowDraft = {
  code: "COUNT",
  entryStepId: "scan_location",
  steps: [
    { id: "scan_location", type: "scan", prompt: "Scan the location to count", expectedScan: ["LOCATION"], next: { default: "scan_item" } },
    { id: "scan_item", type: "scan", prompt: "Scan the item counted", expectedScan: ["GTIN"], next: { default: "enter_count" } },
    { id: "enter_count", type: "input", prompt: "Counted quantity", next: { default: "confirm_count" } },
    { id: "confirm_count", type: "confirm", prompt: "Confirm the count", next: { default: null } },
  ],
};

/** Read-only inquiry on an item, location or LP (FR-32.4). */
export const INQUIRY_FLOW: FlowDraft = {
  code: "INQUIRY",
  entryStepId: "scan_query",
  steps: [
    { id: "scan_query", type: "scan", prompt: "Scan an item, location or LP", expectedScan: ["ANY"], next: { default: "show_result" } },
    { id: "show_result", type: "display", prompt: "Stock and status", next: { default: null } },
  ],
};

/** The seven core flows of flows-core-v1, in menu order. */
export const CORE_FLOWS: readonly FlowDraft[] = [
  RECEIVE_PUTAWAY_FLOW,
  PICK_FLOW,
  CLUSTER_PICK_FLOW,
  PACK_FLOW,
  MOVE_FLOW,
  COUNT_FLOW,
  INQUIRY_FLOW,
];

/**
 * Whether a flow may queue offline by default (§6.32.13 `offline_enabled`: true for pick and
 * put, false for count). A replayed count can apply stale quantities (§6.32.12), and an
 * inquiry reads live stock and has nothing to queue, so both are online only.
 */
export function offlineEnabledByDefault(flowCode: string): boolean {
  return flowCode !== COUNT_FLOW.code && flowCode !== INQUIRY_FLOW.code;
}

/** What the device does when a flow reaches its end. */
export type Completion = { kind: "queue" } | { kind: "none" } | { kind: "blocked"; message: string };

/**
 * Decides how a finished flow is confirmed (FR-32.5, §6.32.13 `offline_enabled`). An inquiry
 * only reads, so nothing is queued. A flow that is online only (count) is not queued while the
 * device is offline: the worker stays on the last step and confirms again once connected.
 * Everything else is queued as ConfirmWorkLine for ordered replay.
 */
export function completionFor(flowCode: string, online: boolean): Completion {
  if (flowCode === INQUIRY_FLOW.code) return { kind: "none" };
  if (!online && !offlineEnabledByDefault(flowCode)) {
    return { kind: "blocked", message: "This flow needs a connection: reconnect, then confirm again." };
  }
  return { kind: "queue" };
}

/** Work line confirmation command queued for replay (FR-32.5, BR-32.3, BR-32.4). */
export const CONFIRM_WORK_LINE = "ConfirmWorkLine";

/** The ConfirmWorkLine payload for a finished flow: what was captured at each step. */
export function confirmationPayload(state: RunnerState): Record<string, unknown> {
  return { flowCode: state.flowCode, steps: state.history, values: { ...state.values }, fields: { ...state.fields } };
}

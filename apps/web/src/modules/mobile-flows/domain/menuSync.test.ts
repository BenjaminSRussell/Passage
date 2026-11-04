import { describe, expect, it } from "vitest";
import type { SavedFlow } from "../api/client";
import { PICK_FLOW, RECEIVE_PUTAWAY_FLOW } from "./coreFlows";
import { latestGraph, MENU_POLL_MS, publishedDrafts } from "./menuSync";
import type { FlowDraft } from "./flowGraph";

const published = (flow: FlowDraft, version: number): SavedFlow => ({
  ...flow,
  flowId: flow.code,
  version,
  status: "PUBLISHED",
});

describe("menuSync (AC-32.4)", () => {
  it("polls well inside the 60 s SLA so a publish reaches the device without an app release", () => {
    expect(MENU_POLL_MS).toBeGreaterThan(0);
    expect(MENU_POLL_MS).toBeLessThanOrEqual(60_000);
  });

  it("drops unpublished rows and keeps published graphs in menu order", () => {
    const draft: SavedFlow = { ...PICK_FLOW, flowId: "PICK", version: 2, status: "DRAFT" };
    expect(publishedDrafts([published(RECEIVE_PUTAWAY_FLOW, 1), draft, published(PICK_FLOW, 1)])).toEqual([
      RECEIVE_PUTAWAY_FLOW,
      PICK_FLOW,
    ]);
  });

  it("switches a restart to the newest menu entry with the same code and keeps the running graph otherwise", () => {
    const v2: FlowDraft = { ...RECEIVE_PUTAWAY_FLOW, steps: [...RECEIVE_PUTAWAY_FLOW.steps] };
    expect(latestGraph([PICK_FLOW, v2], RECEIVE_PUTAWAY_FLOW)).toBe(v2);
    expect(latestGraph([PICK_FLOW], RECEIVE_PUTAWAY_FLOW)).toBe(RECEIVE_PUTAWAY_FLOW);
  });
});

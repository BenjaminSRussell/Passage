import { describe, expect, it } from "vitest";
import type { SavedFlow } from "../api/client";
import { PICK_FLOW, RECEIVE_PUTAWAY_FLOW } from "./coreFlows";
import { applyPublishedFlows, graphForRestart, MENU_POLL_MS, publishedDrafts } from "./menuSync";
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

  it("replaces a published code with the new version", () => {
    const v2: FlowDraft = {
      ...RECEIVE_PUTAWAY_FLOW,
      steps: [
        ...RECEIVE_PUTAWAY_FLOW.steps.slice(0, 2),
        { id: "confirm_lot", type: "scan", prompt: "Scan the lot", expectedScan: ["LOT"], next: { default: "confirm_putaway" } },
        RECEIVE_PUTAWAY_FLOW.steps[2],
      ],
    };
    v2.steps[1] = { ...v2.steps[1], next: { default: "confirm_lot" } };
    const next = applyPublishedFlows([published(v2, 2), published(PICK_FLOW, 1)]);
    expect(next.find((f) => f.code === "RECEIVE_PUTAWAY")?.steps.map((s) => s.id)).toContain("confirm_lot");
  });

  it("restarts on the latest menu graph and leaves the in-flight draft untouched", () => {
    const v2: FlowDraft = {
      ...RECEIVE_PUTAWAY_FLOW,
      steps: [
        ...RECEIVE_PUTAWAY_FLOW.steps.slice(0, 2),
        { id: "confirm_lot", type: "scan", prompt: "Scan the lot", expectedScan: ["LOT"], next: { default: "confirm_putaway" } },
        RECEIVE_PUTAWAY_FLOW.steps[2],
      ],
    };
    const inFlight = RECEIVE_PUTAWAY_FLOW;
    const menu = [v2, PICK_FLOW];
    expect(graphForRestart(menu, inFlight)).toBe(v2);
    expect(inFlight.steps.map((s) => s.id)).not.toContain("confirm_lot");
    expect(graphForRestart([PICK_FLOW], inFlight)).toBe(inFlight);
  });
});

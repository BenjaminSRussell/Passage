import { describe, expect, it } from "vitest";
import { RECEIVE_PUTAWAY_FLOW } from "../domain/coreFlows";
import type { FlowStep } from "../domain/flowGraph";
import { createMobileStore } from "./devStore";

describe("mobile dev store", () => {
  it("seeds the core flows as published version 1 on the default menu", () => {
    const store = createMobileStore();
    const menu = store.menu("default");
    expect(menu.flows.map((f) => f.code)).toContain("RECEIVE_PUTAWAY");
    expect(menu.flows.find((f) => f.code === "RECEIVE_PUTAWAY")).toMatchObject({ version: 1, status: "PUBLISHED" });
  });

  it("publishes a new version and notifies listeners so devices can reload", () => {
    const store = createMobileStore();
    const seen: { code: string; version: number }[] = [];
    store.onPublish((e) => seen.push({ code: e.code, version: e.version }));
    const lot: FlowStep = { id: "confirm_lot", type: "scan", prompt: "Scan the lot", expectedScan: ["LOT"], next: { default: "confirm_putaway" } };
    const withLot = {
      ...RECEIVE_PUTAWAY_FLOW,
      steps: [...RECEIVE_PUTAWAY_FLOW.steps.slice(0, 2), lot, RECEIVE_PUTAWAY_FLOW.steps[2]],
    };
    withLot.steps[1] = { ...withLot.steps[1], next: { default: "confirm_lot" } };
    const draft = store.saveDraft(withLot);
    expect(draft.status).toBe("DRAFT");
    expect(draft.version).toBe(2);
    const published = store.publish(draft.flowId, `"${draft.version}"`);
    expect(published.status).toBe("PUBLISHED");
    expect(store.menu("default").flows.find((f) => f.code === "RECEIVE_PUTAWAY")?.steps.map((s) => s.id)).toContain("confirm_lot");
    expect(seen).toEqual([{ code: "RECEIVE_PUTAWAY", version: 2 }]);
  });

  it("applies replay keys once so a second Sync is a no-op", () => {
    const store = createMobileStore();
    expect(store.menu("default").flows.map((f) => f.code)).toContain("SHIP_LOAD");
    const first = store.replay({ idempotencyKey: "e2e-ac1:1", command: "ConfirmWorkLine" });
    const again = store.replay({ idempotencyKey: "e2e-ac1:1", command: "ConfirmWorkLine" });
    expect(first).toEqual({ applied: true, idempotencyKey: "e2e-ac1:1", duplicate: false });
    expect(again.duplicate).toBe(true);
    expect(store.replayed()).toHaveLength(1);
  });
});

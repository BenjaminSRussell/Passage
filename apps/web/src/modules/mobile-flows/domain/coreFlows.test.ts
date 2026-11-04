import { describe, expect, it } from "vitest";
import {
  CLUSTER_PICK_FLOW,
  CORE_FLOWS,
  COUNT_FLOW,
  INQUIRY_FLOW,
  MOVE_FLOW,
  PACK_FLOW,
  PICK_FLOW,
  RECEIVE_PUTAWAY_FLOW,
  completionFor,
  confirmationPayload,
  offlineEnabledByDefault,
} from "./coreFlows";
import type { FlowDraft } from "./flowGraph";
import { validateFlow } from "./flowGraph";
import { startFlow, submitStep, type RunnerContext, type RunnerState, type StepInput } from "./flowRunner";

const ctx: RunnerContext = { deviceId: "dev-1", userId: "user-7" };
const T0 = Date.UTC(2026, 9, 26, 8, 0, 0);

// Scans that the routing rules accept for each class. GTIN uses AI 01 with a valid check digit.
const SCAN = {
  LP: "LP0001",
  LOCATION: "A-01-02",
  GTIN: "]C101" + "09501101020917",
  ANY: "LP0001",
} as const;

/** Drives a flow through the given inputs and returns the final result. Fails the test on a rejection. */
function walk(flow: FlowDraft, inputs: StepInput[]): { state: RunnerState; finished: boolean; visited: string[] } {
  const start = startFlow(flow, T0);
  if (!start.ok) throw new Error(`start failed: ${JSON.stringify(start.issues)}`);
  let state = start.state;
  let finished = false;
  const visited: string[] = [];
  for (const input of inputs) {
    expect(finished, "flow ended before the inputs ran out").toBe(false);
    visited.push(state.stepId ?? "<end>");
    const r = submitStep(flow, state, input, ctx, T0 + 1_000);
    if (!r.ok) throw new Error(`${flow.code} rejected at ${state.stepId}: ${r.message}`);
    state = r.state;
    finished = r.finished;
  }
  return { state, finished, visited };
}

const scan = (raw: string, source: "KEYBOARD_WEDGE" | "DATAWEDGE" = "KEYBOARD_WEDGE"): StepInput => ({ kind: "scan", raw, source });
const value = (v: string): StepInput => ({ kind: "value", value: v });
const confirm: StepInput = { kind: "confirm" };

describe("core flow catalogue (FR-32.4)", () => {
  it("bundles the seven core flows with unique codes", () => {
    expect(CORE_FLOWS.map((f) => f.code)).toEqual(["RECEIVE_PUTAWAY", "PICK", "PICK_CLUSTER", "PACK", "MOVE", "COUNT", "INQUIRY"]);
    expect(new Set(CORE_FLOWS.map((f) => f.code)).size).toBe(CORE_FLOWS.length);
  });

  it.each(CORE_FLOWS.map((f) => [f.code, f] as const))("%s passes graph validation", (_code, flow) => {
    expect(validateFlow(flow)).toEqual([]);
  });

  it("gives every scan step an expected class", () => {
    for (const flow of CORE_FLOWS) {
      for (const s of flow.steps.filter((x) => x.type === "scan")) {
        expect(s.expectedScan?.length ?? 0, `${flow.code}.${s.id}`).toBeGreaterThan(0);
      }
    }
  });

  it("keeps count and inquiry online only and the other flows offline-capable by default (§6.32.13)", () => {
    expect(offlineEnabledByDefault(COUNT_FLOW.code)).toBe(false);
    expect(offlineEnabledByDefault(INQUIRY_FLOW.code)).toBe(false);
    for (const flow of CORE_FLOWS.filter((f) => f !== COUNT_FLOW && f !== INQUIRY_FLOW)) {
      expect(offlineEnabledByDefault(flow.code), flow.code).toBe(true);
    }
  });
});

describe("core flow happy paths", () => {
  it("receive and put-away", () => {
    const r = walk(RECEIVE_PUTAWAY_FLOW, [scan(SCAN.LP), scan(SCAN.LOCATION), confirm]);
    expect(r.finished).toBe(true);
    expect(r.visited).toEqual(["scan_lp", "scan_location", "confirm_putaway"]);
  });

  it("pick, full quantity", () => {
    const r = walk(PICK_FLOW, [scan(SCAN.LOCATION), scan(SCAN.GTIN), value("5"), value("full"), confirm]);
    expect(r.finished).toBe(true);
    expect(r.visited).toEqual(["scan_location", "scan_item", "enter_qty", "qty_check", "confirm_pick"]);
  });

  it("pick, short quantity takes the short-reason path (FR-32.8)", () => {
    const r = walk(PICK_FLOW, [scan(SCAN.LOCATION), scan(SCAN.GTIN), value("3"), value("short"), value("DAMAGED"), confirm]);
    expect(r.finished).toBe(true);
    expect(r.visited).toContain("short_reason");
    // The confirmation carries the short quantity and the reason for the supervisor log.
    expect(confirmationPayload(r.state).values).toMatchObject({ enter_qty: "3", qty_check: "short", short_reason: "DAMAGED" });
  });

  it("cluster pick loops over two cart positions then ends on done (BR-32.5)", () => {
    const r = walk(CLUSTER_PICK_FLOW, [
      scan(SCAN.LP),
      scan(SCAN.GTIN),
      value("2"),
      confirm,
      value("more"),
      scan("LP0002"),
      scan(SCAN.GTIN),
      value("1"),
      confirm,
      value("done"),
    ]);
    expect(r.finished).toBe(true);
    expect(r.visited.filter((s) => s === "scan_cart_lp")).toHaveLength(2);
    // The loop keeps the latest value per step; the history records both positions.
    expect(r.state.values.scan_cart_lp).toBe("LP0002");
    expect(r.state.history.filter((s) => s === "confirm_position")).toHaveLength(2);
  });

  it("pack", () => {
    const r = walk(PACK_FLOW, [scan(SCAN.LP), scan(SCAN.GTIN), value("4"), confirm]);
    expect(r.finished).toBe(true);
  });

  it("move", () => {
    const r = walk(MOVE_FLOW, [scan(SCAN.LP), value("10"), scan(SCAN.LOCATION), confirm]);
    expect(r.finished).toBe(true);
    expect(r.visited).toEqual(["scan_source_lp", "enter_qty", "scan_destination", "confirm_move"]);
  });

  it("count", () => {
    const r = walk(COUNT_FLOW, [scan(SCAN.LOCATION), scan(SCAN.GTIN), value("12"), confirm]);
    expect(r.finished).toBe(true);
  });

  it("inquiry reads a scan and displays the result", () => {
    const r = walk(INQUIRY_FLOW, [scan(SCAN.ANY), confirm]);
    expect(r.finished).toBe(true);
    expect(r.visited).toEqual(["scan_query", "show_result"]);
  });
});

describe("core flow scan rules", () => {
  it("refuses an LP scan where the pick flow expects a GTIN (BR-32.2, MOB-0101)", () => {
    const start = startFlow(PICK_FLOW, T0);
    if (!start.ok) throw new Error("start failed");
    const bad = submitStep(PICK_FLOW, { ...start.state, stepId: "scan_item" }, scan(SCAN.LP), ctx, T0);
    expect(bad).toMatchObject({ ok: false, ignored: false });
    if (!bad.ok && !bad.ignored) expect(bad.code).toBe("MOB-0101");
  });

  it("refuses a branch outcome the pick flow does not define (MOB-0113)", () => {
    const start = startFlow(PICK_FLOW, T0);
    if (!start.ok) throw new Error("start failed");
    const bad = submitStep(PICK_FLOW, { ...start.state, stepId: "qty_check" }, value("partial"), ctx, T0);
    expect(bad).toMatchObject({ ok: false, ignored: false });
    if (!bad.ok && !bad.ignored) expect(bad.code).toBe("MOB-0113");
  });

  it("refuses a GTIN scan on the cart position step, which expects an LP", () => {
    const start = startFlow(CLUSTER_PICK_FLOW, T0);
    if (!start.ok) throw new Error("start failed");
    const bad = submitStep(CLUSTER_PICK_FLOW, start.state, scan(SCAN.GTIN), ctx, T0);
    expect(bad).toMatchObject({ ok: false, ignored: false });
    if (!bad.ok && !bad.ignored) expect(bad.code).toBe("MOB-0101");
  });
});

describe("completionFor (FR-32.5, §6.32.13)", () => {
  it("queues work flows online and offline", () => {
    for (const flow of [RECEIVE_PUTAWAY_FLOW, PICK_FLOW, CLUSTER_PICK_FLOW, PACK_FLOW, MOVE_FLOW]) {
      expect(completionFor(flow.code, true), flow.code).toEqual({ kind: "queue" });
      expect(completionFor(flow.code, false), flow.code).toEqual({ kind: "queue" });
    }
  });

  it("queues a count only while online and blocks it offline", () => {
    expect(completionFor(COUNT_FLOW.code, true)).toEqual({ kind: "queue" });
    expect(completionFor(COUNT_FLOW.code, false)).toMatchObject({ kind: "blocked" });
  });

  it("never queues an inquiry, which only reads", () => {
    expect(completionFor(INQUIRY_FLOW.code, true)).toEqual({ kind: "none" });
    expect(completionFor(INQUIRY_FLOW.code, false)).toEqual({ kind: "none" });
  });
});

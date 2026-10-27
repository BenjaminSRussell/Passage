import { describe, expect, it } from "vitest";
import type { FlowDraft } from "./flowGraph";
import { confirmationPayload, RECEIVE_PUTAWAY_FLOW } from "./coreFlows";
import { goBack, startFlow, submitStep, type RunnerContext, type RunnerState } from "./flowRunner";
import { validateTiming, type StepTiming } from "./telemetry";

const ctx: RunnerContext = { deviceId: "dev-1", userId: "user-7" };
const T0 = Date.UTC(2026, 9, 26, 8, 0, 0);

function start(flow: FlowDraft): RunnerState {
  const r = startFlow(flow, T0);
  if (!r.ok) throw new Error(`start failed: ${JSON.stringify(r.issues)}`);
  return r.state;
}

const BRANCH_FLOW: FlowDraft = {
  code: "BRANCH_DEMO",
  entryStepId: "pick_kind",
  steps: [
    { id: "pick_kind", type: "branch", prompt: "Lot tracked?", next: { yes: "scan_lot", no: null } },
    { id: "scan_lot", type: "scan", prompt: "Scan the lot", expectedScan: ["ANY"], next: { default: null } },
  ],
};

describe("startFlow", () => {
  it("refuses a flow that fails graph validation", () => {
    const r = startFlow({ code: "EMPTY", entryStepId: "x", steps: [] }, T0);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.issues.every((i) => i.code === "MOB-0113")).toBe(true);
  });

  it("starts the core receive flow at its entry step", () => {
    const state = start(RECEIVE_PUTAWAY_FLOW);
    expect(state).toMatchObject({ flowCode: "RECEIVE_PUTAWAY", stepId: "scan_lp", history: [] });
  });
});

describe("submitStep on RECEIVE_PUTAWAY", () => {
  it("walks LP, location, confirm to the end and records one timing per step", () => {
    let state = start(RECEIVE_PUTAWAY_FLOW);
    const timings: StepTiming[] = [];

    let r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "LP0001", source: "KEYBOARD_WEDGE" }, ctx, T0 + 1_000);
    expect(r.ok).toBe(true);
    if (!r.ok) throw new Error(r.message);
    timings.push(r.timing);
    state = r.state;
    expect(state.stepId).toBe("scan_location");
    expect(state.values).toEqual({ scan_lp: "LP0001" });

    r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "A-01-02", source: "DATAWEDGE" }, ctx, T0 + 2_500);
    expect(r.ok).toBe(true);
    if (!r.ok) throw new Error(r.message);
    timings.push(r.timing);
    state = r.state;

    r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "confirm" }, ctx, T0 + 4_000);
    expect(r.ok).toBe(true);
    if (!r.ok) throw new Error(r.message);
    timings.push(r.timing);
    expect(r.finished).toBe(true);
    expect(r.state.stepId).toBeNull();
    expect(r.state.history).toEqual(["scan_lp", "scan_location", "confirm_putaway"]);

    expect(timings.map((t) => t.durationMs)).toEqual([1_000, 1_500, 1_500]);
    expect(timings.every((t) => !t.failed && validateTiming(t) === null)).toBe(true);
    expect(timings[0]).toMatchObject({ flowCode: "RECEIVE_PUTAWAY", stepId: "scan_lp", userId: "user-7", deviceId: "dev-1" });
    expect(confirmationPayload(r.state)).toEqual({
      flowCode: "RECEIVE_PUTAWAY",
      steps: ["scan_lp", "scan_location", "confirm_putaway"],
      values: { scan_lp: "LP0001", scan_location: "A-01-02" },
      fields: {},
    });
  });

  it("keeps the GS1 fields of an SSCC scanned for the LP step", () => {
    const state = start(RECEIVE_PUTAWAY_FLOW);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "]C100340123456789012340", source: "DATAWEDGE" }, ctx, T0);
    if (!r.ok) throw new Error(r.message);
    expect(r.state.fields).toEqual({ scan_lp: { "00": "340123456789012340" } });
    // Going back and rescanning a plain LP drops the stale GS1 fields.
    const again = submitStep(RECEIVE_PUTAWAY_FLOW, goBack(r.state, T0 + 1), { kind: "scan", raw: "LP0002", source: "KEYBOARD_WEDGE" }, ctx, T0 + 2);
    if (!again.ok) throw new Error(again.message);
    expect(again.state.fields).toEqual({});
    expect(again.state.values.scan_lp).toBe("LP0002");
  });

  it("rejects a GS1 label with a bad check digit with MOB-0102", () => {
    const state = start(RECEIVE_PUTAWAY_FLOW);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "]C100340123456789012341", source: "DATAWEDGE" }, ctx, T0);
    expect(r).toMatchObject({ ok: false, ignored: false, code: "MOB-0102" });
  });

  it("rejects a scan of the wrong class with MOB-0101 and does not advance", () => {
    const state = start(RECEIVE_PUTAWAY_FLOW);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "A-01-02", source: "KEYBOARD_WEDGE" }, ctx, T0 + 500);
    expect(r.ok).toBe(false);
    if (r.ok || r.ignored) throw new Error("expected a rejection");
    expect(r.code).toBe("MOB-0101");
    expect(r.state).toBe(state);
    expect(r.timing?.failed).toBe(true);
  });

  it("ignores a scan that arrives while a confirm step has focus (AC-32.7)", () => {
    let state = start(RECEIVE_PUTAWAY_FLOW);
    state = { ...state, stepId: "confirm_putaway" };
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "]C1" + "00", source: "DATAWEDGE" }, ctx, T0);
    expect(r).toMatchObject({ ok: false, ignored: true });
    expect(r.state).toBe(state);
    expect("timing" in r).toBe(false);
    if (!r.ok) expect(r.message).toMatch(/not a scan step/);
  });

  it("needs a scan, not a confirmation, on a scan step", () => {
    const state = start(RECEIVE_PUTAWAY_FLOW);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "confirm" }, ctx, T0);
    expect(r).toMatchObject({ ok: false, ignored: false, message: "Step scan_lp needs a scan." });
  });

  it("refuses input once the flow has finished", () => {
    let state = start(RECEIVE_PUTAWAY_FLOW);
    state = { ...state, stepId: null, history: ["scan_lp", "scan_location", "confirm_putaway"] };
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "confirm" }, ctx, T0);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.message).toBe("The flow has already finished.");
  });
});

describe("submitStep on branch and typed steps", () => {
  it("takes the branch named by the outcome", () => {
    const state = start(BRANCH_FLOW);
    const r = submitStep(BRANCH_FLOW, state, { kind: "value", value: "yes" }, ctx, T0);
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.state.stepId).toBe("scan_lot");
      expect(r.state.values).toEqual({ pick_kind: "yes" });
    }
  });

  it("ends the flow on the no outcome", () => {
    const state = start(BRANCH_FLOW);
    const r = submitStep(BRANCH_FLOW, state, { kind: "value", value: "no" }, ctx, T0);
    expect(r.ok && r.finished).toBe(true);
  });

  it("rejects an outcome the branch does not define with MOB-0113", () => {
    const state = start(BRANCH_FLOW);
    const r = submitStep(BRANCH_FLOW, state, { kind: "value", value: "maybe" }, ctx, T0);
    expect(r).toMatchObject({ ok: false, ignored: false, code: "MOB-0113" });
  });

  it("needs a non-empty value on an input step", () => {
    const flow: FlowDraft = {
      code: "INPUT_DEMO",
      entryStepId: "qty",
      steps: [{ id: "qty", type: "input", prompt: "Quantity", next: { default: null } }],
    };
    const state = start(flow);
    const blank = submitStep(flow, state, { kind: "value", value: "   " }, ctx, T0);
    expect(blank.ok).toBe(false);
    const filled = submitStep(flow, state, { kind: "value", value: "12" }, ctx, T0);
    expect(filled.ok && filled.finished).toBe(true);
  });

  it("refuses a typed value on a scan step", () => {
    const state = start(RECEIVE_PUTAWAY_FLOW);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "value", value: "LP0001" }, ctx, T0);
    expect(r.ok).toBe(false);
  });

  it("reports several outcomes without a default as MOB-0113 at run time", () => {
    const flow: FlowDraft = {
      code: "AMBIGUOUS",
      entryStepId: "ok_step",
      steps: [{ id: "ok_step", type: "confirm", prompt: "Confirm", next: { yes: null, no: null } }],
    };
    const state = start(flow);
    const r = submitStep(flow, state, { kind: "confirm" }, ctx, T0);
    expect(r).toMatchObject({ ok: false, ignored: false, code: "MOB-0113" });
  });
});

describe("goBack", () => {
  it("returns to the previous step and keeps the flow's start in place", () => {
    let state = start(RECEIVE_PUTAWAY_FLOW);
    const r = submitStep(RECEIVE_PUTAWAY_FLOW, state, { kind: "scan", raw: "LP0001", source: "KEYBOARD_WEDGE" }, ctx, T0);
    if (!r.ok) throw new Error(r.message);
    state = r.state;
    const back = goBack(state, T0 + 10);
    expect(back.stepId).toBe("scan_lp");
    expect(back.history).toEqual([]);
    expect(goBack(back, T0 + 20)).toBe(back);
  });
});

// Seeded generator so the property run is reproducible (no new dependency).
function rng(seed: number): () => number {
  let s = seed >>> 0;
  return () => {
    s = (Math.imul(s, 1664525) + 1013904223) >>> 0;
    return s / 4294967296;
  };
}

describe("property: random linear flows always terminate and stay in the flow", () => {
  it("completes every generated flow exactly once per step and keeps history consistent", () => {
    for (let seed = 1; seed <= 200; seed++) {
      const r = rng(seed);
      const n = 1 + Math.floor(r() * 6);
      const steps: FlowDraft["steps"] = [];
      for (let i = 0; i < n; i++) {
        const id = `s${i}`;
        const next = i + 1 < n ? `s${i + 1}` : null;
        steps.push({ id, type: "scan", prompt: id, expectedScan: ["ANY"], next: { default: next } });
      }
      const flow: FlowDraft = { code: `GEN_${seed}`, entryStepId: "s0", steps };
      let state = start(flow);
      let now = T0;
      let completed = 0;
      while (state.stepId !== null) {
        // Occasionally step back first; the step is re-entered and then completed normally.
        if (state.history.length > 0 && r() < 0.3) {
          state = goBack(state, now);
          completed--;
          expect(steps.some((s) => s.id === state.stepId)).toBe(true);
          continue;
        }
        now += 1 + Math.floor(r() * 500);
        const res = submitStep(flow, state, { kind: "scan", raw: `ITEM-${completed}`, source: "KEYBOARD_WEDGE" }, ctx, now);
        expect(res.ok).toBe(true);
        if (!res.ok) return;
        expect(validateTiming(res.timing)).toBeNull();
        state = res.state;
        completed++;
      }
      expect(state.history.length).toBe(n);
      expect(completed).toBe(n);
      expect(Object.keys(state.values).sort()).toEqual(steps.map((s) => s.id).sort());
    }
  });
});

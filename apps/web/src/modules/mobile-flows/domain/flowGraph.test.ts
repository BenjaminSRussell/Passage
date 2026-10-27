import { describe, expect, it } from "vitest";
import { type FlowDraft, type FlowStep, validateFlow } from "./flowGraph";

const scan = (id: string, next: FlowStep["next"]): FlowStep => ({
  id,
  type: "scan",
  prompt: `scan ${id}`,
  expectedScan: ["LP"],
  next,
});

// Core put-away shape: scan LP, scan location, confirm, end.
const putAway: FlowDraft = {
  code: "PUT_AWAY",
  entryStepId: "scan_lp",
  steps: [
    scan("scan_lp", { default: "scan_loc" }),
    { ...scan("scan_loc", { default: "confirm" }), expectedScan: ["LOCATION"] },
    { id: "confirm", type: "confirm", prompt: "Confirm put-away", next: { default: null } },
  ],
};

describe("validateFlow", () => {
  it("accepts a linear core flow", () => {
    expect(validateFlow(putAway)).toEqual([]);
  });

  const cases: { name: string; flow: FlowDraft; want: string }[] = [
    {
      name: "no steps",
      flow: { code: "X", entryStepId: "a", steps: [] },
      want: "flow has no steps",
    },
    {
      name: "missing entry step",
      flow: { ...putAway, entryStepId: "nope" },
      want: "entry step nope does not exist",
    },
    {
      name: "unreachable step",
      flow: { ...putAway, steps: [...putAway.steps, { id: "orphan", type: "display", prompt: "x", next: {} }] },
      want: "step orphan is unreachable from the entry step",
    },
    {
      name: "dangling outcome",
      flow: { ...putAway, steps: [scan("scan_lp", { default: "ghost" }), putAway.steps[1], putAway.steps[2]] },
      want: "points to missing step ghost",
    },
    {
      name: "scan step without expected class",
      flow: { ...putAway, steps: [{ ...putAway.steps[0], expectedScan: [] }, putAway.steps[1], putAway.steps[2]] },
      want: "must declare an expected scan class",
    },
    {
      name: "branch with one outcome",
      flow: {
        code: "B",
        entryStepId: "b",
        steps: [{ id: "b", type: "branch", prompt: "x", next: { only: null } }],
      },
      want: "needs at least two outcomes",
    },
    {
      name: "loop with no exit never terminates",
      flow: {
        code: "LOOP",
        entryStepId: "a",
        steps: [
          { id: "a", type: "display", prompt: "a", next: { default: "b" } },
          { id: "b", type: "display", prompt: "b", next: { default: "a" } },
        ],
      },
      want: "cannot reach the end of the flow",
    },
    {
      name: "branch where one arm loops forever",
      flow: {
        code: "BR",
        entryStepId: "br",
        steps: [
          { id: "br", type: "branch", prompt: "x", next: { ok: null, retry: "loop" } },
          { id: "loop", type: "display", prompt: "y", next: { default: "loop" } },
        ],
      },
      want: "step loop cannot reach the end of the flow",
    },
    {
      name: "duplicate step id",
      flow: { ...putAway, steps: [...putAway.steps, { id: "confirm", type: "display", prompt: "dup", next: {} }] },
      want: "step id confirm is duplicated",
    },
    {
      name: "bad step id format",
      flow: { ...putAway, steps: [{ id: "Scan-LP", type: "display", prompt: "x", next: {} }], entryStepId: "Scan-LP" },
      want: "must be lower snake case",
    },
  ];

  for (const c of cases) {
    it(`rejects: ${c.name}`, () => {
      const issues = validateFlow(c.flow);
      expect(issues.length).toBeGreaterThan(0);
      expect(issues.every((i) => i.code === "MOB-0113")).toBe(true);
      expect(issues.map((i) => i.message).join("\n")).toContain(c.want);
    });
  }

  it("accepts a branch whose arms both end the flow", () => {
    const flow: FlowDraft = {
      code: "BR_OK",
      entryStepId: "br",
      steps: [
        { id: "br", type: "branch", prompt: "x", next: { ok: "done", damaged: "report" } },
        { id: "done", type: "confirm", prompt: "ok", next: {} },
        { id: "report", type: "display", prompt: "report", next: { default: null } },
      ],
    };
    expect(validateFlow(flow)).toEqual([]);
  });
});

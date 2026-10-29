import { describe, expect, it } from "vitest";
import { gate } from "../src/gate.js";
import { renderEvalReport, renderModelCard } from "../src/report.js";

const passing = { top1: 0.93, top3: 0.985, n: 2000 };
const card = {
  agentId: "ap_coding",
  version: 3,
  modelId: "claude-model-id-pinned",
  provider: "ANTHROPIC",
  residency: "EU",
  evalDatasetId: "eval-ap-coding-v3",
  evalDatasetVersion: "2026-10-01",
  metrics: passing,
  limitations: ["Trained on demo entity USMF only", "Vendors with fewer than 3 invoices | rely on text"],
  approver: "AI governance lead",
};

describe("renderModelCard", () => {
  it("renders the PRD 9.47.15 fields and a metric table against the gate", () => {
    const md = renderModelCard(card, gate("ap_coding", passing));
    expect(md).toContain("# Model card: ap_coding v3");
    expect(md).toContain("| Model id | claude-model-id-pinned |");
    expect(md).toContain("| Evaluated on | eval-ap-coding-v3 @ 2026-10-01 |");
    expect(md).toContain("| top1 | 0.9300 | >= 0.92 | pass |");
    expect(md).toContain("| top3 | 0.9850 | >= 0.98 | pass |");
    expect(md).toContain("- Vendors with fewer than 3 invoices \\| rely on text");
  });

  it("refuses to describe a version whose gate failed", () => {
    const failing = { top1: 0.8, top3: 0.99, n: 10 };
    expect(() => renderModelCard({ ...card, metrics: failing }, gate("ap_coding", failing))).toThrow(/fails its gate/);
  });

  it("says so when there are no recorded limitations", () => {
    expect(renderModelCard({ ...card, limitations: [] }, gate("ap_coding", passing))).toContain("- None recorded.");
  });
});

describe("renderEvalReport", () => {
  it("shows regressions, the failing-case sample and a FAILED result", () => {
    const metrics = { top1: 0.9, top3: 0.99, n: 2000 };
    const previous = { top1: 0.925, top3: 0.99 };
    const g = gate("ap_coding", metrics, previous);
    const failing = Array.from({ length: 12 }, (_, i) => `INV-${i + 1}`);
    const md = renderEvalReport({
      agentId: "ap_coding",
      version: 4,
      evalRunId: "run-77",
      datasetId: "eval-ap-coding-v3",
      datasetVersion: "2026-10-01",
      metrics,
      previous,
      gate: g,
      failingCaseIds: failing,
    });
    expect(md).toContain("Result: **FAILED**.");
    expect(md).toContain("| top1 | 0.9000 | 0.9250 | >= 0.92 | FAIL |");
    expect(md).toContain("regressed from 0.9250 to 0.9000");
    expect(md).toContain("- INV-10");
    expect(md).not.toContain("- INV-11");
  });

  it("reports PASSED with no regressions when the gate is green", () => {
    const g = gate("ap_coding", passing, { top1: 0.93, top3: 0.985 });
    const md = renderEvalReport({
      agentId: "ap_coding",
      version: 3,
      evalRunId: "run-70",
      datasetId: "eval-ap-coding-v3",
      datasetVersion: "2026-10-01",
      metrics: passing,
      gate: g,
    });
    expect(md).toContain("Result: **PASSED**.");
    expect(md).toContain("## Regressions\n\nNone.");
    expect(md).toContain("## Failing cases (sample)\n\nNone.");
  });

  it("marks a missing metric as missing and a failing one as FAIL", () => {
    const metrics = { top1: 0.5 };
    const g = gate("ap_coding", metrics);
    const md = renderEvalReport({
      agentId: "ap_coding",
      version: 5,
      evalRunId: "run-80",
      datasetId: "d",
      datasetVersion: "v",
      metrics,
      gate: g,
    });
    expect(md).toContain("| top3 | missing | missing | >= 0.98 | FAIL |");
  });
});

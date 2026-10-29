/**
 * Markdown renderers for the model card and the eval report (PRD 9.47.15).
 * Pure functions: the caller decides where the text is stored or attached.
 */
import { type GateResult, THRESHOLDS } from "./gate.js";

export interface ModelCardInput {
  agentId: string;
  version: number;
  modelId: string;
  provider: string;
  residency: string;
  evalDatasetId: string;
  evalDatasetVersion: string;
  metrics: Readonly<Record<string, number>>;
  limitations: readonly string[];
  approver: string;
}

export interface EvalReportInput {
  agentId: string;
  version: number;
  evalRunId: string;
  datasetId: string;
  datasetVersion: string;
  metrics: Readonly<Record<string, number>>;
  previous?: Readonly<Record<string, number>>;
  gate: GateResult;
  /** Ids of failing cases; the report shows at most ten. */
  failingCaseIds?: readonly string[];
}

/** Table cells must not break the table: escape pipes and fold newlines. */
function cell(value: string): string {
  return value.replace(/\|/g, "\\|").replace(/[\r\n]+/g, " ");
}

function fmt(value: number | undefined): string {
  return value === undefined || Number.isNaN(value) ? "missing" : value.toFixed(4);
}

function bound(t: { min?: number; max?: number }): string {
  if (t.min !== undefined) return `>= ${t.min}`;
  if (t.max !== undefined) return `<= ${t.max}`;
  return "-";
}

function passes(t: { min?: number; max?: number }, value: number | undefined): boolean {
  if (value === undefined || Number.isNaN(value)) return false;
  if (t.min !== undefined && value < t.min) return false;
  if (t.max !== undefined && value > t.max) return false;
  return true;
}

function metricRows(agentId: string, metrics: Readonly<Record<string, number>>): string[] {
  const thresholds = THRESHOLDS[agentId];
  if (!thresholds) throw new Error(`no thresholds defined for agent ${agentId}`);
  return thresholds.map((t) => {
    const value = metrics[t.metric];
    const status = passes(t, value) ? "pass" : "FAIL";
    return `| ${cell(t.metric)} | ${fmt(value)} | ${bound(t)} | ${status} |`;
  });
}

/**
 * Model card for a promoted agent version. Refuses to render for a version
 * whose gate failed: a card is evidence for a release, so it must not describe
 * a version that should not ship (AGT-0204).
 */
export function renderModelCard(input: ModelCardInput, gate: GateResult): string {
  if (!gate.passed) {
    throw new Error(`model card refused: ${input.agentId} v${input.version} fails its gate (${gate.failures.join("; ")})`);
  }
  const lines = [
    `# Model card: ${cell(input.agentId)} v${input.version}`,
    "",
    "| Field | Value |",
    "|---|---|",
    `| Model id | ${cell(input.modelId)} |`,
    `| Provider | ${cell(input.provider)} |`,
    `| Residency | ${cell(input.residency)} |`,
    `| Evaluated on | ${cell(input.evalDatasetId)} @ ${cell(input.evalDatasetVersion)} |`,
    `| Approved by | ${cell(input.approver)} |`,
    "",
    "## Metrics against release gate",
    "",
    "| Metric | Value | Gate | Result |",
    "|---|---|---|---|",
    ...metricRows(input.agentId, input.metrics),
    "",
    "## Limitations",
    "",
    ...(input.limitations.length === 0 ? ["- None recorded."] : input.limitations.map((l) => `- ${cell(l)}`)),
    "",
    "Generated text is labelled in the product UI. Personal data is pseudonymised before any provider call (FR-47.12).",
    "",
  ];
  return lines.join("\n");
}

/** Eval report attached to the CI run (PRD 9.47.15, retained 24 months). */
export function renderEvalReport(input: EvalReportInput): string {
  const regressions = input.gate.failures.filter((f) => f.includes("regressed"));
  const failing = (input.failingCaseIds ?? []).slice(0, 10);
  const lines = [
    `# Eval report: ${cell(input.agentId)} v${input.version}`,
    "",
    `Eval run ${cell(input.evalRunId)} on dataset ${cell(input.datasetId)} @ ${cell(input.datasetVersion)}.`,
    `Result: **${input.gate.passed ? "PASSED" : "FAILED"}**.`,
    "",
    "| Metric | Value | Previous | Gate | Result |",
    "|---|---|---|---|---|",
    ...(THRESHOLDS[input.agentId] ?? []).map((t) => {
      const value = input.metrics[t.metric];
      const before = input.previous?.[t.metric];
      return `| ${cell(t.metric)} | ${fmt(value)} | ${fmt(before)} | ${bound(t)} | ${passes(t, value) ? "pass" : "FAIL"} |`;
    }),
    "",
    "## Regressions",
    "",
    ...(regressions.length === 0 ? ["None."] : regressions.map((r) => `- ${cell(r)}`)),
    "",
    "## Failing cases (sample)",
    "",
    ...(failing.length === 0 ? ["None."] : failing.map((id) => `- ${cell(id)}`)),
    "",
    "## Gate failures",
    "",
    ...(input.gate.failures.length === 0 ? ["None."] : input.gate.failures.map((f) => `- ${cell(f)}`)),
    "",
  ];
  return lines.join("\n");
}

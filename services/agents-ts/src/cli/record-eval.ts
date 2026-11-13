import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import pg from "pg";
import { loadModelSettings } from "../config.js";
import type { AgentVersion } from "../domain/registry.js";
import { agentCatalog } from "../main.js";
import { PgVersionStore, type EvalResult } from "../store/registry.js";

/** The parts of the evals CLI report (`evals/src/run.ts`) the recorder reads. */
export interface EvalReport {
  datasets: Record<string, { id: string; version: string }>;
  results: Record<string, Record<string, number>>;
  verdicts: Record<string, { passed: boolean; failures: string[] }>;
}

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/** Validates the report; a malformed one is refused whole, nothing is recorded. */
export function parseEvalReport(raw: unknown): EvalReport {
  if (!isObject(raw) || !isObject(raw["datasets"]) || !isObject(raw["results"]) || !isObject(raw["verdicts"])) {
    throw new Error("the report must be the evals CLI output: an object with datasets, results and verdicts");
  }
  const { datasets, results, verdicts } = raw as { datasets: Record<string, unknown>; results: Record<string, unknown>; verdicts: Record<string, unknown> };
  for (const [agent, v] of Object.entries(verdicts)) {
    const d = datasets[agent];
    const r = results[agent];
    if (!isObject(v) || typeof v["passed"] !== "boolean" || !Array.isArray(v["failures"]) || !v["failures"].every((f) => typeof f === "string")) {
      throw new Error(`verdict for ${agent} must have passed and failures`);
    }
    if (v["passed"] !== (v["failures"].length === 0)) throw new Error(`verdict for ${agent}: passed must mean no failures`);
    if (!isObject(d) || typeof d["id"] !== "string" || d["id"] === "" || typeof d["version"] !== "string" || d["version"] === "") {
      throw new Error(`dataset for ${agent} must have an id and a version`);
    }
    if (!isObject(r) || !Object.values(r).every((n) => typeof n === "number" && Number.isFinite(n))) {
      throw new Error(`results for ${agent} must be finite numbers`);
    }
  }
  return raw as unknown as EvalReport;
}

/**
 * Pairs each verdict with the definition this deployment runs for that agent. Agents the report has but the catalog
 * lacks (for example ask_data, the copilot's) are skipped, and catalog agents without a verdict are not evaluated.
 */
export function evalResults(report: EvalReport, catalog: readonly AgentVersion[], recordedBy: string): { results: EvalResult[]; skipped: string[]; notEvaluated: string[] } {
  const byId = new Map(catalog.map((a) => [a.agentId, a]));
  const results: EvalResult[] = [];
  const skipped: string[] = [];
  for (const [agentId, verdict] of Object.entries(report.verdicts)) {
    const agent = byId.get(agentId);
    if (agent === undefined) {
      skipped.push(agentId);
      continue;
    }
    const dataset = report.datasets[agentId]!;
    results.push({ agent, datasetId: dataset.id, datasetVersion: dataset.version, metrics: report.results[agentId]!, failures: verdict.failures, recordedBy });
  }
  const notEvaluated = catalog.map((a) => a.agentId).filter((id) => !(id in report.verdicts));
  return { results, skipped, notEvaluated };
}

/**
 * `node dist/cli/record-eval.js <report.json | ->`: stores the eval pipeline's gate results in eval_run for the
 * definitions this deployment runs. Run it with the deployment's DATABASE_URL, AGENTS_MODEL and
 * AGENTS_MODEL_OVERRIDES, so the prompt hashes are the ones the pods compute. EVAL_RECORDED_BY names the job.
 */
async function main(argv: string[], env: NodeJS.ProcessEnv): Promise<void> {
  const source = argv[0];
  if (source === undefined) throw new Error("usage: record-eval <report.json | ->");
  const url = env["DATABASE_URL"];
  if (url === undefined || url === "") throw new Error("DATABASE_URL is required");
  const report = parseEvalReport(JSON.parse(readFileSync(source === "-" ? 0 : source, "utf8")));
  const { results, skipped, notEvaluated } = evalResults(report, agentCatalog(loadModelSettings(env)), env["EVAL_RECORDED_BY"] || "eval-pipeline");
  const pool = new pg.Pool({ connectionString: url, max: 1 });
  try {
    const store = new PgVersionStore(pool);
    const recorded = [];
    for (const r of results) {
      recorded.push({ agent: `${r.agent.agentId}@${r.agent.version}`, model: r.agent.modelId, passed: r.failures.length === 0, evalRunId: await store.recordEval(r) });
    }
    process.stdout.write(`${JSON.stringify({ recorded, skipped, notEvaluated })}\n`);
  } finally {
    await pool.end();
  }
}

if (process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2), process.env).catch((err: unknown) => {
    process.stderr.write(`${err instanceof Error ? err.message : String(err)}\n`);
    process.exit(1);
  });
}

import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { mrpKeywordBaseline, offHoursAnomalyBaseline, remittanceBaseline, vendorHistoryBaseline } from "./baselines.js";
import { datasetVersion, isAnomalyCase, isApCodingCase, isCashAppCase, isMrpTriageCase, parseJsonl } from "./datasets.js";
import { evaluateAnomaly, evaluateApCoding, evaluateCashApp, evaluateMrpTriage } from "./harness.js";
import { gate } from "./gate.js";

/**
 * CLI: evaluates the baselines against a dataset directory and prints JSON.
 * With --gate, exits 1 when any threshold fails. CI runs this per agent; the
 * LLM agent predictor replaces the baselines when the runtime is wired in.
 * Usage: node dist/run.js [--gate] [datasetDir]
 */
function main(argv: string[]): number {
  const enforce = argv.includes("--gate");
  const dirArg = argv.find((a) => !a.startsWith("--"));
  const dir = dirArg ?? fileURLToPath(new URL("../datasets/", import.meta.url));

  const apText = readFileSync(join(dir, "ap-coding-seed.jsonl"), "utf8");
  const cashText = readFileSync(join(dir, "cash-app-seed.jsonl"), "utf8");
  const mrpText = readFileSync(join(dir, "mrp-triage-seed.jsonl"), "utf8");
  const anomalyText = readFileSync(join(dir, "anomaly-seed.jsonl"), "utf8");
  const ap = parseJsonl(apText, isApCodingCase, "ap-coding-seed.jsonl");
  const cash = parseJsonl(cashText, isCashAppCase, "cash-app-seed.jsonl");
  const mrp = parseJsonl(mrpText, isMrpTriageCase, "mrp-triage-seed.jsonl");
  const anomaly = parseJsonl(anomalyText, isAnomalyCase, "anomaly-seed.jsonl");
  const datasets = {
    ap_coding: { id: "ap-coding-seed", version: datasetVersion(apText) },
    cash_app: { id: "cash-app-seed", version: datasetVersion(cashText) },
    mrp_triage: { id: "mrp-triage-seed", version: datasetVersion(mrpText) },
    anomaly: { id: "anomaly-seed", version: datasetVersion(anomalyText) },
  };

  const results = {
    ap_coding: evaluateApCoding(ap, vendorHistoryBaseline),
    cash_app: evaluateCashApp(cash, remittanceBaseline),
    mrp_triage: evaluateMrpTriage(mrp, mrpKeywordBaseline),
    anomaly: evaluateAnomaly(anomaly, offHoursAnomalyBaseline),
  } as const;
  const verdicts = {
    ap_coding: gate("ap_coding", results.ap_coding),
    cash_app: gate("cash_app", results.cash_app),
    mrp_triage: gate("mrp_triage", results.mrp_triage),
    anomaly: gate("anomaly", results.anomaly),
  };
  process.stdout.write(`${JSON.stringify({ datasets, results, verdicts }, null, 2)}\n`);
  const failed = Object.values(verdicts).some((v) => !v.passed);
  return enforce && failed ? 1 : 0;
}

process.exitCode = main(process.argv.slice(2));

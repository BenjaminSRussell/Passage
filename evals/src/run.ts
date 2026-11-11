import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { mrpKeywordBaseline, offHoursAnomalyBaseline, remittanceBaseline, vendorHistoryBaseline } from "./baselines.js";
import { datasetVersion, isAnomalyCase, isApCodingCase, isAskDataCase, isCashAppCase, isMrpTriageCase, parseJsonl } from "./datasets.js";
import { evaluateAnomaly, evaluateApCoding, evaluateCashApp, evaluateMrpTriage } from "./harness.js";
import { evaluateAskData } from "./askdata.js";
import { askDataBaseline } from "./askdata-baseline.js";
import { gate } from "./gate.js";
import { parseRecording, ReplayPredictor } from "./replay.js";

/**
 * CLI: evaluates the baselines against a dataset directory and prints JSON.
 * With --gate, exits 1 when any threshold fails. CI runs this per agent; the
 * LLM agent predictor replaces the baselines when the runtime is wired in.
 * With --replay <recording.jsonl>, AP coding and cash application run the grown v1 datasets
 * through the recorded model outputs (no network, no key); a missing or stale recording is an
 * error (exit 2), not a pass.
 * Usage: node dist/run.js [--gate] [--replay file] [datasetDir]
 */
function main(argv: string[]): number {
  const enforce = argv.includes("--gate");
  const at = argv.indexOf("--replay");
  const replayFile = at >= 0 ? argv[at + 1] : undefined;
  if (at >= 0 && (replayFile === undefined || replayFile.startsWith("--"))) throw new Error("--replay needs a recording file");
  const dirArg = argv.find((a, i) => !a.startsWith("--") && i !== at + 1);
  const dir = dirArg ?? fileURLToPath(new URL("../datasets/", import.meta.url));

  const apName = replayFile ? "ap-coding-v1.jsonl" : "ap-coding-seed.jsonl";
  const cashName = replayFile ? "cash-app-v1.jsonl" : "cash-app-seed.jsonl";
  const apText = readFileSync(join(dir, apName), "utf8");
  const cashText = readFileSync(join(dir, cashName), "utf8");
  const replay = replayFile ? new ReplayPredictor(parseRecording(readFileSync(replayFile, "utf8"), replayFile)) : undefined;
  const mrpText = readFileSync(join(dir, "mrp-triage-seed.jsonl"), "utf8");
  const anomalyText = readFileSync(join(dir, "anomaly-seed.jsonl"), "utf8");
  const askDataText = readFileSync(join(dir, "ask-data-seed.jsonl"), "utf8");
  const ap = parseJsonl(apText, isApCodingCase, apName);
  const cash = parseJsonl(cashText, isCashAppCase, cashName);
  const mrp = parseJsonl(mrpText, isMrpTriageCase, "mrp-triage-seed.jsonl");
  const anomaly = parseJsonl(anomalyText, isAnomalyCase, "anomaly-seed.jsonl");
  const askData = parseJsonl(askDataText, isAskDataCase, "ask-data-seed.jsonl");
  const datasets = {
    ap_coding: { id: apName.replace(".jsonl", ""), version: datasetVersion(apText) },
    cash_app: { id: cashName.replace(".jsonl", ""), version: datasetVersion(cashText) },
    mrp_triage: { id: "mrp-triage-seed", version: datasetVersion(mrpText) },
    anomaly: { id: "anomaly-seed", version: datasetVersion(anomalyText) },
    ask_data: { id: "ask-data-seed", version: datasetVersion(askDataText) },
  };

  const results = {
    ap_coding: evaluateApCoding(ap, replay?.ap ?? vendorHistoryBaseline),
    cash_app: evaluateCashApp(cash, replay?.cash ?? remittanceBaseline),
    mrp_triage: evaluateMrpTriage(mrp, mrpKeywordBaseline),
    anomaly: evaluateAnomaly(anomaly, offHoursAnomalyBaseline),
    ask_data: evaluateAskData(askData, askDataBaseline),
  } as const;
  const verdicts = {
    ap_coding: gate("ap_coding", results.ap_coding),
    cash_app: gate("cash_app", results.cash_app),
    mrp_triage: gate("mrp_triage", results.mrp_triage),
    anomaly: gate("anomaly", results.anomaly),
    ask_data: gate("ask_data", results.ask_data),
  };
  process.stdout.write(`${JSON.stringify({ datasets, results, verdicts }, null, 2)}\n`);
  const failed = Object.values(verdicts).some((v) => !v.passed);
  return enforce && failed ? 1 : 0;
}

try {
  process.exitCode = main(process.argv.slice(2));
} catch (err) {
  process.stderr.write(`${err instanceof Error ? err.message : String(err)}\n`);
  process.exitCode = 2;
}

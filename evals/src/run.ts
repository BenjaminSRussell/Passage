import { readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { vendorHistoryBaseline, remittanceBaseline } from "./baselines.js";
import { datasetVersion, isApCodingCase, isCashAppCase, parseJsonl } from "./datasets.js";
import { evaluateApCoding, evaluateCashApp } from "./harness.js";
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
  const ap = parseJsonl(apText, isApCodingCase, "ap-coding-seed.jsonl");
  const cash = parseJsonl(cashText, isCashAppCase, "cash-app-seed.jsonl");
  const datasets = {
    ap_coding: { id: "ap-coding-seed", version: datasetVersion(apText) },
    cash_app: { id: "cash-app-seed", version: datasetVersion(cashText) },
  };

  const results = {
    ap_coding: evaluateApCoding(ap, vendorHistoryBaseline),
    cash_app: evaluateCashApp(cash, remittanceBaseline),
  } as const;
  const verdicts = {
    ap_coding: gate("ap_coding", results.ap_coding),
    cash_app: gate("cash_app", results.cash_app),
  };
  process.stdout.write(`${JSON.stringify({ datasets, results, verdicts }, null, 2)}\n`);
  const failed = Object.values(verdicts).some((v) => !v.passed);
  return enforce && failed ? 1 : 0;
}

process.exitCode = main(process.argv.slice(2));

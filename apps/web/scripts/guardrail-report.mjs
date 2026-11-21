import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";

const OUTCOME_ID = /^O(?:[1-9]|10)$/;
const PATH_STATUSES = ["present", "pending"];
const DEFAULT_RESULTS = "test-results/guardrails/results.json";
const DEFAULT_REGISTRY = join(import.meta.dirname, "../e2e/guardrails/registry.json");

const isRecord = (value) => typeof value === "object" && value !== null && !Array.isArray(value);
const isText = (value) => typeof value === "string" && value.trim() !== "";

export function validateRegistry(registry) {
  if (!isRecord(registry) || !Array.isArray(registry.outcomes) || !Array.isArray(registry.paths)) throw new Error("registry needs outcomes and paths arrays");
  const outcomeIds = new Set();
  for (const outcome of registry.outcomes) {
    if (!isRecord(outcome) || !OUTCOME_ID.test(outcome.id) || !isText(outcome.title)) throw new Error(`registry outcome needs an id O1 to O10 and a title: ${JSON.stringify(outcome)}`);
    if (outcomeIds.has(outcome.id)) throw new Error(`registry outcome ${outcome.id} is listed twice`);
    outcomeIds.add(outcome.id);
  }
  const pathIds = new Set();
  for (const path of registry.paths) {
    if (!isRecord(path) || !isText(path.id)) throw new Error(`registry path needs an id: ${JSON.stringify(path)}`);
    if (pathIds.has(path.id)) throw new Error(`registry path ${path.id} is listed twice`);
    pathIds.add(path.id);
    if (!PATH_STATUSES.includes(path.status)) throw new Error(`registry path ${path.id} needs a status of present or pending`);
    if (path.status === "pending" && !isText(path.ticket)) throw new Error(`registry path ${path.id} is pending and needs a ticket`);
    if (!Array.isArray(path.outcomes) || path.outcomes.length === 0 || !path.outcomes.every((id) => outcomeIds.has(id))) {
      throw new Error(`registry path ${path.id} needs outcomes that all appear in the registry outcomes`);
    }
  }
  return registry;
}

function collectScenarios(suites, trail, found) {
  for (const suite of suites) {
    if (!isRecord(suite)) throw new Error("malformed results: a suite is not an object");
    const here = suite.file === suite.title ? trail : [...trail, suite.title];
    for (const spec of suite.specs ?? []) {
      for (const test of spec.tests ?? []) {
        const annotations = test.annotations ?? [];
        if (!Array.isArray(annotations) || !annotations.every((a) => isRecord(a) && typeof a.type === "string")) throw new Error("malformed results: annotations must be objects with a type");
        const outcomes = annotations.filter((a) => a.type === "outcome").map((a) => {
          if (typeof a.description !== "string") throw new Error("malformed results: an outcome annotation has no description");
          return a.description;
        });
        const knownGap = annotations.find((a) => a.type === "fail");
        found.push({ title: [...here, spec.title].join(" > "), project: test.projectName, status: knownGap ? "known-gap" : test.status, outcomes, ...(knownGap ? { gap: knownGap.description ?? "" } : {}) });
      }
    }
    collectScenarios(suite.suites ?? [], here, found);
  }
  return found;
}

export function buildReport(results, registry) {
  validateRegistry(registry);
  if (!isRecord(results) || !Array.isArray(results.suites)) throw new Error("malformed results: no suites array");
  const scenarios = collectScenarios(results.suites, [], []);
  const known = new Set(registry.outcomes.map((outcome) => outcome.id));
  for (const id of scenarios.flatMap((scenario) => scenario.outcomes)) {
    if (!known.has(id)) throw new Error(`scenario names outcome ${id}, which is not in the registry`);
  }
  const pending = registry.paths.filter((path) => path.status === "pending");
  const outcomes = registry.outcomes.map((outcome) => {
    const passing = scenarios.filter((scenario) => scenario.status === "expected" && scenario.outcomes.includes(outcome.id));
    const tickets = [...new Set(pending.filter((path) => path.outcomes.includes(outcome.id)).map((path) => path.ticket))];
    const status = passing.length > 0 ? (tickets.length > 0 ? "partial" : "covered") : tickets.length > 0 ? `pending ${tickets.join(", ")}` : "uncovered";
    return { id: outcome.id, title: outcome.title, status, scenarios: passing.length, pendingTickets: tickets };
  });
  const themes = scenarios.some((scenario) => scenario.status === "expected" && scenario.title.includes(", dark,")) ? "light and dark" : "light only";
  return {
    themes,
    outcomes,
    pending: pending.map((path) => ({ id: path.id, ticket: path.ticket, outcomes: path.outcomes })),
    knownGaps: scenarios.filter((scenario) => scenario.status === "known-gap").map(({ title, project, gap }) => ({ title, project, gap })),
    scenarios,
    ok: outcomes.every((outcome) => outcome.status !== "uncovered"),
  };
}

export function renderMarkdown(report) {
  const rows = report.outcomes.map((outcome) => `| ${outcome.id} | ${outcome.status} | ${outcome.scenarios} |`);
  const pending = report.pending.map((path) => `| ${path.id} | pending ${path.ticket} | ${path.outcomes.join(", ")} |`);
  return [
    "# Guardrail traceability",
    "",
    `Themes scanned: ${report.themes}. Nightly real-stack run: not built.`,
    "",
    "| Outcome | Status | Passing scenarios |",
    "|---|---|---|",
    ...rows,
    "",
    "| Path | Status | Outcomes |",
    "|---|---|---|",
    ...pending,
    "",
    "Known gaps (scenarios marked as expected to fail, not counted as covering):",
    "",
    ...report.knownGaps.map((gap) => `- ${gap.title} [${gap.project}]: ${gap.gap}`),
    "",
  ].join("\n");
}

export function run(resultsPath = DEFAULT_RESULTS, registryPath = DEFAULT_REGISTRY) {
  if (!existsSync(resultsPath)) throw new Error(`results not found at ${resultsPath}; run the guardrails projects first`);
  let results;
  try {
    results = JSON.parse(readFileSync(resultsPath, "utf8"));
  } catch {
    throw new Error(`malformed results: ${resultsPath} is not valid JSON`);
  }
  const report = buildReport(results, JSON.parse(readFileSync(registryPath, "utf8")));
  const outDir = dirname(resolve(resultsPath));
  writeFileSync(join(outDir, "traceability.json"), `${JSON.stringify(report, null, 2)}\n`);
  writeFileSync(join(outDir, "traceability.md"), renderMarkdown(report));
  return report;
}

if (process.argv[1] && resolve(process.argv[1]) === import.meta.filename) {
  try {
    const report = run(process.argv[2]);
    process.stdout.write(renderMarkdown(report));
    process.exit(report.ok ? 0 : 1);
  } catch (error) {
    process.stderr.write(`guardrail-report: ${error.message}\n`);
    process.exit(1);
  }
}

import { spawnSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { buildReport, renderMarkdown, validateRegistry } from "./guardrail-report.mjs";

const SCRIPT = join(import.meta.dirname, "guardrail-report.mjs");
const realRegistry = JSON.parse(readFileSync(join(import.meta.dirname, "../e2e/guardrails/registry.json"), "utf8"));

const registry = (paths) => ({
  outcomes: [
    { id: "O1", title: "first" },
    { id: "O2", title: "second" },
  ],
  paths,
});
const present = { id: "home", status: "present", outcomes: ["O1"] };
const pending = { id: "feed", status: "pending", ticket: "DEPOT-346", outcomes: ["O2"] };
const scenario = (outcomes, status = "expected", title = "scenario") => ({ title, tests: [{ projectName: "guardrails", status, annotations: outcomes.map((description) => ({ type: "outcome", description })) }] });
const results = (...specs) => ({ suites: [{ title: "a.spec.ts", file: "a.spec.ts", specs, suites: [] }] });
const statusOf = (report, id) => report.outcomes.find((outcome) => outcome.id === id).status;

describe("guardrail-report CLI", () => {
  it("exits 1 naming the path when results.json is missing (day-zero probe)", () => {
    const missing = join(mkdtempSync(join(tmpdir(), "guardrail-")), "results.json");
    const run = spawnSync("node", [SCRIPT, missing], { encoding: "utf8" });
    expect(run.status).toBe(1);
    expect(run.stderr).toContain(missing);
  });

  it("exits 1 on results that are not valid JSON", () => {
    const file = join(mkdtempSync(join(tmpdir(), "guardrail-")), "results.json");
    writeFileSync(file, "{not json");
    const run = spawnSync("node", [SCRIPT, file], { encoding: "utf8" });
    expect(run.status).toBe(1);
    expect(run.stderr).toContain("malformed results");
  });

  it("writes traceability.json and traceability.md beside the results and exits 0 when every outcome is accounted for", () => {
    const dir = mkdtempSync(join(tmpdir(), "guardrail-"));
    const specs = realRegistry.outcomes.map((outcome) => scenario([outcome.id], "expected", outcome.id));
    writeFileSync(join(dir, "results.json"), JSON.stringify(results(...specs)));
    const run = spawnSync("node", [SCRIPT, join(dir, "results.json")], { encoding: "utf8" });
    expect(run.status).toBe(0);
    expect(JSON.parse(readFileSync(join(dir, "traceability.json"), "utf8")).ok).toBe(true);
    expect(readFileSync(join(dir, "traceability.md"), "utf8")).toContain("pending DEPOT-347");
  });
});

describe("buildReport", () => {
  it.each([[{}], [{ suites: "none" }], [{ suites: {} }], [null]])("rejects results shaped %j", (bad) => {
    expect(() => buildReport(bad, registry([present]))).toThrow("malformed results");
  });

  it("rejects an annotation whose description is not text", () => {
    expect(() => buildReport(results({ title: "x", tests: [{ status: "expected", annotations: [{ type: "outcome", description: 7 }] }] }), registry([present]))).toThrow("malformed results");
  });

  it("rejects an outcome id the registry does not list", () => {
    expect(() => buildReport(results(scenario(["O11"])), registry([present]))).toThrow("O11");
  });

  it("marks an outcome with no passing scenario and no pending ticket uncovered", () => {
    const report = buildReport(results(scenario(["O1"])), registry([present]));
    expect(statusOf(report, "O2")).toBe("uncovered");
    expect(report.ok).toBe(false);
  });

  it.each(["unexpected", "skipped", "flaky"])("does not count a %s scenario as covering", (status) => {
    const report = buildReport(results(scenario(["O1"], status)), registry([present]));
    expect(statusOf(report, "O1")).toBe("uncovered");
  });

  it("reports an outcome whose only path is pending as pending its ticket, and a covered one with a pending path as partial", () => {
    const only = buildReport(results(scenario(["O1"])), registry([present, pending]));
    expect(statusOf(only, "O2")).toBe("pending DEPOT-346");
    expect(only.ok).toBe(true);
    const partial = buildReport(results(scenario(["O1", "O2"])), registry([present, pending]));
    expect(statusOf(partial, "O2")).toBe("partial");
    expect(statusOf(partial, "O1")).toBe("covered");
  });

  it("reads scenarios from nested suites and records both themes when a dark scenario passed", () => {
    const nested = { suites: [{ title: "a.spec.ts", file: "a.spec.ts", specs: [], suites: [{ title: "home, dark, mobile", file: "a.spec.ts", specs: [scenario(["O1"])], suites: [] }] }] };
    const report = buildReport(nested, registry([present, pending]));
    expect(report.themes).toBe("light and dark");
    expect(report.scenarios[0].title).toBe("home, dark, mobile > scenario");
  });

  it("lists a scenario marked as expected to fail as a known gap and does not count it as covering", () => {
    const gap = { title: "gap", tests: [{ projectName: "guardrails", status: "expected", annotations: [{ type: "outcome", description: "O1" }, { type: "fail", description: "DEPOT-291: alert on the home" }] }] };
    const report = buildReport(results(gap), registry([present]));
    expect(statusOf(report, "O1")).toBe("uncovered");
    expect(report.knownGaps).toEqual([{ title: "gap", project: "guardrails", gap: "DEPOT-291: alert on the home" }]);
    expect(renderMarkdown(report)).toContain("DEPOT-291: alert on the home");
  });

  it("renders the pending ticket in the markdown table", () => {
    expect(renderMarkdown(buildReport(results(scenario(["O1"])), registry([present, pending])))).toContain("| feed | pending DEPOT-346 | O2 |");
  });
});

describe("validateRegistry", () => {
  it("accepts the committed registry", () => {
    expect(validateRegistry(realRegistry)).toBe(realRegistry);
  });

  it.each([
    ["an outcome without a title", { outcomes: [{ id: "O1" }], paths: [] }],
    ["an outcome id outside O1 to O10", { outcomes: [{ id: "O11", title: "x" }], paths: [] }],
    ["a duplicate outcome", { outcomes: [{ id: "O1", title: "x" }, { id: "O1", title: "y" }], paths: [] }],
    ["a path status other than present or pending", registry([{ ...present, status: "done" }])],
    ["a pending path without a ticket", registry([{ id: "feed", status: "pending", outcomes: ["O2"] }])],
    ["a duplicate path id", registry([present, present])],
    ["an outcomes array holding text that is not a registry outcome", registry([{ ...present, outcomes: ["first"] }])],
    ["a path with no outcomes", registry([{ ...present, outcomes: [] }])],
    ["a registry that is not an object", "registry"],
  ])("rejects %s", (_name, bad) => {
    expect(() => validateRegistry(bad)).toThrow();
  });
});

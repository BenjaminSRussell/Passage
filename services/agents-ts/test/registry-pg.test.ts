import { randomUUID } from "node:crypto";
import { cpSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import pg from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { evalResults, parseEvalReport } from "../src/cli/record-eval.js";
import { buildActionRecord } from "../src/domain/audit.js";
import { promptHash } from "../src/domain/registry.js";
import type { RunResult } from "../src/runtime/engine.js";
import { migrate, MIGRATIONS_DIR } from "../src/store/pg.js";
import { PgVersionStore } from "../src/store/registry.js";
import { makeRequest, TENANT } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const [ap, cash] = defaultCatalog("claude-sonnet-5-5");
const apOpus = { ...ap!, modelId: "claude-opus-5-5" };

const report = {
  datasets: { ap_coding: { id: "ap-coding-v1", version: "0123456789abcdef" }, ask_data: { id: "ask-data-seed", version: "fedcba9876543210" } },
  results: { ap_coding: { top1: 0.95, top3: 0.99 }, ask_data: { selection: 1 } },
  verdicts: { ap_coding: { passed: true, failures: [] }, ask_data: { passed: true, failures: [] } },
};

const action = (runId: string, seq: number) =>
  buildActionRecord({
    runId,
    seq,
    toolName: "passage.ap.invoice.propose_coding",
    inputRedacted: "{}",
    outputRedacted: "{}",
    approverUserId: null,
    decision: "REJECTED",
    decidedAt: "2026-11-13T10:00:00.000Z",
  });

const finished = (runId: string, seqs: number[]): RunResult => ({
  runId,
  status: "REJECTED",
  actions: seqs.map((s) => action(runId, s)),
  tokensIn: 1,
  tokensOut: 1,
  estimated: false,
  budgetState: "OK",
  displayText: "",
});

describe("eval report parsing (record-eval)", () => {
  it("pairs each verdict with the deployment's definition and names what it skipped", () => {
    const parsed = parseEvalReport(report);
    const out = evalResults(parsed, defaultCatalog("claude-sonnet-5-5"), "ci");
    expect(out.results.map((r) => r.agent.agentId)).toEqual(["ap_coding"]);
    expect(out.results[0]).toMatchObject({ datasetId: "ap-coding-v1", datasetVersion: "0123456789abcdef", metrics: { top1: 0.95 }, failures: [], recordedBy: "ci" });
    expect(out.skipped).toEqual(["ask_data"]);
    expect(out.notEvaluated).toEqual(["cash_app", "mrp_triage", "collections", "anomaly"]);
  });

  it.each([
    [{}, /datasets, results and verdicts/],
    [{ ...report, verdicts: { ap_coding: { passed: true, failures: ["top1 below 0.92"] } } }, /passed must mean no failures/],
    [{ ...report, datasets: {} }, /dataset for ap_coding/],
    [{ ...report, results: { ap_coding: { top1: "high" }, ask_data: {} } }, /finite numbers/],
  ])("refuses a malformed report whole (%#)", (raw, message) => {
    expect(() => parseEvalReport(raw)).toThrow(message);
  });
});

describe.runIf(ownerUrl)("agent_version, eval_run and the partitioned agent_action in Postgres", () => {
  let fx: Fixture;
  let versions: PgVersionStore;

  beforeAll(async () => {
    fx = await fixture();
    versions = new PgVersionStore(fx.app);
  });
  afterAll(async () => {
    await fx.close();
  });

  const evalOf = (agent: typeof ap, failures: string[] = []) => ({ agent: agent!, datasetId: "ap-coding-v1", datasetVersion: "v1", metrics: { top1: 0.95 }, failures, recordedBy: "ci" });

  it("registers a definition once, and the same version with another model as a second definition", async () => {
    await versions.register([ap!, ap!, apOpus]);
    await versions.register([ap!]);
    const rows = await fx.app.query("select model_id, prompt_hash from agent_version where agent_id = 'ap_coding' order by model_id");
    expect(rows.rows).toEqual([
      { model_id: "claude-opus-5-5", prompt_hash: promptHash(apOpus) },
      { model_id: "claude-sonnet-5-5", prompt_hash: promptHash(ap!) },
    ]);
  });

  it("a definition may run only when its newest eval run passed, and only that exact definition", async () => {
    expect(await versions.evalPassed(cash!)).toBe(false);
    await versions.recordEval({ ...evalOf(cash), datasetId: "cash-app-v1" });
    expect(await versions.evalPassed(cash!)).toBe(true);
    expect(await versions.evalPassed({ ...cash!, modelId: "claude-haiku-5-5" })).toBe(false);
    // A later failing run, say on a new dataset version, blocks it again (BR-47.8).
    await versions.recordEval({ ...evalOf(cash, ["precision 0.81 below 0.90"]), datasetId: "cash-app-v1", datasetVersion: "v2" });
    expect(await versions.evalPassed(cash!)).toBe(false);
    const runs = await versions.evalRuns(cash!);
    expect(runs.map((r) => [r.datasetVersion, r.passed])).toEqual([
      ["v2", false],
      ["v1", true],
    ]);
    expect(runs[0]).toMatchObject({ failures: ["precision 0.81 below 0.90"], recordedBy: "ci", promptHash: promptHash(cash!) });
  });

  it("versions and eval runs are append-only for the application role, and an eval must name a registered definition", async () => {
    await versions.recordEval(evalOf(ap));
    await expect(fx.app.query("update agent_version set system_prompt = 'x'")).rejects.toThrow(/permission denied/);
    await expect(fx.app.query("delete from agent_version")).rejects.toThrow(/permission denied/);
    await expect(fx.app.query("update eval_run set passed = true")).rejects.toThrow(/permission denied/);
    await expect(fx.app.query("delete from eval_run")).rejects.toThrow(/permission denied/);
    await expect(
      fx.app.query(
        "insert into eval_run (eval_run_id, agent_id, version, prompt_hash, dataset_id, dataset_version, passed, metrics, recorded_by) values ($1, 'ap_coding', 9, $2, 'd', 'v', true, '{}', 'ci')",
        [randomUUID(), promptHash(ap!)],
      ),
    ).rejects.toThrow(/foreign key/);
    await expect(
      fx.app.query(
        "insert into eval_run (eval_run_id, agent_id, version, prompt_hash, dataset_id, dataset_version, passed, metrics, failures, recorded_by) values ($1, 'ap_coding', 1, $2, 'd', 'v', true, '{}', '[\"x\"]', 'ci')",
        [randomUUID(), promptHash(ap!)],
      ),
    ).rejects.toThrow(/check constraint/);
  });

  /** Where each action row of the run is stored. */
  const partitionsOf = async (runId: string) =>
    (
      await fx.store.withTenant(TENANT, (c) =>
        c.query<{ part: string; run_month: string }>("select tableoid::regclass::text as part, run_month::text from agent_action where run_id = $1 order by seq", [runId]),
      )
    ).rows;

  it("stores a run's actions in the partition of the month the run started", async () => {
    const req = makeRequest({ runId: `part-${randomUUID()}` });
    await fx.store.startRun(req);
    await fx.store.finishRun(req, finished(req.runId, [1, 2]));
    const month = new Date().toISOString().slice(0, 7);
    const rows = await partitionsOf(req.runId);
    expect(rows).toEqual([
      { part: `agent_action_${month.replace("-", "")}`, run_month: `${month}-01` },
      { part: `agent_action_${month.replace("-", "")}`, run_month: `${month}-01` },
    ]);
    expect((await fx.store.loadActions(TENANT, req.runId)).map((a) => a.seq)).toEqual([1, 2]);
  });

  it("a month without a partition falls into the default partition, and creating the month moves its rows out", async () => {
    const runId = `old-${randomUUID()}`;
    // A run from a month the pods never prepared (the owner can set created_at; the application role cannot).
    await fx.owner.query("begin");
    try {
      await fx.owner.query("select set_config('passage.tenant_id', $1, true)", [TENANT]);
      await fx.owner.query(
        "insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, tokens_in, tokens_out, estimated, created_at) values ($1, $2, 'ap_coding', 1, 'u', 'RUNNING', 0, 0, false, '2020-01-15T12:00:00Z')",
        [TENANT, runId],
      );
      await fx.owner.query("commit");
    } catch (err) {
      await fx.owner.query("rollback");
      throw err;
    }
    const req = makeRequest({ runId });
    await fx.store.finishRun(req, finished(runId, [1]));
    expect(await partitionsOf(runId)).toEqual([{ part: "agent_action_default", run_month: "2020-01-01" }]);
    expect((await fx.owner.query("select agent_action_create_partitions('2020-01-01', '2020-01-01') as made")).rows[0].made).toBe(1);
    expect(await partitionsOf(runId)).toEqual([{ part: "agent_action_202001", run_month: "2020-01-01" }]);
    // The application role may only prepare the coming months, and only through the one function.
    await expect(fx.app.query("select agent_action_create_partitions('2019-01-01', '2019-02-01')")).rejects.toThrow(/permission denied/);
    await expect(fx.app.query("select agent_action_ensure_partitions(13)")).rejects.toThrow(/0 to 12/);
  });

  it("every pod may ensure the coming partitions at once; only missing months are created", async () => {
    const made = await Promise.all(Array.from({ length: 5 }, () => fx.store.ensureActionPartitions(4)));
    expect(made.reduce((a, b) => a + b, 0)).toBe(2); // the migration prepared this month and the next two
    expect(await fx.store.ensureActionPartitions(4)).toBe(0);
  });

  it("keeps (tenant, run, seq) unique and pins run_month to the run's own month", async () => {
    const req = makeRequest({ runId: `uniq-${randomUUID()}` });
    await fx.store.startRun(req);
    await fx.store.finishRun(req, finished(req.runId, [1]));
    const insert = (seq: number, month: string) =>
      fx.store.withTenant(TENANT, (c) =>
        c.query(
          "insert into agent_action (tenant_id, run_id, run_month, seq, tool_name, input_json, output_hash, output_json, output_truncated, decision, decided_at) values ($1, $2, $3, $4, 't', '{}', $5, '{}', false, 'DENIED', now())",
          [TENANT, req.runId, month, seq, "0".repeat(64)],
        ),
      );
    const month = `${new Date().toISOString().slice(0, 7)}-01`;
    await expect(insert(1, month)).rejects.toThrow(/duplicate key/);
    await expect(insert(2, "2020-01-01")).rejects.toThrow(/foreign key/);
    await expect(fx.store.withTenant(TENANT, (c) => c.query("update agent_action set reason = 'x'"))).rejects.toThrow(/permission denied/);
    await expect(fx.app.query(`select 1 from agent_action_${month.slice(0, 7).replace("-", "")}`)).rejects.toThrow(/permission denied/);
    // created_at decides the partition, so a run cannot move it.
    await fx.store.startRun(makeRequest({ runId: `${req.runId}-b` }));
    await expect(
      fx.store.withTenant(TENANT, (c) => c.query("update agent_run set created_at = created_at - interval '40 days' where run_id = $1", [`${req.runId}-b`])),
    ).rejects.toThrow(/identity cannot change/);
  });
});

describe.runIf(ownerUrl)("migration 0006 on a database with actions", () => {
  it("moves the existing actions into monthly partitions with their run's month", async () => {
    const schema = `agents_${randomUUID().replaceAll("-", "").slice(0, 12)}`;
    const admin = new pg.Pool({ connectionString: ownerUrl, max: 1 });
    await admin.query(`create schema ${schema}`);
    const owner = new pg.Pool({ connectionString: ownerUrl, options: `-c search_path=${schema}`, max: 1 });
    const before = mkdtempSync(join(tmpdir(), "agents-mig-"));
    try {
      for (const n of readdirSync(MIGRATIONS_DIR).filter((n) => n.endsWith(".sql") && n < "0006")) cpSync(new URL(n, MIGRATIONS_DIR), join(before, n));
      await migrate(owner, pathToFileURL(`${before}/`));
      const c = await owner.connect();
      try {
        await c.query("begin");
        await c.query("select set_config('passage.tenant_id', $1, true)", [TENANT]);
        for (const [run, at] of [
          ["r-old", "2025-06-30T23:59:59Z"],
          ["r-new", new Date().toISOString()],
        ] as const) {
          await c.query(
            "insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, tokens_in, tokens_out, estimated, created_at) values ($1, $2, 'ap_coding', 1, 'u', 'RUNNING', 0, 0, false, $3)",
            [TENANT, run, at],
          );
          for (const seq of [1, 2]) {
            await c.query(
              "insert into agent_action (tenant_id, run_id, seq, tool_name, input_json, output_hash, output_json, output_truncated, decision, decided_at) values ($1, $2, $3, 't', '{}', $4, '{}', false, 'DENIED', now())",
              [TENANT, run, seq, "0".repeat(64)],
            );
          }
        }
        await c.query("commit");
      } finally {
        c.release();
      }
      await migrate(owner);
      // The migration lifts FORCE row-level security for the copy only.
      const seen = await owner.query("select (select relforcerowsecurity from pg_class where oid = 'agent_run'::regclass) as run_forced, (select relforcerowsecurity from pg_class where oid = 'agent_action'::regclass) as action_forced");
      expect(seen.rows[0]).toEqual({ run_forced: true, action_forced: true });
      const c2 = await owner.connect();
      try {
        await c2.query("begin");
        await c2.query("select set_config('passage.tenant_id', $1, true)", [TENANT]);
        const moved = await c2.query("select run_id, seq, run_month::text, tableoid::regclass::text as part from agent_action order by run_id, seq");
        const month = new Date().toISOString().slice(0, 7);
        expect(moved.rows).toEqual([
          { run_id: "r-new", seq: 1, run_month: `${month}-01`, part: `agent_action_${month.replace("-", "")}` },
          { run_id: "r-new", seq: 2, run_month: `${month}-01`, part: `agent_action_${month.replace("-", "")}` },
          { run_id: "r-old", seq: 1, run_month: "2025-06-01", part: "agent_action_202506" },
          { run_id: "r-old", seq: 2, run_month: "2025-06-01", part: "agent_action_202506" },
        ]);
        await c2.query("rollback");
      } finally {
        c2.release();
      }
      expect((await owner.query("select to_regclass('agent_action_unpartitioned') as old")).rows[0].old).toBeNull();
    } finally {
      rmSync(before, { recursive: true, force: true });
      await owner.end();
      await admin.query(`drop schema ${schema} cascade`);
      await admin.end();
    }
  });
});

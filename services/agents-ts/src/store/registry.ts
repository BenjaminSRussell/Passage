import { randomUUID } from "node:crypto";
import type pg from "pg";
import { promptHash, type AgentVersion } from "../domain/registry.js";

/** One gate result of the eval pipeline for one agent definition (evals/src/run.ts report). */
export interface EvalResult {
  agent: AgentVersion;
  datasetId: string;
  datasetVersion: string;
  metrics: Readonly<Record<string, number>>;
  /** The gate's failures; empty means the definition passed. */
  failures: readonly string[];
  /** Who recorded it: the pipeline job, never a person's id. */
  recordedBy: string;
}

export interface StoredEvalRun {
  evalRunId: string;
  agentId: string;
  version: number;
  promptHash: string;
  datasetId: string;
  datasetVersion: string;
  passed: boolean;
  metrics: Record<string, number>;
  failures: string[];
  recordedBy: string;
  recordedAt: string;
}

interface EvalRow {
  eval_run_id: string;
  agent_id: string;
  version: number;
  prompt_hash: string;
  dataset_id: string;
  dataset_version: string;
  passed: boolean;
  metrics: Record<string, number>;
  failures: string[];
  recorded_by: string;
  recorded_at: Date;
}

const asEvalRun = (r: EvalRow): StoredEvalRun => ({
  evalRunId: r.eval_run_id,
  agentId: r.agent_id,
  version: r.version,
  promptHash: r.prompt_hash,
  datasetId: r.dataset_id,
  datasetVersion: r.dataset_version,
  passed: r.passed,
  metrics: r.metrics,
  failures: r.failures,
  recordedBy: r.recorded_by,
  recordedAt: r.recorded_at.toISOString(),
});

/**
 * `agent_version` and `eval_run` (migration 0006, FR-47.10, BR-47.8). A definition is its agent id, version and
 * prompt hash; an eval passes one exact definition. These are platform rows, the same for every tenant, so the
 * statements run on the pool without a tenant setting. `recordEval` needs the owner role (the eval pipeline,
 * cli/record-eval.ts): the pods' passage_app role may only read eval_run.
 */
export class PgVersionStore {
  constructor(private readonly pool: pg.Pool) {}

  /** Stores each definition once; registering the same definition again changes nothing. */
  async register(versions: readonly AgentVersion[]): Promise<void> {
    for (const v of versions) {
      if (!Number.isSafeInteger(v.version) || v.version < 1) throw new Error(`${v.agentId}: version must be a positive integer`);
      await this.pool.query(
        `insert into agent_version (agent_id, version, prompt_hash, model_id, system_prompt, allowed_tools, required_duties)
         values ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb) on conflict (agent_id, version, prompt_hash) do nothing`,
        [v.agentId, v.version, promptHash(v), v.modelId, v.systemPrompt, JSON.stringify(v.allowedTools), JSON.stringify(v.requiredDuties)],
      );
    }
  }

  /** Records one gate result, registering its definition first. Returns the eval run id. */
  async recordEval(e: EvalResult): Promise<string> {
    for (const [k, n] of Object.entries(e.metrics)) {
      if (typeof n !== "number" || !Number.isFinite(n)) throw new Error(`metric ${k} must be a finite number`);
    }
    await this.register([e.agent]);
    const id = randomUUID();
    await this.pool.query(
      `insert into eval_run (eval_run_id, agent_id, version, prompt_hash, dataset_id, dataset_version, passed, metrics, failures, recorded_by)
       values ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10)`,
      [
        id,
        e.agent.agentId,
        e.agent.version,
        promptHash(e.agent),
        e.datasetId,
        e.datasetVersion,
        e.failures.length === 0,
        JSON.stringify(e.metrics),
        JSON.stringify(e.failures),
        e.recordedBy,
      ],
    );
    return id;
  }

  /** The definition's eval runs, newest first. */
  async evalRuns(agent: AgentVersion): Promise<StoredEvalRun[]> {
    const res = await this.pool.query<EvalRow>(
      "select * from eval_run where agent_id = $1 and version = $2 and prompt_hash = $3 order by recorded_at desc, eval_run_id",
      [agent.agentId, agent.version, promptHash(agent)],
    );
    return res.rows.map(asEvalRun);
  }

  /**
   * True when the newest eval run of this exact definition passed (AGT-0204 otherwise). A definition never
   * evaluated, or evaluated with another model or prompt, has not passed.
   */
  async evalPassed(agent: AgentVersion): Promise<boolean> {
    const res = await this.pool.query<{ passed: boolean }>(
      // Two runs recorded at the same instant: the failing one wins.
      "select passed from eval_run where agent_id = $1 and version = $2 and prompt_hash = $3 order by recorded_at desc, passed limit 1",
      [agent.agentId, agent.version, promptHash(agent)],
    );
    return res.rows[0]?.passed ?? false;
  }
}

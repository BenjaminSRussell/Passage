import { randomUUID } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import pg from "pg";
import type { ActionRecord, Decision } from "../domain/audit.js";
import { budgetStatus, type BudgetState, type TokenBudget } from "../domain/budget.js";
import { budgetThresholdEvent } from "../domain/events.js";
import { AgentError } from "../errors.js";
import type {
  AuditPort,
  BudgetHold,
  BudgetPort,
  BudgetRequest,
  ModelUsage,
  RunRequest,
  RunResult,
} from "../runtime/engine.js";

export const MIGRATIONS_DIR = new URL("../../migrations/", import.meta.url);

/** Same contract as integration-hub-ts pgStore.migrate (dbx.Migrate); the two are separate npm packages with no shared TS library to import it from. */
export async function migrate(pool: pg.Pool, dir: URL = MIGRATIONS_DIR): Promise<void> {
  await pool.query(
    "create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())",
  );
  const names = (await readdir(dir)).filter((n) => n.endsWith(".sql")).sort();
  for (const name of names) {
    const done = await pool.query("select 1 from schema_migrations where name = $1", [name]);
    if (done.rowCount) continue;
    const body = await readFile(new URL(name, dir), "utf8");
    const client = await pool.connect();
    try {
      await client.query("begin");
      await client.query(body);
      await client.query("insert into schema_migrations(name) values ($1)", [name]);
      await client.query("commit");
    } catch (err) {
      await client.query("rollback").catch(() => undefined);
      throw new Error(`migration ${name}: ${(err as Error).message}`);
    } finally {
      client.release();
    }
  }
}

export interface BudgetLimits {
  limitIn: number;
  limitOut: number;
  softPct: number;
}

/** FR-47.11 default monthly budget per tenant: 50M input and 10M output tokens, soft alert at 80% (ai.budget.soft_pct). */
export const DEFAULT_BUDGET: BudgetLimits = { limitIn: 50_000_000, limitOut: 10_000_000, softPct: 80 };

const SEVERITY: Record<BudgetState, number> = { OK: 0, SOFT: 1, HARD: 2 };

interface ActionRow {
  run_id: string;
  seq: number;
  tool_name: string;
  input_json: string;
  output_hash: string;
  output_json: string;
  output_truncated: boolean;
  approver_user_id: string | null;
  decision: Decision;
  decided_at: Date;
  committed_entity_ref: string | null;
  reason: string | null;
}

interface BudgetRow {
  limit_in: string;
  limit_out: string;
  used_in: string;
  used_out: string;
  soft_pct: number;
  alert_state: BudgetState;
}

const asTokenBudget = (r: BudgetRow): TokenBudget => ({
  limitIn: Number(r.limit_in),
  limitOut: Number(r.limit_out),
  usedIn: Number(r.used_in),
  usedOut: Number(r.used_out),
  softPct: r.soft_pct,
});

const assertTokens = (what: string, n: number, min: number): void => {
  if (!Number.isSafeInteger(n) || n < min) throw new Error(`${what} must be a safe integer of at least ${min}, got ${n}`);
};

/** Postgres store behind the audit and budget ports. Every statement runs in a tenant-scoped transaction under row-level security. */
export class PgAgentStore implements AuditPort, BudgetPort {
  /** `defaults` null means a tenant month with no configured row cannot run. */
  constructor(
    private readonly pool: pg.Pool,
    private readonly defaults: BudgetLimits | null = DEFAULT_BUDGET,
  ) {}

  async withTenant<T>(tenantId: string, fn: (client: pg.PoolClient) => Promise<T>): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query("begin");
      await client.query("select set_config('passage.tenant_id', $1, true)", [tenantId]);
      const out = await fn(client);
      await client.query("commit");
      return out;
    } catch (err) {
      await client.query("rollback").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  async startRun(req: RunRequest): Promise<void> {
    await this.withTenant(req.tenantId, (c) =>
      c.query(
        `insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, tokens_in, tokens_out, estimated)
         values ($1, $2, $3, $4, $5, 'RUNNING', 0, 0, false)`,
        [req.tenantId, req.runId, req.agent.agentId, req.agent.version, req.invokerUserId],
      ),
    );
  }

  async finishRun(req: RunRequest, result: RunResult, afterCommitError?: string): Promise<void> {
    await this.withTenant(req.tenantId, async (c) => {
      const res = await c.query(
        `update agent_run
            set status = $3, code = $4, reason = $5, tokens_in = $6, tokens_out = $7, estimated = $8, post_commit_error = $9
          where tenant_id = $1 and run_id = $2 and status = 'RUNNING'`,
        [
          req.tenantId,
          result.runId,
          afterCommitError === undefined ? result.status : "FAILED_AFTER_COMMIT",
          result.code ?? null,
          result.reason ?? null,
          result.tokensIn,
          result.tokensOut,
          result.estimated,
          afterCommitError ?? null,
        ],
      );
      if (res.rowCount === 0) throw new Error(`run ${result.runId} is not open for audit`);
      for (const a of result.actions) {
        await c.query(
          `insert into agent_action (tenant_id, run_id, seq, tool_name, input_json, output_hash, output_json, output_truncated, approver_user_id, decision, decided_at, committed_entity_ref, reason)
           values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
          [
            req.tenantId,
            a.runId,
            a.seq,
            a.toolName,
            a.inputJson,
            a.outputHash,
            a.outputJson,
            a.outputTruncated,
            a.approverUserId,
            a.decision,
            a.decidedAt,
            a.committedEntityRef,
            a.reason,
          ],
        );
      }
    });
  }

  async loadActions(tenantId: string, runId: string): Promise<ActionRecord[]> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<ActionRow>("select * from agent_action where run_id = $1 order by seq", [runId]);
      return res.rows.map((r) => ({
        runId: r.run_id,
        seq: r.seq,
        toolName: r.tool_name,
        inputJson: r.input_json,
        outputHash: r.output_hash,
        outputJson: r.output_json,
        outputTruncated: r.output_truncated,
        approverUserId: r.approver_user_id,
        decision: r.decision,
        decidedAt: r.decided_at.toISOString(),
        committedEntityRef: r.committed_entity_ref,
        reason: r.reason,
      }));
    });
  }

  /** Locks the tenant's budget row for the month, creating it from the default when none exists. */
  private async lockBudget(c: pg.PoolClient, tenantId: string, month: string): Promise<BudgetRow> {
    if (this.defaults !== null) {
      await c.query(
        "insert into token_budget (tenant_id, month, limit_in, limit_out, soft_pct) values ($1, $2, $3, $4, $5) on conflict do nothing",
        [tenantId, month, this.defaults.limitIn, this.defaults.limitOut, this.defaults.softPct],
      );
    }
    const res = await c.query<BudgetRow>("select * from token_budget where tenant_id = $1 and month = $2 for update", [tenantId, month]);
    const row = res.rows[0];
    if (row === undefined) throw new Error(`no token budget is configured for ${month} and defaults are disabled`);
    return row;
  }

  /** The stored budget for a run start: used tokens only, reserve is the authority on live holds. */
  async budgetSnapshot(tenantId: string, month: string): Promise<TokenBudget> {
    return this.withTenant(tenantId, async (c) => asTokenBudget(await this.lockBudget(c, tenantId, month)));
  }

  /** Sets a month's limits. Raising the limit lowers alert_state to the new status, so the next crossing alerts again. */
  async setBudget(tenantId: string, month: string, limits: BudgetLimits): Promise<void> {
    await this.withTenant(tenantId, async (c) => {
      await c.query(
        `insert into token_budget (tenant_id, month, limit_in, limit_out, soft_pct)
         values ($1, $2, $3, $4, $5)
         on conflict (tenant_id, month)
         do update set limit_in = $3, limit_out = $4, soft_pct = $5, updated_at = now()`,
        [tenantId, month, limits.limitIn, limits.limitOut, limits.softPct],
      );
      const row = await this.lockBudget(c, tenantId, month);
      const status = budgetStatus(asTokenBudget(row));
      if (SEVERITY[status.state] < SEVERITY[row.alert_state]) {
        await c.query("update token_budget set alert_state = $3 where tenant_id = $1 and month = $2", [tenantId, month, status.state]);
      }
    });
  }

  /**
   * The row lock serializes concurrent runs of a tenant. Expired holds (a worker that died before settling)
   * are swept first, charged in full and judged by the database clock; the hold lands only while used plus live holds plus this hold fit both limits (AGT-0202).
   */
  async reserve(r: BudgetRequest): Promise<BudgetHold> {
    assertTokens("tokensIn", r.tokensIn, 1);
    assertTokens("tokensOut", r.tokensOut, 1);
    return this.withTenant(r.tenantId, async (c) => {
      await this.lockBudget(c, r.tenantId, r.month);
      await c.query(
        `with swept as (delete from budget_hold where tenant_id = $1 and expires_at <= now() returning month, tokens_in, tokens_out),
              spent as (select month, sum(tokens_in) as tokens_in, sum(tokens_out) as tokens_out from swept group by month)
         update token_budget b set used_in = b.used_in + spent.tokens_in, used_out = b.used_out + spent.tokens_out, updated_at = now()
           from spent where b.tenant_id = $1 and b.month = spent.month`,
        [r.tenantId],
      );
      const row = (await c.query<BudgetRow>("select * from token_budget where tenant_id = $1 and month = $2", [r.tenantId, r.month])).rows[0]!;
      const held = (
        await c.query<{ held_in: string; held_out: string }>(
          "select coalesce(sum(tokens_in), 0) as held_in, coalesce(sum(tokens_out), 0) as held_out from budget_hold where tenant_id = $1 and month = $2",
          [r.tenantId, r.month],
        )
      ).rows[0]!;
      const b = asTokenBudget(row);
      if (b.usedIn + Number(held.held_in) + r.tokensIn > b.limitIn || b.usedOut + Number(held.held_out) + r.tokensOut > b.limitOut) {
        throw new AgentError("AGT-0202", `token budget for ${r.month} cannot cover a run of ${r.tokensIn} in / ${r.tokensOut} out`);
      }
      const holdId = randomUUID();
      await c.query(
        "insert into budget_hold (tenant_id, hold_id, month, agent_id, run_id, tokens_in, tokens_out, expires_at) values ($1, $2, $3, $4, $5, $6, $7, now() + $8 * interval '1 millisecond')",
        [r.tenantId, holdId, r.month, r.agentId, r.runId, r.tokensIn, r.tokensOut, r.ttlMs],
      );
      return { holdId, tenantId: r.tenantId, month: r.month };
    });
  }

  /** Drops the hold and charges the actual usage (a hold already swept was charged in full, so nothing more), and writes `budget.threshold` to the outbox in the same transaction when the budget moves up into SOFT or HARD. */
  async settle(hold: BudgetHold, actual: ModelUsage): Promise<BudgetState> {
    assertTokens("tokensIn", actual.tokensIn, 0);
    assertTokens("tokensOut", actual.tokensOut, 0);
    return this.withTenant(hold.tenantId, async (c) => {
      const locked = await c.query("select 1 from token_budget where tenant_id = $1 and month = $2 for update", [hold.tenantId, hold.month]);
      if (locked.rowCount === 0) throw new Error(`no token budget to settle for ${hold.month}`);
      const dropped = await c.query("delete from budget_hold where tenant_id = $1 and hold_id = $2", [hold.tenantId, hold.holdId]);
      const charge = dropped.rowCount === 0 ? { tokensIn: 0, tokensOut: 0 } : actual;
      const res = await c.query<BudgetRow>(
        `update token_budget
            set used_in = used_in + $3, used_out = used_out + $4, updated_at = now()
          where tenant_id = $1 and month = $2
      returning limit_in, limit_out, used_in, used_out, soft_pct, alert_state`,
        [hold.tenantId, hold.month, charge.tokensIn, charge.tokensOut],
      );
      const row = res.rows[0]!;
      const current = budgetStatus(asTokenBudget(row));
      const event = budgetThresholdEvent({ tenantId: hold.tenantId, month: hold.month, previous: row.alert_state, current });
      if (event === undefined) return current.state;
      await c.query("update token_budget set alert_state = $3 where tenant_id = $1 and month = $2", [hold.tenantId, hold.month, event.data.state]);
      await c.query(
        "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'token_budget', $3, $4, $5::jsonb)",
        [randomUUID(), hold.tenantId, hold.month, event.type, JSON.stringify(event.data)],
      );
      return current.state;
    });
  }
}

/**
 * Production wiring, the same shape as integration-hub-ts main: DATABASE_URL is required and
 * MIGRATE_ON_START=true runs the migrations (as the owner role) before the store is used.
 * AGENTS_BUDGET_DEFAULTS=off requires every tenant month's row to be configured.
 */
export async function openAgentStore(env: Record<string, string | undefined>): Promise<{ pool: pg.Pool; store: PgAgentStore }> {
  const url = env["DATABASE_URL"];
  if (url === undefined || url === "") throw new Error("DATABASE_URL is required: agent runs are not audited or budgeted without the store");
  const limit = (key: string, fallback: number): number => {
    const raw = env[key];
    if (raw === undefined || raw === "") return fallback;
    const n = Number(raw);
    assertTokens(key, n, 1);
    return n;
  };
  const max = limit("AGENTS_DB_POOL", 10);
  const defaults =
    env["AGENTS_BUDGET_DEFAULTS"] === "off"
      ? null
      : {
          limitIn: limit("AGENTS_BUDGET_DEFAULT_IN", DEFAULT_BUDGET.limitIn),
          limitOut: limit("AGENTS_BUDGET_DEFAULT_OUT", DEFAULT_BUDGET.limitOut),
          softPct: DEFAULT_BUDGET.softPct,
        };
  const pool = new pg.Pool({ connectionString: url, max });
  if (env["MIGRATE_ON_START"] === "true") await migrate(pool);
  return { pool, store: new PgAgentStore(pool, defaults) };
}

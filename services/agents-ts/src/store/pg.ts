import { randomUUID } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import type pg from "pg";
import type { ActionRecord, Decision } from "../domain/audit.js";
import { budgetStatus, type BudgetState } from "../domain/budget.js";
import { budgetThresholdEvent } from "../domain/events.js";
import { AgentError } from "../errors.js";
import type {
  AuditPort,
  BudgetHold,
  BudgetPort,
  ModelUsage,
  RunRequest,
  RunResult,
} from "../runtime/engine.js";

export const MIGRATIONS_DIR = new URL("../../migrations/", import.meta.url);

/** Applies migrations/*.sql in order, each in its own transaction (the dbx.Migrate contract). */
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

/** Postgres store behind the audit and budget ports. Every statement runs in a tenant-scoped transaction under row-level security. */
export class PgAgentStore implements AuditPort, BudgetPort {
  constructor(private readonly pool: pg.Pool) {}

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

  async saveRun(req: RunRequest, result: RunResult): Promise<void> {
    await this.withTenant(req.tenantId, async (c) => {
      await c.query(
        `insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, code, reason, tokens_in, tokens_out, estimated)
         values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
        [
          req.tenantId,
          result.runId,
          req.agent.agentId,
          req.agent.version,
          req.invokerUserId,
          result.status,
          result.code ?? null,
          result.reason ?? null,
          result.tokensIn,
          result.tokensOut,
          result.estimated,
        ],
      );
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

  async setBudget(tenantId: string, agentId: string, month: string, limits: BudgetLimits): Promise<void> {
    await this.withTenant(tenantId, async (c) => {
      await c.query(
        `insert into token_budget (tenant_id, agent_id, month, limit_in, limit_out, soft_pct)
         values ($1, $2, $3, $4, $5, $6)
         on conflict (tenant_id, agent_id, month)
         do update set limit_in = $4, limit_out = $5, soft_pct = $6, updated_at = now()`,
        [tenantId, agentId, month, limits.limitIn, limits.limitOut, limits.softPct],
      );
    });
  }

  /** One conditional UPDATE: the row lock serializes concurrent runs, and the reservation lands only while used plus reserved plus this hold fits both limits (AGT-0202 otherwise). */
  async reserve(hold: BudgetHold): Promise<void> {
    const res = await this.withTenant(hold.tenantId, (c) =>
      c.query(
        `update token_budget
            set reserved_in = reserved_in + $4, reserved_out = reserved_out + $5, updated_at = now()
          where tenant_id = $1 and agent_id = $2 and month = $3
            and used_in + reserved_in + $4 <= limit_in
            and used_out + reserved_out + $5 <= limit_out`,
        [hold.tenantId, hold.agentId, hold.month, hold.tokensIn, hold.tokensOut],
      ),
    );
    if (res.rowCount === 0) {
      throw new AgentError("AGT-0202", `token budget for ${hold.agentId} in ${hold.month} cannot cover a run of ${hold.tokensIn} in / ${hold.tokensOut} out, or is not configured`);
    }
  }

  /** Releases the hold, charges the actual usage, and writes `budget.threshold` to the outbox in the same transaction when the budget moves up into SOFT or HARD. */
  async settle(hold: BudgetHold, actual: ModelUsage): Promise<void> {
    await this.withTenant(hold.tenantId, async (c) => {
      const res = await c.query<BudgetRow>(
        `update token_budget
            set reserved_in = greatest(reserved_in - $4, 0), reserved_out = greatest(reserved_out - $5, 0),
                used_in = used_in + $6, used_out = used_out + $7, updated_at = now()
          where tenant_id = $1 and agent_id = $2 and month = $3
      returning limit_in, limit_out, used_in, used_out, soft_pct, alert_state`,
        [hold.tenantId, hold.agentId, hold.month, hold.tokensIn, hold.tokensOut, actual.tokensIn, actual.tokensOut],
      );
      const row = res.rows[0];
      if (row === undefined) throw new Error(`no token budget to settle for ${hold.agentId} in ${hold.month}`);
      const current = budgetStatus({
        limitIn: Number(row.limit_in),
        limitOut: Number(row.limit_out),
        usedIn: Number(row.used_in),
        usedOut: Number(row.used_out),
        softPct: row.soft_pct,
      });
      const event = budgetThresholdEvent({ tenantId: hold.tenantId, month: hold.month, previous: row.alert_state, current });
      if (event === undefined) return;
      await c.query("update token_budget set alert_state = $4 where tenant_id = $1 and agent_id = $2 and month = $3", [
        hold.tenantId,
        hold.agentId,
        hold.month,
        event.data.state,
      ]);
      await c.query(
        "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, 'token_budget', $3, $4, $5::jsonb)",
        [randomUUID(), hold.tenantId, `${hold.agentId}/${hold.month}`, event.type, JSON.stringify(event.data)],
      );
    });
  }
}

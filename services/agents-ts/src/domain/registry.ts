import { createHash } from "node:crypto";
import { AgentError } from "../errors.js";

/** FR-47.10: a pinned prompt, tool list, model id and parameters. */
export interface AgentVersion {
  agentId: string;
  version: number;
  modelId: string;
  systemPrompt: string;
  allowedTools: string[];
  requiredDuties: string[];
}

export interface EvalRun {
  agentId: string;
  version: number;
  evalRunId: string;
  passed: boolean;
}

/** Hash over everything that changes agent behaviour. Stored as prompt hash in audit. */
export function promptHash(v: AgentVersion): string {
  const canonical = JSON.stringify({
    systemPrompt: v.systemPrompt,
    modelId: v.modelId,
    allowedTools: [...v.allowedTools].sort(),
  });
  return createHash("sha256").update(canonical).digest("hex");
}

/**
 * In-memory version registry. Production pins one version per agent; rollback
 * moves the pointer back (9.47.10). Persistence arrives with the store slice.
 */
export class VersionRegistry {
  private readonly versions = new Map<string, AgentVersion>();
  private readonly evals: EvalRun[] = [];
  private readonly history = new Map<string, number[]>();

  register(v: AgentVersion): void {
    if (!Number.isInteger(v.version) || v.version < 1) throw new Error("version must be a positive integer");
    const key = keyOf(v.agentId, v.version);
    if (this.versions.has(key)) throw new Error(`${key} is already registered`);
    this.versions.set(key, v);
  }

  recordEval(e: EvalRun): void {
    this.evals.push(e);
  }

  hasPassingEval(agentId: string, version: number): boolean {
    return this.evals.some((e) => e.agentId === agentId && e.version === version && e.passed);
  }

  /** Points production at a version. Refuses without a passing eval (AGT-0204). */
  promote(agentId: string, version: number): AgentVersion {
    const v = this.versions.get(keyOf(agentId, version));
    if (!v) throw new Error(`${keyOf(agentId, version)} is not registered`);
    if (!this.hasPassingEval(agentId, version)) {
      throw new AgentError("AGT-0204", `agent version ${version} has no passing evaluation and cannot be promoted`);
    }
    const past = this.history.get(agentId) ?? [];
    this.history.set(agentId, [...past, version]);
    return v;
  }

  production(agentId: string): AgentVersion | undefined {
    const past = this.history.get(agentId) ?? [];
    const current = past[past.length - 1];
    return current === undefined ? undefined : this.versions.get(keyOf(agentId, current));
  }

  /** Rollback is a pointer change to the previous promoted version. */
  rollback(agentId: string): AgentVersion {
    const past = this.history.get(agentId) ?? [];
    if (past.length < 2) throw new Error(`${agentId} has no earlier version to roll back to`);
    this.history.set(agentId, past.slice(0, -1));
    const previous = this.production(agentId);
    if (!previous) throw new Error(`${agentId} rollback target is missing`);
    return previous;
  }
}

function keyOf(agentId: string, version: number): string {
  return `${agentId}@${version}`;
}

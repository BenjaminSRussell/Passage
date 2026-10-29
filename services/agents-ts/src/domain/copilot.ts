import { canCallTool } from "./authz.js";

/** Copilot modes from PRD 9.47.13 (`copilot.modes`). */
export const COPILOT_MODES = ["ask", "explain", "draft", "summarize"] as const;
export type CopilotMode = (typeof COPILOT_MODES)[number];

export interface CopilotTool {
  name: string;
  requiredDuty: string;
}

export interface MissingDutyReply {
  tool: string;
  duty: string;
  code: "AGT-0203";
  /** User text from the PRD 9.47.18 catalog. */
  message: string;
}

export interface ToolGate {
  allowed: string[];
  denied: MissingDutyReply[];
}

/** AGT-0203 text, verbatim from the message catalog. */
export function missingDutyMessage(tool: string, duty: string): string {
  return `I cannot do that: you do not hold duty ${duty} required by ${tool}.`;
}

/**
 * Splits the tools a copilot turn wants into those the user may call and those
 * they may not (BR-47.11). A denied tool is answered with AGT-0203 and never
 * called, so the answer cannot carry data the user could not read.
 */
export function gateCopilotTools(userDuties: ReadonlySet<string>, tools: readonly CopilotTool[]): ToolGate {
  const allowed: string[] = [];
  const denied: MissingDutyReply[] = [];
  for (const t of tools) {
    if (canCallTool(userDuties, t.requiredDuty)) {
      allowed.push(t.name);
    } else {
      denied.push({ tool: t.name, duty: t.requiredDuty, code: "AGT-0203", message: missingDutyMessage(t.name, t.requiredDuty) });
    }
  }
  return { allowed, denied };
}

/** Stable, order-independent key for a duty set, so a cache entry is reused only under the same entitlements. */
export function dutiesKey(duties: ReadonlySet<string>): string {
  return JSON.stringify([...duties].sort());
}

/** Who is asking: the cache never crosses any of these. */
export interface CopilotAsker {
  tenantId: string;
  userId: string;
  duties: ReadonlySet<string>;
}

/** Default lifetime of a cached answer: the 5 minutes PRD 9.47 allows for cached `tools/list` results. */
export const ANSWER_CACHE_TTL_MS = 5 * 60_000;
/** Bound on entries per process; the oldest entry is evicted first. */
export const ANSWER_CACHE_MAX_ENTRIES = 1_000;

interface CachedAnswer {
  answer: string;
  expiresAtMs: number;
}

/**
 * Per-process copilot answer cache (BR-47.11). The key is the tenant, the
 * user, the user's duty set, the mode and the prompt, so an answer is served
 * only to the asker who produced it, with the same entitlements: never to
 * another user, another tenant or the same user after their duties changed.
 * Entries expire after `ttlMs` so a cached figure does not outlive the data
 * behind it for long.
 */
export class CopilotAnswerCache {
  private readonly entries = new Map<string, CachedAnswer>();

  constructor(
    private readonly nowMs: () => number = Date.now,
    private readonly ttlMs: number = ANSWER_CACHE_TTL_MS,
    private readonly maxEntries: number = ANSWER_CACHE_MAX_ENTRIES,
  ) {
    if (!(ttlMs > 0)) throw new Error("ttlMs must be positive");
    if (!Number.isInteger(maxEntries) || maxEntries < 1) throw new Error("maxEntries must be a positive integer");
  }

  put(asker: CopilotAsker, mode: CopilotMode, prompt: string, answer: string): void {
    const key = cacheKey(asker, mode, prompt);
    this.entries.delete(key);
    while (this.entries.size >= this.maxEntries) {
      const oldest = this.entries.keys().next().value;
      if (oldest === undefined) break;
      this.entries.delete(oldest);
    }
    this.entries.set(key, { answer, expiresAtMs: this.nowMs() + this.ttlMs });
  }

  get(asker: CopilotAsker, mode: CopilotMode, prompt: string): string | undefined {
    const key = cacheKey(asker, mode, prompt);
    const hit = this.entries.get(key);
    if (!hit) return undefined;
    if (this.nowMs() >= hit.expiresAtMs) {
      this.entries.delete(key);
      return undefined;
    }
    return hit.answer;
  }

  get size(): number {
    return this.entries.size;
  }
}

function cacheKey(asker: CopilotAsker, mode: CopilotMode, prompt: string): string {
  return JSON.stringify([asker.tenantId, asker.userId, dutiesKey(asker.duties), mode, prompt]);
}

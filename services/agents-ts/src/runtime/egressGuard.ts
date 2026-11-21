import { createHash } from "node:crypto";
import type { LogFn } from "../logging/pseudonymize.js";
import { toolAnnotation, type ToolClass } from "../agents/catalog.js";
import type { AiPolicyGuard } from "./aiPolicyGuard.js";
import { AI_POLICY_MODULES } from "../domain/aiDataPolicy.js";
import { MAX_LOOKBEHIND_CHARS, PSEUDONYM_RE, scanPii, type PiiCategory, type PiiFinding, type Pseudonymizer } from "../domain/redaction.js";

export const EGRESS_GUARD_TOOL = "egress.guard";
export const GUARD_POLICY_VERSION = 1;
export const ANALYZER_DEADLINE_MS = 2000;
export const GUARD_REFUSAL_MESSAGE =
  "I could not check this request for sensitive data, so I did not send it to the AI model. Your ERP work is not affected; please try again later.";

export type DataClass = "P0" | "P1" | "P2" | "P3";
export type GuardDecision = "ALLOWED" | "REDACTED" | "BLOCKED" | "BLOCKED_BOUNDARY" | "OUTPUT_BLOCKED";
export type GuardErrorCode = "ANALYZER_ERROR" | "ANALYZER_TIMEOUT" | "RESIDUAL_PII" | "NO_APPROVED_ROUTE" | "POLICY_UNREADABLE";
export type OutputGuardReason = "TOKEN_NOT_IN_INPUT" | "RECORD_NOT_IN_INPUT" | "DUTY_LEAK" | "RAW_IDENTIFIER" | "LINK_IN_ARGS" | "GUARD_TIMEOUT" | "GUARD_ERROR";

export interface GuardResult {
  callId: string;
  highestClass: DataClass;
  routeId: string;
  policyVersion: number;
  policyCell?: "tenant" | "default";
  counts: Record<string, number>;
  excluded: Record<string, number>;
  decision: GuardDecision;
  errorCode?: GuardErrorCode | OutputGuardReason;
}

// ponytail: seam coverage assumes scanPii's lookbehind bounds (MAX_LOOKBEHIND_CHARS) and results, empty ones included, are memoised per digest for any analyzer; an analyzer with wider context or non-deterministic output needs a declared context span.
export interface Analyzer {
  scan(text: string): PiiFinding[] | Promise<PiiFinding[]>;
}

export interface EgressInput {
  tenantId: string;
  callId: string;
  routeId: string;
  pseudo: Pseudonymizer;
  system: string;
  typed: string;
  results: readonly string[];
  recordIds?: ReadonlySet<string>;
  excluded?: Record<string, number>;
  policyCell?: "tenant" | "default";
  policyVersion?: number;
  declared?: DataClass;
}

export interface GuardContext {
  inputTokens: Set<string>;
  inputRecordIds: Set<string>;
  highestClass: DataClass;
}

export interface EgressVerdict {
  user: string;
  context: GuardContext;
  result: GuardResult;
  typedReplaced: number;
}

export interface Admission {
  pass: boolean;
  class: DataClass;
  policyCell: "tenant" | "default";
  policyVersion: number;
  failure?: GuardErrorCode;
  reason?: "TENANT_POLICY";
}

export interface EgressGuard {
  admit(input: { tenantId: string; tool: string; text: string }): Promise<Admission>;
  inspect(input: EgressInput): Promise<EgressVerdict>;
}

const CLASS_ORDER: readonly DataClass[] = ["P0", "P1", "P2", "P3"];

const CATEGORY_CLASS: Readonly<Record<PiiCategory, DataClass>> = {
  EMAIL: "P2",
  IBAN: "P3",
  CARD: "P3",
  SSN: "P3",
  TAXID: "P3",
  BANKACCT: "P3",
  PAY: "P3",
};

export const highestClassOf = (classes: readonly DataClass[]): DataClass =>
  classes.reduce<DataClass>((top, c) => (CLASS_ORDER.indexOf(c) > CLASS_ORDER.indexOf(top) ? c : top), "P0");

class GuardFailure extends Error {
  constructor(readonly code: GuardErrorCode, detail: string = code) {
    super(detail);
    this.name = "GuardFailure";
  }
}

const ERROR_CODE_RE = /^[A-Z][A-Z0-9_-]{1,31}$/;
const ERROR_MESSAGE_MAX = 160;
const ERROR_SCAN_MAX = 4096;

const SYSTEM_ERROR_MESSAGES: Readonly<Record<string, string>> = {
  ETIMEDOUT: "analyzer connection timed out",
  ECONNREFUSED: "analyzer connection refused",
  ECONNRESET: "analyzer connection reset",
  ENOTFOUND: "analyzer host could not be resolved",
  EAI_AGAIN: "analyzer host could not be resolved",
  ABORT_ERR: "analyzer request aborted",
};
const SYSTEM_ERROR_TOKEN_RE = new RegExp(`\\b(${Object.keys(SYSTEM_ERROR_MESSAGES).join("|")})\\b`);
const NAME_ERROR_MESSAGES: Readonly<Record<string, string>> = {
  AbortError: "analyzer request aborted",
  TimeoutError: "analyzer timed out",
  SyntaxError: "analyzer error: SyntaxError",
  TypeError: "analyzer error: TypeError",
  RangeError: "analyzer error: RangeError",
};
const UNKNOWN_ERROR_MESSAGE = "analyzer error";

const ownMessage = (messages: Readonly<Record<string, string>>, key: unknown): string | undefined =>
  typeof key === "string" && Object.hasOwn(messages, key) ? messages[key] : undefined;

function foreignErrorMessage(err: Error, code: unknown): string {
  const message = typeof err.message === "string" ? err.message.slice(0, ERROR_SCAN_MAX) : "";
  const token = SYSTEM_ERROR_TOKEN_RE.exec(message)?.[1];
  return ownMessage(SYSTEM_ERROR_MESSAGES, code) ?? ownMessage(SYSTEM_ERROR_MESSAGES, token) ?? ownMessage(NAME_ERROR_MESSAGES, err.name) ?? UNKNOWN_ERROR_MESSAGE;
}

// Deliberately the local scanPii, not the injected analyzer: error text must be scrubbed even when the analyzer is the thing that failed.
function describeError(err: unknown): Record<string, string> {
  if (!(err instanceof Error)) return { error: "unknown" };
  const code = (err as { code?: unknown }).code;
  const detail: Record<string, string> = { error: String(err.name) };
  if (!(err instanceof GuardFailure)) {
    if (typeof code === "string" && ERROR_CODE_RE.test(code)) detail.errorDetailCode = code;
    detail.errorMessage = foreignErrorMessage(err, code);
    return detail;
  }
  const bounded = err.message.slice(0, ERROR_SCAN_MAX);
  const scrubbed = scanPii(bounded)
    .filter((f) => f.value.length > 0)
    .sort((a, b) => b.value.length - a.value.length)
    .reduce((text, f) => text.split(f.value).join("[REDACTED]"), bounded)
    .replace(/\d{4,}/g, "[N]")
    .slice(0, ERROR_MESSAGE_MAX);
  if (scrubbed.length > 0 && scrubbed !== err.code) detail.errorMessage = scrubbed;
  return detail;
}

function withDeadline<T>(work: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new GuardFailure("ANALYZER_TIMEOUT")), ms);
  });
  return Promise.race([work, late]).finally(() => clearTimeout(timer));
}

export const SEAM_WINDOW_CHARS = 4 * MAX_LOOKBEHIND_CHARS;
const SEPARATOR = "\n\n";

const isAlnum = (c: string | undefined): boolean => c !== undefined && /[A-Za-z0-9]/.test(c);
const isDigit = (c: string | undefined): boolean => c !== undefined && /\d/.test(c);

function isCutPoint(text: string, i: number): boolean {
  const c = text.charAt(i);
  if (/\w/.test(c)) return false;
  const before = text[i - 1];
  const after = text[i + 1];
  const insideGroupedNumber = (c === " " || c === "-") && isAlnum(before) && isAlnum(after) && (isDigit(before) || isDigit(after));
  return !insideGroupedNumber;
}

function seamWindows(parts: readonly string[]): string[] {
  const joined = parts.join(SEPARATOR);
  const windows: string[] = [];
  let offset = 0;
  for (const part of parts.slice(0, -1)) {
    offset += part.length;
    const lead = Math.max(0, offset - SEAM_WINDOW_CHARS);
    let start = lead;
    if (lead > 0) {
      let cut = -1;
      for (let i = lead; i < offset - MAX_LOOKBEHIND_CHARS && cut < 0; i++) if (isCutPoint(joined, i)) cut = i;
      if (cut >= 0) start = cut + 1;
      else while (start > 0 && /\w/.test(joined.charAt(start - 1))) start--;
    }
    const tailStart = offset + SEPARATOR.length + MAX_LOOKBEHIND_CHARS;
    const trail = Math.min(joined.length, offset + SEPARATOR.length + SEAM_WINDOW_CHARS);
    let end = trail;
    if (trail < joined.length) {
      let cut = -1;
      for (let i = trail - 1; i >= tailStart && cut < 0; i--) if (isCutPoint(joined, i)) cut = i;
      if (cut >= 0) end = cut;
      else while (end < joined.length && /\w/.test(joined.charAt(end))) end++;
    }
    windows.push(joined.slice(start, end));
    offset += SEPARATOR.length;
  }
  return windows;
}

const SCAN_MEMO_MAX = 512;

type ScanMemo = Map<string, PiiCategory[]>;

const memoKey = (text: string): string => createHash("sha256").update(text).digest("hex");

async function memoisedCategories(
  memo: ScanMemo,
  scan: (texts: readonly string[]) => Promise<PiiFinding[][]>,
  segments: readonly string[],
): Promise<PiiCategory[][]> {
  const keys = new Map([...new Set(segments)].map((t) => [t, memoKey(t)] as const));
  const known = new Map<string, PiiCategory[]>();
  for (const [t, key] of keys) {
    const held = key === undefined ? undefined : memo.get(key);
    if (held !== undefined) known.set(t, held);
  }
  const fresh = [...keys.keys()].filter((t) => !known.has(t));
  const scanned = new Map<string, PiiCategory[]>();
  if (fresh.length > 0) {
    const results = await scan(fresh);
    fresh.forEach((t, i) => {
      const findings = results[i];
      if (!Array.isArray(findings)) throw new GuardFailure("ANALYZER_ERROR", "analyzer returned a non-array");
      scanned.set(t, findings.map((f) => f.category));
    });
  }
  const out = segments.map((t) => scanned.get(t) ?? known.get(t) ?? []);
  const remember = (key: string, categories: PiiCategory[]) => {
    memo.delete(key);
    if (memo.size >= SCAN_MEMO_MAX) {
      const oldest = memo.keys().next();
      if (!oldest.done) memo.delete(oldest.value);
    }
    memo.set(key, categories);
  };
  for (const [t, held] of known) {
    const key = keys.get(t);
    if (key !== undefined) remember(key, held);
  }
  for (const [t, categories] of scanned) {
    const key = keys.get(t);
    if (key !== undefined) remember(key, categories);
  }
  return out;
}

export function createEgressGuard(
  deps: { analyzer?: Analyzer; policy: AiPolicyGuard; log?: LogFn; observe?: (seconds: number, decision: GuardDecision) => void; deadlineMs?: number },
): EgressGuard {
  const analyzer = deps.analyzer ?? { scan: scanPii };
  const deadlineMs = deps.deadlineMs ?? ANALYZER_DEADLINE_MS;
  const memos = new WeakMap<Pseudonymizer, { segments: ScanMemo; windows: ScanMemo }>();
  const scan = (texts: readonly string[]): Promise<PiiFinding[][]> => withDeadline(Promise.all(texts.map(async (t) => analyzer.scan(t))), deadlineMs);

  return {
    async admit({ tenantId, tool, text }) {
      const { module, declared } = toolAnnotation(tool);
      const policyCell = (AI_POLICY_MODULES as readonly string[]).includes(module) ? "tenant" : "default";
      const base = { policyCell, policyVersion: GUARD_POLICY_VERSION } as const;
      try {
        const [found = []] = await scan([text]);
        const cls = highestClassOf([declared satisfies ToolClass, ...found.map((f) => CATEGORY_CLASS[f.category])]);
        if (policyCell === "default") return { ...base, pass: true, class: cls };
        const [p0, p1] = await Promise.all([deps.policy.check({ tenantId, module, dataClass: "P0" }), deps.policy.check({ tenantId, module, dataClass: "P1" })]);
        if (!p0.allowed && !p1.allowed) return { ...base, pass: false, class: cls, policyVersion: p1.policyVersion, reason: "TENANT_POLICY" };
        // P2 and P3 values are pseudonymized later in inspect(), which throws RESIDUAL_PII on any leftover.
        if (cls === "P2" || cls === "P3") return { ...base, pass: true, class: cls, policyVersion: p1.policyVersion };
        const decision = cls === "P0" ? p0 : p1;
        return { ...base, pass: decision.allowed, class: cls, policyVersion: decision.policyVersion };
      } catch (err) {
        const failure = err instanceof GuardFailure ? err.code : err instanceof Error && err.name === "AgentError" ? "POLICY_UNREADABLE" : "ANALYZER_ERROR";
        deps.log?.("warn", "egress guard could not admit a tool result", { tenantId, tool, errorCode: failure, ...describeError(err) });
        return { ...base, pass: false, class: "P3", failure };
      }
    },
    async inspect(input) {
      const started = performance.now();
      const result: GuardResult = {
        callId: input.callId,
        highestClass: "P0",
        routeId: input.routeId,
        policyVersion: input.policyVersion ?? GUARD_POLICY_VERSION,
        ...(input.policyCell !== undefined ? { policyCell: input.policyCell } : {}),
        counts: {},
        excluded: { ...input.excluded },
        decision: "BLOCKED",
      };
      try {
        const texts = [input.typed, ...input.results];
        const held = memos.get(input.pseudo) ?? { segments: new Map<string, PiiCategory[]>(), windows: new Map<string, PiiCategory[]>() };
        memos.set(input.pseudo, held);
        const perText = await memoisedCategories(held.segments, scan, texts);
        const found = perText.flat();
        for (const category of found) result.counts[category] = (result.counts[category] ?? 0) + 1;
        result.highestClass = highestClassOf([input.declared ?? "P0", ...found.map((c) => CATEGORY_CLASS[c])]);
        const redacted = texts.map((t) => input.pseudo.redact(t));
        const user = redacted.join(SEPARATOR);
        const parts = [input.system, ...redacted];
        const residual = await Promise.all([memoisedCategories(held.segments, scan, parts), memoisedCategories(held.windows, scan, seamWindows(parts))]);
        if (residual.some((perPart) => perPart.some((categories) => categories.length > 0))) throw new GuardFailure("RESIDUAL_PII");
        result.decision = found.length > 0 ? "REDACTED" : "ALLOWED";
        return { user, context: { inputTokens: new Set(user.match(PSEUDONYM_RE)), inputRecordIds: new Set(input.recordIds), highestClass: result.highestClass }, result, typedReplaced: (perText[0] ?? []).filter((c) => c !== "EMAIL").length };
      } catch (err) {
        result.errorCode = err instanceof GuardFailure ? err.code : "ANALYZER_ERROR";
        deps.log?.("warn", "egress guard blocked the provider call", {
          tenantId: input.tenantId,
          callId: input.callId,
          errorCode: result.errorCode,
          ...describeError(err),
          counts: result.counts,
        });
        return { user: "", context: { inputTokens: new Set(), inputRecordIds: new Set(), highestClass: result.highestClass }, result, typedReplaced: 0 };
      } finally {
        deps.observe?.((performance.now() - started) / 1000, result.decision);
      }
    },
  };
}

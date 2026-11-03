import type { ModelResponse, ModelUsage, Proposal } from "../runtime/engine.js";
import { AgentError } from "../errors.js";

/**
 * The one tool every provider adapter forces the model to call. Its arguments
 * are the whole answer: free text for the UI plus at most one proposal. Both
 * adapters translate this same definition, so their responses are comparable
 * (provider parity, FR-47.13 and AC-47.10).
 */
export const PROPOSE_TOOL_NAME = "propose_action";

export interface ProposeToolSpec {
  name: string;
  description: string;
  inputSchema: Record<string, unknown>;
}

export function proposeTool(allowedTools: readonly string[]): ProposeToolSpec {
  if (allowedTools.length === 0) throw new Error("an agent needs at least one allowed tool");
  return {
    name: PROPOSE_TOOL_NAME,
    description: "Propose one governed action for human approval, or explain without proposing.",
    inputSchema: {
      type: "object",
      properties: {
        text: { type: "string", description: "Answer shown to the user. Use pseudonyms exactly as given." },
        tool: { type: "string", enum: [...allowedTools] },
        args: { type: "object", description: "Arguments for the tool. Never include tenant_id." },
        confidence: { type: "number", minimum: 0, maximum: 1 },
        amount: {
          type: "object",
          properties: {
            amount: { type: "string", pattern: "^-?[0-9]+(\\.[0-9]+)?$" },
            currency: { type: "string", pattern: "^[A-Z]{3}$" },
          },
          required: ["amount", "currency"],
        },
      },
      // Only text is required: the model may explain or refuse without proposing.
      // When it names a tool, toModelResponse requires args, confidence and amount.
      required: ["text"],
    },
  };
}

/**
 * Every provider failure is AGT-0206 for the run. `outage` separates the
 * failures that count towards the BR-47.9 failover clock (5xx, timeouts,
 * transport errors) from a provider that answered but refused the request or
 * returned an unusable answer: switching providers does not fix those, so they
 * must never move a tenant to its secondary.
 */
export class ProviderError extends AgentError {
  readonly outage: boolean;

  constructor(message: string, outage: boolean) {
    super("AGT-0206", message);
    this.name = "ProviderError";
    this.outage = outage;
  }
}

/** The provider is down or unreachable (counts towards failover). */
export function providerUnavailable(detail: string): ProviderError {
  return new ProviderError(detail, true);
}

/** The provider answered, but refused the request or gave an unusable answer (no failover). */
export function providerRejected(detail: string): ProviderError {
  return new ProviderError(detail, false);
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Validates the forced-tool arguments and maps them to a ModelResponse. */
export function toModelResponse(input: unknown, usage: ModelUsage | undefined, allowedTools: readonly string[]): ModelResponse {
  if (!isRecord(input)) throw providerRejected("provider returned no structured proposal arguments");
  const text = input.text;
  if (typeof text !== "string") throw providerRejected("proposal text is missing");
  const response: ModelResponse = { text };
  const tool = input.tool;
  if (tool !== undefined && tool !== null && tool !== "") {
    if (typeof tool !== "string" || !allowedTools.includes(tool)) {
      throw providerRejected("proposal names a tool outside the allow-list");
    }
    const confidence = input.confidence;
    if (typeof confidence !== "number" || !Number.isFinite(confidence) || confidence < 0 || confidence > 1) {
      throw providerRejected("proposal confidence must be between 0 and 1");
    }
    const amount = isRecord(input.amount) ? input.amount : {};
    const value = amount.amount;
    const currency = amount.currency;
    if (typeof value !== "string" || !/^-?[0-9]+(\.[0-9]+)?$/.test(value)) {
      throw providerRejected("proposal amount must be a decimal string");
    }
    if (typeof currency !== "string" || !/^[A-Z]{3}$/.test(currency)) {
      throw providerRejected("proposal currency must be ISO 4217");
    }
    if (!isRecord(input.args)) throw providerRejected("proposal args must be an object");
    const args = input.args;
    const proposal: Proposal = {
      tool,
      args,
      confidence,
      amount: { amount: value, currency },
    };
    response.proposal = proposal;
  }
  if (usage !== undefined) response.usage = usage;
  return response;
}

/** Parses a provider JSON body, or throws AGT-0206 (not an outage) when it is not JSON. */
export function parseJsonBody(text: string): unknown {
  try {
    return JSON.parse(text) as unknown;
  } catch {
    throw providerRejected("provider returned a non-JSON body");
  }
}

export { isRecord };

export const outputLimit = (configured: number | undefined, requested: number | undefined): number =>
  Math.min(configured ?? 1024, requested ?? Number.POSITIVE_INFINITY);

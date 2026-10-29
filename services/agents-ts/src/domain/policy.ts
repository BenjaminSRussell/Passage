import { compareMoney, scaled6, sumMoney, type Money } from "./money.js";

/** agent.<id>.mode per legal entity (FR-47.5, 9.47.13). */
export type AgentMode = "OFF" | "SUGGEST" | "AUTO";

export interface ModePolicy {
  mode: AgentMode;
  /** agent.ap_coding.auto_threshold, allowed 0.80 to 1.00. */
  autoThreshold: number;
  /** agent.ap_coding.auto_amount_limit, USD 5,000 / EUR 4,500 by default. */
  autoAmountLimit: Money;
}

export interface Proposal {
  /** Model's confidence in [0, 1]. */
  confidence: number;
  amount: Money;
  /** Computed by the system, never by the model. */
  vendorHighRisk: boolean;
  sodConflict: boolean;
}

/** BLOCKED: agent off. PROPOSE: goes to the approval gate. AUTO: commits without a gate. */
export type Disposition = "BLOCKED" | "PROPOSE" | "AUTO";

export function validatePolicy(p: ModePolicy): string[] {
  const errors: string[] = [];
  if (!(p.autoThreshold >= 0.8 && p.autoThreshold <= 1)) errors.push("auto_threshold must be between 0.80 and 1.00");
  let limitOk = true;
  try {
    scaled6(p.autoAmountLimit.amount);
  } catch {
    limitOk = false;
    errors.push("auto_amount_limit must be a decimal amount");
  }
  if (limitOk && p.autoAmountLimit.amount.startsWith("-")) errors.push("auto_amount_limit must be >= 0");
  if (!/^[A-Z]{3}$/.test(p.autoAmountLimit.currency)) errors.push("auto_amount_limit currency must be an ISO 4217 code");
  return errors;
}

/** BR-47.2: auto commits only when confidence, amount, risk and SoD all pass. */
export function decideDisposition(policy: ModePolicy, p: Proposal): Disposition {
  if (policy.mode === "OFF") return "BLOCKED";
  if (policy.mode === "SUGGEST") return "PROPOSE";
  const autoOk =
    p.confidence >= policy.autoThreshold &&
    p.amount.currency === policy.autoAmountLimit.currency &&
    compareMoney(p.amount, policy.autoAmountLimit) < 0 &&
    !p.vendorHighRisk &&
    !p.sodConflict;
  return autoOk ? "AUTO" : "PROPOSE";
}

/** BR-47.3: approval requests expire after expiryHours and are REJECTED with reason EXPIRED. */
export function isApprovalExpired(requestedAtMs: number, nowMs: number, expiryHours: number): boolean {
  return nowMs - requestedAtMs >= expiryHours * 3_600_000;
}

/** agent.cash_app.max_invoices_per_payment (9.47.13): default 20, allowed 1 to 50. */
export const CASH_APP_MAX_INVOICES_DEFAULT = 20;

export interface CashApplicationProposal {
  payment: Money;
  /** One settlement per invoice. */
  allocations: ReadonlyArray<{ invoiceId: string; amount: Money }>;
  /** Short-payment write-off the proposal would create, if any. */
  writeOff?: Money;
}

export interface CashApplicationLimits {
  maxInvoices: number;
  /** The wave-30 write-off tolerance for the customer. */
  writeOffTolerance: Money;
}

/**
 * BR-47.10: a cash application proposal never splits a payment across more than
 * maxInvoices invoices and never creates a write-off above the tolerance. It also
 * never applies more than the payment, never settles an invoice twice, and every
 * amount is positive. Returns every violation; empty means the proposal may go
 * to the approval gate.
 */
export function checkCashApplication(p: CashApplicationProposal, limits: CashApplicationLimits): string[] {
  const errors: string[] = [];
  if (!Number.isInteger(limits.maxInvoices) || limits.maxInvoices < 1 || limits.maxInvoices > 50) {
    errors.push("max_invoices_per_payment must be an integer between 1 and 50");
  }
  if (p.allocations.length === 0) errors.push("a proposal settles at least one invoice");
  if (p.allocations.length > limits.maxInvoices) {
    errors.push(`a payment may be split across at most ${limits.maxInvoices} invoices, got ${p.allocations.length}`);
  }
  const seen = new Set<string>();
  for (const a of p.allocations) {
    if (seen.has(a.invoiceId)) errors.push(`invoice ${a.invoiceId} is settled twice`);
    seen.add(a.invoiceId);
    if (a.amount.currency !== p.payment.currency) {
      errors.push(`invoice ${a.invoiceId} is in ${a.amount.currency}, the payment in ${p.payment.currency}`);
    } else if (scaled6(a.amount.amount) <= 0n) {
      errors.push(`invoice ${a.invoiceId} allocation must be positive`);
    }
  }
  if (errors.length > 0) return errors;
  const applied = sumMoney(p.payment.currency, p.allocations.map((a) => a.amount));
  if (compareMoney(applied, p.payment) > 0) {
    errors.push(`allocations ${applied.amount} exceed the payment ${p.payment.amount}`);
  }
  if (p.writeOff) {
    if (p.writeOff.currency !== limits.writeOffTolerance.currency) {
      errors.push(`write-off currency ${p.writeOff.currency} differs from the tolerance currency`);
    } else if (compareMoney(p.writeOff, limits.writeOffTolerance) > 0) {
      errors.push(`write-off ${p.writeOff.amount} exceeds the tolerance ${limits.writeOffTolerance.amount}`);
    }
  }
  return errors;
}

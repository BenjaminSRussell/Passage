import type { AnomalyCase, ApCodingCase, CashAppCase, MrpTriageCase } from "./datasets.js";

/** Decimal strings compared by value ("75" equals "75.00"); never parsed as floats. */
function sameAmount(a: string, b: string): boolean {
  const norm = (s: string): string => {
    const m = /^(-?)(\d+)(?:\.(\d+))?$/.exec(s.trim());
    if (!m) return `invalid:${s}`;
    const whole = (m[2] ?? "0").replace(/^0+(?=\d)/, "");
    const fraction = (m[3] ?? "").replace(/0+$/, "");
    const zero = whole === "0" && fraction === "";
    return `${zero ? "" : (m[1] ?? "")}${whole}.${fraction}`;
  };
  return norm(a) === norm(b);
}

/**
 * Deterministic baselines. They are the floor the LLM agent must beat and the
 * predictors the harness self-tests run against; they are not the agents.
 */

/** Ranks accounts by how often the vendor was coded to them; ties keep first-seen order. */
export function vendorHistoryBaseline(c: ApCodingCase): string[] {
  const counts = new Map<string, number>();
  for (const account of c.history) counts.set(account, (counts.get(account) ?? 0) + 1);
  return [...counts.entries()].sort((a, b) => b[1] - a[1]).map(([account]) => account);
}

/**
 * Proposes the invoices named in the remittance text. When none are named,
 * proposes the single open invoice whose amount equals the payment, if unique.
 */
export { sameAmount };

export function remittanceBaseline(c: CashAppCase): string[] {
  const named = c.openInvoices.filter((i) => c.remittance.includes(i.invoiceId)).map((i) => i.invoiceId);
  if (named.length > 0) return named;
  const exact = c.openInvoices.filter((i) => sameAmount(i.amount, c.amount));
  return exact.length === 1 && exact[0] ? [exact[0].invoiceId] : [];
}


/** Keyword rules over the action message text. The first matching rule wins. */
const MRP_RULES: ReadonlyArray<readonly [RegExp, string]> = [
  [/duplicate/i, "DATA_ERROR"],
  [/quality|hold|inspection/i, "QUALITY_HOLD"],
  [/overload|capacity|breakdown/i, "CAPACITY"],
  [/delay|late/i, "SUPPLY_LATE"],
  [/forecast|surge|demand/i, "DEMAND_SPIKE"],
];

export function mrpKeywordBaseline(c: MrpTriageCase): string {
  const hit = MRP_RULES.find(([pattern]) => pattern.test(c.text));
  return hit ? hit[1] : "UNKNOWN";
}

/** Scores off-hours postings (before 06:00 or from 22:00) high, all others low. */
export function offHoursAnomalyBaseline(c: AnomalyCase): number {
  return c.postedAtHour < 6 || c.postedAtHour >= 22 ? 0.9 : 0.1;
}

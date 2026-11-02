import type { BadgeTone } from "../../shell/ui";
import type { CommodityCalculation } from "./api/client";
import { CUSTOMER_KIND_LABELS } from "./freeGoodsFormat";

export { CUSTOMER_KIND_LABELS };

export const MARGIN_LABELS = { PERCENT: "Percentage of the converted index", AMOUNT: "Fixed amount per unit" } as const;

/** The pricing formula in words, such as "index x 0.0012 + 10%, 2 decimals". */
export function describeFormula(c: Pick<CommodityCalculation, "factor" | "marginKind" | "margin" | "rounding" | "currency" | "unit">): string {
  const margin = Number(c.margin) === 0 ? "" : c.marginKind === "PERCENT" ? ` + ${c.margin}%` : ` + ${c.margin} ${c.currency}`;
  return `index x ${c.factor}${margin}, ${c.rounding} decimals`;
}

/** Which prices a calculation reprices, in words. */
export function describeTarget(c: Pick<CommodityCalculation, "productId" | "unit" | "currency" | "customerKind" | "customerKey">): string {
  const who = c.customerKind === "all" ? "all customers" : c.customerKind === "customer_group" ? `group ${c.customerKey}` : c.customerKey;
  return `${c.productId} per ${c.unit} in ${c.currency} for ${who}`;
}

export const CALC_STATES = {
  active: { label: "Active", tone: "success" as BadgeTone },
  withdrawn: { label: "Withdrawn", tone: "danger" as BadgeTone },
};

export function calcState(c: Pick<CommodityCalculation, "withdrawn">): keyof typeof CALC_STATES {
  return c.withdrawn ? "withdrawn" : "active";
}

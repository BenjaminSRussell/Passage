import type { BadgeTone } from "../../shell/ui";
import type { FreeGoodsCalcRule, FreeGoodsCustomerKind, FreeGoodsKind, FreeGoodsProductKind, FreeGoodsRule } from "./api/client";

export const KIND_LABELS: Record<FreeGoodsKind, string> = {
  INCLUSIVE: "Inclusive (free units come out of the order)",
  EXCLUSIVE: "Exclusive (free units are added)",
};

export const CALC_LABELS: Record<FreeGoodsCalcRule, string> = {
  PROPORTIONAL: "Proportional (fractions allowed)",
  UNIT_REFERENCE: "Unit reference (whole units)",
  WHOLE_UNITS: "Whole multiples only",
};

export const CUSTOMER_KIND_LABELS: Record<FreeGoodsCustomerKind, string> = {
  all: "All customers",
  customer_group: "A customer group",
  customer: "One customer",
};

export const PRODUCT_KIND_LABELS: Record<FreeGoodsProductKind, string> = {
  product: "One product",
  product_group: "A product group",
};

/** What the rule's product key is, in words: the product itself, or "Group PG1". */
export function describeProduct(r: Pick<FreeGoodsRule, "productKind" | "productId">): string {
  return r.productKind === "product_group" ? `Group ${r.productId}` : r.productId;
}

export type FreeGoodsState = "withdrawn" | "scheduled" | "live" | "ended";

/** Where a rule stands on day: withdrawn wins, then its validity (inclusive). */
export function freeGoodsState(r: Pick<FreeGoodsRule, "withdrawn" | "validFrom" | "validTo">, day: string): FreeGoodsState {
  if (r.withdrawn) return "withdrawn";
  if (day < r.validFrom) return "scheduled";
  if (day > r.validTo) return "ended";
  return "live";
}

export const FREE_GOODS_STATES: Record<FreeGoodsState, { label: string; tone: BadgeTone }> = {
  live: { label: "Live", tone: "success" },
  scheduled: { label: "Scheduled", tone: "info" },
  ended: { label: "Ended", tone: "neutral" },
  withdrawn: { label: "Withdrawn", tone: "danger" },
};

/** The offer in words, such as "1 free per 10 EA from 20". */
export function describeOffer(r: Pick<FreeGoodsRule, "freeQuantity" | "everyQuantity" | "unit" | "minQuantity" | "kind">): string {
  const min = Number(r.minQuantity) > 0 ? ` from ${r.minQuantity}` : "";
  return `${r.freeQuantity} free ${r.kind === "INCLUSIVE" ? "within every" : "per"} ${r.everyQuantity} ${r.unit}${min}`;
}

/** Who a rule is for, in words. */
export function describeWho(r: Pick<FreeGoodsRule, "customerKind" | "customerKey">): string {
  if (r.customerKind === "all") return "All customers";
  if (r.customerKind === "customer_group") return `Group ${r.customerKey}`;
  return r.customerKey;
}

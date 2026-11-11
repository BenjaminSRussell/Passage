// Pricing service (/api/v1/pricing) for the demo, beside the condition records in pricing.ts: promotion plans with
// their baseline, forecast lift and measured effectiveness, and the rebate claims Cedar Ridge raises against its
// vendors' volume rebate agreements. Products and vendors are the company's; claim amounts follow the receipts.
import type { Promotion, PromotionActuals, PromotionInput, VendorClaim } from "../../../modules/pricing/api/client";
import { LEGAL_ENTITY, USER, VENDORS, itemByNumber } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";

const BASE = "/api/v1/pricing";
const K_PROMOTION = 437;
const K_CLAIM = 438;
const K_REBATE = 439;

// ---- Promotions ---------------------------------------------------------------------------------------------

const average = (units: string[]) => units.reduce((s, u) => s + Number(u), 0) / Math.max(1, units.length);
const weeksOf = (start: string, end: string) => Math.max(1, Math.round((Date.parse(end) - Date.parse(start)) / (7 * 86_400_000)));

function derive(p: Omit<Promotion, "baseline" | "forecastUnits" | "effectivenessPct">): Promotion {
  const baseline = average(p.baselineWeeklyUnits);
  const forecast = baseline * (1 + Number(p.liftPct) / 100) * weeksOf(p.startDate, p.endDate);
  const actualWeekly = p.actuals ? Number(p.actuals.units) / Math.max(1, p.actuals.weeks) : undefined;
  return {
    ...p,
    baseline: dec(baseline, 1),
    forecastUnits: dec(forecast, 0),
    ...(actualWeekly !== undefined && baseline > 0 ? { effectivenessPct: dec(((actualWeekly - baseline) / baseline) * 100, 1) } : {}),
  };
}

function promotion(n: number, name: string, item: string, category: string, type: string, scope: string, start: number, end: number, baseline: number[], lift: number, spend: number, actuals?: PromotionActuals): Promotion {
  return derive({
    id: uid(K_PROMOTION, n),
    legalEntityId: LEGAL_ENTITY.id,
    name,
    eventCategory: category,
    eventType: type,
    customerScope: scope,
    productId: itemByNumber(item).id,
    currency: "USD",
    startDate: day(start),
    endDate: day(end),
    baselineWeeklyUnits: baseline.map((u) => String(u)),
    liftPct: dec(lift, 1),
    plannedSpend: dec(spend),
    actuals,
  });
}

const promotions = table<Promotion>([
  promotion(6, "Spring irrigation pre-season, SP-150", "SP-150", "Seasonal", "Early-order discount", "Agriculture dealers", 95, 151, [9, 11, 10, 8, 12, 10, 9, 11, 13, 10, 9, 12, 11], 35, 18500),
  promotion(5, "Municipal budget year-end, CP-200", "CP-200", "Fiscal year end", "Volume price break", "Municipal utilities", -6, 45, [5, 4, 6, 5, 7, 5, 4, 6, 5, 6, 5, 7, 6], 40, 22000, { units: "7", weeks: 1, spend: "2800.00" }),
  promotion(4, "Mining expo demo units, CP-400", "CP-400", "Trade show", "Show special", "Mining and aggregates", -40, -26, [2, 1, 2, 2, 1, 3, 2, 2, 1, 2, 2, 3, 2], 60, 9500, { units: "8", weeks: 2, spend: "8900.00" }),
  promotion(3, "Seal kit service bundle", "SEAL-MK-125", "Aftermarket", "Bundle with service visit", "All installed-base customers", -75, -47, [30, 28, 34, 31, 29, 33, 35, 30, 32, 28, 31, 33, 30], 25, 6000, { units: "152", weeks: 4, spend: "5400.00" }),
  promotion(2, "Diaphragm pump winter clearance, DP-100", "DP-100", "Clearance", "Price discount", "Facilities and industrial", -130, -102, [3, 4, 3, 2, 4, 3, 3, 4, 2, 3, 3, 4, 3], 30, 7000, { units: "12", weeks: 4, spend: "7600.00" }),
]);

route("GET", `${BASE}/promotions`, ({ query }) => {
  const campaignId = query.get("campaignId");
  return { items: promotions.all().filter((p) => !campaignId || p.campaignId === campaignId).sort((a, b) => b.startDate.localeCompare(a.startDate)) };
});

route("GET", `${BASE}/promotions/{id}`, ({ params }) => promotions.get(params.id) ?? reply.problem(404, "PRC-0404", "Promotion not found"));

route("POST", `${BASE}/promotions`, ({ body }) => {
  const input = body as PromotionInput;
  if (input.baselineWeeklyUnits?.length !== 13) return reply.problem(422, "PRC-0031", "Give the units sold in each of the 13 weeks before the promotion", { baselineWeeklyUnits: "Give 13 weekly figures." });
  const made = derive({ ...input, id: uid(K_PROMOTION, 100 + promotions.all().length) });
  return reply.created(promotions.insert(made));
});

route("POST", `${BASE}/promotions/{id}:actuals`, ({ params, body }) => {
  const p = promotions.get(params.id);
  if (!p) return reply.problem(404, "PRC-0404", "Promotion not found");
  const input = body as PromotionActuals;
  return promotions.update(p.id, derive({ ...p, actuals: { units: input.units, weeks: Number(input.weeks), spend: input.spend } }));
});

// ---- Vendor rebate claims -----------------------------------------------------------------------------------

const AGREEMENTS = {
  bearings: { id: uid(K_REBATE, 1), vendor: VENDORS[5] },
  seals: { id: uid(K_REBATE, 2), vendor: VENDORS[2] },
  motors: { id: uid(K_REBATE, 3), vendor: VENDORS[1] },
  fasteners: { id: uid(K_REBATE, 4), vendor: VENDORS[3] },
};

function claim(n: number, agreement: keyof typeof AGREEMENTS, period: string, accrued: number, prior: number, mode: VendorClaim["settlementMode"], status: VendorClaim["status"], ago: number): VendorClaim {
  const a = AGREEMENTS[agreement];
  const amount = accrued - prior;
  return {
    id: uid(K_CLAIM, n),
    number: `RC-${String(n).padStart(5, "0")}`,
    legalEntityId: LEGAL_ENTITY.id,
    agreementId: a.id,
    vendorId: a.vendor.id,
    periodId: period,
    accruedTotal: dec(accrued),
    priorClaimed: dec(prior),
    amount: dec(amount),
    currency: "USD",
    settlementMode: amount < 0 ? "VENDOR_INVOICE" : mode,
    status,
    version: status === "APPROVED" ? 2 : 1,
    createdBy: "dana.whitfield",
    approvedBy: status === "APPROVED" ? "morgan.lee" : undefined,
    createdAt: at(-ago),
    approvedAt: status === "APPROVED" ? at(-ago + 1.5) : undefined,
  };
}

/** Quarters of the current year, so period ids stay plausible whatever day the demo runs. */
const year = Number(day(0).slice(0, 4));
const quarter = Math.floor((Number(day(0).slice(5, 7)) - 1) / 3) + 1;
const q = (back: number) => {
  const index = year * 4 + (quarter - 1) - back;
  return `${Math.floor(index / 4)}-Q${(index % 4) + 1}`;
};

const claims = table<VendorClaim>([
  claim(41, "bearings", q(1), 1840.5, 0, "CREDIT_NOTE", "OPEN", 2),
  claim(40, "seals", q(1), 3125, 0, "PAYMENT_DEDUCTION", "OPEN", 3),
  claim(39, "motors", q(1), 6480, 0, "CREDIT_NOTE", "APPROVED", 6),
  claim(38, "fasteners", q(1), 412.8, 0, "PAYMENT_DEDUCTION", "APPROVED", 7),
  claim(36, "seals", q(2), 2860, 2410, "PAYMENT_DEDUCTION", "APPROVED", 40),
  claim(35, "bearings", q(2), 1515.2, 1690, "CREDIT_NOTE", "APPROVED", 48),
  claim(33, "motors", q(2), 5920, 0, "CREDIT_NOTE", "APPROVED", 95),
]);

route("GET", `${BASE}/rebates/vendor-claims`, ({ query }) => {
  const agreementId = query.get("agreementId");
  const vendorId = query.get("vendorId");
  const period = query.get("period");
  const status = query.get("status");
  const items = claims
    .all()
    .filter((c) => (!agreementId || c.agreementId === agreementId) && (!vendorId || c.vendorId === vendorId) && (!period || c.periodId === period) && (!status || c.status === status))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .slice(0, 100);
  return { items };
});

route("GET", `${BASE}/rebates/vendor-claims/{id}`, ({ params }) => claims.get(params.id) ?? reply.problem(404, "PRC-0404", "Vendor rebate claim not found"));

route("POST", `${BASE}/rebates/vendor-claims/{id}:approve`, ({ params, body }) => {
  const c = claims.get(params.id);
  if (!c) return reply.problem(404, "PRC-0404", "Vendor rebate claim not found");
  const version = Number((body as { version?: number } | undefined)?.version ?? c.version);
  if (version !== c.version) return reply.problem(409, "PRC-0002", "The claim changed since you opened it. Reload it and try again.");
  if (c.status !== "OPEN") return reply.problem(409, "PRC-0047", "The claim is already approved");
  if (c.createdBy === USER.name) return reply.problem(409, "PRC-0046", "The person who created a claim cannot approve it");
  return claims.update(c.id, { status: "APPROVED", approvedBy: USER.name, approvedAt: at(0), version: c.version + 1 });
});

import { z } from "zod";
import { request } from "./client";

/**
 * Planning calculators (FR-22.16, FR-22.17, FR-22.19, FR-22.20, FR-22.21,
 * FR-22.22). The service stores nothing for them, so each is a POST that
 * returns its result and needs no Idempotency-Key.
 */

/** A quantity due on a day offset from day 0 of the calculation (DayQty in the OpenAPI). */
export interface DayQty {
  day: number;
  qty: string;
}

const post = (body: unknown): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
});

// DDMRP buffer (FR-22.16, BR-22.11)

export const BufferZone = z.enum(["red", "yellow", "green"]);
export type BufferZone = z.infer<typeof BufferZone>;

export const BufferResult = z.object({
  adu: z.string(),
  adjustedAdu: z.string(),
  zones: z.object({
    redBase: z.string(),
    redSafety: z.string(),
    yellow: z.string(),
    green: z.string(),
    topOfRed: z.string(),
    topOfYellow: z.string(),
    topOfGreen: z.string(),
  }),
  qualifiedDemand: z.string(),
  netFlow: z.string(),
  plannedQty: z.string(),
  priority: z.string(),
  zone: BufferZone,
});
export type BufferResult = z.infer<typeof BufferResult>;

export interface BufferInput {
  profile: {
    leadTimeFactor: string;
    variabilityFactor: string;
    orderCycleDays: number;
    minOrderQty: string;
    spikeThresholdPct?: string;
  };
  adu: string;
  dltDays: string;
  daf?: string;
  onHand: string;
  openSupply: string;
  demand: DayQty[];
  orderMultiple?: string;
}

/** POST /ddmrp/buffers:evaluate: zones, net-flow position, planned quantity and priority. */
export function evaluateBuffer(input: BufferInput): Promise<BufferResult> {
  return request("/ddmrp/buffers:evaluate", BufferResult, post(input));
}

// Quota allocation (FR-22.17, BR-22.12)

export const QuotaSourceType = z.enum(["vendor", "production", "transfer"]);
export type QuotaSourceType = z.infer<typeof QuotaSourceType>;

export interface QuotaSourceInput {
  id: string;
  type: QuotaSourceType;
  quota: string;
  allocatedQty: string;
  baseQty?: string;
  priority?: number;
  blocked?: boolean;
}

export const QuotaResult = z.object({
  assignments: z.array(z.object({ sourceId: z.string(), qty: z.string(), ratingAfter: z.string() })),
  qty: z.string(),
});
export type QuotaResult = z.infer<typeof QuotaResult>;

export interface QuotaInput {
  sources: QuotaSourceInput[];
  qty: string;
  split: boolean;
  minSplitQty?: string;
}

/** POST /quota-arrangements:allocate: which source gets the requirement (422 PLAN-0009 when none can). */
export function allocateQuota(input: QuotaInput): Promise<QuotaResult> {
  return request("/quota-arrangements:allocate", QuotaResult, post(input));
}

// Forecast consumption (FR-22.19, BR-22.13)

export const ConsumptionMode = z.enum(["backward", "forward", "backward-forward"]);
export type ConsumptionMode = z.infer<typeof ConsumptionMode>;

export interface ForecastPeriodInput {
  id: string;
  startDate: string;
  endDate: string;
  qty: string;
}

export interface SalesLineInput {
  id: string;
  date: string;
  qty: string;
}

export const ConsumptionResult = z.object({
  forecast: z.array(z.object({ id: z.string(), remainingQty: z.string() })),
  sales: z.array(z.object({ id: z.string(), consumedQty: z.string(), unconsumedQty: z.string() })),
  grossQty: z.string(),
});
export type ConsumptionResult = z.infer<typeof ConsumptionResult>;

export interface ConsumptionInput {
  mode: ConsumptionMode;
  backwardDays: number;
  forwardDays: number;
  forecast: ForecastPeriodInput[];
  sales: SalesLineInput[];
}

/** POST /strategies:consume-forecast: forecast left per period and the gross requirement. */
export function consumeForecast(input: ConsumptionInput): Promise<ConsumptionResult> {
  return request("/strategies:consume-forecast", ConsumptionResult, post(input));
}

// Supply schedule and inventory forecast (FR-22.21, FR-22.22)

export const Bucket = z.enum(["day", "week", "month"]);
export type Bucket = z.infer<typeof Bucket>;

export const SupplyPeriod = z.object({
  startDay: z.number().int(),
  endDay: z.number().int(),
  demand: z.string(),
  supply: z.string(),
  planned: z.string(),
  closingOnHand: z.string(),
  safetyStock: z.string(),
  shortage: z.boolean(),
  value: z.string(),
  daysOfSupply: z.string().nullable(),
});
export type SupplyPeriod = z.infer<typeof SupplyPeriod>;

export const SupplySchedule = z.object({
  bucket: Bucket,
  horizonDays: z.number().int(),
  currency: z.string(),
  periods: z.array(SupplyPeriod),
  totals: z.object({
    demand: z.string(),
    supply: z.string(),
    planned: z.string(),
    closingOnHand: z.string(),
  }),
});
export type SupplySchedule = z.infer<typeof SupplySchedule>;

export interface SupplyScheduleInput {
  horizonDays: number;
  bucket: Bucket;
  openingOnHand: string;
  safetyStock: string;
  unitCost: string;
  currency: string;
  demand: DayQty[];
  supply: DayQty[];
  planned: DayQty[];
}

/** POST /supply-schedule:project: bucketed schedule, value and days of supply. */
export function projectSupplySchedule(input: SupplyScheduleInput): Promise<SupplySchedule> {
  return request("/supply-schedule:project", SupplySchedule, post(input));
}

// Shelf-life allocation (FR-22.20, BR-22.15)

export const ShelfLifeResult = z.object({
  allocations: z.array(z.object({ batchId: z.string(), qty: z.string() })),
  shortfallQty: z.string(),
  expiringUnpegged: z.array(z.object({ batchId: z.string(), qty: z.string(), expiryDay: z.number().int() })),
});
export type ShelfLifeResult = z.infer<typeof ShelfLifeResult>;

export interface BatchInput {
  id: string;
  qty: string;
  expiryDay: number;
}

export interface ShelfLifeInput {
  demandDay: number;
  demandQty: string;
  requiredShelfLifeDays: number;
  horizonDay?: number;
  batches: BatchInput[];
}

/** POST /shelf-life:allocate: FEFO allocation of batches to one demand. */
export function allocateShelfLife(input: ShelfLifeInput): Promise<ShelfLifeResult> {
  return request("/shelf-life:allocate", ShelfLifeResult, post(input));
}

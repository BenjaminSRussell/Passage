import type { OrderSummary, ProductionOrder, StatusLogEntry } from "./api/client";

export const orderId = "0195d6a0-0000-7000-8000-000000000001";

export function summary(overrides: Partial<OrderSummary> = {}): OrderSummary {
  return {
    id: orderId,
    legalEntityId: "0195d6a0-0000-7000-8000-0000000000e1",
    orderNumber: "PO-000001",
    productId: "0195d6a0-0000-7000-8000-0000000000f1",
    siteId: "0195d6a0-0000-7000-8000-0000000000a1",
    warehouseId: "0195d6a0-0000-7000-8000-0000000000b1",
    currency: "USD",
    quantity: "100",
    qtyRaf: "0",
    qtyError: "0",
    remaining: "100",
    status: "Created",
    deliveryDate: "2026-11-30",
    plannedCost: "0",
    version: 1,
    createdAt: "2026-10-26T12:00:00Z",
    ...overrides,
  };
}

export function order(overrides: Partial<ProductionOrder> = {}): ProductionOrder {
  return {
    id: orderId,
    legalEntityId: "0195d6a0-0000-7000-8000-0000000000e1",
    orderNumber: "PO-000001",
    productId: "0195d6a0-0000-7000-8000-0000000000f1",
    siteId: "0195d6a0-0000-7000-8000-0000000000a1",
    warehouseId: "0195d6a0-0000-7000-8000-0000000000b1",
    currency: "USD",
    quantity: "100",
    qtyRaf: "0",
    qtyError: "0",
    status: "Created",
    deliveryDate: "2026-11-30",
    originType: "Manual",
    version: 1,
    lines: [
      { lineNo: "10", itemId: "RM-STEEL", qtyPer: "3", seriesSize: "1", scrapPct: "5", roundUp: false, qtyRequired: "315", unitCost: "10" },
      { lineNo: "30.1", itemId: "RM-BOLT", qtyPer: "2", seriesSize: "1", scrapPct: "0", roundUp: false, qtyRequired: "200", unitCost: "4" },
    ],
    operations: [
      {
        operNumber: 10,
        resourceId: "WC-WELD",
        setupTimeH: "0",
        runTimeHPer: "0.5",
        scrapPct: "0",
        laborRate: "30",
        overheadRate: "10",
        subcontract: false,
        subcontractPrice: "0",
        processQty: "100",
      },
    ],
    costs: [],
    createdAt: "2026-10-26T12:00:00Z",
    updatedAt: "2026-10-26T12:00:00Z",
    ...overrides,
  };
}

export const logEntry = (overrides: Partial<StatusLogEntry> = {}): StatusLogEntry => ({
  command: "estimate",
  from: "Created",
  to: "Estimated",
  actor: "planner-1",
  traceId: "trace-abc",
  occurredAt: "2026-10-26T12:05:00Z",
  ...overrides,
});

export const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

export const problem = (status: number, code: string, detail: string, errors?: Record<string, string>) =>
  json({ type: `https://passage.dev/problems/${code}`, title: "Problem", status, code, detail, errors }, status);

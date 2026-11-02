import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import type { Message, PlannedOrder } from "./api/client";
import planning from "./module";
import { summarize } from "./pages";

const order: PlannedOrder = {
  id: "00000000-0000-0000-0000-0000000000a1",
  versionId: "00000000-0000-0000-0000-0000000000b1",
  number: "PLN-000005",
  itemId: "00000000-0000-0000-0000-0000000000c1",
  orderType: "production",
  qty: "110",
  orderDate: "2026-11-02",
  deliveryDate: "2026-11-30",
  futuresDate: "2026-12-07",
  delayDays: 7,
  lotRule: "lot-for-lot",
  status: "unprocessed",
  version: 1,
};

const message: Message = {
  action: "delay",
  itemId: order.itemId,
  supplyRef: "PLN-000005",
  demandRef: "SO-1",
  suggestedDate: "2026-12-07",
  delayDays: 7,
  qty: "110",
};

describe("planning module", () => {
  it("uses its folder name as id and registers a page for every screen of the slice", () => {
    expect(planning.id).toBe("planning");
    expect(planning.routes.map((r) => r.path)).toEqual([
      "/planning",
      "/planning/planned-orders",
      "/planning/planned-orders/:id",
      "/planning/messages",
      "/planning/net-requirements",
      "/planning/supply-schedule",
      "/planning/runs",
      "/planning/runs/compare",
      "/planning/runs/new",
      "/planning/forecast",
      "/planning/calculators",
      "/planning/coverage-groups",
      "/planning/coverage-groups/new",
      "/planning/coverage-groups/:code",
      "/planning/item-coverages",
      "/planning/item-coverages/new",
      "/planning/item-coverages/:id",
      "/planning/buffer-profiles",
      "/planning/buffer-profiles/new",
      "/planning/buffer-profiles/:id",
      "/planning/ddmrp-buffers",
      "/planning/quota-arrangements",
      "/planning/quota-arrangements/new",
      "/planning/quota-arrangements/:id",
      "/planning/strategies",
      "/planning/strategies/:code",
      "/planning/scenarios",
      "/planning/scenarios/new",
      "/planning/scenarios/:code",
    ]);
    for (const route of planning.routes) {
      expect(route.element).not.toBeNull();
    }
  });

  it("gates reads on planning.read and the run form on planning.run", () => {
    expect(planning.nav.every((n) => n.to.startsWith("/planning"))).toBe(true);
    const duties = Object.fromEntries(planning.nav.map((n) => [n.to, n.duty]));
    expect(duties["/planning"]).toBe("planning.read");
    expect(duties["/planning/planned-orders"]).toBe("planning.read");
    expect(duties["/planning/runs/new"]).toBe("planning.run");
    expect(duties["/planning/forecast"]).toBe("planning.read");
    expect(duties["/planning/calculators"]).toBe("planning.read");
    expect(duties["/planning/net-requirements"]).toBe("planning.read");
    expect(duties["/planning/supply-schedule"]).toBe("planning.read");
    expect(duties["/planning/coverage-groups"]).toBe("planning.read");
    expect(duties["/planning/buffer-profiles"]).toBe("planning.read");
    expect(duties["/planning/ddmrp-buffers"]).toBe("planning.read");
    expect(duties["/planning/quota-arrangements"]).toBe("planning.read");
    expect(duties["/planning/scenarios"]).toBe("planning.read");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("planning");
  });

  it("summarizes orders by type, counts delays and messages", () => {
    const s = summarize([order, { ...order, orderType: "purchase", delayDays: 0 }], [message]);
    expect(s.byType).toEqual({ purchase: 1, production: 1, transfer: 0 });
    expect(s.delayed).toBe(1);
    expect(s.messages).toBe(1);
  });
});

import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import inventory from "./module";

describe("inventory module", () => {
  it("uses its folder name as id and declares routes and nav under /inventory", () => {
    expect(inventory.id).toBe("inventory");
    expect(inventory.routes.every((r) => r.element !== null && r.path?.startsWith("/inventory"))).toBe(true);
    expect(inventory.nav.every((n) => n.to.startsWith("/inventory"))).toBe(true);
  });

  it("guards reads with inventory.read and journal creation with inventory.journal.write", () => {
    expect(Object.fromEntries(inventory.nav.map((n) => [n.to, n.duty]))).toEqual({
      "/inventory/onhand": "inventory.read",
      "/inventory/journals": "inventory.read",
      "/inventory/journals/new": "inventory.journal.write",
      "/inventory/transactions": "inventory.read",
      "/inventory/status-changes": "inventory.read",
      "/inventory/reservations": "inventory.read",
      "/inventory/blockings": "inventory.read",
      "/inventory/count-plans": "inventory.read",
      "/inventory/count-sheets": "inventory.read",
      "/inventory/transfer-orders": "inventory.read",
      "/inventory/transfer-lines": "inventory.read",
      "/inventory/in-transit": "inventory.read",
      "/inventory/abc-runs": "inventory.read",
      "/inventory/consignment-orders": "inventory.read",
      "/inventory/consignment/stock": "inventory.read",
      "/inventory/reconciliations": "inventory.read",
    });
  });

  it("routes every slice 2 screen", () => {
    const paths = inventory.routes.map((r) => r.path);
    for (const path of [
      "/inventory/transactions",
      "/inventory/reservations",
      "/inventory/reservations/new",
      "/inventory/blockings",
      "/inventory/blockings/new",
      "/inventory/count-plans",
      "/inventory/count-plans/new",
      "/inventory/count-sheets",
      "/inventory/count-sheets/:id",
    ]) {
      expect(paths).toContain(path);
    }
  });

  it("routes every slice 3 screen", () => {
    const paths = inventory.routes.map((r) => r.path);
    for (const path of [
      "/inventory/transfer-lines",
      "/inventory/transfer-orders",
      "/inventory/transfer-orders/new",
      "/inventory/transfer-orders/:id",
      "/inventory/in-transit",
      "/inventory/abc-runs",
      "/inventory/abc-runs/new",
      "/inventory/abc-runs/:id",
    ]) {
      expect(paths).toContain(path);
    }
  });

  it("routes every slice 5 screen", () => {
    const paths = inventory.routes.map((r) => r.path);
    for (const path of [
      "/inventory/consignment-orders",
      "/inventory/consignment-orders/new",
      "/inventory/consignment-orders/:id",
      "/inventory/consignment/stock",
    ]) {
      expect(paths).toContain(path);
    }
  });

  it("routes every slice 6 screen", () => {
    const paths = inventory.routes.map((r) => r.path);
    for (const path of ["/inventory/transactions/:id", "/inventory/status-changes"]) {
      expect(paths).toContain(path);
    }
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("inventory");
  });
});

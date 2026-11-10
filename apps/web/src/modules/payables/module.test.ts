import { describe, expect, it } from "vitest";
import { loadModules } from "../registry";
import payables from "./module";

describe("payables module", () => {
  it("uses its folder name as id and declares routes and nav", () => {
    expect(payables.id).toBe("payables");
    expect(payables.routes.length).toBeGreaterThan(0);
    expect(payables.nav.every((n) => n.to.startsWith("/payables"))).toBe(true);
  });

  it("gates every nav entry and the proposal form by a duty", () => {
    expect(payables.nav.map((n) => n.duty)).toEqual(["payment-proposal.read", "open-item.read"]);
    const form = payables.routes.find((r) => r.path === "/payables/proposals/new");
    expect((form?.handle as { duty?: string }).duty).toBe("payment-proposal.write");
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("payables");
  });
});

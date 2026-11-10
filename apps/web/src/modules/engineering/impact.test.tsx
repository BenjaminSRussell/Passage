import { findAlert, findTable } from "../../test-utils";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { EcoImpactPanel } from "./impact";

afterEach(() => vi.unstubAllGlobals());

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const report = {
  asOf: "2026-03-01",
  replacements: [{ oldVersionId: "o", newVersionId: "n", productId: "BIKE", changes: [{ componentProductId: "WHEEL", change: "changed", oldQuantity: "1", newQuantity: "2" }] }],
  products: [
    { productId: "BIKE", reason: "replaced", level: 0, topLevel: false, siteIds: [], versionIds: ["o"] },
    { productId: "TRIKE", reason: "parent", level: 1, topLevel: true, siteIds: ["plant-1"], versionIds: ["t1"] },
  ],
  siteIds: ["plant-1"],
  openProductionOrders: 0,
  openPlannedOrders: 0,
};

describe("EcoImpactPanel", () => {
  it("loads the report on request and lists products and line changes", async () => {
    const fetchMock = vi.fn(async () => ok(report));
    vi.stubGlobal("fetch", fetchMock);
    render(<EcoImpactPanel id="e1" />);
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Show impact" }));
    const table = await findTable({ name: "Affected products" });
    expect(table).toHaveTextContent("TRIKE");
    expect(table).toHaveTextContent("Uses it (level 1)");
    expect(table).toHaveTextContent("All sites");
    expect(screen.getByText(/2 affected products \(1 top-level product\) across 1 site\./)).toBeInTheDocument();
    expect(await findTable({ name: "Line changes for BIKE" })).toHaveTextContent("WHEEL");
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/engineering/ecos/e1/impact", expect.anything());
  });

  it("shows the empty state", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ok({ ...report, replacements: [], products: [], siteIds: [] })));
    render(<EcoImpactPanel id="e1" />);
    fireEvent.click(screen.getByRole("button", { name: "Show impact" }));
    expect(await screen.findByText("No products are affected")).toBeInTheDocument();
    expect(screen.getByText(/0 affected products \(0 top-level products\) on every site\./)).toBeInTheDocument();
  });

  it("shows an error with retry", async () => {
    let calls = 0;
    vi.stubGlobal("fetch", vi.fn(async () => (calls++ === 0 ? { ok: false, status: 422, json: async () => ({ code: "ENG-BOM-CYCLE", title: "Cycle found" }) } : ok(report))));
    render(<EcoImpactPanel id="e1" />);
    fireEvent.click(screen.getByRole("button", { name: "Show impact" }));
    expect(await findAlert()).toHaveTextContent("Cycle found");
    fireEvent.click(screen.getByRole("button", { name: "Retry impact" }));
    await findTable({ name: "Affected products" });
  });
});

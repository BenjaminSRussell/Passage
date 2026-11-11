import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { clearResolvedOptions, rememberOption } from "../../shell/lookup/entities";
import inventory from "./module";

const ENTITY = "9e8d7c6b-0000-4000-8000-000000000003";
const TRUCK = "7a7a7a7a-0000-4000-8000-000000000041";
const SITE = "7a7a7a7a-0000-4000-8000-000000000042";
const SEAL = "5f2c1e7a-0000-4000-8000-000000000051";
const BEARING = "5f2c1e7a-0000-4000-8000-000000000052";

let calls: { method: string; url: URL; body: unknown }[];
let levels: Record<string, unknown>[];
let orders: unknown[];

const level = (productId: string, on: string, short: boolean) => ({
  legalEntityId: ENTITY, siteId: SITE, warehouseId: TRUCK, sourceSiteId: "CR", sourceWarehouseId: "CR-FG", productId, unit: "EA",
  minQty: "4", maxQty: "10", onHandQty: on, inboundQty: "0", short,
});

beforeEach(() => {
  calls = [];
  orders = [{ id: "o1" }];
  levels = [level(SEAL, "4", false), level(BEARING, "3", true)];
  clearResolvedOptions();
  rememberOption("warehouse", { id: TRUCK, name: "Truck J. Avery", number: "TRK-01" });
  rememberOption("product", { id: SEAL, name: "Mechanical seal MK-125", number: "SEAL-MK-125" });
  rememberOption("product", { id: BEARING, name: "Bearing 6206", number: "BRG-6206" });
  rememberOption("legalEntity", { id: ENTITY, name: "Cedar Ridge Pumps, Inc.", number: "CRP-US" });
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(String(init.body)) : undefined });
      if (url.pathname.endsWith("/truck-stock-levels")) return new Response(JSON.stringify({ items: levels }), { status: 200 });
      if (url.pathname.endsWith("/truck-stock:replenish")) return new Response(JSON.stringify({ orders }), { status: 200 });
      return new Response("{}", { status: 404 });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(duties: string[], search = `?legalEntityId=${ENTITY}`) {
  return render(
    <MemoryRouter initialEntries={[`/inventory/truck-stock${search}`]}>
      <PrincipalProvider duties={duties}>
        <Routes>
          {inventory.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </PrincipalProvider>
    </MemoryRouter>,
  );
}

describe("truck stock", () => {
  it("asks for a legal entity before it loads anything", async () => {
    renderAt(["inventory.read"], "");
    expect(await screen.findByText("Choose a legal entity")).toBeInTheDocument();
    expect(calls.filter((c) => c.url.pathname.endsWith("/truck-stock-levels"))).toHaveLength(0);
  });

  it("lists each truck level by name with its status, and never shows an id", async () => {
    const view = renderAt(["inventory.read"]);
    expect(await screen.findByText("Mechanical seal MK-125", { exact: false })).toBeInTheDocument();
    expect(screen.getByText("Bearing 6206", { exact: false })).toBeInTheDocument();
    expect(screen.getAllByText("Truck J. Avery", { exact: false }).length).toBe(2);
    expect(screen.getByText("Below minimum")).toBeInTheDocument();
    expect(screen.getByText("Stocked")).toBeInTheDocument();
    expect(view.container.textContent).not.toContain(TRUCK);
    expect(calls[0]?.url.searchParams.get("legalEntityId")).toBe(ENTITY);
    expect(screen.queryByRole("button", { name: "Refill trucks below minimum" })).toBeNull();
  });

  it("creates the draft transfer orders for trucks below their minimum and reads the levels again", async () => {
    renderAt(["inventory.read", "inventory.transfer.write"]);
    const refill = await screen.findByRole("button", { name: "Refill trucks below minimum" });
    await waitFor(() => expect(refill).toBeEnabled());
    levels = [level(SEAL, "4", false), { ...level(BEARING, "3", false), inboundQty: "7" }];
    fireEvent.click(refill);
    expect(await screen.findByText("1 draft transfer order created")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ legalEntityId: ENTITY });
    await waitFor(() => expect(screen.queryByText("Below minimum")).toBeNull());
  });

  it("says so when no truck needed a refill", async () => {
    levels = [level(SEAL, "9", false)];
    orders = [];
    renderAt(["inventory.read", "inventory.transfer.write"]);
    expect(await screen.findByText("Stocked")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refill trucks below minimum" })).toBeDisabled();
  });
});

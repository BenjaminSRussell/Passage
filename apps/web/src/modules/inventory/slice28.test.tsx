import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";

const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";
const PLAN_ID = "3d3d3d3d-0000-4000-8000-000000000011";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];
const json = (status: number, body: unknown) => ({ status, body });

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init });
      const { status, body } = handler(url, init);
      return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {inventory.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}


describe("count plan with vendor-owned stock", () => {
  const fill = () => {
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Consignment aisle" } });
    fireEvent.change(screen.getByLabelText("Currency"), { target: { value: "USD" } });
  };

  it("sends includeVendorOwned when the box is ticked", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, { id: PLAN_ID }) : json(200, { items: [] }));
    renderAt("/inventory/count-plans/new");
    fill();
    fireEvent.click(screen.getByLabelText("Include vendor-owned stock"));
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string)).toMatchObject({ includeVendorOwned: true });
  });

  it("leaves it off by default", async () => {
    handler = (_url, init) => (init.method === "POST" ? json(201, { id: PLAN_ID }) : json(200, { items: [] }));
    renderAt("/inventory/count-plans/new");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Create count plan" }));
    await waitFor(() => expect(calls.some((c) => c.method === "POST")).toBe(true));
    expect(JSON.parse(calls.find((c) => c.method === "POST")!.init.body as string)).not.toHaveProperty("includeVendorOwned");
  });

  it("names the choice in the plan list", async () => {
    handler = () =>
      json(200, {
        items: [{ id: PLAN_ID, legalEntityId: ENTITY_ID, name: "Consignment aisle", currency: "USD", unit: "EA", tolerancePct: "5", toleranceValue: "100", blind: false, freezeStock: false, fourEyes: false, includeVendorOwned: true, createdAt: "2026-09-01T08:00:00Z" }],
      });
    renderAt("/inventory/count-plans");
    expect(await screen.findByRole("table", { name: "Count plans" })).toHaveTextContent("Includes vendor-owned stock");
  });
});

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import inventory from "./module";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit }[];

const json = (status: number, body: unknown) => ({ status, body });
const sent = (method: string) => calls.filter((c) => c.method === method);

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

afterEach(() => {
  vi.unstubAllGlobals();
});

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

function fillIds() {
  fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: ENTITY_ID } });
  fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT_ID } });
}

const batch = (name: string, extra: Record<string, unknown> = {}) => ({
  dimensions: { site: "S1", warehouse: "W1", batch: name },
  batchId: name,
  available: "5",
  status: "AVAILABLE",
  eligible: true,
  ...extra,
});

describe("Available stock on the new reservation form", () => {
  it("asks for the ids before it loads anything", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/reservations/new");
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });

  it("lists the candidates in pick order with the reason for the rest, and fills the form from a row", async () => {
    handler = () =>
      json(200, {
        items: [
          batch("B3", { expiryDate: "2027-01-20", available: "7" }),
          batch("B5"),
          batch("B4", { eligible: false, reason: "EXPIRES_BEFORE", expiryDate: "2026-09-04" }),
          batch("B2", { eligible: false, reason: "RESTRICTED", status: "BLOCKED" }),
        ],
      });
    renderAt("/inventory/reservations/new");
    fillIds();
    fireEvent.change(screen.getByLabelText("Policy"), { target: { value: "FEFO" } });
    fireEvent.change(screen.getByLabelText("Shelf-life margin (days)"), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText(/^Site/), { target: { value: "S1" } });
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findByText("2027-01-20")).toBeInTheDocument();
    const url = calls[0].url;
    expect(url.pathname).toMatch(/reservation-candidates$/);
    expect(url.searchParams.get("policy")).toBe("FEFO");
    expect(url.searchParams.get("shelfLifeMarginDays")).toBe("5");
    expect(url.searchParams.get("site")).toBe("S1");
    expect(url.searchParams.get("productId")).toBe(PRODUCT_ID);
    expect(screen.getByText("Expires before the required date plus the margin")).toBeInTheDocument();
    expect(screen.getByText("Blocked or in quarantine")).toBeInTheDocument();
    // Only the eligible rows offer the pick.
    const picks = screen.getAllByRole("button", { name: "Reserve this stock" });
    expect(picks).toHaveLength(2);
    fireEvent.click(picks[1]);
    await waitFor(() => expect(screen.getByLabelText("Policy")).toHaveValue("MANUAL"));
    expect(screen.getByLabelText(/^Batch/)).toHaveValue("B5");
    expect(screen.getByLabelText(/^Warehouse/)).toHaveValue("W1");
    expect(sent("POST")).toHaveLength(0);
  });

  it("does not offer a pick for stock with dimensions the form cannot name", async () => {
    handler = () => json(200, { items: [{ ...batch("B1"), dimensions: { site: "S1", batch: "B1", owner: "V1" } }] });
    renderAt("/inventory/reservations/new");
    fillIds();
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findByText("Eligible")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reserve this stock" })).toBeNull();
  });

  it("offers the pick only for stock of the demand's own segment, which the manual reservation names", async () => {
    handler = () =>
      json(200, {
        items: [
          { ...batch("B7"), dimensions: { site: "S1", batch: "B7", segment: "SALES_ORDER:SO-1" }, segment: "SALES_ORDER:SO-1" },
          batch("B8"),
        ],
      });
    renderAt("/inventory/reservations/new");
    fillIds();
    fireEvent.change(screen.getByLabelText("Demand segment"), { target: { value: "SALES_ORDER" } });
    fireEvent.change(screen.getByLabelText("Segment reference"), { target: { value: "SO-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findAllByText("Eligible")).toHaveLength(2);
    expect(calls[0].url.searchParams.get("segmentType")).toBe("SALES_ORDER");
    expect(calls[0].url.searchParams.get("segmentRef")).toBe("SO-1");
    // B8 is unsegmented fallback stock: the form would add the demand's segment and reserve another dimension set.
    const picks = screen.getAllByRole("button", { name: "Reserve this stock" });
    expect(picks).toHaveLength(1);
    fireEvent.click(picks[0]);
    await waitFor(() => expect(screen.getByLabelText(/^Batch/)).toHaveValue("B7"));
  });

  it("hides a list loaded for another product instead of picking from it", async () => {
    handler = () => json(200, { items: [batch("B1")] });
    renderAt("/inventory/reservations/new");
    fillIds();
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findByRole("button", { name: "Reserve this stock" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: "5f2c1e7a-0000-4000-8000-000000000009" } });
    expect(screen.queryByRole("button", { name: "Reserve this stock" })).toBeNull();
    expect(screen.getByText(/Show the available stock again/)).toBeInTheDocument();
  });

  it("shows an empty state and an error with its code", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/reservations/new");
    fillIds();
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findByText("No available stock")).toBeInTheDocument();
    handler = () => json(403, { code: "forbidden", title: "Forbidden", status: 403, detail: "no" });
    fireEvent.click(screen.getByRole("button", { name: "Show available stock" }));
    expect(await screen.findByText("Could not load the stock")).toBeInTheDocument();
  });
});

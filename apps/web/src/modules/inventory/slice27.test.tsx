import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import inventory from "./module";

const PRODUCT_ID = "5f2c1e7a-0000-4000-8000-000000000002";
const ENTITY_ID = "9e8d7c6b-0000-4000-8000-000000000003";

type Handler = (url: URL, init: RequestInit) => { status: number; body: unknown };
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

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

const row = (group: Record<string, string>) => ({ group, physicalInvent: "10", availPhysical: "8", availOrdered: "8", postedValue: "30.00", blocked: "0", currency: "USD", negative: false });

describe("On-hand drill-down by dimension", () => {
  it("links each row to the transactions of its dimensions", async () => {
    handler = () =>
      json(200, { items: [row({ product: PRODUCT_ID, site: "S1", warehouse: "W1", batch: "B 7" }), row({ product: PRODUCT_ID, site: "S2" })], truncated: false });
    renderAt("/inventory/onhand");
    const links = await screen.findAllByRole("link", { name: "Transactions" });
    expect(links[0]).toHaveAttribute("href", `/inventory/transactions?product=${PRODUCT_ID}&site=S1&warehouse=W1&batch=B+7`);
    expect(links[1]).toHaveAttribute("href", `/inventory/transactions?product=${PRODUCT_ID}&site=S2`);
  });

  it("carries inventory status, owner and segment into the link", async () => {
    handler = () =>
      json(200, { items: [row({ product: PRODUCT_ID, inventory_status: "BLOCKED", owner: "V1", segment: "SALES_ORDER:SO-1" })], truncated: false });
    renderAt("/inventory/onhand");
    const link = await screen.findByRole("link", { name: "Transactions" });
    expect(link).toHaveAttribute("href", `/inventory/transactions?product=${PRODUCT_ID}&inventoryStatus=BLOCKED&owner=V1&segment=SALES_ORDER%3ASO-1`);
  });

  it("shows a dash instead of a link when the row names no dimension the inquiry filters on", async () => {
    handler = () => json(200, { items: [row({})], truncated: false });
    renderAt("/inventory/onhand");
    await screen.findByText("30.00 USD");
    expect(screen.queryByRole("link", { name: "Transactions" })).toBeNull();
  });
});

describe("Transaction inquiry by dimension", () => {
  it("opens on the linked dimensions and sends them to the service", async () => {
    handler = () => json(200, { items: [] });
    renderAt(
      `/inventory/transactions?product=${PRODUCT_ID}&legalEntity=${ENTITY_ID}&site=S1&warehouse=W1&location=A-01&batch=B7&serial=SN9&inventoryStatus=BLOCKED&owner=V1&segment=PROJECT%3AP1`,
    );
    await screen.findByText("No transactions match");
    const q = calls[0].url.searchParams;
    expect(Object.fromEntries(q.entries())).toEqual({
      product: PRODUCT_ID,
      legalEntity: ENTITY_ID,
      site: "S1",
      warehouse: "W1",
      location: "A-01",
      batch: "B7",
      serial: "SN9",
      inventoryStatus: "BLOCKED",
      owner: "V1",
      segment: "PROJECT:P1",
    });
    expect((screen.getByLabelText("Batch") as HTMLInputElement).value).toBe("B7");
    expect((screen.getByLabelText("Inventory status") as HTMLInputElement).value).toBe("BLOCKED");
  });

  it("filters by a typed dimension and clears it again", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/transactions");
    await screen.findByText("No transactions match");
    fireEvent.change(screen.getByLabelText("Serial number"), { target: { value: " SN-1 " } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("serial")).toBe("SN-1"));
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("serial")).toBeNull());
  });

  it("ignores a malformed legal entity in the link and does not take typed text as one", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/inventory/transactions?legalEntity=oops");
    await screen.findByLabelText("Legal entity");
    expect(calls[0].url.searchParams.get("legalEntity")).toBeNull();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: "nope" } });
    expect(screen.getByLabelText("Legal entity")).toHaveAttribute("data-value", "");
  });

  it("shows the service problem and lets the user retry", async () => {
    handler = () => json(400, { title: "Bad request", status: 400, errors: { batch: "must be at most 100 characters" } });
    const { container } = renderAt(`/inventory/transactions?batch=${"b".repeat(101)}`);
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(calls[0].url.searchParams.get("batch")).toHaveLength(101);
    expect(pageViolations(container)).toEqual([]);
    handler = () => json(200, { items: [] });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No transactions match")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(calls).toHaveLength(2);
  });
});

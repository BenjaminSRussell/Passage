// Short picks and backorders (FR-28.2) and returnable packaging aging (FR-28.16):
// list on open, filter, page, empty and error states.
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AgedReturnableBalance, BackorderRow, ReturnableAging } from "./api/client";
import { BACKORDER_PAGE } from "./BackordersPage";
import { AGING_LIMIT } from "./ReturnableAgingPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const BACKORDERS = "/api/v1/fulfillment/backorders";
const AGING = "/api/v1/fulfillment/returnable-packaging/aging";
const SHIPMENT = "0195f0a0-0000-7000-8000-000000000001";
const WAREHOUSE = "0195f0a0-0000-7000-8000-000000000040";
const CUSTOMER = "0195f0a0-0000-7000-8000-000000000030";

function backorder(line: string, patch: Partial<BackorderRow> = {}): BackorderRow {
  return {
    shipmentLineId: line,
    shipmentId: SHIPMENT,
    shipmentNumber: "SH-0000007",
    status: "PARTIALLY_SHIPPED",
    warehouseId: WAREHOUSE,
    orderId: "0195f0a0-0000-7000-8000-000000000020",
    customerId: CUSTOMER,
    orderLineId: "0195f0a0-0000-7000-8000-000000000021",
    productId: "0195f0a1-0000-7000-8000-000000000050",
    unit: "EA",
    qtyPlanned: "100",
    qtyPicked: "90",
    qtyShipped: "90",
    qtyRemainder: "10",
    open: true,
    shortPickedAt: "2026-10-21T14:05:00Z",
    ...patch,
  };
}

const calls = (path: string) => server.calls.filter((c) => c.url.pathname === path);
const last = (path: string) => calls(path)[calls(path).length - 1].url.searchParams;

describe("Short picks and backorders", () => {
  it("lists open backorders on open with the remainder and links the shipment", async () => {
    server.handler = (url) => (url.pathname === BACKORDERS ? json(200, { items: [backorder("0195f0a0-0000-7000-8000-0000000000a1")] }) : undefined);
    const { container } = renderAt("/fulfillment/backorders");
    expect(screen.getByRole("table", { name: "Short picks and backorders" })).toHaveAttribute("aria-busy", "true");

    const table = await screen.findByRole("table", { name: "Short picks and backorders" });
    const row = (await within(table).findByText("SH-0000007")).closest("tr") as HTMLElement;
    const cells = Array.from(row.querySelectorAll("td, th")).map((cell) => cell.textContent);
    expect(cells).toEqual(["SH-0000007", "Partially shipped", "0195f0a1", "0195f0a0", "100 EA", "90 EA", "10 EA", "Open", "2026-10-21 14:05 UTC"]);
    expect(within(row).getByRole("link", { name: "SH-0000007" })).toHaveAttribute("href", `/fulfillment/shipments/${SHIPMENT}`);
    const params = last(BACKORDERS);
    expect(params.get("limit")).toBe(String(BACKORDER_PAGE));
    expect(params.has("status")).toBe(false);
    expect(params.has("cursor")).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends the filter, shows closed backorders and the filtered empty state", async () => {
    server.handler = (url) => {
      if (url.pathname !== BACKORDERS) return undefined;
      return url.searchParams.get("status") === "all"
        ? json(200, { items: [backorder("0195f0a0-0000-7000-8000-0000000000a2", { status: "CANCELLED", open: false, qtyRemainder: "3" })] })
        : json(200, { items: [] });
    };
    renderAt("/fulfillment/backorders");
    expect(await screen.findByText("No open backorders")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Warehouse id", { exact: false }), { target: { value: WAREHOUSE } });
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "all" } });
    fireEvent.change(screen.getByLabelText("Short-picked from", { exact: false }), { target: { value: "2026-10-01" } });
    fireEvent.change(screen.getByLabelText("Short-picked to", { exact: false }), { target: { value: "2026-10-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));

    expect(await screen.findByText("Closed")).toBeInTheDocument();
    expect(screen.getByText("Cancelled")).toBeInTheDocument();
    const params = last(BACKORDERS);
    expect(params.get("warehouseId")).toBe(WAREHOUSE);
    expect(params.get("status")).toBe("all");
    expect(params.get("from")).toBe("2026-10-01");
    expect(params.get("to")).toBe("2026-10-31");

    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "open" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));
    expect(await screen.findByText("No short picks match the filter")).toBeInTheDocument();
  });

  it("validates the filter before calling the service", async () => {
    server.handler = (url) => (url.pathname === BACKORDERS ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/backorders");
    await screen.findByText("No open backorders");
    const before = calls(BACKORDERS).length;

    fireEvent.change(screen.getByLabelText("Warehouse id", { exact: false }), { target: { value: "WH-1" } });
    fireEvent.change(screen.getByLabelText("Short-picked from", { exact: false }), { target: { value: "2026-10-31" } });
    fireEvent.change(screen.getByLabelText("Short-picked to", { exact: false }), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));

    expect(await screen.findByText("Enter the warehouse id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(screen.getByText("The last date cannot be before the first.")).toBeInTheDocument();
    expect(calls(BACKORDERS)).toHaveLength(before);
  });

  it("pages forward with the cursor and back again", async () => {
    const first = "0195f0a0-0000-7000-8000-0000000000b1";
    const second = "0195f0a0-0000-7000-8000-0000000000b2";
    server.handler = (url) => {
      if (url.pathname !== BACKORDERS) return undefined;
      return url.searchParams.get("cursor") === first
        ? json(200, { items: [backorder(second, { shipmentNumber: "SH-0000008" })] })
        : json(200, { items: [backorder(first)], nextCursor: first });
    };
    renderAt("/fulfillment/backorders");
    await screen.findByText("SH-0000007");
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(await screen.findByText("SH-0000008")).toBeInTheDocument();
    expect(last(BACKORDERS).get("cursor")).toBe(first);
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Previous page" }));
    expect(await screen.findByText("SH-0000007")).toBeInTheDocument();
    await waitFor(() => expect(last(BACKORDERS).has("cursor")).toBe(false));
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    server.handler = (url) =>
      url.pathname !== BACKORDERS ? undefined : fail ? json(403, { code: "COM-0403", detail: "missing duty WarehouseView" }) : json(200, { items: [backorder("0195f0a0-0000-7000-8000-0000000000c1")] });
    renderAt("/fulfillment/backorders");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Could not load the backorders");
    expect(alert.textContent).toContain("COM-0403");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("SH-0000007")).toBeInTheDocument();
  });
});

function aged(containerType: string, patch: Partial<AgedReturnableBalance> = {}): AgedReturnableBalance {
  return {
    customerId: CUSTOMER,
    containerType,
    balance: "6",
    lastShippedAt: "2026-10-11T08:00:00Z",
    lastReturnedAt: "2026-10-15T09:30:00Z",
    oldestOutstandingDate: "2026-07-13",
    daysOutstanding: 100,
    chargeDue: true,
    ...patch,
  };
}

function aging(items: AgedReturnableBalance[], patch: Partial<ReturnableAging> = {}): ReturnableAging {
  return { asOf: "2026-10-21", chargeDays: 90, count: items.length, chargeDueCount: items.filter((i) => i.chargeDue).length, items, ...patch };
}

describe("Returnable packaging aging", () => {
  it("lists aged balances with the counts and the charge flag", async () => {
    server.handler = (url) =>
      url.pathname === AGING ? json(200, aging([aged("PALLET"), aged("CRATE", { balance: "4", oldestOutstandingDate: "2026-10-11", daysOutstanding: 10, chargeDue: false })])) : undefined;
    const { container } = renderAt("/fulfillment/returnable-aging");

    const table = await screen.findByRole("table", { name: "Returnable packaging aging" });
    const pallet = (await within(table).findByText("PALLET")).closest("tr") as HTMLElement;
    const cells = Array.from(pallet.querySelectorAll("td, th")).map((cell) => cell.textContent);
    expect(cells).toEqual(["0195f0a0", "PALLET", "6", "2026-07-13", "100", "Charge due", "2026-10-11 08:00 UTC", "2026-10-15 09:30 UTC"]);
    expect(within(within(table).getByText("CRATE").closest("tr") as HTMLElement).getByText("Not due")).toBeInTheDocument();
    const tiles = screen.getByRole("list", { name: "Returnable packaging aging totals" });
    expect(within(tiles).getByText("Outstanding balances", { exact: false }).closest("li")?.textContent).toContain("2");
    expect(within(tiles).getByText("Charge due", { exact: false }).closest("li")?.textContent).toContain("Out more than 90 days");
    const params = last(AGING);
    expect(params.get("chargeDays")).toBe("90");
    expect(params.get("limit")).toBe(String(AGING_LIMIT));
    expect(params.has("chargeDue")).toBe(false);
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends the customer, charge days and charge-due filter, then shows the filtered empty state", async () => {
    server.handler = (url) => (url.pathname === AGING ? json(200, aging([], { chargeDays: Number(url.searchParams.get("chargeDays")) })) : undefined);
    renderAt("/fulfillment/returnable-aging");
    expect(await screen.findByText("No returnable packaging outstanding")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Customer id", { exact: false }), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Charge after days"), { target: { value: "120" } });
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "due" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));

    expect(await screen.findByText("No balances match the filter")).toBeInTheDocument();
    const params = last(AGING);
    expect(params.get("customerId")).toBe(CUSTOMER);
    expect(params.get("chargeDays")).toBe("120");
    expect(params.get("chargeDue")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "Clear filter" }));
    await waitFor(() => expect(last(AGING).get("chargeDays")).toBe("90"));
    expect(last(AGING).has("customerId")).toBe(false);
  });

  it("validates the filter before calling the service", async () => {
    server.handler = (url) => (url.pathname === AGING ? json(200, aging([])) : undefined);
    renderAt("/fulfillment/returnable-aging");
    await screen.findByText("No returnable packaging outstanding");
    const before = calls(AGING).length;

    fireEvent.change(screen.getByLabelText("Customer id", { exact: false }), { target: { value: "C1" } });
    fireEvent.change(screen.getByLabelText("Charge after days"), { target: { value: "29" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));

    expect(await screen.findByText("Enter the customer id (a UUID), or leave it empty.")).toBeInTheDocument();
    expect(screen.getByText("Enter a whole number of days from 30 to 365.")).toBeInTheDocument();
    expect(calls(AGING)).toHaveLength(before);
  });

  it("says when the list is cut and the counts cover more", async () => {
    server.handler = (url) => (url.pathname === AGING ? json(200, aging([aged("PALLET")], { count: 700, chargeDueCount: 12 })) : undefined);
    renderAt("/fulfillment/returnable-aging");
    expect(await screen.findByText("Showing the 1 oldest of 700 balances; the counts cover all of them.")).toBeInTheDocument();
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    server.handler = (url) =>
      url.pathname !== AGING ? undefined : fail ? json(400, { code: "FUL-0050", detail: "chargeDays must be an integer from 30 to 365" }) : json(200, aging([aged("PALLET")]));
    renderAt("/fulfillment/returnable-aging");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Could not load the aging");
    expect(alert.textContent).toContain("FUL-0050");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("PALLET")).toBeInTheDocument();
  });
});

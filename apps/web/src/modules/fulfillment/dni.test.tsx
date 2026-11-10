// Delivered-not-invoiced report (FR-28.12, AC-28.8): the GL 1405 balance of a
// legal entity as of a date and the packing slips that make it up.
import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { DniReport } from "./api/client";
import { DNI_LINES } from "./DeliveredNotInvoicedPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0195f0a0-0000-7000-8000-000000000040";
const CUSTOMER = "0195f0a0-0000-7000-8000-000000000030";
const SLIP = "0195f0a0-0000-7000-8000-000000000005";
const PATH = "/api/v1/fulfillment/delivered-not-invoiced";

const report: DniReport = {
  legalEntityId: LE,
  customerId: null,
  asOf: "2026-10-21",
  account: "1405",
  totals: [{ currency: "USD", count: 2, amount: "250" }],
  lineCount: 2,
  lines: [
    { packingSlipId: SLIP, packingSlipNumber: "PS-0000003", shipmentId: SLIP, orderId: SLIP, customerId: CUSTOMER, currency: "USD", postingDate: "2026-10-18T00:00:00Z", amount: "125", daysOutstanding: 3 },
    { packingSlipId: LE, packingSlipNumber: "PS-0000004", shipmentId: SLIP, orderId: SLIP, customerId: CUSTOMER, currency: "USD", postingDate: "2026-10-21T00:00:00Z", amount: "125", daysOutstanding: 0 },
  ],
};

const calls = () => server.calls.filter((c) => c.url.pathname === PATH);

function ask(le: string, asOf = "2026-10-21", customer = "") {
  fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: le } });
  fireEvent.change(screen.getByLabelText("Customer", { exact: false }), { target: { value: customer } });
  fireEvent.change(screen.getByLabelText("As of"), { target: { value: asOf } });
  fireEvent.click(screen.getByRole("button", { name: "Show report" }));
}

describe("delivered not invoiced", () => {
  it("asks for a legal entity, then shows the 1405 total and the open slips", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, report) : undefined);
    const { container } = renderAt("/fulfillment/delivered-not-invoiced");
    expect(screen.getByText(/Choose a legal entity/)).toBeInTheDocument();
    expect(calls()).toHaveLength(0);

    ask(LE, "2026-10-21", CUSTOMER);
    const table = await findTable({ name: "Delivered not invoiced as of 2026-10-21" });
    const row = (await within(table).findByText("PS-0000003")).closest("tr");
    const cells = Array.from(row?.querySelectorAll("td, th") ?? []).map((cell) => cell.textContent);
    expect(cells).toEqual(["PS-0000003", "2026-10-18", "3", "Not available⋯Copy id", "Sales order", "125.00 USD"]);
    const totals = screen.getByRole("list", { name: "Delivered not invoiced totals" });
    expect(totals.textContent).toContain("Open on 1405 (USD)");
    expect(totals.textContent).toContain("250.00 USD");
    expect(totals.textContent).toContain("2 packing slips");

    const call = calls()[0];
    expect(call.url.searchParams.get("legalEntityId")).toBe(LE);
    expect(call.url.searchParams.get("customerId")).toBe(CUSTOMER);
    expect(call.url.searchParams.get("asOf")).toBe("2026-10-21");
    expect(call.url.searchParams.get("limit")).toBe(String(DNI_LINES));
    expect(pageViolations(container)).toEqual([]);
  });

  it("opens a slip on its packing slip page", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, report) : json(503, { code: "COM-0500", detail: "not faked" }));
    renderAt("/fulfillment/delivered-not-invoiced");
    ask(LE);
    fireEvent.click(await screen.findByRole("button", { name: "PS-0000003" }));
    expect(await screen.findByRole("heading", { name: /Packing slip/ })).toBeInTheDocument();
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === `/api/v1/fulfillment/packing-slips/${SLIP}`)).toBe(true));
  });

  it("validates the query before calling the service", async () => {
    renderAt("/fulfillment/delivered-not-invoiced");
    ask("not-a-uuid", "2026-10-21", "nope");
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.queryByText("Enter the customer id (a UUID), or leave it empty.")).toBeNull();
    expect(calls()).toHaveLength(0);
  });

  it("shows the empty state and says when the list is cut", async () => {
    let body: DniReport = { ...report, totals: [], lineCount: 0, lines: [] };
    server.handler = (url) => (url.pathname === PATH ? json(200, body) : undefined);
    renderAt("/fulfillment/delivered-not-invoiced");
    ask(LE);
    expect(await screen.findByText("Nothing delivered and not invoiced")).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Delivered not invoiced totals" })).toBeNull();

    body = { ...report, lineCount: 900 };
    ask(LE, "2026-10-20");
    expect(await screen.findByText(/Showing the 2 oldest of 900 packing slips/)).toBeInTheDocument();
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    server.handler = (url) => (url.pathname !== PATH ? undefined : fail ? json(400, { code: "FUL-0050", detail: "request failed validation" }) : json(200, report));
    renderAt("/fulfillment/delivered-not-invoiced");
    ask(LE);
    expect((await findAlert()).textContent).toContain("FUL-0050");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("PS-0000003")).toBeInTheDocument();
  });
});

// SSCC register (section 5.28.14, BR-28.5): every issued SSCC with its container,
// shipment and no-reuse date, filtered and paged by cursor.
import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SsccPage, SsccRow } from "./api/client";
import { SSCC_PAGE } from "./SsccRegisterPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const PATH = "/api/v1/fulfillment/sscc-register";
const SHIPMENT = "0195f0a0-0000-7000-8000-000000000001";
const CUSTOMER = "0195f0a0-0000-7000-8000-000000000030";

function row(sscc: string, status: SsccRow["status"] = "SHIPPED"): SsccRow {
  return {
    sscc,
    containerId: "0195f0a0-0000-7000-8000-000000000010",
    containerType: "CARTON-C12",
    status,
    shipmentId: SHIPMENT,
    shipmentNumber: "SH-0000007",
    orderId: "0195f0a0-0000-7000-8000-000000000020",
    customerId: CUSTOMER,
    packingSlipId: status === "SHIPPED" ? "0195f0a0-0000-7000-8000-000000000005" : null,
    issuedAt: "2026-10-21T14:05:00Z",
    noReuseUntil: "2027-10-21",
  };
}

const calls = () => server.calls.filter((c) => c.url.pathname === PATH);

describe("SSCC register", () => {
  it("lists the register on open with status and the no-reuse date", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, { items: [row("006141410123456789"), row("006141410123456796", "CLOSED"), row("006141410123456802", "UNPACKED")] } satisfies SsccPage) : undefined);
    const { container } = renderAt("/fulfillment/sscc-register");
    expect(screen.getByRole("table", { name: "SSCC register" })).toHaveAttribute("aria-busy", "true");

    const table = await findTable({ name: "SSCC register" });
    const first = (await within(table).findByText("006141410123456789")).closest("tr");
    const cells = Array.from(first?.querySelectorAll("td, th") ?? []).map((cell) => cell.textContent);
    expect(cells).toEqual(["006141410123456789", "Shipped", "CARTON-C12", "SH-0000007", "Not available⋯Copy id", "2026-10-21 14:05 UTC", "2027-10-21"]);
    expect(within(table).getByText("Closed")).toBeInTheDocument();
    expect(within(table).getByText("Unpacked")).toBeInTheDocument();
    expect(within(first as HTMLElement).getByRole("link", { name: "SH-0000007" })).toHaveAttribute("href", `/fulfillment/shipments/${SHIPMENT}`);
    expect(calls()[0].url.searchParams.get("limit")).toBe(String(SSCC_PAGE));
    expect(calls()[0].url.searchParams.has("cursor")).toBe(false);
    expect(screen.queryByRole("button", { name: "Next page" })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("sends the filter and shows the filtered empty state", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/sscc-register");
    expect(await screen.findByText("No SSCCs issued yet")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("GS1 company prefix", { exact: false }), { target: { value: "0614141" } });
    fireEvent.change(screen.getByLabelText("Customer", { exact: false }), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Issued from", { exact: false }), { target: { value: "2026-10-01" } });
    fireEvent.change(screen.getByLabelText("Issued to", { exact: false }), { target: { value: "2026-10-31" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));

    expect(await screen.findByText("No SSCCs match the filter")).toBeInTheDocument();
    const params = calls()[calls().length - 1].url.searchParams;
    expect(params.get("companyPrefix")).toBe("0614141");
    expect(params.get("customerId")).toBe(CUSTOMER);
    expect(params.get("from")).toBe("2026-10-01");
    expect(params.get("to")).toBe("2026-10-31");

    fireEvent.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(await screen.findByText("No SSCCs issued yet")).toBeInTheDocument();
    expect(calls()[calls().length - 1].url.searchParams.has("companyPrefix")).toBe(false);
  });

  it("validates the filter before calling the service", async () => {
    server.handler = (url) => (url.pathname === PATH ? json(200, { items: [] }) : undefined);
    renderAt("/fulfillment/sscc-register");
    await screen.findByText("No SSCCs issued yet");
    const before = calls().length;

    fireEvent.change(screen.getByLabelText("GS1 company prefix", { exact: false }), { target: { value: "12345" } });
    fireEvent.change(screen.getByLabelText("Customer", { exact: false }), { target: { value: "nope" } });
    fireEvent.change(screen.getByLabelText("Issued from", { exact: false }), { target: { value: "2026-10-31" } });
    fireEvent.change(screen.getByLabelText("Issued to", { exact: false }), { target: { value: "2026-10-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filter" }));

    expect(await screen.findByText("Enter a GS1 company prefix of 6 to 12 digits.")).toBeInTheDocument();
    expect(screen.queryByText("Enter the customer id (a UUID), or leave it empty.")).toBeNull();
    expect(screen.getByText("The last date cannot be before the first.")).toBeInTheDocument();
    expect(calls()).toHaveLength(before);
  });

  it("pages forward with the cursor and back again", async () => {
    server.handler = (url) => {
      if (url.pathname !== PATH) return undefined;
      return url.searchParams.get("cursor") === "006141410123456789"
        ? json(200, { items: [row("006141410123456796")] })
        : json(200, { items: [row("006141410123456789")], nextCursor: "006141410123456789" });
    };
    renderAt("/fulfillment/sscc-register");
    await screen.findByText("006141410123456789");
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(await screen.findByText("006141410123456796")).toBeInTheDocument();
    expect(calls()[calls().length - 1].url.searchParams.get("cursor")).toBe("006141410123456789");
    expect(screen.getByRole("button", { name: "Next page" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Previous page" }));
    expect(await screen.findByText("006141410123456789")).toBeInTheDocument();
    await waitFor(() => expect(calls()[calls().length - 1].url.searchParams.has("cursor")).toBe(false));
  });

  it("shows a load error with a retry", async () => {
    let fail = true;
    server.handler = (url) =>
      url.pathname !== PATH ? undefined : fail ? json(403, { code: "COM-0403", detail: "missing duty fulfillment.warehouse.view" }) : json(200, { items: [row("006141410123456789")] });
    renderAt("/fulfillment/sscc-register");
    const alert = await findAlert();
    expect(alert.textContent).toContain("Could not load the SSCC register");
    expect(alert.textContent).toContain("COM-0403");
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("006141410123456789")).toBeInTheDocument();
  });
});

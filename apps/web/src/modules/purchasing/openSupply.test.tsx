import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { validateOpenSupply } from "./OpenSupplyPage";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const WH = "99999999-9999-4999-8999-999999999999";
const PO_ID = "55555555-5555-4555-8555-555555555555";

afterEach(() => vi.unstubAllGlobals());

const row = (n: number, due: string) => ({
  purchaseOrderId: PO_ID,
  number: "PO-00000007",
  lineNo: n,
  legalEntityId: "11111111-1111-4111-8111-111111111111",
  vendorId: "22222222-2222-4222-8222-222222222222",
  description: `Bolt ${n}`,
  warehouseId: WH,
  unit: "EA",
  qty: "10",
  receivedQty: "4",
  openQty: "6",
  deliveryDate: due,
  confirmedDate: n === 1 ? "2027-03-02" : undefined,
});

describe("open supply", () => {
  it("lists open order lines with their warehouse and filters by warehouse and dates", async () => {
    const fetchMock = stubFetch(() => json({ items: [row(1, "2027-03-01"), row(2, "2027-03-05")] }));
    const { container } = renderAt("/purchasing/open-supply");
    const table = await findTable({ name: "Open order lines" });
    await waitFor(() => expect(within(table).getAllByRole("link", { name: "PO-00000007" })).toHaveLength(2));
    expect(within(table).getAllByText("99999999")).toHaveLength(2);
    expect(within(table).getAllByText("6 EA")).toHaveLength(2);
    expect(within(table).getByText("2027-03-02")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Warehouse id/), { target: { value: WH } });
    fireEvent.change(screen.getByLabelText(/Due from/), { target: { value: "2027-03-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Show supply" }));
    await waitFor(() =>
      expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe(`/api/v1/procurement/purchase-order-lines?warehouseId=${WH}&dueFrom=2027-03-01`),
    );
  });

  it("checks the filters before calling the service", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/open-supply");
    await screen.findByRole("heading", { name: "No open supply" });
    const calls = fetchMock.mock.calls.length;
    fireEvent.change(screen.getByLabelText(/Warehouse id/), { target: { value: "nope" } });
    fireEvent.click(screen.getByRole("button", { name: "Show supply" }));
    expect(await screen.findByText("Warehouse id must be a UUID.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.length).toBe(calls);
  });

  it("validates ids and the date range", () => {
    expect(validateOpenSupply({ warehouseId: WH, siteId: "", productId: "", dueFrom: "2027-03-01", dueTo: "2027-03-31" })).toEqual({});
    expect(validateOpenSupply({ warehouseId: "", siteId: "x", productId: "y", dueFrom: "", dueTo: "" })).toEqual({
      siteId: "Site id must be a UUID.",
      productId: "Product id must be a UUID.",
    });
    expect(validateOpenSupply({ warehouseId: "", siteId: "", productId: "", dueFrom: "2027-03-05", dueTo: "2027-03-01" }).dueTo).toBe(
      "The end date is before the start date.",
    );
  });

  it("explains a load error", async () => {
    stubFetch(() => problem(503, "COM-0503", "unavailable"));
    renderAt("/purchasing/open-supply");
    expect(await screen.findByText(/unavailable/)).toBeInTheDocument();
  });

  it("pages with the cursor", async () => {
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=") ? json({ items: [row(2, "2027-03-05")] }) : json({ items: [row(1, "2027-03-01")], nextCursor: "2027-03-01|p|1" }),
    );
    renderAt("/purchasing/open-supply");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("cursor=2027-03-01%7Cp%7C1"));
    const table = await findTable({ name: "Open order lines" });
    await waitFor(() => expect(within(table).getAllByRole("link", { name: "PO-00000007" })).toHaveLength(2));
  });
});

import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { validateInquiry } from "./InquiryPages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const VENDOR = "22222222-2222-4222-8222-222222222222";
const PRODUCT = "33333333-3333-4333-8333-333333333333";
const PO_ID = "55555555-5555-4555-8555-555555555555";

afterEach(() => vi.unstubAllGlobals());

const vendorRow = {
  vendorId: VENDOR,
  currency: "USD",
  orderCount: 2,
  lineCount: 3,
  openValue: "450",
  overdueLines: 1,
  overdueValue: "100",
  earliestDue: "2027-01-02",
};

describe("purchasing inquiries", () => {
  it("shows backordered lines with received and open quantity, an empty state and an error", async () => {
    const fetchMock = stubFetch(() =>
      json({
        items: [
          {
            purchaseOrderId: PO_ID,
            number: "PO-00000009",
            lineNo: 2,
            legalEntityId: "11111111-1111-4111-8111-111111111111",
            vendorId: VENDOR,
            description: "Washer",
            unit: "EA",
            qty: "10",
            receivedQty: "4",
            openQty: "6",
            deliveryDate: "2027-01-02",
            dueDate: "2027-01-02",
            currency: "USD",
            daysLate: 0,
            openValue: "60",
          },
        ],
      }),
    );
    renderAt("/purchasing/inquiries/backordered");
    const table = await findTable({ name: "Backordered order lines" });
    expect(await within(table).findByRole("link", { name: "PO-00000009" })).toBeInTheDocument();
    expect(within(table).getByText("4 EA")).toBeInTheDocument();
    expect(within(table).getByText("6 EA")).toBeInTheDocument();
    expect(within(table).getByText("60 USD")).toBeInTheDocument();
    const inquiryNav = screen.getByRole("navigation", { name: "Inquiries" });
    expect(within(inquiryNav).getByRole("link", { name: "Backordered lines" })).toHaveAttribute("aria-current", "page");
    expect(within(inquiryNav).getByRole("link", { name: "Open orders by vendor" })).not.toHaveAttribute("aria-current");
    fireEvent.change(screen.getByLabelText(/Product id/), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() =>
      expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe(`/api/v1/procurement/inquiries/backordered-lines?productId=${PRODUCT}`),
    );
    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inquiries/backordered");
    expect(await screen.findByRole("heading", { name: "Nothing backordered" })).toBeInTheDocument();
    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "PRC-INTERNAL", "boom"));
    renderAt("/purchasing/inquiries/backordered");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("lists open orders by vendor and filters by vendor and as-of date", async () => {
    const fetchMock = stubFetch(() => json({ items: [vendorRow] }));
    const { container } = renderAt("/purchasing/inquiries");
    const table = await findTable({ name: "Open orders by vendor" });
    expect(await within(table).findByText("450 USD")).toBeInTheDocument();
    expect(within(table).getByText("100 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Vendor id/), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText(/As of/), { target: { value: "2027-02-01" } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() =>
      expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe(`/api/v1/procurement/inquiries/open-orders-by-vendor?vendorId=${VENDOR}&asOf=2027-02-01`),
    );
  });

  it("shows overdue lines with days late and an empty state", async () => {
    stubFetch(() =>
      json({
        items: [
          {
            purchaseOrderId: PO_ID,
            number: "PO-00000007",
            lineNo: 1,
            legalEntityId: "11111111-1111-4111-8111-111111111111",
            vendorId: VENDOR,
            description: "Bolt",
            unit: "EA",
            qty: "10",
            receivedQty: "4",
            openQty: "6",
            deliveryDate: "2026-12-20",
            confirmedDate: "2027-01-02",
            dueDate: "2027-01-02",
            currency: "USD",
            daysLate: 30,
            openValue: "60",
          },
        ],
      }),
    );
    renderAt("/purchasing/inquiries/overdue");
    const table = await findTable({ name: "Overdue order lines" });
    expect(await within(table).findByRole("link", { name: "PO-00000007" })).toBeInTheDocument();
    expect(within(table).getByText("30")).toBeInTheDocument();
    // Due is the vendor-confirmed date; the requested date is shown beside it.
    expect(within(table).getAllByText("2027-01-02")).toHaveLength(2);
    expect(within(table).getByText("2026-12-20")).toBeInTheDocument();
    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inquiries/overdue");
    expect(await screen.findByRole("heading", { name: "Nothing overdue" })).toBeInTheDocument();
  });

  it("price history waits for a product, then lists prices", async () => {
    const fetchMock = stubFetch(() =>
      json({
        items: [
          {
            purchaseOrderId: PO_ID,
            number: "PO-00000007",
            lineNo: 1,
            orderDate: "2027-01-01",
            vendorId: VENDOR,
            currency: "USD",
            unit: "EA",
            qty: "10",
            unitPrice: "9.5",
            priceUnit: "1",
            discountPct: "0",
            priceSource: "TRADE_AGREEMENT",
          },
        ],
      }),
    );
    renderAt("/purchasing/inquiries/price-history");
    await screen.findByRole("heading", { name: "No price history" });
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Product id is required.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText(/Product id/), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    const table = await findTable({ name: "Order price history" });
    expect(await within(table).findByText("9.5 USD per 1")).toBeInTheDocument();
    expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe(`/api/v1/procurement/inquiries/price-history?productId=${PRODUCT}`);
  });

  it("explains a load error and pages with the cursor", async () => {
    stubFetch(() => problem(503, "COM-0503", "unavailable"));
    renderAt("/purchasing/inquiries");
    expect(await screen.findByText(/unavailable \(COM-0503\)/)).toBeInTheDocument();
    vi.unstubAllGlobals();
    const fetchMock = stubFetch((url) =>
      url.includes("cursor=") ? json({ items: [{ ...vendorRow, currency: "EUR" }] }) : json({ items: [vendorRow], nextCursor: `${VENDOR}|USD` }),
    );
    const second = renderAt("/purchasing/inquiries").container;
    fireEvent.click(await within(second).findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain(`cursor=${VENDOR}%7CUSD`));
    // The next page is added under the first, not in its place.
    const table = within(second).getByRole("table", { name: "Open orders by vendor" });
    expect(await within(table).findByText("450 EUR")).toBeInTheDocument();
    expect(within(table).getByText("450 USD")).toBeInTheDocument();
    await waitFor(() => expect(within(second).queryByRole("button", { name: "Load more" })).not.toBeInTheDocument());
  });

  it("validates ids, dates and the range", () => {
    const base = { vendorId: "", productId: "", asOf: "", from: "", to: "" };
    expect(validateInquiry({ ...base, vendorId: VENDOR, from: "2027-01-01", to: "2027-02-01" })).toEqual({});
    expect(validateInquiry({ ...base, vendorId: "x", productId: "y" })).toEqual({
      vendorId: "Vendor id must be a UUID.",
      productId: "Product id must be a UUID.",
    });
    expect(validateInquiry(base, true).productId).toBe("Product id is required.");
    expect(validateInquiry({ ...base, from: "2027-02-01", to: "2027-01-01" }).to).toBe("The end date is before the start date.");
  });
});

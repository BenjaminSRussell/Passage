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

  it("lists expiring agreements with lapsed ones marked, filters by days ahead, and shows empty and error states", async () => {
    const AGREEMENT = "66666666-6666-4666-8666-666666666666";
    const row = {
      id: AGREEMENT,
      number: "PA-00000004",
      legalEntityId: "11111111-1111-4111-8111-111111111111",
      vendorId: VENDOR,
      currency: "USD",
      status: "EFFECTIVE",
      validTo: "2027-02-20",
      daysLeft: -9,
      lapsed: true,
      lineCount: 2,
      qtyLines: 1,
      committedAmount: "500",
      releasedAmount: "125.5",
    };
    const fetchMock = stubFetch(() => json({ items: [row, { ...row, id: PO_ID, number: "PA-00000005", daysLeft: 14, lapsed: false, status: "ON_HOLD" }] }));
    renderAt("/purchasing/inquiries/expiring-agreements");
    const table = await findTable({ name: "Expiring purchase agreements" });
    expect(await within(table).findByRole("link", { name: "PA-00000004" })).toHaveAttribute("href", `/purchasing/agreements/${AGREEMENT}`);
    expect(within(table).getByText("Lapsed 9 days ago")).toBeInTheDocument();
    expect(within(table).getByText("14")).toBeInTheDocument();
    expect(within(table).getAllByText("125.5 USD")).toHaveLength(2);
    const inquiryNav = screen.getByRole("navigation", { name: "Inquiries" });
    expect(within(inquiryNav).getByRole("link", { name: "Expiring agreements" })).toHaveAttribute("aria-current", "page");
    fireEvent.change(screen.getByLabelText(/Days ahead/), { target: { value: "400" } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Days ahead must be a whole number from 0 to 366.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Days ahead/), { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/inquiries/expiring-agreements?withinDays=90"));
    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inquiries/expiring-agreements");
    expect(await screen.findByRole("heading", { name: "No agreement is about to end" })).toBeInTheDocument();
    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "PRC-INTERNAL", "boom"));
    renderAt("/purchasing/inquiries/expiring-agreements");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("lists agreement lines near their limit with overruns marked, filters by threshold, and shows empty and error states", async () => {
    const AGREEMENT = "66666666-6666-4666-8666-666666666666";
    const row = {
      agreementId: AGREEMENT,
      number: "PA-00000006",
      legalEntityId: "11111111-1111-4111-8111-111111111111",
      vendorId: VENDOR,
      currency: "USD",
      lineId: "77777777-7777-4777-8777-777777777777",
      lineNo: 2,
      commitmentType: "TOTAL_VALUE",
      maxEnforced: "SOFT",
      committed: "500",
      released: "520",
      remaining: "-20",
      pctReleased: "104",
      overrun: true,
    };
    const qtyRow = { ...row, lineId: "88888888-8888-4888-8888-888888888888", lineNo: 1, commitmentType: "QUANTITY", productId: PRODUCT, unit: "EA", maxEnforced: "HARD", committed: "1000", released: "900", remaining: "100", pctReleased: "90", overrun: false };
    const fetchMock = stubFetch(() => json({ items: [row, qtyRow] }));
    renderAt("/purchasing/inquiries/near-limit-agreements");
    const table = await findTable({ name: "Agreement lines near their limit" });
    expect(await within(table).findAllByRole("link", { name: "PA-00000006" })).toHaveLength(2);
    expect(within(table).getAllByRole("link", { name: "PA-00000006" })[0]).toHaveAttribute("href", `/purchasing/agreements/${AGREEMENT}`);
    expect(within(table).getByText("104 % (over)")).toBeInTheDocument();
    expect(within(table).getByText("-20 USD")).toBeInTheDocument();
    expect(within(table).getByText("900 EA")).toBeInTheDocument();
    expect(within(table).getByText("90 %")).toBeInTheDocument();
    const inquiryNav = screen.getByRole("navigation", { name: "Inquiries" });
    expect(within(inquiryNav).getByRole("link", { name: "Agreements near limit" })).toHaveAttribute("aria-current", "page");
    fireEvent.change(screen.getByLabelText(/Released at least/), { target: { value: "40" } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(await screen.findByText("Released at least must be a whole number from 50 to 100.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText(/Released at least/), { target: { value: "95" } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/inquiries/near-limit-agreements?thresholdPct=95"));
    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inquiries/near-limit-agreements");
    expect(await screen.findByRole("heading", { name: "No agreement line is near its limit" })).toBeInTheDocument();
    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "PRC-INTERNAL", "boom"));
    renderAt("/purchasing/inquiries/near-limit-agreements");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });

  it("lists change orders with both totals, filters by vendor, and shows empty and error states", async () => {
    const row = {
      purchaseOrderId: PO_ID,
      number: "PO-00000011",
      legalEntityId: "11111111-1111-4111-8111-111111111111",
      vendorId: VENDOR,
      currency: "USD",
      confirmedVersion: 1,
      pendingVersion: 2,
      changeCount: 2,
      confirmedTotal: "1000",
      proposedTotal: "1100",
      changedAt: "2027-01-07T09:30:15.123456Z",
    };
    const fetchMock = stubFetch(() => json({ items: [row] }));
    renderAt("/purchasing/inquiries/change-orders");
    const table = await findTable({ name: "Purchase order changes awaiting re-approval" });
    expect(await within(table).findByRole("link", { name: "PO-00000011" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
    expect(within(table).getByText("1 to 2")).toBeInTheDocument();
    expect(within(table).getByText("1000 USD")).toBeInTheDocument();
    expect(within(table).getByText("1100 USD")).toBeInTheDocument();
    expect(within(table).getByText("2027-01-07 09:30 UTC")).toBeInTheDocument();
    expect(within(screen.getByRole("navigation", { name: "Inquiries" })).getByRole("link", { name: "Change orders" })).toHaveAttribute("aria-current", "page");
    fireEvent.change(screen.getByLabelText(/Vendor id/), { target: { value: VENDOR } });
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe(`/api/v1/procurement/inquiries/change-orders?vendorId=${VENDOR}`));
    vi.unstubAllGlobals();
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inquiries/change-orders");
    expect(await screen.findByRole("heading", { name: "No change is waiting" })).toBeInTheDocument();
    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "PRC-INTERNAL", "boom"));
    renderAt("/purchasing/inquiries/change-orders");
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
    const base = { vendorId: "", productId: "", asOf: "", from: "", to: "", withinDays: "", thresholdPct: "" };
    expect(validateInquiry({ ...base, vendorId: VENDOR, from: "2027-01-01", to: "2027-02-01" })).toEqual({});
    expect(validateInquiry({ ...base, vendorId: "x", productId: "y" })).toEqual({
      vendorId: "Vendor id must be a UUID.",
      productId: "Product id must be a UUID.",
    });
    expect(validateInquiry(base, true).productId).toBe("Product id is required.");
    expect(validateInquiry({ ...base, from: "2027-02-01", to: "2027-01-01" }).to).toBe("The end date is before the start date.");
    expect(validateInquiry({ ...base, withinDays: "0" })).toEqual({});
    expect(validateInquiry({ ...base, withinDays: " 366 " })).toEqual({});
    expect(validateInquiry({ ...base, thresholdPct: "50" })).toEqual({});
    expect(validateInquiry({ ...base, thresholdPct: " 100 " })).toEqual({});
    for (const bad of ["49", "101", "90.5", "x"]) expect(validateInquiry({ ...base, thresholdPct: bad }).thresholdPct).toBe("Released at least must be a whole number from 50 to 100.");
    for (const bad of ["367", "-1", "1.5", "x"]) expect(validateInquiry({ ...base, withinDays: bad }).withinDays).toBe("Days ahead must be a whole number from 0 to 366.");
  });
});

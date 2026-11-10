import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { agreementActions } from "./AgreementPages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PRODUCT = "33333333-3333-4333-8333-333333333333";
const PA_ID = "99999999-9999-4999-8999-999999999999";
const LINE_ID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const PO_ID = "55555555-5555-4555-8555-555555555555";

afterEach(() => vi.unstubAllGlobals());

const agreement = {
  id: PA_ID,
  number: "PA-00000004",
  legalEntityId: LE,
  vendorId: VENDOR,
  currency: "USD",
  status: "DRAFT",
  validFrom: "2027-01-01",
  validTo: "2027-12-31",
  version: 1,
  lines: [
    {
      id: LINE_ID,
      lineNo: 1,
      commitmentType: "QUANTITY",
      productId: PRODUCT,
      unit: "EA",
      committedQty: "1000",
      committedAmount: "0",
      unitPrice: "2.5",
      discountPct: "0",
      maxEnforced: "HARD",
      priceOverride: true,
      releasedQty: "950",
      releasedAmount: "2375",
      pctReleased: "95",
      remaining: "50",
    },
    {
      id: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
      lineNo: 2,
      commitmentType: "TOTAL_VALUE",
      committedQty: "0",
      committedAmount: "5000",
      unitPrice: "0",
      discountPct: "0",
      maxEnforced: "SOFT",
      priceOverride: false,
      releasedQty: "0",
      releasedAmount: "0",
      pctReleased: "0",
      remaining: "5000",
    },
  ],
  releases: [
    {
      agreementLineId: LINE_ID,
      purchaseOrderId: PO_ID,
      purchaseOrderNumber: "PO-00000009",
      lineNo: 1,
      qty: "950",
      unit: "EA",
      lineAmount: "2375",
      deliveryDate: "2027-03-01",
      approvalStatus: "DRAFT",
      createdAt: "2026-10-26T10:00:00Z",
    },
  ],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

describe("purchase agreement list", () => {
  it("shows a loading table, then rows with the release level that open the detail page", async () => {
    let release: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((resolve) => (release = resolve)));
    const { router } = renderAt("/purchasing/agreements");
    expect(await screen.findByRole("table", { name: "Purchase agreements" })).toHaveAttribute("aria-busy", "true");
    release(
      json({
        items: [
          { id: PA_ID, number: "PA-00000004", legalEntityId: LE, vendorId: VENDOR, currency: "USD", validFrom: "2027-01-01", status: "EFFECTIVE", lineCount: 2, maxPctReleased: "95", version: 2, createdAt: "2026-10-26T10:00:00Z" },
        ],
      }),
    );
    const table = await findTable({ name: "Purchase agreements" });
    await waitFor(() => expect(within(table).getByText("PA-00000004")).toBeInTheDocument());
    expect(within(table).getByText("Effective")).toBeInTheDocument();
    expect(within(table).getByText("95 %")).toBeInTheDocument();
    expect(within(table).getByText("From 2027-01-01, open-ended")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "PA-00000004" }));
    expect(router.state.location.pathname).toBe(`/purchasing/agreements/${PA_ID}`);
  });

  it("explains an empty list and filters by status", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/agreements");
    expect(await screen.findByRole("heading", { name: "No purchase agreements yet" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "ON_HOLD" } });
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/agreements?status=ON_HOLD"));
  });

  it("shows the problem code on error and retries", async () => {
    const fetchMock = stubFetch(() => problem(500, "COM-0500", "internal error"));
    renderAt("/purchasing/agreements");
    expect(await screen.findByText(/internal error \(COM-0500\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});

describe("new purchase agreement", () => {
  it("validates before calling the service", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/agreements/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save agreement" }));
    expect(await screen.findByText("Enter the vendor id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the product id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a positive quantity.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("asks only for what the commitment type needs", async () => {
    stubFetch(() => json({}));
    renderAt("/purchasing/agreements/new");
    fireEvent.change(await screen.findByLabelText("Commitment"), { target: { value: "CATEGORY_VALUE" } });
    expect(screen.getByLabelText("Category")).toBeInTheDocument();
    expect(screen.getByLabelText("Committed amount")).toBeInTheDocument();
    expect(screen.queryByLabelText("Product")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Unit")).not.toBeInTheDocument();
    expect(screen.queryByText(/instead of trade agreements/)).not.toBeInTheDocument();
  });

  it("posts the commitments with an Idempotency-Key and opens the new agreement", async () => {
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(agreement, 201, { ETag: '"1"' }) : json(agreement)));
    const { router } = renderAt("/purchasing/agreements/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Valid to"), { target: { value: "2027-12-31" } });
    fireEvent.change(screen.getByLabelText("Start as"), { target: { value: "DRAFT" } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Committed quantity"), { target: { value: "1000" } });
    fireEvent.change(screen.getByLabelText("Unit price"), { target: { value: "2.50" } });
    fireEvent.click(screen.getByLabelText("Use this price instead of trade agreements on releases"));
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    const line2 = screen.getByRole("group", { name: "Line 2" });
    fireEvent.change(within(line2).getByLabelText("Commitment"), { target: { value: "TOTAL_VALUE" } });
    fireEvent.change(within(line2).getByLabelText("Committed amount"), { target: { value: "5000" } });
    fireEvent.change(within(line2).getByLabelText("Limit"), { target: { value: "SOFT" } });
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));

    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/agreements/${PA_ID}`));
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/agreements");
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toMatch(/[0-9a-f-]{36}/);
    const body = JSON.parse(String(init.body));
    expect(body).toMatchObject({ legalEntityId: LE, vendorId: VENDOR, currency: "USD", status: "DRAFT", validTo: "2027-12-31" });
    expect(body.lines[0]).toEqual({
      commitmentType: "QUANTITY",
      productId: PRODUCT,
      unit: "EA",
      committedQty: "1000",
      unitPrice: "2.50",
      maxEnforced: "HARD",
      priceOverride: true,
    });
    expect(body.lines[1]).toEqual({ commitmentType: "TOTAL_VALUE", committedAmount: "5000", maxEnforced: "SOFT" });
  });

  it("maps a service field error back onto the form", async () => {
    stubFetch(() => problem(422, "COM-0001", "lines[0].unit is required", { "lines[0].unit": "is required for a QUANTITY commitment" }));
    renderAt("/purchasing/agreements/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Vendor"), { target: { value: VENDOR } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Committed quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByText("is required for a QUANTITY commitment")).toBeInTheDocument();
    expect(screen.getAllByRole("alert").some((a) => a.textContent?.includes("COM-0001"))).toBe(true);
  });
});

describe("purchase agreement detail", () => {
  it("shows commitments with their release position and the releases", async () => {
    stubFetch(() => json(agreement, 200, { ETag: '"1"' }));
    const { container } = renderAt(`/purchasing/agreements/${PA_ID}`);
    expect(await screen.findByRole("heading", { name: "Purchase agreement PA-00000004" })).toBeInTheDocument();
    const lines = screen.getByRole("table", { name: "Agreement lines" });
    const first = within(lines).getAllByRole("row")[1];
    expect(first).toHaveTextContent("1000 EA");
    expect(first).toHaveTextContent("950 EA");
    expect(first).toHaveTextContent("50 EA");
    expect(within(first).getByText("95 %")).toBeInTheDocument();
    expect(first).toHaveTextContent("2.5 USD (overrides)");
    expect(within(lines).getAllByRole("row")[2]).toHaveTextContent("5000 USD");
    const releases = screen.getByRole("table", { name: "Releases" });
    expect(within(releases).getByRole("link", { name: "PO-00000009" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
    expect(pageViolations(container)).toEqual([]);
  });

  it("activates with If-Match and an Idempotency-Key and shows the new status", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST" ? json({ ...agreement, status: "EFFECTIVE", version: 2 }) : json(agreement, 200, { ETag: '"1"' }),
    );
    renderAt(`/purchasing/agreements/${PA_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    expect(await screen.findByText("The agreement is now effective.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/agreements/${PA_ID}:activate`);
    const headers = init.headers as Record<string, string>;
    expect(headers["If-Match"]).toBe('"1"');
    expect(headers["Idempotency-Key"]).toBeTruthy();
    expect(screen.getByRole("button", { name: "Put on hold" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Close" })).toBeInTheDocument();
  });

  it("shows the conflict code when someone else changed it", async () => {
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-VERSION-CONFLICT", "agreement is at version 2") : json(agreement)));
    renderAt(`/purchasing/agreements/${PA_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Activate" }));
    expect(await screen.findByText(/PRC-VERSION-CONFLICT/)).toBeInTheDocument();
  });

  it("explains a missing agreement", async () => {
    stubFetch(() => problem(404, "PRC-AGR-NOT-FOUND", "purchase agreement not found"));
    renderAt(`/purchasing/agreements/${PA_ID}`);
    expect(await screen.findByRole("heading", { name: "Purchase agreement not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to purchase agreements" })).toHaveAttribute("href", "/purchasing/agreements");
  });

  it("offers only the transitions the status allows", () => {
    expect(agreementActions("DRAFT").map((a) => a.action)).toEqual(["activate"]);
    expect(agreementActions("EFFECTIVE").map((a) => a.action)).toEqual(["hold", "close"]);
    expect(agreementActions("ON_HOLD").map((a) => a.action)).toEqual(["activate", "close"]);
    expect(agreementActions("CLOSED")).toEqual([]);
  });
});

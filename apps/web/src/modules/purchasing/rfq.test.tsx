import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { defaultDeadline, formatInstant, parseVendorIds, rfqActions, validateRFQ } from "./RfqPages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR_A = "22222222-2222-4222-8222-222222222222";
const VENDOR_B = "33333333-3333-4333-8333-333333333333";
const VENDOR_C = "44444444-4444-4444-8444-444444444444";
const PRODUCT = "55555555-5555-4555-8555-555555555555";
const RFQ_ID = "99999999-9999-4999-8999-999999999999";
const LINE_1 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const LINE_2 = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb";
const REPLY_A = "a1a1a1a1-a1a1-4a1a-8a1a-a1a1a1a1a1a1";
const REPLY_B = "b1b1b1b1-b1b1-4b1b-8b1b-b1b1b1b1b1b1";
const REPLY_C = "c1c1c1c1-c1c1-4c1c-8c1c-c1c1c1c1c1c1";
const PO_ID = "66666666-6666-4666-8666-666666666666";

afterEach(() => vi.unstubAllGlobals());

const lines = [
  { id: LINE_1, lineNo: 1, productId: PRODUCT, qty: "10", unit: "EA" },
  { id: LINE_2, lineNo: 2, description: "Calibration", qty: "4", unit: "HR", requiredDate: "2026-12-01" },
];

const draft = {
  id: RFQ_ID,
  number: "RFQ-00000007",
  legalEntityId: LE,
  status: "DRAFT",
  replyDeadline: "2026-11-01T17:00:00Z",
  rankingRule: "LOWEST_PRICE",
  version: 1,
  lines,
  invitations: [],
  replies: [],
  createdAt: "2026-10-26T10:00:00Z",
  updatedAt: "2026-10-26T10:00:00Z",
};

const reply = (id: string, vendorId: string, unitPrice: string, leadTimeDays: number, late = false) => ({
  id,
  lineId: LINE_1,
  vendorId,
  unitPrice,
  priceUnit: "1",
  currency: "USD",
  leadTimeDays,
  receivedAt: late ? "2026-11-01T18:00:00Z" : "2026-10-30T09:00:00Z",
  late,
  awarded: false,
});

const open = {
  ...draft,
  status: "REPLIES_OPEN",
  version: 4,
  invitations: [
    { vendorId: VENDOR_A, status: "REPLIED", sentAt: "2026-10-26T11:00:00Z" },
    { vendorId: VENDOR_B, status: "REPLIED", sentAt: "2026-10-26T11:00:00Z" },
    { vendorId: VENDOR_C, status: "REPLIED", sentAt: "2026-10-26T11:00:00Z" },
  ],
  replies: [reply(REPLY_A, VENDOR_A, "10.00", 5), reply(REPLY_B, VENDOR_B, "9.50", 12), reply(REPLY_C, VENDOR_C, "8.00", 3, true)],
};

const comparison = {
  rfqId: RFQ_ID,
  rankingRule: "LOWEST_PRICE",
  lines: [
    {
      lineId: LINE_1,
      lineNo: 1,
      productId: PRODUCT,
      qty: "10",
      unit: "EA",
      ranked: [
        { rank: 1, replyId: REPLY_B, vendorId: VENDOR_B, unitPrice: "9.5", currency: "USD", leadTimeDays: 12, total: "95" },
        { rank: 2, replyId: REPLY_A, vendorId: VENDOR_A, unitPrice: "10", currency: "USD", leadTimeDays: 5, total: "100" },
      ],
      excluded: [{ replyId: REPLY_C, vendorId: VENDOR_C, reason: "LATE" }],
    },
    { lineId: LINE_2, lineNo: 2, description: "Calibration", qty: "4", unit: "HR", ranked: [], excluded: [] },
  ],
};

const purchaseOrder = { id: PO_ID, number: "PO-00000042", vendorId: VENDOR_B, currency: "USD", total: "95" };

const body = (init: RequestInit) => JSON.parse(String(init.body));

describe("RFQ helpers", () => {
  it("formats instants in UTC whatever the offset", () => {
    expect(formatInstant("2026-11-01T13:00:00-04:00")).toBe("2026-11-01 17:00 UTC");
    expect(formatInstant("not a date")).toBe("not a date");
  });

  it("offers close and cancel only where the state machine allows them", () => {
    expect(rfqActions("DRAFT").map((a) => a.action)).toEqual(["cancel"]);
    expect(rfqActions("REPLIES_OPEN").map((a) => a.action)).toEqual(["close", "cancel"]);
    expect(rfqActions("AWARDED")).toEqual([]);
    expect(rfqActions("CANCELLED")).toEqual([]);
  });

  it("splits pasted vendor ids and defaults the deadline to a week out at 17:00", () => {
    expect(parseVendorIds(` ${VENDOR_A},\n${VENDOR_B}  `)).toEqual([VENDOR_A, VENDOR_B]);
    expect(defaultDeadline(new Date(2026, 9, 26, 9, 0))).toBe("2026-11-02T17:00");
  });

  it("refuses a deadline in the past and a line with neither product nor description", () => {
    const errors = validateRFQ(
      { legalEntityId: LE, deadline: "2026-10-01T17:00", rankingRule: "LOWEST_PRICE" },
      [{ key: "k", productId: "", description: " ", qty: "1", unit: "EA", requiredDate: "" }],
      new Date(2026, 9, 26),
    );
    expect(errors.replyDeadline).toBe("Choose a deadline in the future.");
    expect(errors["lines[0].productId"]).toBe("Enter a product id or a description.");
  });
});

describe("RFQ list", () => {
  it("shows a loading table, then rows that open the detail page", async () => {
    let release: (r: Response) => void = () => undefined;
    stubFetch(() => new Promise<Response>((resolve) => (release = resolve)));
    const { router, container } = renderAt("/purchasing/rfqs");
    expect(await screen.findByRole("table", { name: "Requests for quotation" })).toHaveAttribute("aria-busy", "true");
    release(
      json({
        items: [
          { id: RFQ_ID, number: "RFQ-00000007", legalEntityId: LE, status: "REPLIES_OPEN", replyDeadline: "2026-11-01T17:00:00Z", rankingRule: "LOWEST_PRICE", lineCount: 2, vendorCount: 3, replyCount: 3, version: 4, createdAt: "2026-10-26T10:00:00Z" },
        ],
      }),
    );
    const table = await findTable({ name: "Requests for quotation" });
    await waitFor(() => expect(within(table).getByText("RFQ-00000007")).toBeInTheDocument());
    expect(within(table).getByText("Replies open")).toBeInTheDocument();
    expect(within(table).getByText("2026-11-01 17:00 UTC")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "RFQ-00000007" }));
    expect(router.state.location.pathname).toBe(`/purchasing/rfqs/${RFQ_ID}`);
  });

  it("explains an empty list and filters by status", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/rfqs");
    expect(await screen.findByRole("heading", { name: "No requests for quotation yet" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "AWARDED" } });
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/rfqs?status=AWARDED"));
  });

  it("shows the problem code on error and retries", async () => {
    const fetchMock = stubFetch(() => problem(500, "COM-0500", "internal error"));
    renderAt("/purchasing/rfqs");
    expect(await screen.findByText(/internal error \(COM-0500\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});

describe("new RFQ", () => {
  it("validates before calling the service", async () => {
    const fetchMock = stubFetch(() => json({}));
    renderAt("/purchasing/rfqs/new");
    fireEvent.click(await screen.findByRole("button", { name: "Save RFQ" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a product id or a description.")).toBeInTheDocument();
    expect(screen.getByText("Enter a positive quantity.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("posts the lines with the deadline as an instant and opens the new RFQ", async () => {
    const fetchMock = stubFetch(() => json(draft, 201, { ETag: '"1"' }));
    const { router, container } = renderAt("/purchasing/rfqs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Reply deadline"), { target: { value: "2099-11-01T17:00" } });
    fireEvent.change(screen.getByLabelText("Rank replies by"), { target: { value: "LEAD_TIME" } });
    fireEvent.change(screen.getByLabelText("Product id"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Add line" }));
    const second = screen.getByRole("group", { name: "Line 2" });
    fireEvent.change(within(second).getByLabelText("Description"), { target: { value: "Calibration" } });
    fireEvent.change(within(second).getByLabelText("Quantity"), { target: { value: "4" } });
    fireEvent.change(within(second).getByLabelText("Unit"), { target: { value: "HR" } });
    fireEvent.change(within(second).getByLabelText("Required by"), { target: { value: "2099-12-01" } });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/rfqs/${RFQ_ID}`));
    const [url, init] = fetchMock.mock.calls.find((c) => (c[1] as RequestInit).method === "POST") as [string, RequestInit];
    expect(url).toBe("/api/v1/procurement/rfqs");
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    const sent = body(init);
    expect(sent.replyDeadline).toBe(new Date("2099-11-01T17:00").toISOString());
    expect(sent.rankingRule).toBe("LEAD_TIME");
    expect(sent.lines).toEqual([
      { productId: PRODUCT, qty: "10", unit: "EA" },
      { description: "Calibration", qty: "4", unit: "HR", requiredDate: "2099-12-01" },
    ]);
  });

  it("shows field errors from the service", async () => {
    stubFetch(() => problem(422, "COM-0001", "replyDeadline must be in the future", { replyDeadline: "must be in the future" }));
    renderAt("/purchasing/rfqs/new");
    fireEvent.change(await screen.findByLabelText("Legal entity id"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Reply deadline"), { target: { value: "2099-11-01T17:00" } });
    fireEvent.change(screen.getByLabelText("Description"), { target: { value: "Bolts" } });
    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save RFQ" }));
    expect(await screen.findByText(/replyDeadline must be in the future \(COM-0001\)/)).toBeInTheDocument();
    expect(screen.getByText("must be in the future")).toBeInTheDocument();
  });
});

describe("RFQ detail", () => {
  it("sends a draft to the vendors with If-Match", async () => {
    const fetchMock = stubFetch((url, init) => {
      if (url.endsWith(":send")) {
        expect((init.headers as Record<string, string>)["If-Match"]).toBe('"1"');
        return json({ ...draft, status: "SENT", version: 2, invitations: [{ vendorId: VENDOR_A, status: "INVITED", sentAt: "2026-10-26T11:00:00Z" }, { vendorId: VENDOR_B, status: "INVITED", sentAt: "2026-10-26T11:00:00Z" }] });
      }
      return json(draft, 200, { ETag: '"1"' });
    });
    const { container } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByRole("heading", { name: "Request for quotation RFQ-00000007" })).toBeInTheDocument();
    expect(screen.getByText("2026-11-01 17:00 UTC")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Send RFQ" }));
    expect(await screen.findByText("Enter at least one vendor id.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Vendor ids"), { target: { value: `${VENDOR_A}\n${VENDOR_B}` } });
    fireEvent.click(screen.getByRole("button", { name: "Send RFQ" }));
    expect(await screen.findByText("Sent to 2 vendors.")).toBeInTheDocument();
    const [, init] = fetchMock.mock.calls.find(([u]) => String(u).endsWith(":send")) as [string, RequestInit];
    expect(body(init)).toEqual({ vendorIds: [VENDOR_A, VENDOR_B] });
    expect(screen.getByRole("table", { name: "Invited vendors" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send RFQ" })).not.toBeInTheDocument();
  });

  it("records a reply for the priced lines only and shows the late warning", async () => {
    const sent = { ...draft, status: "SENT", version: 2, invitations: open.invitations };
    const fetchMock = stubFetch((url) => {
      if (url.includes("/compare")) return json(comparison);
      if (url.endsWith("/replies")) {
        return json(
          { ...sent, status: "REPLIES_OPEN", version: 3, replies: [reply(REPLY_C, VENDOR_C, "8.00", 3, true)], warnings: [{ code: "PRC-RFQ-003", lineNo: 1, detail: "received after the deadline" }] },
          201,
        );
      }
      return json(sent);
    });
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.change(await screen.findByLabelText("Vendor"), { target: { value: VENDOR_C } });
    const line1 = screen.getByRole("group", { name: "Reply to line 1" });
    fireEvent.change(within(line1).getByLabelText("Unit price"), { target: { value: "8.00" } });
    fireEvent.change(within(line1).getByLabelText("Lead time (days)"), { target: { value: "3" } });
    fireEvent.click(screen.getByRole("button", { name: "Save reply" }));
    expect(await screen.findByText(/Line 1: received after the deadline \(PRC-RFQ-003\)/)).toBeInTheDocument();
    const [, init] = fetchMock.mock.calls.find(([u]) => String(u).endsWith("/replies")) as [string, RequestInit];
    expect(body(init)).toEqual({ vendorId: VENDOR_C, currency: "USD", replies: [{ lineId: LINE_1, unitPrice: "8.00", priceUnit: "1", leadTimeDays: 3 }] });
    const replies = screen.getByRole("table", { name: "Replies" });
    expect(within(replies).getByText("Late")).toBeInTheDocument();
  });

  it("asks for a price before recording a reply", async () => {
    const fetchMock = stubFetch(() => json({ ...draft, status: "SENT", version: 2, invitations: open.invitations }));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save reply" }));
    expect(await screen.findByText("Enter a price for at least one line.")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("compares with the late reply set apart, needs a reason to award it, and links the created purchase order", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/compare")) return json(comparison);
      if (url.endsWith(":award")) {
        return json({ rfq: { ...open, status: "AWARDED", version: 5 }, purchaseOrders: [purchaseOrder] });
      }
      return json(open);
    });
    const { container } = renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    const best = await screen.findByLabelText(/^#1 vendor 33333333: 9\.5 USD each, 12 days, total 95 USD$/);
    expect(best).toBeChecked();
    expect(screen.getByLabelText(/^Late, excluded from ranking: vendor 44444444/)).not.toBeChecked();
    expect(screen.getByText("No replies to this line.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByLabelText(/^Late, excluded from ranking/));
    fireEvent.click(screen.getByRole("button", { name: "Award and create purchase orders" }));
    expect(await screen.findByText("Give a reason to award a late reply.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith(":award"))).toBe(false);

    fireEvent.click(best);
    fireEvent.click(screen.getByRole("button", { name: "Award and create purchase orders" }));
    expect(await screen.findByText("Awarded. 1 draft purchase order created.")).toBeInTheDocument();
    const [, init] = fetchMock.mock.calls.find(([u]) => String(u).endsWith(":award")) as [string, RequestInit];
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"4"');
    expect(body(init)).toEqual({ awards: [{ lineId: LINE_1, replyId: REPLY_B }] });
    expect(screen.getByRole("link", { name: "PO-00000042" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
    expect(screen.queryByRole("button", { name: "Award and create purchase orders" })).not.toBeInTheDocument();
  });

  it("sends the override reason with a late award and shows a refused award's code", async () => {
    const fetchMock = stubFetch((url) => {
      if (url.includes("/compare")) return json(comparison);
      if (url.endsWith(":award")) return problem(403, "AUTH-0003", "missing duty procurement.rfq.award");
      return json(open);
    });
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.click(await screen.findByLabelText(/^Late, excluded from ranking/));
    fireEvent.change(screen.getByLabelText("Reason to award a late reply"), { target: { value: "Only vendor with stock" } });
    fireEvent.click(screen.getByRole("button", { name: "Award and create purchase orders" }));
    expect(await screen.findByText(/missing duty procurement\.rfq\.award \(AUTH-0003\)/)).toBeInTheDocument();
    const [, init] = fetchMock.mock.calls.find(([u]) => String(u).endsWith(":award")) as [string, RequestInit];
    expect(body(init)).toEqual({ awards: [{ lineId: LINE_1, replyId: REPLY_C, overrideReason: "Only vendor with stock" }] });
  });

  it("re-ranks by lead time on request", async () => {
    const fetchMock = stubFetch((url) => (url.includes("/compare") ? json(comparison) : json(open)));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.change(await screen.findByLabelText("Rank by"), { target: { value: "LEAD_TIME" } });
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith("/compare?rule=LEAD_TIME"))).toBe(true));
  });

  it("closes an open RFQ and shows a conflict", async () => {
    stubFetch((url) => {
      if (url.includes("/compare")) return json(comparison);
      if (url.endsWith(":close")) return problem(409, "PRC-VERSION-CONFLICT", "RFQ is at version 5, not 4; reload and retry");
      return json(open);
    });
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Close replies" }));
    expect(await screen.findByText(/reload and retry \(PRC-VERSION-CONFLICT\)/)).toBeInTheDocument();
  });

  it("explains a missing RFQ", async () => {
    stubFetch(() => problem(404, "PRC-RFQ-NOT-FOUND", "RFQ not found"));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByRole("heading", { name: "Request for quotation not found" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Back to requests for quotation" })).toHaveAttribute("href", "/purchasing/rfqs");
  });

  it("offers a retry when the RFQ cannot load", async () => {
    const fetchMock = stubFetch(() => problem(503, "COM-0503", "unavailable"));
    renderAt(`/purchasing/rfqs/${RFQ_ID}`);
    expect(await screen.findByText(/unavailable \(COM-0503\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
  });
});

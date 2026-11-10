import { findTable } from "../../test-utils";
import { cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Requisition, RequisitionLine } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const USER = "22222222-2222-4222-8222-222222222222";
const REQ_ID = "33333333-3333-4333-8333-333333333333";
const SITE = "99999999-9999-4999-8999-999999999999";

afterEach(() => vi.unstubAllGlobals());

const line = (no: number, patch: Partial<RequisitionLine> = {}): RequisitionLine => ({
  id: `44444444-4444-4444-8444-44444444444${no}`,
  lineNo: no,
  description: no === 1 ? "Laptop" : "Dock",
  category: "IT",
  qty: "2",
  unit: "EA",
  estimatedUnitPrice: "3500.00",
  amount: "7000.00",
  requestedDate: "2027-03-10",
  status: "OPEN",
  ...patch,
});

const requisition = (patch: Partial<Requisition> = {}): Requisition => ({
  id: REQ_ID,
  number: "PR-00000007",
  legalEntityId: LE,
  requesterId: USER,
  status: "DRAFT",
  currency: "USD",
  total: "14000.00",
  approverChain: [],
  approvedSteps: 0,
  version: 4,
  lines: [line(1), line(2)],
  history: [],
  createdAt: "2027-03-01T09:00:00Z",
  updatedAt: "2027-03-01T09:30:00Z",
  ...patch,
});

describe("editing a draft requisition", () => {
  it("offers Edit on a draft, loads the saved lines and saves them with If-Match and an Idempotency-Key", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "PATCH" ? json(requisition({ version: 5, total: "5.00" }), 200, { ETag: '"5"' }) : json(requisition({ lines: [line(1, { siteId: SITE })] })),
    );
    const { container, router } = renderAt(`/purchasing/requisitions/${REQ_ID}/edit`);
    expect(await screen.findByRole("heading", { name: "Edit requisition PR-00000007" })).toBeInTheDocument();
    expect(screen.getByLabelText("Legal entity")).toHaveAttribute("data-value", LE);
    expect(screen.getByLabelText("Legal entity")).toBeDisabled();
    expect(screen.getByLabelText("Description")).toHaveValue("Laptop");
    expect(screen.getByLabelText("Site")).toHaveAttribute("data-value", SITE);
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Quantity"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/requisitions/${REQ_ID}`));
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/requisitions/${REQ_ID}`);
    expect(init.method).toBe("PATCH");
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"4"');
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    const sent = JSON.parse(String(init.body));
    expect(sent.currency).toBe("USD");
    expect(sent.lines).toHaveLength(1);
    expect(sent.lines[0]).toMatchObject({ description: "Laptop", category: "IT", qty: "5", unit: "EA", estimatedUnitPrice: "3500.00", siteId: SITE });
    expect(sent.legalEntityId).toBeUndefined();
  });

  it("ignores typed text that is not a site and sends nothing", async () => {
    const fetchMock = stubFetch(() => json(requisition()));
    renderAt(`/purchasing/requisitions/${REQ_ID}/edit`);
    const sites = await screen.findAllByLabelText("Site");
    const before = sites[0].getAttribute("data-value");
    fireEvent.change(sites[0], { target: { value: "not-a-uuid" } });
    expect(sites[0]).toHaveAttribute("data-value", before ?? "");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("shows the service's refusal when the requisition has moved on", async () => {
    stubFetch((_url, init) => (init.method === "PATCH" ? problem(409, "PRC-REQ-STATE", "requisition is IN_REVIEW; only a DRAFT can be edited") : json(requisition())));
    renderAt(`/purchasing/requisitions/${REQ_ID}/edit`);
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/only a DRAFT can be edited \(PRC-REQ-STATE\)/)).toBeInTheDocument();
  });

  it("does not offer the form for a requisition that is not a draft, and shows the load error", async () => {
    stubFetch(() => json(requisition({ status: "IN_REVIEW", approverChain: ["MANAGER"], nextApprover: "MANAGER" })));
    renderAt(`/purchasing/requisitions/${REQ_ID}/edit`);
    expect(await screen.findByText(/Only a draft can be edited/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();

    vi.unstubAllGlobals();
    stubFetch(() => problem(500, "COM-0500", "boom"));
    renderAt(`/purchasing/requisitions/${REQ_ID}/edit`);
    expect(await screen.findByText(/Could not load the requisition/)).toBeInTheDocument();
  });

  it("links to the edit page from a draft and not from an approved requisition", async () => {
    stubFetch(() => json(requisition()));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    expect(await screen.findByRole("link", { name: "Edit" })).toHaveAttribute("href", `/purchasing/requisitions/${REQ_ID}/edit`);

    vi.unstubAllGlobals();
    stubFetch(() => json(requisition({ status: "APPROVED" })));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    await screen.findAllByRole("heading", { name: "Requisition PR-00000007" });
    expect(screen.getAllByRole("link", { name: "Edit" })).toHaveLength(1);
  });
});

describe("withdrawing a requisition from review", () => {
  const inReview = (patch: Partial<Requisition> = {}) =>
    requisition({ status: "IN_REVIEW", approverChain: ["MANAGER", "CFO"], nextApprover: "MANAGER", ...patch });

  it("posts :withdraw and returns to the draft view", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST" ? json(requisition({ version: 5, history: [{ id: "a", action: "WITHDRAWN", actorId: USER, fromStatus: "IN_REVIEW", toStatus: "DRAFT", requisitionVersion: 5, createdAt: "2027-03-02T10:00:00Z" }] })) : json(inReview()),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Withdraw" }));
    expect(await screen.findByText("Withdrawn from review; edit it and submit it again.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/requisitions/${REQ_ID}:withdraw`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"4"');
    expect(screen.getByRole("link", { name: "Edit" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();
    const history = await findTable({ name: "Requisition history" });
    expect(within(history).getByText("Withdrawn from review")).toBeInTheDocument();
  });

  it("is not offered once a level has approved, and a refusal shows its code", async () => {
    stubFetch(() => json(inReview({ approvedSteps: 1, nextApprover: "CFO" })));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    await screen.findByRole("heading", { name: "Requisition PR-00000007" });
    expect(screen.queryByRole("button", { name: "Withdraw" })).toBeNull();

    vi.unstubAllGlobals();
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-REQ-STATE", "level 1 has already approved") : json(inReview())));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const buttons = await screen.findAllByRole("button", { name: "Withdraw" });
    fireEvent.click(buttons[buttons.length - 1]);
    expect(await screen.findByText(/level 1 has already approved \(PRC-REQ-STATE\)/)).toBeInTheDocument();
  });
});

describe("cancelling one line of an approved requisition", () => {
  const approved = (patch: Partial<Requisition> = {}) => requisition({ status: "APPROVED", ...patch });

  it("asks first, posts the line id with If-Match and shows the line cancelled while the rest continues", async () => {
    const first = line(1);
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST"
        ? json(
            approved({
              version: 5,
              lines: [line(1, { status: "CANCELLED" }), line(2)],
              history: [{ id: "b", action: "LINE_CANCELLED", lineId: first.id, actorId: USER, fromStatus: "APPROVED", toStatus: "APPROVED", requisitionVersion: 5, createdAt: "2027-03-02T10:00:00Z" }],
            }),
          )
        : json(approved()),
    );
    const { container } = renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const lines = await findTable({ name: "Requisition lines" });
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(lines).getByRole("button", { name: "Cancel line 1" }));
    const dialog = await screen.findByRole("alertdialog", { name: /Cancel line 1 of PR-00000007/ });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep it" }));
    expect(fetchMock).toHaveBeenCalledTimes(1);
    fireEvent.click(within(lines).getByRole("button", { name: "Cancel line 1" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel line 1" }));
    expect(await screen.findByText("Line 1 cancelled; the rest of the requisition continues.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/requisitions/${REQ_ID}:cancel-line`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"4"');
    expect(JSON.parse(String(init.body))).toEqual({ lineId: first.id });
    // The last live line has no cancel button: the requisition is cancelled instead.
    const after = await findTable({ name: "Requisition lines" });
    expect(within(after).queryByRole("button", { name: /Cancel line/ })).toBeNull();
    const history = await findTable({ name: "Requisition history" });
    expect(within(history).getByText("Line 1")).toBeInTheDocument();
  });

  it("has no line buttons on a requisition that is not approved or whose lines are ordered", async () => {
    stubFetch(() => json(requisition()));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const lines = await findTable({ name: "Requisition lines" });
    expect(within(lines).queryByRole("button", { name: /Cancel line/ })).toBeNull();

    cleanup();
    vi.unstubAllGlobals();
    stubFetch(() => json(approved({ lines: [line(1, { status: "CONSOLIDATED", purchaseOrderId: "55555555-5555-4555-8555-555555555555", purchaseOrderLineNo: 1 }), line(2)] })));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    // The first render is still mounted; wait for the second one's table, whose only line button is line 2.
    const buttons = await screen.findAllByRole("button", { name: /Cancel line/ });
    expect(buttons.map((b) => b.textContent)).toEqual(["Cancel line 2"]);
  });

  it("shows the service's code when a running RFQ holds the line", async () => {
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-REQ-006", "line 1 is being sourced on RFQ-1") : json(approved())));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const lines = await findTable({ name: "Requisition lines" });
    fireEvent.click(within(lines).getByRole("button", { name: "Cancel line 1" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel line 1" }));
    expect(await screen.findByText(/being sourced on RFQ-1 \(PRC-REQ-006\)/)).toBeInTheDocument();
  });
});

describe("module", () => {
  it("registers the edit page", async () => {
    const { default: purchasing } = await import("./module");
    expect(purchasing.routes.map((r) => r.path)).toContain("/purchasing/requisitions/:id/edit");
  });
});

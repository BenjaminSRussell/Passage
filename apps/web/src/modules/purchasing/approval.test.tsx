import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Requisition, RequisitionApproval } from "./api/client";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const USER = "22222222-2222-4222-8222-222222222222";
const REQ_ID = "33333333-3333-4333-8333-333333333333";

afterEach(() => vi.unstubAllGlobals());

const requisition = (patch: Partial<Requisition> = {}): Requisition => ({
  id: REQ_ID,
  number: "PR-00000007",
  legalEntityId: LE,
  requesterId: USER,
  status: "IN_REVIEW",
  currency: "USD",
  total: "7000.00",
  approverChain: ["MANAGER", "CFO"],
  approvedSteps: 0,
  nextApprover: "MANAGER",
  version: 4,
  lines: [
    {
      id: "44444444-4444-4444-8444-444444444444",
      lineNo: 1,
      description: "Laptop",
      category: "IT",
      qty: "2",
      unit: "EA",
      estimatedUnitPrice: "3500.00",
      amount: "7000.00",
      requestedDate: "2027-03-10",
      status: "OPEN",
    },
  ],
  history: [
    {
      id: "55555555-5555-4555-8555-555555555555",
      action: "SUBMITTED",
      actorId: USER,
      fromStatus: "DRAFT",
      toStatus: "IN_REVIEW",
      requisitionVersion: 4,
      createdAt: "2027-03-01T09:30:00Z",
    },
  ],
  createdAt: "2027-03-01T09:00:00Z",
  updatedAt: "2027-03-01T09:30:00Z",
  ...patch,
});

const waiting = (patch: Partial<RequisitionApproval> = {}): RequisitionApproval => ({
  id: REQ_ID,
  number: "PR-00000007",
  legalEntityId: LE,
  requesterId: USER,
  currency: "USD",
  total: "7000.00",
  lineCount: 1,
  approvedSteps: 0,
  chainLength: 2,
  nextApprover: "MANAGER",
  version: 4,
  submittedAt: "2027-03-01T09:30:00Z",
  ...patch,
});

describe("requisition approvals inbox", () => {
  it("lists what waits for an approval level and filters by role", async () => {
    // Opening a row loads the requisition page, so its GET answers with a requisition, not the inbox page.
    const fetchMock = stubFetch((url) =>
      String(url).includes("/requisitions/")
        ? json(requisition())
        : json({ items: [waiting(), waiting({ id: "66666666-6666-4666-8666-666666666666", number: "PR-00000008", nextApprover: "CFO", approvedSteps: 1 })] }),
    );
    const { container, router } = renderAt("/purchasing/approvals");
    const table = await findTable({ name: "Requisition approvals" });
    expect(within(table).getByText("PR-00000007")).toBeInTheDocument();
    expect(within(table).getByText("2 of 2")).toBeInTheDocument();
    expect(within(table).getAllByText("7000.00 USD")).toHaveLength(2);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Waiting for"), { target: { value: "CFO" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/requisition-approvals?role=CFO"));
    fireEvent.click(within(table).getByText("PR-00000007"));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/requisitions/${REQ_ID}`));
    expect(await screen.findByRole("heading", { name: "Requisition PR-00000007" })).toBeInTheDocument();
  });

  it("shows the empty state, and the load error with a retry", async () => {
    let fail = true;
    stubFetch(() => (fail ? problem(500, "PRC-0500", "store unavailable") : json({ items: [] })));
    renderAt("/purchasing/approvals");
    expect(await screen.findByText(/Could not load approvals/)).toBeInTheDocument();
    expect(screen.getByText(/PRC-0500/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Nothing is waiting for approval")).toBeInTheDocument();
  });
});

describe("requisition decisions", () => {
  it("approves a level with a comment, If-Match and an Idempotency-Key, then shows the progress", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST"
        ? json(requisition({ version: 5, approvedSteps: 1, nextApprover: "CFO" }), 200, { ETag: '"5"' })
        : json(requisition(), 200, { ETag: '"4"' }),
    );
    const { container } = renderAt(`/purchasing/requisitions/${REQ_ID}`);
    expect(await screen.findByRole("heading", { name: "Requisition PR-00000007" })).toBeInTheDocument();
    expect(screen.getByText("Waiting for manager.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/Comment/), { target: { value: " ok for Q1 " } });
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Approved.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/requisitions/${REQ_ID}:approve`);
    expect((init.headers as Record<string, string>)["If-Match"]).toBe('"4"');
    expect((init.headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(init.body))).toEqual({ comment: "ok for Q1" });
    expect(screen.getByRole("list", { name: "Approver chain" })).toHaveTextContent("1. Manager (approved)");
    expect(screen.getByText("Waiting for cfo.")).toBeInTheDocument();
  });

  it("says when the last level approved", async () => {
    stubFetch((_url, init) =>
      init.method === "POST" ? json(requisition({ status: "APPROVED", version: 5, approvedSteps: 2, nextApprover: undefined })) : json(requisition()),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("Approved: the last level has approved.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.getByRole("button", { name: "Cancel requisition" })).toBeInTheDocument();
  });

  it("needs a reason to send it back, then posts it", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST" ? json(requisition({ status: "REJECTED", version: 5 })) : json(requisition()),
    );
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Send back" }));
    expect(await screen.findByText("Say why you are sending it back.")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    fireEvent.change(screen.getByLabelText(/Comment/), { target: { value: "Use the framework agreement" } });
    fireEvent.click(screen.getByRole("button", { name: "Send back" }));
    expect(await screen.findByText("Sent back to the requester.")).toBeInTheDocument();
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit];
    expect(url).toBe(`/api/v1/procurement/requisitions/${REQ_ID}:reject`);
    expect(JSON.parse(String(init.body))).toEqual({ comment: "Use the framework agreement" });
    expect(screen.getByRole("button", { name: "Reopen as a draft" })).toBeInTheDocument();
  });

  it("shows the service's code when it refuses, and reloads when the requisition has moved on", async () => {
    let calls = 0;
    stubFetch((_url, init) => {
      if (init.method === "POST") return problem(403, "PRC-SL-002", "the amount is above your approval limit");
      calls += 1;
      return json(requisition({ version: calls === 1 ? 4 : 5 }));
    });
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText(/above your approval limit/)).toBeInTheDocument();

    vi.unstubAllGlobals();
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-VERSION-CONFLICT", "requisition is at version 5, not 4") : json(requisition())));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const buttons = await screen.findAllByRole("button", { name: "Approve" });
    fireEvent.click(buttons[buttons.length - 1]);
    expect(await screen.findByRole("button", { name: "Reload requisition" })).toBeInTheDocument();
  });

  it("lets the requester reopen or cancel a rejected requisition and shows the reason in the history", async () => {
    const rejected = requisition({
      status: "REJECTED",
      approvedSteps: 0,
      nextApprover: undefined,
      history: [
        ...(requisition().history ?? []),
        {
          id: "77777777-7777-4777-8777-777777777777",
          action: "REJECTED",
          stepNo: 1,
          role: "MANAGER",
          actorId: "88888888-8888-4888-8888-888888888888",
          comment: "Use the framework agreement",
          fromStatus: "IN_REVIEW",
          toStatus: "REJECTED",
          requisitionVersion: 5,
          createdAt: "2027-03-02T10:00:00Z",
        },
      ],
    });
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(requisition({ status: "CANCELLED", version: 6 })) : json(rejected)));
    renderAt(`/purchasing/requisitions/${REQ_ID}`);
    const history = await findTable({ name: "Requisition history" });
    expect(within(history).getByText("Use the framework agreement")).toBeInTheDocument();
    expect(within(history).getByText("1. Manager")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reopen as a draft" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel requisition" }));
    // Cancelling asks first; keeping it sends nothing.
    const dialog = await screen.findByRole("alertdialog", { name: /Cancel requisition/ });
    fireEvent.click(within(dialog).getByRole("button", { name: "Keep it" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(fetchMock.mock.calls).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Cancel requisition" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel requisition" }));
    expect(await screen.findByText("Requisition cancelled.")).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[1]?.[0])).toBe(`/api/v1/procurement/requisitions/${REQ_ID}:cancel`);
  });
});

describe("module", () => {
  it("registers the approvals page", async () => {
    const { default: purchasing } = await import("./module");
    expect(purchasing.routes.map((r) => r.path)).toContain("/purchasing/approvals");
    expect(purchasing.nav.find((n) => n.to === "/purchasing/approvals")?.duty).toBe("procurement.purchase-order.inquire");
  });
});

import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { ChangeRequest } from "./api/client";
import { diffSummary } from "./ChangeRequestsPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const REQUEST_ID = "33333333-0000-4000-8000-000000000003";
const ORDER_ID = "44444444-0000-4000-8000-000000000004";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: string[] = ["sales.order.read", "sales.order.approve"]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

function request(overrides: Partial<ChangeRequest> = {}): ChangeRequest {
  return {
    id: REQUEST_ID,
    legalEntityId: LE,
    orderId: ORDER_ID,
    orderNumber: "SO-0000042",
    customerId: CUSTOMER,
    kind: "CHANGE",
    status: "PENDING",
    baseVersion: 3,
    reason: "customer call",
    change: { reason: "customer call", lines: [{ lineNo: 1, quantity: "8" }] },
    diff: [{ lineNo: 1, field: "quantity", old: "5", new: "8" }],
    requestedBy: "rep-1",
    requestedAt: "2027-03-01T09:00:00Z",
    decisionComment: "",
    version: 1,
    ...overrides,
  };
}

describe("diffSummary", () => {
  it("names a cancellation", () => {
    expect(diffSummary({ kind: "CANCEL", diff: [{ lineNo: 0, field: "status", old: "CONFIRMED", new: "CANCELLED" }] })).toBe("Cancel the order");
  });

  it("names the fields and lines a change touches", () => {
    expect(diffSummary(request())).toBe("1 field on line 1");
    expect(diffSummary({ diff: [{ lineNo: 0, field: "requestedShipDate", old: "a", new: "b" }] })).toBe("1 field");
    expect(diffSummary({ diff: [1, 2, 2].map((n) => ({ lineNo: n, field: "quantity", old: "1", new: "2" })) })).toBe("3 fields on 2 lines");
  });
});

describe("Change requests list", () => {
  it("loads the pending queue and filters", async () => {
    handler = () => ({ status: 200, body: { items: [request()] } });
    const { container } = renderAt("/sales/change-requests");
    expect(screen.getByText("Loading…")).toBeTruthy();
    expect(await screen.findByRole("link", { name: "1 field on line 1" })).toHaveAttribute("href", `/sales/change-requests/${REQUEST_ID}`);
    expect(calls[0].url.searchParams.get("status")).toBe("PENDING");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await vi.waitFor(() => expect(calls).toHaveLength(2));
    expect(calls[1].url.searchParams.get("status")).toBeNull();
  });

  it("shows the empty state and the error with a retry", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    const { unmount } = renderAt("/sales/change-requests");
    expect(await screen.findByText("No change requests")).toBeInTheDocument();
    unmount();
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "down" } });
    renderAt("/sales/change-requests");
    expect(await screen.findByText(/down/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("refuses a malformed legal entity without calling the service", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/change-requests");
    await screen.findByText("No change requests");
    fireEvent.change(screen.getByLabelText(/Legal entity/), { target: { value: "x" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(await screen.findByText("Enter a legal entity UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(1);
  });
});

describe("Change request detail", () => {
  it("approves at the request's ETag and reports the applied version", async () => {
    handler = (_u, init) =>
      init.method === "POST"
        ? { status: 200, body: { request: request({ status: "APPROVED", version: 2 }), order: { id: ORDER_ID, orderNumber: "SO-0000042", version: 4 } }, etag: '"2"' }
        : { status: 200, body: request(), etag: '"1"' };
    const { container } = renderAt(`/sales/change-requests/${REQUEST_ID}`);
    expect(await screen.findByText("Change request for SO-0000042")).toBeInTheDocument();
    expect(screen.getByRole("table", { name: "Requested changes" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Approve and apply" }));
    expect(await screen.findByText(/is now at version 4/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/change-requests/${REQUEST_ID}:approve`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
  });

  it("needs a comment to reject, then rejects", async () => {
    handler = (_u, init) => (init.method === "POST" ? { status: 200, body: request({ status: "REJECTED" }), etag: '"2"' } : { status: 200, body: request(), etag: '"1"' });
    renderAt(`/sales/change-requests/${REQUEST_ID}`);
    await screen.findByText("Change request for SO-0000042");
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByText(/Give a comment of 1 to 500/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText(/Comment/), { target: { value: "price is fixed" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject" }));
    expect(await screen.findByText(/The order stays as it was/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ comment: "price is fixed" });
  });

  it("shows the service's refusal, such as approving your own change", async () => {
    handler = (_u, init) =>
      init.method === "POST" ? { status: 403, body: { code: "SLS-0045", detail: "a change cannot be approved by the user who requested it" } } : { status: 200, body: request(), etag: '"1"' };
    renderAt(`/sales/change-requests/${REQUEST_ID}`);
    await screen.findByText("Change request for SO-0000042");
    fireEvent.click(screen.getByRole("button", { name: "Approve and apply" }));
    expect(await screen.findByText("You cannot approve your own change")).toBeInTheDocument();
  });

  it("offers no decision to a caller without the approve duty or on a decided request", async () => {
    handler = () => ({ status: 200, body: request(), etag: '"1"' });
    const { unmount } = renderAt(`/sales/change-requests/${REQUEST_ID}`, ["sales.order.read"]);
    await screen.findByText(/needs the sales.order.approve duty/);
    expect(screen.queryByRole("button", { name: "Approve and apply" })).not.toBeInTheDocument();
    unmount();
    handler = () => ({ status: 200, body: request({ status: "APPROVED", decidedBy: "mgr", decidedAt: "2027-03-02T09:00:00Z", appliedVersion: 4 }), etag: '"2"' });
    renderAt(`/sales/change-requests/${REQUEST_ID}`);
    await screen.findByText("Applied as version");
    expect(screen.queryByRole("button", { name: "Reject" })).not.toBeInTheDocument();
  });

  it("withdraws a pending request at its ETag and shows the refusal for someone else's", async () => {
    handler = (_u, init) => (init.method === "POST" ? { status: 200, body: request({ status: "WITHDRAWN", version: 2 }), etag: '"2"' } : { status: 200, body: request(), etag: '"1"' });
    const { container } = renderAt(`/sales/change-requests/${REQUEST_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Change request for SO-0000042");
    expect(screen.queryByRole("button", { name: "Approve and apply" })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Withdraw request" }));
    expect(await screen.findByText(/Withdrawn\. The order stays as it was/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/change-requests/${REQUEST_ID}:withdraw`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ comment: "" });
  });

  it("explains that only the requester can withdraw", async () => {
    handler = (_u, init) =>
      init.method === "POST" ? { status: 403, body: { code: "SLS-0114", detail: "only the user who requested a change can withdraw it" } } : { status: 200, body: request(), etag: '"1"' };
    renderAt(`/sales/change-requests/${REQUEST_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Change request for SO-0000042");
    fireEvent.click(screen.getByRole("button", { name: "Withdraw request" }));
    expect(await screen.findByText("Only the user who made the request can withdraw it")).toBeInTheDocument();
  });

  it("shows a cancellation request as such and offers no withdrawal once decided", async () => {
    handler = () => ({ status: 200, body: request({ kind: "CANCEL", change: {}, reason: "duplicate", diff: [{ lineNo: 0, field: "status", old: "CONFIRMED", new: "CANCELLED" }] }), etag: '"1"' });
    const { unmount } = renderAt(`/sales/change-requests/${REQUEST_ID}`);
    expect(await screen.findByText("Cancellation of the order")).toBeInTheDocument();
    unmount();
    handler = () => ({ status: 200, body: request({ status: "WITHDRAWN", decidedBy: "rep-1", decidedAt: "2027-03-02T09:00:00Z" }), etag: '"2"' });
    renderAt(`/sales/change-requests/${REQUEST_ID}`, ["sales.order.read", "sales.order.write"]);
    await screen.findByText("Withdrawn");
    expect(screen.queryByRole("button", { name: "Withdraw request" })).not.toBeInTheDocument();
  });

  it("shows a load error", async () => {
    handler = () => ({ status: 404, body: { code: "SLS-0112", detail: "change request not found" } });
    renderAt(`/sales/change-requests/${REQUEST_ID}`);
    expect(await screen.findByText(/change request not found/)).toBeInTheDocument();
  });
});

describe("Customer change approval", () => {
  const policy = { legalEntityId: LE, customerId: CUSTOMER, approvalRequired: false, version: 0 };
  it("opens a customer, turns approval on with If-Match 0", async () => {
    handler = (_u, init) =>
      init.method === "PUT" ? { status: 200, body: { ...policy, approvalRequired: true, version: 1 }, etag: '"1"' } : { status: 200, body: policy, etag: '"0"' };
    const { container } = renderAt("/sales/change-policy");
    expect(screen.getByText("Choose a customer")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText(/no setting saved/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByLabelText(/need approval/));
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/now need approval/)).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.headers.get("If-Match")).toBe('"0"');
    expect(put?.body).toEqual({ approvalRequired: true, approvalThreshold: null, thresholdCurrency: null, approverIds: [] });
  });

  it("saves a threshold with its currency and a named approver group, and loads them back", async () => {
    const mgrA = "0190a3c4-0000-7000-8000-00000000000a";
    const mgrB = "0190a3c4-0000-7000-8000-00000000000b";
    handler = (_u, init) =>
      init.method === "PUT"
        ? { status: 200, body: { ...policy, approvalRequired: true, approvalThreshold: "250.5", thresholdCurrency: "USD", approverIds: [mgrA, mgrB], version: 3 }, etag: '"3"' }
        : { status: 200, body: { ...policy, approvalRequired: true, approvalThreshold: "100", thresholdCurrency: "EUR", approverIds: [mgrA], version: 2 }, etag: '"2"' };
    renderAt("/sales/change-policy");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Change approval" });
    expect(screen.getByLabelText("Approval threshold")).toHaveValue("100");
    expect(screen.getByLabelText("Threshold currency")).toHaveValue("EUR");
    expect(screen.getByLabelText("Named approvers")).toHaveValue(mgrA);
    fireEvent.change(screen.getByLabelText("Approval threshold"), { target: { value: "250.5" } });
    fireEvent.change(screen.getByLabelText("Threshold currency"), { target: { value: "usd" } });
    fireEvent.change(screen.getByLabelText("Named approvers"), { target: { value: `${mgrB}, ${mgrA.toUpperCase()}\n${mgrA}\n` } });
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/now need approval/)).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.headers.get("If-Match")).toBe('"2"');
    expect(put?.body).toEqual({ approvalRequired: true, approvalThreshold: "250.5", thresholdCurrency: "USD", approverIds: [mgrB, mgrA] });
  });

  it("refuses an invalid threshold or a threshold without a currency before calling the service", async () => {
    handler = () => ({ status: 200, body: policy, etag: '"0"' });
    renderAt("/sales/change-policy");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Change approval" });
    fireEvent.click(screen.getByLabelText(/need approval/));
    fireEvent.change(screen.getByLabelText("Approval threshold"), { target: { value: "0" } });
    expect(screen.getByText(/amount above 0/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Approval threshold"), { target: { value: "50" } });
    expect(screen.getByText(/three-letter currency/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Threshold currency"), { target: { value: "USD" } });
    fireEvent.change(screen.getByLabelText("Named approvers"), { target: { value: "mgr-a" } });
    expect(screen.getByText(/user id \(UUID\)/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("shows the service's refusal of a policy", async () => {
    handler = (_u, init) => (init.method === "PUT" ? { status: 400, body: { code: "SLS-0001", detail: "approvalThreshold must be greater than 0" } } : { status: 200, body: policy, etag: '"0"' });
    renderAt("/sales/change-policy");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Change approval" });
    fireEvent.click(screen.getByLabelText(/need approval/));
    fireEvent.click(screen.getByRole("button", { name: "Save setting" }));
    expect(await screen.findByText(/must be greater than 0/)).toBeInTheDocument();
  });

  it("is read only without the approve duty and reports a load error", async () => {
    handler = () => ({ status: 200, body: { ...policy, approvalRequired: true, version: 2, updatedBy: "mgr", updatedAt: "2027-03-02T09:00:00Z" }, etag: '"2"' });
    const { unmount } = renderAt("/sales/change-policy", ["sales.order.read"]);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await screen.findByRole("form", { name: "Change approval" });
    expect(screen.queryByRole("button", { name: "Save setting" })).not.toBeInTheDocument();
    expect(screen.getByLabelText(/need approval/)).toBeDisabled();
    unmount();
    handler = () => ({ status: 403, body: { code: "AUTH-0003", detail: "outside scope" } });
    renderAt("/sales/change-policy");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    expect(await screen.findByText(/outside scope/)).toBeInTheDocument();
  });
});

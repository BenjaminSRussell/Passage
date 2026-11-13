import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const WORKER = "33333333-3333-4333-8333-333333333333";
const ID = "44444444-4444-4444-8444-444444444444";
const BASE = "/api/v1/procurement";

afterEach(() => vi.unstubAllGlobals());

const limit = {
  id: ID,
  legalEntityId: LE,
  workerId: WORKER,
  documentType: "PURCHASE_ORDER",
  currency: "USD",
  spendingLimit: "1000",
  approvalLimit: "5000",
  recertifyBy: "2999-12-31",
  version: 2,
  createdAt: "2026-03-01T10:00:00Z",
  updatedAt: "2026-03-01T10:00:00Z",
};

const request = {
  id: ID,
  legalEntityId: LE,
  workerId: WORKER,
  documentType: "REQUISITION",
  currency: "USD",
  spendingLimit: "2500",
  approvalLimit: "7000",
  status: "PENDING",
  requestedBy: "55555555-5555-4555-8555-555555555555",
  requestedAt: "2026-03-01T10:00:00Z",
  version: 1,
};

describe("signing limits list", () => {
  it("shows a loading table, then the limits with expired ones marked, and opens one", async () => {
    let resolve: (r: Response) => void = () => undefined;
    stubFetch((url) => (url.endsWith("/signing-limits") ? new Promise<Response>((r) => (resolve = r)) : json(limit)));
    const { router } = renderAt("/purchasing/signing-limits");
    expect(await screen.findByRole("table", { name: "Signing limits" })).toHaveAttribute("aria-busy", "true");
    resolve(json({ items: [limit, { ...limit, id: "66666666-6666-4666-8666-666666666666", approvalLimit: "9000", recertifyBy: "2020-01-01" }] }));
    expect(await screen.findByText("5,000.00 USD")).toBeInTheDocument();
    expect(screen.getByText("Expired 2020-01-01")).toBeInTheDocument();
    fireEvent.click(screen.getByText("5,000.00 USD"));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/signing-limits/${ID}`));
  });

  it("explains the empty state without accessibility violations", async () => {
    stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/signing-limits");
    expect(await screen.findByText("No signing limits yet")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the problem code and retries", async () => {
    const mock = stubFetch(() => problem(500, "PRC-0500", "store unavailable"));
    renderAt("/purchasing/signing-limits");
    expect(await screen.findByText(/PRC-0500/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(mock).toHaveBeenCalledTimes(2));
  });

  it("filters by document type and expiry on the server", async () => {
    const mock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/signing-limits");
    await screen.findByText("No signing limits yet");
    fireEvent.change(screen.getByLabelText("Document"), { target: { value: "REQUISITION" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("documentType=REQUISITION"))).toBe(true));
    fireEvent.change(screen.getByLabelText("Recertification"), { target: { value: "yes" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(mock.mock.calls.some(([url]) => String(url).includes("expired=true"))).toBe(true));
  });
});

describe("signing limit detail", () => {
  it("recertifies under If-Match and shows the new date", async () => {
    const mock = stubFetch((_url, init) => {
      if (init.method === "POST") return json({ ...limit, version: 3, recertifyBy: "3000-01-01" }, 200, { ETag: '"3"' });
      return json(limit, 200, { ETag: '"2"' });
    });
    const { container } = renderAt(`/purchasing/signing-limits/${ID}`);
    expect(await screen.findByText("1,000.00 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Recertify for 12 months" }));
    expect(await screen.findByText("Recertified until 3000-01-01.")).toBeInTheDocument();
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    expect(post[0]).toBe(`${BASE}/signing-limits/${ID}:recertify`);
    expect((post[1] as RequestInit).headers).toMatchObject({ "If-Match": '"2"' });
    expect((post[1] as RequestInit).headers).toHaveProperty("Idempotency-Key");
  });

  it("shows a refusal with its code and offers a reload for a stale version", async () => {
    let reads = 0;
    stubFetch((_url, init) => {
      if (init.method === "POST") return problem(412, "PRC-SL-005", "signing limit is at version 3, not 2");
      reads += 1;
      return json(reads === 1 ? limit : { ...limit, version: 3 });
    });
    renderAt(`/purchasing/signing-limits/${ID}`);
    await screen.findByText("1,000.00 USD");
    fireEvent.click(screen.getByRole("button", { name: "Recertify for 12 months" }));
    expect(await screen.findByText(/version 3, not 2/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reload limit" }));
    await waitFor(() => expect(screen.queryByText(/PRC-SL-005/)).toBeNull());
    expect(reads).toBe(2);
  });

  it("shows not-found as a load failure", async () => {
    stubFetch(() => problem(404, "PRC-SL-LIMIT-NOT-FOUND", "signing limit not found"));
    renderAt(`/purchasing/signing-limits/${ID}`);
    expect(await screen.findByText(/PRC-SL-LIMIT-NOT-FOUND/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Recertify for 12 months" })).toBeNull();
  });
});

describe("signing limit requests", () => {
  it("lists pending requests first and explains the empty state", async () => {
    const mock = stubFetch(() => json({ items: [] }));
    const { container } = renderAt("/purchasing/signing-limit-requests");
    expect(await screen.findByText("No signing limit requests match these filters")).toBeInTheDocument();
    expect(String(mock.mock.calls[0][0])).toContain("status=PENDING");
    expect(pageViolations(container)).toEqual([]);
  });

  it("opens a request from the list", async () => {
    stubFetch((url) => (url.includes("?") ? json({ items: [request] }) : json(request)));
    const { router } = renderAt("/purchasing/signing-limit-requests");
    fireEvent.click(await screen.findByText("7,000.00 USD"));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/signing-limit-requests/${ID}`));
  });

  it("validates the create form before sending", async () => {
    const mock = stubFetch(() => json({}));
    const { container } = renderAt("/purchasing/signing-limit-requests/new");
    fireEvent.click(await screen.findByRole("button", { name: "Send request" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter the worker id (a UUID).")).toBeInTheDocument();
    expect(screen.getAllByText("Enter an amount of zero or more.")).toHaveLength(2);
    expect(mock).not.toHaveBeenCalled();
    expect(pageViolations(container)).toEqual([]);
  });

  it("posts the request with an Idempotency-Key and opens it", async () => {
    const mock = stubFetch(() => json(request, 201, { ETag: '"1"' }));
    const { router } = renderAt("/purchasing/signing-limit-requests/new");
    fireEvent.change(await screen.findByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Worker"), { target: { value: WORKER } });
    fireEvent.change(screen.getByLabelText("Document"), { target: { value: "REQUISITION" } });
    fireEvent.change(screen.getByLabelText("Spending limit"), { target: { value: "2500" } });
    fireEvent.change(screen.getByLabelText("Approval limit"), { target: { value: "7000.50" } });
    fireEvent.click(screen.getByRole("button", { name: "Send request" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/signing-limit-requests/${ID}`));
    const [url, init] = mock.mock.calls[0];
    expect(url).toBe(`${BASE}/signing-limit-requests`);
    expect((init as RequestInit).headers).toHaveProperty("Idempotency-Key");
    expect(JSON.parse((init as RequestInit).body as string)).toEqual({
      legalEntityId: LE,
      workerId: WORKER,
      documentType: "REQUISITION",
      currency: "USD",
      spendingLimit: "2500",
      approvalLimit: "7000.50",
    });
  });

  it("approves a pending request with a note under If-Match", async () => {
    const mock = stubFetch((_url, init) => {
      if (init.method === "POST") return json({ ...request, status: "APPROVED", version: 2, signingLimitId: ID, decidedBy: WORKER }, 200, { ETag: '"2"' });
      return json(request);
    });
    const { container } = renderAt(`/purchasing/signing-limit-requests/${ID}`);
    expect(await screen.findByText("7,000.00 USD")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/^Note/), { target: { value: "within budget" } });
    fireEvent.click(screen.getByRole("button", { name: "Approve request" }));
    expect(await screen.findByText("Request approved; the limit is in force.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the limit" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve request" })).toBeNull();
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    expect(post[0]).toBe(`${BASE}/signing-limit-requests/${ID}:approve`);
    expect((post[1] as RequestInit).headers).toMatchObject({ "If-Match": '"1"' });
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ note: "within budget" });
  });

  it("shows the self-decision refusal with its code", async () => {
    stubFetch((_url, init) => (init.method === "POST" ? problem(403, "PRC-SL-003", "a worker cannot decide a request they made") : json(request)));
    renderAt(`/purchasing/signing-limit-requests/${ID}`);
    await screen.findByText("7,000.00 USD");
    fireEvent.click(screen.getByRole("button", { name: "Reject request" }));
    expect(await screen.findByText(/cannot decide a request they made/)).toBeInTheDocument();
  });

  it("offers no decision on a decided request", async () => {
    stubFetch(() => json({ ...request, status: "REJECTED", decidedBy: WORKER, decisionNote: "no" }));
    renderAt(`/purchasing/signing-limit-requests/${ID}`);
    await screen.findByText("7,000.00 USD");
    expect(screen.queryByRole("button", { name: "Approve request" })).toBeNull();
    expect(screen.getByText("no")).toBeInTheDocument();
  });
});

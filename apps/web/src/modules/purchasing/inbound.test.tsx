import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { InboundChange } from "./api/client";
import { actionLabel, effectOf, settledLabel, sourceLabel } from "./InboundPages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const PO_ID = "55555555-5555-4555-8555-555555555555";
const LE = "11111111-1111-4111-8111-111111111111";

afterEach(() => vi.unstubAllGlobals());

const change = (n: number, patch: Partial<InboundChange> = {}): InboundChange => ({
  id: `00000000-0000-4000-8000-00000000000${n}`,
  legalEntityId: LE,
  source: "PLANNING_MESSAGE",
  sourceKey: `66666666-6666-4666-8666-66666666666${n}`,
  eventId: `77777777-7777-4777-8777-77777777777${n}`,
  action: "postpone",
  purchaseOrderId: PO_ID,
  poNumber: "PO-00000007",
  lineNo: 1,
  status: "APPLIED",
  qtyBefore: "100",
  qtyAfter: "100",
  dateBefore: "2027-03-10",
  dateAfter: "2027-03-17",
  versionNo: 2,
  reapprovalRequired: false,
  requestedBy: "planner@example.com",
  note: "agreed with the vendor",
  createdAt: "2027-03-01T09:30:00Z",
  ...patch,
});

describe("changes from other services", () => {
  it("lists the changes with their effect and status and filters by status", async () => {
    const closed = change(2, {
      source: "DROPSHIP_CLOSED",
      action: "close-remainder",
      qtyBefore: "100",
      qtyAfter: "96",
      dateBefore: undefined,
      dateAfter: undefined,
      requestedBy: undefined,
    });
    const refused = change(3, { action: "decrease", status: "REFUSED", reasonCode: "PRC-PO-007", reason: "line 1 quantity 10 cannot be below received 30", qtyBefore: undefined, qtyAfter: undefined, dateBefore: undefined, dateAfter: undefined, versionNo: undefined });
    const fetchMock = stubFetch((url) => (/inbound-changes\/[0-9a-f-]{36}$/.test(url) ? json(change(1)) : json({ items: [change(1), closed, refused] })));
    const { container, router } = renderAt("/purchasing/inbound-changes");
    const table = await findTable({ name: "Changes from other services" });
    await waitFor(() => expect(within(table).getAllByRole("link", { name: /PO-00000007 line 1/ })).toHaveLength(3));
    expect(within(table).getByText("Due 2027-03-10 → 2027-03-17")).toBeInTheDocument();
    expect(within(table).getByText("Quantity 100 → 96")).toBeInTheDocument();
    expect(within(table).getByText("Drop-ship closed")).toBeInTheDocument();
    expect(within(table).getByText("Close remainder")).toBeInTheDocument();
    expect(within(table).getAllByText("Applied")).toHaveLength(2);
    expect(within(table).getByText("Refused")).toBeInTheDocument();
    expect(within(table).getByText(/PRC-PO-007: line 1 quantity 10 cannot be below received 30/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "REFUSED" } });
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/inbound-changes?status=REFUSED"));
    fireEvent.change(screen.getByLabelText("From"), { target: { value: "DROPSHIP_CLOSED" } });
    await waitFor(() =>
      expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/inbound-changes?source=DROPSHIP_CLOSED&status=REFUSED"),
    );
    fireEvent.click(within(table).getAllByRole("row")[1]);
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/purchasing\/inbound-changes\//));
  });

  it("narrows to one purchase order from the address", async () => {
    const fetchMock = stubFetch(() => json({ items: [change(1)] }));
    renderAt(`/purchasing/inbound-changes?purchaseOrderId=${PO_ID}`);
    await findTable({ name: "Changes from other services" });
    expect(String(fetchMock.mock.calls[0]?.[0])).toBe(`/api/v1/procurement/inbound-changes?purchaseOrderId=${PO_ID}`);
    fireEvent.click(screen.getByRole("button", { name: "Show all orders" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/inbound-changes"));
  });

  it("does not call the service for a purchase order id that is not a UUID", async () => {
    const fetchMock = stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inbound-changes?purchaseOrderId=nope");
    expect(await screen.findByText("The purchase order id in the address is not a UUID.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("shows the empty state and loads more pages", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/inbound-changes");
    expect(await screen.findByRole("heading", { name: "No changes from other services" })).toBeInTheDocument();
    vi.unstubAllGlobals();
    const fetchMock = stubFetch((url) => (url.includes("cursor=") ? json({ items: [change(2)] }) : json({ items: [change(1)], nextCursor: "c1" })));
    renderAt("/purchasing/inbound-changes");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toContain("cursor=c1"));
  });

  it("explains a load error and retries", async () => {
    let fail = true;
    stubFetch(() => (fail ? problem(503, "COM-0503", "unavailable") : json({ items: [change(1)] })));
    renderAt("/purchasing/inbound-changes");
    expect(await screen.findByText(/Could not load the changes/)).toBeInTheDocument();
    expect(screen.getByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("link", { name: /PO-00000007 line 1/ })).toBeInTheDocument();
  });

  it("shows one applied change with its effect and a link to the order", async () => {
    const c = change(1);
    stubFetch(() => json(c));
    const { container } = renderAt(`/purchasing/inbound-changes/${c.id}`);
    expect(await screen.findByRole("heading", { name: "Planning message: postpone" })).toBeInTheDocument();
    expect(screen.getByText("Due 2027-03-10 → 2027-03-17")).toBeInTheDocument();
    expect(screen.getByText("planner@example.com")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open purchase order" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
    expect(screen.queryByText(/Waiting for re-approval/)).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says why a refused change did not apply and when one waits for re-approval", async () => {
    const refused = change(3, { status: "REFUSED", reasonCode: "PRC-PO-007", reason: "line 1 quantity 10 cannot be below received 30", qtyAfter: undefined });
    stubFetch(() => json(refused));
    const { unmount } = renderAt(`/purchasing/inbound-changes/${refused.id}`);
    expect(await screen.findByText("Not applied (PRC-PO-007)")).toBeInTheDocument();
    expect(screen.getByText(/Fix the cause on the purchase order and retry/)).toBeInTheDocument();
    unmount();
    vi.unstubAllGlobals();

    const waiting = change(4, { reapprovalRequired: true, versionNo: 4 });
    stubFetch(() => json(waiting));
    const second = renderAt(`/purchasing/inbound-changes/${waiting.id}`);
    expect(await screen.findByText("Waiting for re-approval")).toBeInTheDocument();
    second.unmount();
    vi.unstubAllGlobals();

    const ignored = change(5, { status: "IGNORED", reasonCode: "PRC-INB-007", reason: "the line already has that quantity and date", purchaseOrderId: undefined });
    stubFetch(() => json(ignored));
    renderAt(`/purchasing/inbound-changes/${ignored.id}`);
    expect(await screen.findByText("Nothing to change")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open purchase order" })).not.toBeInTheDocument();
  });

  it("reports a change that does not exist", async () => {
    stubFetch(() => problem(404, "PRC-INB-NOT-FOUND", "inbound change not found"));
    renderAt("/purchasing/inbound-changes/00000000-0000-4000-8000-000000000009");
    expect(await screen.findByText(/Could not load the change/)).toBeInTheDocument();
    expect(screen.getByText(/PRC-INB-NOT-FOUND/)).toBeInTheDocument();
  });

  it("words sources, actions and effects", () => {
    expect(sourceLabel("DROPSHIP_CLOSED")).toBe("Drop-ship closed");
    expect(sourceLabel("DROPSHIP_CANCELLED")).toBe("Drop-ship cancelled");
    expect(settledLabel(change(1))).toBe("—");
    expect(settledLabel(change(1, { status: "REFUSED" }))).toBe("Waiting for a buyer");
    expect(settledLabel(change(1, { status: "REFUSED", resolution: "ACKNOWLEDGED" }))).toBe("Acknowledged");
    expect(settledLabel(change(1, { resolution: "RETRIED" }))).toBe("Retried");
    expect(actionLabel("close-remainder")).toBe("Close remainder");
    expect(effectOf(change(1, { dateBefore: undefined, dateAfter: undefined }))).toBe("");
    expect(effectOf(change(1, { qtyBefore: "100", qtyAfter: "0", dateBefore: undefined, dateAfter: undefined }))).toBe("Line cancelled (was 100)");
    expect(effectOf(change(1, { qtyBefore: "100", qtyAfter: "90" }))).toBe("Quantity 100 → 90, Due 2027-03-10 → 2027-03-17");
  });
  it("filters by whether a buyer has settled the change and shows the settled column", async () => {
    const waiting = change(6, { status: "REFUSED", reasonCode: "PRC-PO-007", reason: "below received", source: "DROPSHIP_CANCELLED", action: "cancel-dropship" });
    const acknowledged = change(7, { status: "REFUSED", resolution: "ACKNOWLEDGED", reasonCode: "PRC-PO-007", reason: "below received" });
    const fetchMock = stubFetch(() => json({ items: [waiting, acknowledged] }));
    renderAt("/purchasing/inbound-changes");
    const table = await findTable({ name: "Changes from other services" });
    await waitFor(() => expect(within(table).getByText("Waiting for a buyer")).toBeInTheDocument());
    expect(within(table).getByText("Acknowledged")).toBeInTheDocument();
    expect(within(table).getByText("Drop-ship cancelled")).toBeInTheDocument();
    expect(within(table).getByText("Cancel dropship")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Settled"), { target: { value: "NONE" } });
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/inbound-changes?resolution=NONE"));
  });

  it("retries a refused change and shows its new outcome", async () => {
    const refused = change(8, { status: "REFUSED", reasonCode: "PRC-PO-007", reason: "line 1 quantity 80 cannot be below received 90", attempts: 1, qtyAfter: undefined });
    const done = { ...refused, status: "APPLIED" as const, reasonCode: undefined, reason: undefined, resolution: "RETRIED" as const, attempts: 2, resolvedBy: "buyer@example.com", qtyBefore: "100", qtyAfter: "80" };
    const mock = stubFetch((_url, init) => (init.method === "POST" ? json(done) : json(refused)));
    const { container } = renderAt(`/purchasing/inbound-changes/${refused.id}`);
    expect(await screen.findByText("Not applied (PRC-PO-007)")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/^Note/), { target: { value: "receipt corrected" } });
    fireEvent.click(screen.getByRole("button", { name: "Retry the change" }));
    expect(await screen.findByText(/2 runs in all/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Retry the change" })).toBeNull();
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    expect(post[0]).toBe(`/api/v1/procurement/inbound-changes/${refused.id}:retry`);
    expect((post[1] as RequestInit).headers).toHaveProperty("Idempotency-Key");
    expect(JSON.parse((post[1] as RequestInit).body as string)).toEqual({ note: "receipt corrected" });
  });

  it("says so when a retry is refused again and keeps the change open", async () => {
    const refused = change(10, { status: "REFUSED", reasonCode: "PRC-PO-007", reason: "line 1 quantity 80 cannot be below received 90", attempts: 1 });
    const again = { ...refused, reasonCode: "PRC-PO-007", reason: "line 1 quantity 80 cannot be below received 85", attempts: 2 };
    stubFetch((_url, init) => (init.method === "POST" ? json(again) : json(refused)));
    renderAt(`/purchasing/inbound-changes/${refused.id}`);
    await screen.findByText(/below received 90/);
    fireEvent.click(screen.getByRole("button", { name: "Retry the change" }));
    expect(await screen.findByText(/refused again/)).toBeInTheDocument();
    expect(screen.getByText(/below received 85/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry the change" })).toBeEnabled();
  });

  it("acknowledges a refused change", async () => {
    const refused = change(9, { status: "REFUSED", reasonCode: "PRC-INB-006", reason: "purchase order is IN_REVIEW", attempts: 1 });
    const mock = stubFetch((_url, init) => (init.method === "POST" ? json({ ...refused, resolution: "ACKNOWLEDGED", resolvedBy: "buyer@example.com" }) : json(refused)));
    renderAt(`/purchasing/inbound-changes/${refused.id}`);
    await screen.findByText("Not applied (PRC-INB-006)");
    fireEvent.click(screen.getByRole("button", { name: "Acknowledge" }));
    expect(await screen.findByText(/buyer@example.com dealt with it by hand/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Acknowledge" })).toBeNull();
    const post = mock.mock.calls.find(([, i]) => (i as RequestInit).method === "POST")!;
    expect(post[0]).toBe(`/api/v1/procurement/inbound-changes/${refused.id}:acknowledge`);
    expect((post[1] as RequestInit).body).toBeUndefined();
  });

  it("shows why a settlement failed and offers a reload when the change was settled meanwhile", async () => {
    const refused = change(1, { status: "REFUSED", reasonCode: "PRC-PO-007", reason: "below received", attempts: 1 });
    stubFetch((_url, init) => (init.method === "POST" ? problem(409, "PRC-INB-008", "this one is REFUSED and acknowledged") : json(refused)));
    renderAt(`/purchasing/inbound-changes/${refused.id}`);
    await screen.findByText("Not applied (PRC-PO-007)");
    fireEvent.click(screen.getByRole("button", { name: "Retry the change" }));
    expect(await screen.findByText(/acknowledged.*PRC-INB-008/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload change" })).toBeInTheDocument();
  });

  it("offers no settlement on an applied or acknowledged change", async () => {
    const applied = change(2);
    stubFetch(() => json(applied));
    const { unmount } = renderAt(`/purchasing/inbound-changes/${applied.id}`);
    await screen.findByRole("heading", { name: "Planning message: postpone" });
    expect(screen.queryByRole("button", { name: "Retry the change" })).toBeNull();
    unmount();
    vi.unstubAllGlobals();
    const acked = change(3, { status: "REFUSED", resolution: "ACKNOWLEDGED", reasonCode: "PRC-PO-007", reason: "below received", resolvedBy: "buyer@example.com" });
    stubFetch(() => json(acked));
    renderAt(`/purchasing/inbound-changes/${acked.id}`);
    expect(await screen.findByText(/buyer@example.com dealt with it by hand/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Acknowledge" })).toBeNull();
  });
});

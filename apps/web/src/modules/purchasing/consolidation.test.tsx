import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const OTHER_VENDOR = "77777777-7777-4777-8777-777777777777";
const PO_ID = "55555555-5555-4555-8555-555555555555";

afterEach(() => vi.unstubAllGlobals());

const line = (id: string, n: number, preferredVendorId?: string) => ({
  id,
  lineNo: n,
  description: `Item ${n}`,
  category: "MRO",
  qty: "2",
  unit: "EA",
  estimatedUnitPrice: "10",
  amount: "20",
  requestedDate: "2027-03-01",
  preferredVendorId,
  status: "OPEN",
  requisitionId: "44444444-4444-4444-8444-444444444444",
  requisitionNumber: "PR-00000007",
  requisitionStatus: "APPROVED",
  legalEntityId: LE,
  currency: "USD",
});

const L1 = "c1c1c1c1-c1c1-4c1c-8c1c-c1c1c1c1c1c1";
const L2 = "c2c2c2c2-c2c2-4c2c-8c2c-c2c2c2c2c2c2";
const queue = { items: [line(L1, 1, VENDOR), line(L2, 2)] };

describe("consolidation board", () => {
  it("lists approved lines waiting to be ordered and filters the needs-sourcing queue", async () => {
    const fetchMock = stubFetch(() => json(queue));
    const { container } = renderAt("/purchasing/consolidation");
    const table = await screen.findByRole("table", { name: "Lines to order" });
    await waitFor(() => expect(within(table).getAllByRole("link", { name: "PR-00000007" })).toHaveLength(2));
    expect(within(table).getByLabelText("Vendor for PR-00000007 line 2")).toHaveAttribute("placeholder", "Needs a vendor");
    expect(within(table).getByLabelText("Vendor for PR-00000007 line 1")).toHaveAttribute("placeholder", VENDOR.slice(0, 8));
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "NEEDS_SOURCING" } });
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/requisition-lines?sourcing=NEEDS_SOURCING"));
  });

  it("explains an empty queue and a load error", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/consolidation");
    expect(await screen.findByRole("heading", { name: "Nothing to order" })).toBeInTheDocument();
    vi.unstubAllGlobals();
    stubFetch(() => problem(503, "COM-0503", "unavailable"));
    renderAt("/purchasing/consolidation");
    expect(await screen.findByText(/unavailable \(COM-0503\)/)).toBeInTheDocument();
  });

  it("checks the selection before calling the service", async () => {
    const fetchMock = stubFetch(() => json(queue));
    renderAt("/purchasing/consolidation");
    fireEvent.click(await screen.findByRole("button", { name: "Create purchase orders" }));
    expect(await screen.findByText("Select at least one line.")).toBeInTheDocument();
    fireEvent.click(await screen.findByLabelText("Select PR-00000007 line 2"));
    fireEvent.change(screen.getByLabelText("Delivery window (days)"), { target: { value: "90" } });
    fireEvent.click(screen.getByRole("button", { name: "Create purchase orders" }));
    expect(await screen.findByText("Choose a vendor for PR-00000007 line 2: it has no preferred vendor.")).toBeInTheDocument();
    expect(screen.getByText("Enter a whole number of days from 1 to 60.")).toBeInTheDocument();
    expect(fetchMock.mock.calls.every(([, init]) => (init as RequestInit | undefined)?.method !== "POST")).toBe(true);
  });

  it("consolidates the selected lines with the vendor override and lists the new orders", async () => {
    const fetchMock = stubFetch((_url, init) =>
      init.method === "POST"
        ? json(
            {
              purchaseOrders: [
                { id: PO_ID, number: "PO-00000011", currency: "USD", total: "40", lines: [{ lineNo: 1 }, { lineNo: 2 }] },
              ],
            },
            201,
          )
        : json(queue),
    );
    renderAt("/purchasing/consolidation");
    fireEvent.click(await screen.findByLabelText("Select PR-00000007 line 1"));
    fireEvent.click(screen.getByLabelText("Select PR-00000007 line 2"));
    fireEvent.change(screen.getByLabelText("Vendor for PR-00000007 line 2"), { target: { value: OTHER_VENDOR } });
    fireEvent.change(screen.getByLabelText("Delivery window (days)"), { target: { value: "14" } });
    expect(screen.getByText("2 lines selected")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create purchase orders" }));

    const created = await screen.findByRole("list", { name: "Purchase orders created" });
    expect(within(created).getByRole("link", { name: "PO-00000011" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
    expect(created).toHaveTextContent("2 lines · 40 USD");
    const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "POST") as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/procurement/requisitions:consolidate");
    expect((post[1].headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(post[1].body))).toEqual({
      windowDays: 14,
      lines: [{ requisitionLineId: L1 }, { requisitionLineId: L2, vendorId: OTHER_VENDOR }],
    });
    await waitFor(() => expect(screen.getByText("0 lines selected")).toBeInTheDocument());
    expect(fetchMock.mock.calls.filter(([, init]) => (init as RequestInit).method !== "POST")).toHaveLength(2);
  });

  it("shows the service's code when it refuses", async () => {
    stubFetch((_url, init) =>
      init.method === "POST" ? problem(409, "PRC-REQ-STATE", "requisition PR-00000007 line 1 is CONSOLIDATED") : json(queue),
    );
    renderAt("/purchasing/consolidation");
    fireEvent.click(await screen.findByLabelText("Select PR-00000007 line 1"));
    fireEvent.click(screen.getByRole("button", { name: "Create purchase orders" }));
    expect(await screen.findByText(/is CONSOLIDATED \(PRC-REQ-STATE\)/)).toBeInTheDocument();
  });
});

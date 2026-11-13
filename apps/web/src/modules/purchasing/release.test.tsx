import { findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReleaseRun } from "./api/client";
import { summaryOf, validateRelease } from "./ReleasePages";
import { json, pageViolations, problem, renderAt, stubFetch } from "./testHarness";

const LE = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const PO_ID = "55555555-5555-4555-8555-555555555555";
const RUN_ID = "66666666-6666-4666-8666-666666666666";

afterEach(() => vi.unstubAllGlobals());

const run = (patch: Partial<ReleaseRun> = {}): ReleaseRun => ({
  id: RUN_ID,
  legalEntityId: LE,
  status: "PARTIAL",
  windowDays: 7,
  maxLines: 200,
  linesConsidered: 4,
  linesReleased: 3,
  linesSkipped: 1,
  purchaseOrders: [{ purchaseOrderId: PO_ID, number: "PO-00000011", vendorId: VENDOR, currency: "USD", total: "30.00", lineCount: 3 }],
  skipped: [
    {
      requisitionLineId: "c1c1c1c1-c1c1-4c1c-8c1c-c1c1c1c1c1c1",
      requisitionNumber: "PR-00000007",
      lineNo: 2,
      code: "PRC-CONS-002",
      reason: "the line has no vendor; choose one or source it with an RFQ",
    },
  ],
  warnings: [],
  createdAt: "2027-03-01T07:00:00Z",
  ...patch,
});

describe("release runs", () => {
  it("lists the runs with what they released and filters by status", async () => {
    const fetchMock = stubFetch(() => json({ items: [run(), run({ id: "77777777-7777-4777-8777-777777777777", status: "COMPLETED", linesSkipped: 0, linesReleased: 4, skipped: [] })] }));
    const { container } = renderAt("/purchasing/releases");
    const table = await findTable({ name: "Release runs" });
    await waitFor(() => expect(within(table).getByText("Partial")).toBeInTheDocument());
    expect(within(table).getByText("Completed")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Start a release run" })).toHaveAttribute("href", "/purchasing/releases/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "FAILED" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply filters" }));
    await waitFor(() => expect(String(fetchMock.mock.calls.at(-1)?.[0])).toBe("/api/v1/procurement/requisition-releases?status=FAILED"));
  });

  it("shows the empty state and the load error with a retry", async () => {
    let fail = true;
    stubFetch(() => (fail ? problem(500, "PRC-0500", "store unavailable") : json({ items: [] })));
    renderAt("/purchasing/releases");
    expect(await screen.findByText(/Could not load release runs/)).toBeInTheDocument();
    expect(screen.getByText(/PRC-0500/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("No release runs yet")).toBeInTheDocument();
  });

  it("checks the form before calling the service", async () => {
    const fetchMock = stubFetch(() => json(run()));
    renderAt("/purchasing/releases/new");
    fireEvent.change(screen.getByLabelText("Delivery window (days)"), { target: { value: "90" } });
    fireEvent.change(screen.getByLabelText("Most lines to read"), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Release lines" }));
    expect(await screen.findByText("Enter the legal entity id (a UUID).")).toBeInTheDocument();
    expect(screen.getByText("Enter a whole number of days from 1 to 60.")).toBeInTheDocument();
    expect(screen.getByText("Enter a whole number of lines from 1 to 500.")).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("starts a run with an Idempotency-Key and opens its record", async () => {
    const fetchMock = stubFetch((_url, init) => (init.method === "POST" ? json(run(), 201) : json(run())));
    const { router } = renderAt("/purchasing/releases/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Delivery window (days)"), { target: { value: "14" } });
    fireEvent.click(screen.getByRole("button", { name: "Release lines" }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/purchasing/releases/${RUN_ID}`));
    const post = fetchMock.mock.calls.find(([, init]) => (init as RequestInit).method === "POST") as [string, RequestInit];
    expect(post[0]).toBe("/api/v1/procurement/requisition-releases");
    expect((post[1].headers as Record<string, string>)["Idempotency-Key"]).toBeTruthy();
    expect(JSON.parse(String(post[1].body))).toEqual({ legalEntityId: LE, windowDays: 14, maxLines: 200 });
  });

  it("shows the service's code when it refuses the run", async () => {
    stubFetch(() => problem(403, "AUTH-0003", "legal entity is outside the caller's scope"));
    renderAt("/purchasing/releases/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.click(screen.getByRole("button", { name: "Release lines" }));
    expect(await screen.findByText(/outside the caller's scope/)).toBeInTheDocument();
  });

  it("shows a run with its orders and the lines it left in the queue", async () => {
    stubFetch(() => json(run({ warnings: [{ code: "PRC-CAT-002", lineNo: 1, detail: "vendor is not assigned to MRO" }] })));
    const { container } = renderAt(`/purchasing/releases/${RUN_ID}`);
    const orders = await findTable({ name: "Orders created" });
    expect(within(orders).getByRole("link", { name: "PO-00000011" })).toHaveAttribute("href", `/purchasing/purchase-orders/${PO_ID}`);
    expect(within(orders).getByText("30.00 USD")).toBeInTheDocument();
    const left = await findTable({ name: "Lines left in the queue" });
    expect(within(left).getByText("PRC-CONS-002")).toBeInTheDocument();
    expect(within(left).getByText(/the line has no vendor/)).toBeInTheDocument();
    expect(screen.getByText("3 of 4 lines released into 1 order; 1 left in the queue.")).toBeInTheDocument();
    expect(screen.getByText(/PRC-CAT-002: vendor is not assigned to MRO/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open consolidation queue" })).toHaveAttribute("href", "/purchasing/consolidation");
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows an empty run and a missing run", async () => {
    stubFetch(() => json(run({ status: "EMPTY", linesConsidered: 0, linesReleased: 0, linesSkipped: 0, purchaseOrders: [], skipped: [] })));
    renderAt(`/purchasing/releases/${RUN_ID}`);
    expect(await screen.findByText("No approved lines were waiting to be ordered.")).toBeInTheDocument();
    expect(screen.getByText("No orders were created")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Open consolidation queue" })).toBeNull();
    vi.unstubAllGlobals();
    stubFetch(() => problem(404, "PRC-REL-NOT-FOUND", "release run not found"));
    renderAt(`/purchasing/releases/${RUN_ID}`);
    expect(await screen.findByText(/PRC-REL-NOT-FOUND/)).toBeInTheDocument();
  });

  it("is reachable from the consolidation board", async () => {
    stubFetch(() => json({ items: [] }));
    renderAt("/purchasing/consolidation");
    expect(await screen.findByRole("link", { name: "Release all lines with a vendor" })).toHaveAttribute("href", "/purchasing/releases/new");
  });

  it("summarises and validates", () => {
    expect(summaryOf(run({ linesSkipped: 0, linesReleased: 4, purchaseOrders: [] }))).toBe("4 of 4 lines released into 0 orders.");
    expect(validateRelease(LE, "7", "200")).toEqual({});
    expect(Object.keys(validateRelease("x", "7.5", "501")).sort()).toEqual(["legalEntityId", "maxLines", "windowDays"]);
  });
});

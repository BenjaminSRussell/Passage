import { findTable } from "../../test-utils";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import sales from "./module";
import { validateScheduleRelease } from "./ScheduleReleasesPage";

const AGREEMENT_ID = "88888888-0000-4000-8000-000000000008";
const RELEASE_ID = "99999999-0000-4000-8000-000000000009";
const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";

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

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {sales.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
        <Route path="*" element={<p>Elsewhere</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

function fill(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

const agreement = {
  id: AGREEMENT_ID,
  legalEntityId: LE,
  agreementNumber: "SA-0000007",
  customerId: CUSTOMER,
  invoiceAccountId: CUSTOMER,
  productId: PRODUCT,
  unit: "EA",
  unitPrice: "12.5",
  currency: "USD",
  commitmentQty: "100000",
  releasedQty: "0",
  remainingQty: "100000",
  status: "ACTIVE",
  cumTolerance: "0",
  cumShipped: "1200",
  version: 3,
  createdBy: "key-account",
  createdAt: "2027-03-01T09:00:00Z",
  updatedAt: "2027-03-02T09:00:00Z",
};

const base = {
  id: RELEASE_ID,
  agreementId: AGREEMENT_ID,
  agreementNumber: "SA-0000007",
  releaseNo: "R-17",
  releaseType: "JIT",
  status: "ACCEPTED",
  cumReceived: "1200",
  cumShipped: "1200",
  tolerance: "0",
  difference: "0",
  openQuantity: "300",
  buckets: [{ date: "2027-04-15", cumRequired: "1500" }],
  lines: [{ id: "l1", dueDate: "2027-04-15", quantity: "300", status: "OPEN" }],
  receivedBy: "edi",
  receivedAt: "2027-03-10T09:00:00Z",
  version: 1,
};
const held = { ...base, status: "HELD", cumReceived: "1150", difference: "-50", openQuantity: "0", code: "CUM_QTY_MISMATCH", serviceCode: "SLS-0014", lines: [] };

describe("Scheduling releases", () => {
  it("lists the releases of an agreement with the cumulative settings", async () => {
    handler = (url) => (url.pathname.endsWith("/schedule-releases") ? { status: 200, body: { items: [held, { ...base, id: "x", releaseNo: "R-16", status: "SUPERSEDED" }] } } : { status: 200, body: agreement, etag: '"3"' });
    const { container } = renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases`);
    expect(await screen.findByRole("heading", { name: "Scheduling releases for SA-0000007" })).toBeInTheDocument();
    const table = await findTable({ name: "Scheduling releases" });
    expect(within(table).getByText("R-17")).toBeInTheDocument();
    expect(within(table).getByText("R-16")).toBeInTheDocument();
    expect(await screen.findByText("1200 EA")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows the empty state and the error state with a retry", async () => {
    handler = (url) => (url.pathname.endsWith("/schedule-releases") ? { status: 200, body: { items: [] } } : { status: 200, body: agreement, etag: '"3"' });
    const first = renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases`);
    expect(await screen.findByRole("heading", { name: "No releases yet" })).toBeInTheDocument();
    first.unmount();

    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "boom" } });
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases`);
    expect(await screen.findByText("Could not load the releases")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Try again" }).length).toBeGreaterThan(0);
  });

  it("records the shipped quantity at the agreement's ETag", async () => {
    handler = (url, init) =>
      init.method === "POST" ? { status: 200, body: { ...agreement, cumShipped: "1300", version: 4 }, etag: '"4"' } : url.pathname.endsWith("/schedule-releases") ? { status: 200, body: { items: [] } } : { status: 200, body: agreement, etag: '"3"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases`);
    await screen.findByRole("heading", { name: "Scheduling releases for SA-0000007" });
    fireEvent.click(await screen.findByRole("button", { name: "Record shipped quantity" }));
    expect(screen.getByText("Enter the cumulative shipped quantity (0 or more).")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fill("New cumulative shipped", "1300");
    fireEvent.click(screen.getByRole("button", { name: "Record shipped quantity" }));
    expect(await screen.findByText(/Shipped quantity recorded/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}:record-shipped`);
    expect(post?.headers.get("If-Match")).toBe('"3"');
    expect(post?.body).toEqual({ cumShipped: "1300" });
  });

  it("shows the service's refusal when the shipped quantity would fall", async () => {
    handler = (url, init) =>
      init.method === "POST"
        ? { status: 422, body: { code: "SLS-0040", detail: "cumulative shipped quantity cannot decrease without a later reset date" } }
        : url.pathname.endsWith("/schedule-releases") ? { status: 200, body: { items: [] } } : { status: 200, body: agreement, etag: '"3"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases`);
    await screen.findByRole("heading", { name: "Scheduling releases for SA-0000007" });
    await screen.findByLabelText("New cumulative shipped");
    fill("New cumulative shipped", "5");
    fireEvent.click(screen.getByRole("button", { name: "Record shipped quantity" }));
    expect(await screen.findByText(/cannot decrease/)).toBeInTheDocument();
  });

  it("the new release form validates, posts with an Idempotency-Key and opens the release", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: held, etag: '"1"' } : { status: 200, body: held, etag: '"1"' });
    const { container } = renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases/new`);
    fireEvent.click(await screen.findByRole("button", { name: "Store release" }));
    expect(screen.getByText("Enter the customer's release number (up to 40 characters).")).toBeInTheDocument();
    expect(screen.getByText("Choose the requirement date.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    expect(pageViolations(container)).toEqual([]);

    fill("Release number", "R-17");
    fill("Type", "JIT");
    fill("Cumulative received (customer)", "1150");
    fill("Date", "2027-04-15");
    fill("Cumulative required", "1500");
    fireEvent.click(screen.getByRole("button", { name: "Store release" }));
    expect(await screen.findByRole("heading", { name: "Release R-17" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}/schedule-releases`);
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ releaseNo: "R-17", releaseType: "JIT", cumReceived: "1150", buckets: [{ date: "2027-04-15", cumRequired: "1500" }] });
  });

  it("names a duplicate release number", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0038", detail: "release R-17 was already received for agreement SA-0000007" } });
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases/new`);
    fill("Release number", "R-17");
    fill("Cumulative received (customer)", "1");
    fill("Date", "2027-04-15");
    fill("Cumulative required", "2");
    fireEvent.click(await screen.findByRole("button", { name: "Store release" }));
    expect(await screen.findByText(/already received/)).toBeInTheDocument();
  });

  it("a held release shows the mismatch and is rejected at its ETag with a reason", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 200, body: { ...held, status: "REJECTED", version: 2 }, etag: '"2"' } : { status: 200, body: held, etag: '"1"' });
    const { container } = renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases/${RELEASE_ID}`);
    expect(await screen.findByText("Held: cumulative quantity mismatch")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "No open lines" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Reject release" }));
    expect(screen.getByText("Enter a reason of up to 500 characters.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fill("Reason", "customer will send a corrected release");
    fireEvent.click(screen.getByRole("button", { name: "Reject release" }));
    await screen.findByRole("button", { name: "Reject release" });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}/schedule-releases/${RELEASE_ID}:resolve`);
    expect(post?.headers.get("If-Match")).toBe('"1"');
    expect(post?.body).toEqual({ decision: "REJECT", reason: "customer will send a corrected release" });
  });

  it("an accepted release lists only its open lines and offers no review", async () => {
    const recalculated = {
      ...base,
      shippedOffset: "350",
      lines: [
        { id: "l0", dueDate: "2027-04-01", quantity: "500", status: "SUPERSEDED" },
        { id: "l1", dueDate: "2027-04-15", quantity: "300", status: "OPEN" },
      ],
    };
    handler = () => ({ status: 200, body: recalculated, etag: '"1"' });
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases/${RELEASE_ID}`);
    const table = await findTable({ name: "Open schedule lines" });
    expect(within(table).getByText("2027-04-15")).toBeInTheDocument();
    expect(within(table).queryByText("2027-04-01")).not.toBeInTheDocument();
    expect(screen.getByText("Shipped before the reset")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject release" })).not.toBeInTheDocument();
  });

  it("shows the service's refusal of an override when a newer release is in effect", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 422, body: { code: "SLS-0039", detail: "a newer release of agreement SA-0000007 is in effect; reject this one" } } : { status: 200, body: held, etag: '"1"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases/${RELEASE_ID}`);
    await screen.findByText("Held: cumulative quantity mismatch");
    fill("Decision", "ACCEPT");
    fill("Reason", "override");
    fireEvent.click(screen.getByRole("button", { name: "Accept release" }));
    expect(await screen.findByText(/newer release/)).toBeInTheDocument();
  });

  it("an unknown release shows the error state", async () => {
    handler = () => ({ status: 404, body: { code: "SLS-0110", detail: "schedule release not found" } });
    renderAt(`/sales/agreements/${AGREEMENT_ID}/schedule-releases/${RELEASE_ID}`);
    expect(await screen.findByText("Could not load the release")).toBeInTheDocument();
  });

  it("validateScheduleRelease mirrors the service rules", () => {
    const ok = { releaseNo: "R-1", releaseType: "FORECAST" as const, cumReceived: "10", buckets: [{ date: "2027-04-15", cumRequired: "20" }] };
    expect(validateScheduleRelease(ok).input).toEqual(ok);
    expect(validateScheduleRelease({ ...ok, releaseNo: " " }).errors.releaseNo).toBeDefined();
    expect(validateScheduleRelease({ ...ok, cumReceived: "-1" }).errors.cumReceived).toBeDefined();
    expect(validateScheduleRelease({ ...ok, buckets: [] }).errors.buckets).toBeDefined();
    const two = { ...ok, buckets: [{ date: "2027-04-15", cumRequired: "20" }, { date: "2027-04-01", cumRequired: "10" }] };
    const { errors } = validateScheduleRelease(two);
    expect(errors["buckets[1].date"]).toMatch(/on or after/);
    expect(errors["buckets[1].cumRequired"]).toMatch(/go down/);
  });
});

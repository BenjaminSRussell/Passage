import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { Agreement } from "./api/client";
import { revisionValues, validateRevision } from "./AgreementLifecycle";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const AGREEMENT_ID = "88888888-0000-4000-8000-000000000008";

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

function renderAt(path: string, duties: string[] = ["sales.order.read", "sales.agreement.maintain"]) {
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

const agreement: Agreement = {
  id: AGREEMENT_ID,
  legalEntityId: LE,
  agreementNumber: "SA-0000007",
  customerId: CUSTOMER,
  invoiceAccountId: CUSTOMER,
  productId: PRODUCT,
  unit: "EA",
  unitPrice: "12.5",
  currency: "USD",
  commitmentQty: "1000",
  releasedQty: "600",
  remainingQty: "400",
  status: "ACTIVE",
  version: 2,
  createdBy: "key-account",
  createdAt: "2027-03-01T09:00:00Z",
  updatedAt: "2027-03-02T09:00:00Z",
  releases: [
    { id: "r1", quantity: "600", salesOrderId: ORDER_ID, orderNumber: "SO-0000041", actor: "key-account", releasedAt: "2027-03-02T09:00:00Z" },
    { id: "r0", quantity: "50", salesOrderId: ORDER_ID, orderNumber: "SO-0000040", actor: "key-account", releasedAt: "2027-03-01T10:00:00Z", returnedAt: "2027-03-03T08:00:00Z" },
  ],
};

const history = [
  { version: 3, kind: "REVISED", actor: "key-account", reason: "volume deal", changedAt: "2027-03-03T09:00:00Z", diff: [{ lineNo: 0, field: "commitmentQty", old: "800", new: "1000" }] },
  { version: 2, kind: "RETURNED", actor: "rep", reason: "order SO-0000040 cancelled", changedAt: "2027-03-03T08:00:00Z", diff: [{ lineNo: 0, field: "releasedQty", old: "650", new: "600" }] },
];

describe("validateRevision", () => {
  it("sends only the changed fields and mirrors the service rules", () => {
    const base = revisionValues(agreement);
    expect(validateRevision(base, agreement).errors.form).toMatch(/at least one/);
    expect(validateRevision({ ...base, commitmentQty: "1500", reason: " deal " }, agreement).input).toEqual({ commitmentQty: "1500", reason: "deal" });
    expect(validateRevision({ ...base, commitmentQty: "599" }, agreement).errors.commitmentQty).toMatch(/below the 600/);
    expect(validateRevision({ ...base, commitmentQty: "0" }, agreement).errors.commitmentQty).toMatch(/above 0/);
    expect(validateRevision({ ...base, unitPrice: "1.00001" }, agreement).errors.unitPrice).toMatch(/4 decimals/);
    expect(validateRevision({ ...base, validTo: "soon" }, agreement).errors.validTo).toBeDefined();
    expect(validateRevision({ ...base, validFrom: "2027-05-01", validTo: "2027-04-01" }, agreement).errors.validTo).toMatch(/before the start/);
    // decimals compare exactly, never through float
    expect(validateRevision({ ...base, commitmentQty: "1000.000000" }, agreement).errors.form).toMatch(/at least one/);
    const big = { ...agreement, commitmentQty: "12345678901234.5", releasedQty: "12345678901234.5" };
    expect(validateRevision({ ...revisionValues(big), commitmentQty: "12345678901234.500001" }, big).input).toEqual({ commitmentQty: "12345678901234.500001" });
    expect(validateRevision({ ...revisionValues(big), commitmentQty: "12345678901234.499999" }, big).errors.commitmentQty).toMatch(/below/);
    const open = { ...agreement, validTo: "2027-12-31" };
    expect(validateRevision({ ...revisionValues(open), validTo: "" }, open).input).toEqual({ validTo: "" });
  });
});

describe("validateRevision with an over-delivery tolerance", () => {
  const tolerant: Agreement = { ...agreement, releasedQty: "1040", remainingQty: "10", overdeliveryPct: "5", ceilingQty: "1050" };
  it("sends the changed tolerance and leaves the below-released check to the service", () => {
    const base = revisionValues(tolerant);
    expect(base.overdeliveryPct).toBe("5");
    expect(validateRevision({ ...base, overdeliveryPct: "10" }, tolerant).input).toEqual({ overdeliveryPct: "10" });
    expect(validateRevision({ ...base, overdeliveryPct: "101" }, tolerant).errors.overdeliveryPct).toMatch(/0 to 100/);
    expect(validateRevision({ ...base, overdeliveryPct: "" }, tolerant).errors.overdeliveryPct).toBeDefined();
    // 991 is below the released 1040 but within the ceiling, so only the service can judge it
    expect(validateRevision({ ...base, commitmentQty: "991" }, tolerant).input).toEqual({ commitmentQty: "991" });
    expect(validateRevision({ ...revisionValues(agreement), commitmentQty: "599" }, agreement).errors.commitmentQty).toMatch(/below the 600/);
  });
});

describe("Agreement lifecycle", () => {
  it("shows the tolerance, the ceiling, and what each release drew and gave back", async () => {
    const withDraws: Agreement = {
      ...agreement,
      overdeliveryPct: "5",
      ceilingQty: "1050",
      remainingQty: "450",
      releases: [{ id: "r1", quantity: "600", drawnQty: "40", returnedQty: "15", salesOrderId: ORDER_ID, orderNumber: "SO-0000041", actor: "key-account", releasedAt: "2027-03-02T09:00:00Z", returnedAt: "2027-03-04T09:00:00Z" }],
    };
    handler = (url) => (url.pathname.endsWith("/versions") ? { status: 200, body: { items: [{ version: 4, kind: "DRAWN", actor: "rep", reason: "quantity of order SO-0000041 raised", changedAt: "2027-03-05T09:00:00Z", diff: [] }] } } : { status: 200, body: withDraws, etag: '"2"' });
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    expect(screen.getByText("5% (up to 1050 EA)")).toBeInTheDocument();
    const releases = await findTable({ name: "Releases" });
    expect(within(releases).getByText("40")).toBeInTheDocument();
    expect(within(releases).getByText("15")).toBeInTheDocument();
    expect(await screen.findByText("Quantity drawn")).toBeInTheDocument();
  });

  it("revises the tolerance", async () => {
    handler = (url, init) =>
      init.method === "PATCH"
        ? { status: 200, body: { ...agreement, overdeliveryPct: "7.5", version: 3 }, etag: '"3"' }
        : url.pathname.endsWith("/versions")
          ? { status: 200, body: { items: [] } }
          : { status: 200, body: agreement, etag: '"2"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    const form = screen.getByRole("form", { name: "Revise the agreement" });
    fireEvent.change(within(form).getByLabelText(/Over-delivery tolerance/), { target: { value: "7.5" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save revision" }));
    expect(await screen.findByText("Agreement revised to version 3.")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({ overdeliveryPct: "7.5" });
  });

  it("revises at the ETag, sends the diff only and reloads", async () => {
    let revised = false;
    handler = (url, init) => {
      if (init.method === "PATCH") {
        revised = true;
        return { status: 200, body: { ...agreement, commitmentQty: "1500", remainingQty: "900", version: 3 }, etag: '"3"' };
      }
      if (url.pathname.endsWith("/versions")) return { status: 200, body: { items: revised ? history : [] } };
      return { status: 200, body: revised ? { ...agreement, commitmentQty: "1500", version: 3 } : agreement, etag: revised ? '"3"' : '"2"' };
    };
    const { container } = renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    expect(await screen.findByRole("heading", { name: "Sales agreement SA-0000007" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { name: "No changes yet" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const form = screen.getByRole("form", { name: "Revise the agreement" });
    fireEvent.click(within(form).getByRole("button", { name: "Save revision" }));
    expect(await within(form).findByText("Change at least one value.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);

    fireEvent.change(within(form).getByLabelText(/Committed quantity/), { target: { value: "1500" } });
    fireEvent.change(within(form).getByLabelText(/Reason/), { target: { value: "volume deal" } });
    fireEvent.click(within(form).getByRole("button", { name: "Save revision" }));
    expect(await screen.findByText("Agreement revised to version 3.")).toBeInTheDocument();
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}`);
    expect(patch?.headers.get("If-Match")).toBe('"2"');
    expect(patch?.body).toEqual({ commitmentQty: "1500", reason: "volume deal" });
    const table = await findTable({ name: "Agreement history" });
    expect(within(table).getByText("Revised")).toBeInTheDocument();
    expect(within(table).getByText("Quantity returned")).toBeInTheDocument();
  });

  it("shows the service's refusal of a commitment below the released quantity", async () => {
    handler = (url, init) =>
      init.method === "PATCH"
        ? { status: 422, body: { code: "SLS-0053", detail: "commitment cannot fall below the 600 EA already released" } }
        : url.pathname.endsWith("/versions")
          ? { status: 200, body: { items: [] } }
          : { status: 200, body: { ...agreement, releasedQty: "0" }, etag: '"2"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    fireEvent.change(screen.getByLabelText(/Agreed price/), { target: { value: "13" } });
    fireEvent.click(screen.getByRole("button", { name: "Save revision" }));
    expect(await screen.findByText("Commitment is below the released quantity")).toBeInTheDocument();
    expect(screen.getByText(/SLS-0053/)).toBeInTheDocument();
  });

  it("closes with a reason and shows the returned releases", async () => {
    let closed = false;
    handler = (url, init) => {
      if (init.method === "POST") {
        closed = true;
        return { status: 200, body: { ...agreement, status: "CLOSED", version: 3 }, etag: '"3"' };
      }
      if (url.pathname.endsWith("/versions")) return { status: 200, body: { items: [] } };
      return { status: 200, body: closed ? { ...agreement, status: "CLOSED", closeReason: "customer left", closedBy: "ka", closedAt: "2027-03-04T09:00:00Z", version: 3 } : agreement, etag: closed ? '"3"' : '"2"' };
    };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    const releases = await findTable({ name: "Releases" });
    expect(within(releases).getAllByRole("row")).toHaveLength(3);
    const form = screen.getByRole("form", { name: "Close the agreement" });
    fireEvent.click(within(form).getByRole("button", { name: "Close agreement" }));
    expect(await within(form).findByText("Give the reason for closing the agreement.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText(/Reason/), { target: { value: "customer left" } });
    fireEvent.click(within(form).getByRole("button", { name: "Close agreement" }));
    expect(await screen.findByRole("heading", { name: "Agreement closed" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}:close`);
    expect(post?.headers.get("If-Match")).toBe('"2"');
    expect(post?.body).toEqual({ reason: "customer left" });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Release order" })).not.toBeInTheDocument());
    expect(screen.queryByRole("form", { name: "Revise the agreement" })).not.toBeInTheDocument();
    expect(screen.getByText(/customer left/)).toBeInTheDocument();
  });

  it("carries the agreement price to the released orders and shows what was repriced and left", async () => {
    const result = {
      agreement: { ...agreement, version: 3 },
      repriced: [{ orderId: ORDER_ID, orderNumber: "SO-0000041", oldPrice: "12", newPrice: "12.5", netTotal: "7500", version: 2 }],
      skipped: [{ orderId: "o2", orderNumber: "SO-0000042", reason: "order is CONFIRMED: change its price with a change request" }],
      more: true,
    };
    handler = (url, init) => {
      if (init.method === "POST") return { status: 200, body: result, etag: '"3"' };
      if (url.pathname.endsWith("/versions")) return { status: 200, body: { items: [{ version: 3, kind: "REPRICED", actor: "ka", reason: "", changedAt: "2027-03-04T09:00:00Z", diff: [] }] } };
      return { status: 200, body: agreement, etag: '"2"' };
    };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    fireEvent.click(screen.getByRole("button", { name: "Reprice released orders" }));
    const repriced = await findTable({ name: "Repriced orders" });
    expect(within(repriced).getByText("SO-0000041")).toBeInTheDocument();
    expect(within(repriced).getByText("7500 USD")).toBeInTheDocument();
    const skipped = await findTable({ name: "Orders left at their price" });
    expect(within(skipped).getByText(/CONFIRMED/)).toBeInTheDocument();
    expect(screen.getByText(/Run it again/)).toBeInTheDocument();
    expect(screen.getByText("Orders repriced")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/agreements/${AGREEMENT_ID}:reprice-orders`);
    expect(post?.headers.get("If-Match")).toBe('"2"');
    expect(pageViolations(document.body)).toEqual([]);
  });

  it("says when no order needed repricing and shows the service's refusal", async () => {
    let refuse = false;
    handler = (url, init) => {
      if (init.method === "POST") return refuse ? { status: 422, body: { code: "SLS-0052", detail: "agreement SA-0000007 is closed" } } : { status: 200, body: { agreement, repriced: [], skipped: [], more: false }, etag: '"2"' };
      if (url.pathname.endsWith("/versions")) return { status: 200, body: { items: [] } };
      return { status: 200, body: agreement, etag: '"2"' };
    };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    fireEvent.click(screen.getByRole("button", { name: "Reprice released orders" }));
    expect(await screen.findByText("No released order needed repricing.")).toBeInTheDocument();
    refuse = true;
    fireEvent.click(screen.getByRole("button", { name: "Reprice released orders" }));
    expect(await screen.findByText("The agreement is closed")).toBeInTheDocument();
  });

  it("hides the actions from a caller without sales.agreement.maintain and reports a failed history", async () => {
    handler = (url) => (url.pathname.endsWith("/versions") ? { status: 500, body: { code: "COM-0500", detail: "boom" } } : { status: 200, body: agreement, etag: '"2"' });
    renderAt(`/sales/agreements/${AGREEMENT_ID}`, ["sales.order.read"]);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000007" });
    expect(await screen.findByText("Could not load the history")).toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Revise the agreement" })).not.toBeInTheDocument();
    expect(screen.queryByRole("form", { name: "Close the agreement" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reprice released orders" })).not.toBeInTheDocument();
  });
});

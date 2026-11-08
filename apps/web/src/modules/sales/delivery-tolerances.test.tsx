import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { DUTY_DELIVERY_TOLERANCE_MAINTAIN, percentText, validateTolerance } from "./DeliveryTolerancesPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const RULE_ID = "88888888-0000-4000-8000-000000000008";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body?: unknown; etag?: string };
let handler: (url: URL, init: RequestInit) => Reply | Promise<Reply>;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body, etag } = await handler(url, init);
      const headers: Record<string, string> = { "Content-Type": "application/json" };
      if (etag) headers.ETag = etag;
      return new Response(status === 204 ? null : JSON.stringify(body ?? {}), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string, duties: string[] = ["sales.order.read", DUTY_DELIVERY_TOLERANCE_MAINTAIN]) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {sales.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
          <Route path="*" element={<p>Elsewhere</p>} />
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}

const rule = {
  id: RULE_ID,
  legalEntityId: LE,
  productId: PRODUCT,
  customerId: CUSTOMER,
  overdeliveryPct: "5",
  underdeliveryPct: "2.5",
  partialDelivery: "COMPLETE_ONLY",
  version: 1,
  updatedBy: "admin",
  createdAt: "2027-02-20T09:00:00Z",
  updatedAt: "2027-02-20T09:00:00Z",
};

describe("validateTolerance", () => {
  const ok = { legalEntityId: LE, productId: PRODUCT, customerId: "", overdeliveryPct: "5", underdeliveryPct: "2.5", partialDelivery: "ALLOWED" as const };
  it("leaves the customer out for every customer", () => {
    const { input } = validateTolerance(ok, true);
    expect(input).toEqual({ legalEntityId: LE, productId: PRODUCT, overdeliveryPct: "5", underdeliveryPct: "2.5", partialDelivery: "ALLOWED" });
    expect(validateTolerance({ ...ok, customerId: CUSTOMER }, true).input?.customerId).toBe(CUSTOMER);
  });
  it("mirrors the service bounds", () => {
    expect(Object.keys(validateTolerance({ ...ok, overdeliveryPct: "100.01", underdeliveryPct: "1.005" }, false).errors)).toEqual(["overdeliveryPct", "underdeliveryPct"]);
    expect(Object.keys(validateTolerance({ ...ok, legalEntityId: "x", productId: "y", customerId: "z" }, true).errors)).toEqual(["legalEntityId", "productId", "customerId"]);
    expect(validateTolerance({ ...ok, overdeliveryPct: "100", underdeliveryPct: "0" }, false).input).toBeDefined();
  });
  it("shows percentages", () => {
    expect(percentText("2.5")).toBe("2.5%");
  });
});

describe("Delivery tolerance pages", () => {
  it("lists rules, filters, and passes axe", async () => {
    handler = () => ({ status: 200, body: { items: [rule, { ...rule, id: "x", customerId: undefined }] } });
    const { container } = renderAt("/sales/delivery-tolerances");
    const table = await findTable({ name: "Delivery tolerances" });
    await waitFor(() => expect(within(table).getByText("Every customer")).toBeInTheDocument());
    expect(within(table).getAllByText("2.5%")).toHaveLength(2);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getAllByLabelText(/^Product/)[0]!, { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("productId")).toBe(PRODUCT));
  });

  it("shows an empty state and hides the create link without the duty", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/delivery-tolerances", ["sales.order.read"]);
    expect(await screen.findByRole("heading", { name: "No delivery tolerances" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New delivery tolerance" })).not.toBeInTheDocument();
  });

  it("shows the problem code when the list fails and recovers on retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "down" } } : { status: 200, body: { items: [rule] } });
    renderAt("/sales/delivery-tolerances");
    expect(await screen.findByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await findTable({ name: "Delivery tolerances" });
  });

  it("checks what applies and shows the shipment range", async () => {
    handler = (url) => (url.pathname.endsWith(":resolve") ? { status: 200, body: { source: "CUSTOMER", toleranceId: RULE_ID, overdeliveryPct: "5", underdeliveryPct: "2", partialDelivery: "COMPLETE_ONLY", orderedQty: "1000", minQty: "980", maxQty: "1050" } } : { status: 200, body: { items: [] } });
    renderAt("/sales/delivery-tolerances");
    const form = await screen.findByRole("form", { name: "Check the delivery tolerance" });
    fireEvent.click(within(form).getByRole("button", { name: "Check" }));
    expect(await within(form).findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls.some((c) => c.url.pathname.endsWith(":resolve"))).toBe(false);
    fireEvent.change(within(form).getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(within(form).getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.change(within(form).getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(within(form).getByLabelText(/^Ordered quantity/), { target: { value: "1000" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check" }));
    expect(await screen.findByText("980 to 1050")).toBeInTheDocument();
    expect(screen.getByText("The customer's own rule")).toBeInTheDocument();
    const call = calls.find((c) => c.url.pathname.endsWith(":resolve"));
    expect(call?.url.searchParams.get("orderedQty")).toBe("1000");
    expect(call?.url.searchParams.get("customerId")).toBe(CUSTOMER);
  });

  it("creates a rule with an Idempotency-Key and opens it", async () => {
    handler = (_u, init) => (init.method === "POST" ? { status: 201, body: rule, etag: '"1"' } : { status: 200, body: rule, etag: '"1"' });
    renderAt("/sales/delivery-tolerances/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.change(screen.getByLabelText(/^Customer/), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText(/^Over-delivery tolerance/), { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText(/^Under-delivery tolerance/), { target: { value: "2.5" } });
    fireEvent.change(screen.getByLabelText("Partial delivery"), { target: { value: "COMPLETE_ONLY" } });
    fireEvent.click(screen.getByRole("button", { name: "Save delivery tolerance" }));
    await screen.findByRole("form", { name: "Change the delivery tolerance" });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, productId: PRODUCT, customerId: CUSTOMER, overdeliveryPct: "5", underdeliveryPct: "2.5", partialDelivery: "COMPLETE_ONLY" });
  });

  it("does not call the service for invalid input and reports SLS-0126", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0126", detail: "exists" } });
    renderAt("/sales/delivery-tolerances/new");
    fireEvent.click(screen.getByRole("button", { name: "Save delivery tolerance" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Product"), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Save delivery tolerance" }));
    expect(await screen.findByText("This product already has a rule for that customer")).toBeInTheDocument();
    expect(calls[0]?.body).not.toHaveProperty("customerId");
  });

  it("changes a rule under If-Match and shows the stale-version problem", async () => {
    let stale = false;
    handler = (_u, init) => {
      if (init.method === "PUT") return stale ? { status: 412, body: { code: "SLS-0006", detail: "stale" } } : { status: 200, body: { ...rule, overdeliveryPct: "10", version: 2 }, etag: '"2"' };
      return { status: 200, body: rule, etag: '"1"' };
    };
    renderAt(`/sales/delivery-tolerances/${RULE_ID}`);
    await screen.findByRole("form", { name: "Change the delivery tolerance" });
    fireEvent.change(screen.getByLabelText(/^Over-delivery tolerance/), { target: { value: "10" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The delivery tolerance is saved.")).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.headers.get("If-Match")).toBe('"1"');
    expect(put?.body).toEqual({ overdeliveryPct: "10", underdeliveryPct: "2.5", partialDelivery: "COMPLETE_ONLY" });
    stale = true;
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed this tolerance")).toBeInTheDocument();
  });

  it("removes a rule after confirmation and returns to the list", async () => {
    handler = (_u, init) => (init.method === "DELETE" ? { status: 204 } : { status: 200, body: { items: [], ...rule }, etag: '"1"' });
    renderAt(`/sales/delivery-tolerances/${RULE_ID}`);
    await screen.findByRole("form", { name: "Change the delivery tolerance" });
    fireEvent.click(screen.getByRole("button", { name: "Remove tolerance" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove tolerance" }));
    await waitFor(() => expect(calls.some((c) => c.method === "DELETE" && c.url.pathname.endsWith(RULE_ID))).toBe(true));
    expect(await screen.findByRole("heading", { name: "No delivery tolerances" })).toBeInTheDocument();
  });

  it("shows a failed removal and keeps the rule", async () => {
    handler = (_u, init) => (init.method === "DELETE" ? { status: 404, body: { code: "SLS-0127", detail: "gone" } } : { status: 200, body: rule, etag: '"1"' });
    renderAt(`/sales/delivery-tolerances/${RULE_ID}`);
    await screen.findByRole("form", { name: "Change the delivery tolerance" });
    fireEvent.click(screen.getByRole("button", { name: "Remove tolerance" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Remove tolerance" }));
    expect(await screen.findByText(/SLS-0127/)).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("is read only without the maintain duty", async () => {
    handler = () => ({ status: 200, body: rule, etag: '"1"' });
    renderAt(`/sales/delivery-tolerances/${RULE_ID}`, ["sales.order.read"]);
    await screen.findByRole("form", { name: "Change the delivery tolerance" });
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove tolerance" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the sales.parameter.maintain duty/)).toBeInTheDocument();
  });

  it("shows an error with a retry when the rule cannot be loaded", async () => {
    handler = () => ({ status: 404, body: { code: "SLS-0127", detail: "not found" } });
    renderAt(`/sales/delivery-tolerances/${RULE_ID}`);
    expect(await screen.findByText(/SLS-0127/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { validateTerms, weekdayNames, DUTY_DELIVERY_TERMS_MAINTAIN } from "./DeliveryTermsPage";
import { validateParameters } from "./ParametersPage";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const TERMS_ID = "77777777-0000-4000-8000-000000000007";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
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
      return new Response(JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderAt(path: string, duties: string[] = ["sales.order.read", DUTY_DELIVERY_TERMS_MAINTAIN]) {
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

const terms = {
  id: TERMS_ID,
  legalEntityId: LE,
  customerId: CUSTOMER,
  transportDays: 2,
  receivingDays: [1, 2, 3, 4, 5],
  version: 1,
  updatedBy: "admin",
  createdAt: "2027-02-20T09:00:00Z",
  updatedAt: "2027-02-20T09:00:00Z",
};

describe("validateTerms", () => {
  const ok = { legalEntityId: LE, customerId: CUSTOMER, transportDays: "2", receivingDays: [5, 1] };
  it("sorts the weekdays and returns numbers", () => {
    expect(validateTerms(ok, true).input).toEqual({ legalEntityId: LE, customerId: CUSTOMER, transportDays: 2, receivingDays: [1, 5] });
  });
  it("mirrors the service bounds", () => {
    expect(Object.keys(validateTerms({ ...ok, transportDays: "61", receivingDays: [] }, false).errors)).toEqual(["transportDays", "receivingDays"]);
    expect(Object.keys(validateTerms({ ...ok, legalEntityId: "x", customerId: "y" }, true).errors)).toEqual(["legalEntityId", "customerId"]);
  });
  it("names weekdays", () => {
    expect(weekdayNames([1, 3, 5])).toBe("Monday".slice(0, 3) + ", Wed, Fri");
    expect(weekdayNames([1, 2, 3, 4, 5, 6, 7])).toBe("Every day");
  });
});

describe("sales lead time parameters", () => {
  const ok = { quotationExpiryDays: "30", quotationProbabilityDefault: "50", creditReleaseValidityDays: "7", creditHoldSlaBusinessDays: "2", defaultOrderType: "ORDER" as const, creditCheckPoints: ["confirm" as const], intakeDedupWindowDays: "30" };
  it("sends the control and lead time", () => {
    const r = validateParameters({ ...ok, deliveryDateControlDefault: "SALES_LEAD_TIME", salesLeadTimeDays: "4" });
    expect(r.input).toMatchObject({ deliveryDateControlDefault: "SALES_LEAD_TIME", salesLeadTimeDays: 4 });
  });
  it("refuses a lead time above 365", () => {
    expect(Object.keys(validateParameters({ ...ok, salesLeadTimeDays: "366" }).errors)).toEqual(["salesLeadTimeDays"]);
  });
});

describe("Delivery terms pages", () => {
  it("lists terms, filters, and passes axe", async () => {
    handler = () => ({ status: 200, body: { items: [terms] } });
    const { container } = renderAt("/sales/delivery-terms");
    const table = await findTable({ name: "Delivery terms" });
    await waitFor(() => expect(within(table).getByText("Mon, Tue, Wed, Thu, Fri")).toBeInTheDocument());
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText(/^Customer/), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("customerId")).toBe(CUSTOMER));
  });

  it("shows an empty state and hides the create link without the duty", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/delivery-terms", ["sales.order.read"]);
    expect(await screen.findByRole("heading", { name: "No delivery terms" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New delivery terms" })).not.toBeInTheDocument();
  });

  it("shows the problem code when the list fails and recovers on retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "down" } } : { status: 200, body: { items: [terms] } });
    renderAt("/sales/delivery-terms");
    expect(await screen.findByText(/COM-0503/)).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await findTable({ name: "Delivery terms" });
  });

  it("creates terms with an Idempotency-Key and opens them", async () => {
    handler = (_u, init) => (init.method === "POST" ? { status: 201, body: terms, etag: '"1"' } : { status: 200, body: terms, etag: '"1"' });
    renderAt("/sales/delivery-terms/new");
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.change(screen.getByLabelText("Transport days"), { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save delivery terms" }));
    await screen.findByRole("form", { name: "Change the delivery terms" });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toEqual({ legalEntityId: LE, customerId: CUSTOMER, transportDays: 2, receivingDays: [1, 2, 3, 4, 5] });
  });

  it("does not call the service for invalid input and reports SLS-0056", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0056", detail: "exists" } });
    renderAt("/sales/delivery-terms/new");
    fireEvent.click(screen.getByRole("button", { name: "Save delivery terms" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(screen.getByLabelText("Customer"), { target: { value: CUSTOMER } });
    fireEvent.click(screen.getByRole("button", { name: "Save delivery terms" }));
    expect(await screen.findByText("This customer already has delivery terms")).toBeInTheDocument();
  });

  it("changes terms under If-Match and shows the stale-version problem", async () => {
    let stale = false;
    handler = (_u, init) => {
      if (init.method === "PUT") return stale ? { status: 412, body: { code: "SLS-0006", detail: "stale" } } : { status: 200, body: { ...terms, transportDays: 4, version: 2 }, etag: '"2"' };
      return { status: 200, body: terms, etag: '"1"' };
    };
    renderAt(`/sales/delivery-terms/${TERMS_ID}`);
    await screen.findByRole("form", { name: "Change the delivery terms" });
    fireEvent.change(screen.getByLabelText("Transport days"), { target: { value: "4" } });
    fireEvent.click(screen.getByLabelText("Saturday"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("The delivery terms are saved.")).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.headers.get("If-Match")).toBe('"1"');
    expect(put?.body).toEqual({ transportDays: 4, receivingDays: [1, 2, 3, 4, 5, 6] });
    stale = true;
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Someone else changed these terms")).toBeInTheDocument();
  });

  it("is read only without the maintain duty", async () => {
    handler = () => ({ status: 200, body: terms, etag: '"1"' });
    renderAt(`/sales/delivery-terms/${TERMS_ID}`, ["sales.order.read"]);
    await screen.findByRole("form", { name: "Change the delivery terms" });
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
    expect(screen.getByText(/needs the sales.parameter.maintain duty/)).toBeInTheDocument();
  });
});

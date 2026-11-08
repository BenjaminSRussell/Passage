import { fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { findTable } from "../../test-utils";
import type { Agreement } from "./api/client";
import { revisionValues, validateRevision } from "./AgreementLifecycle";
import { validateAgreement } from "./AgreementsPage";
import { commitmentText } from "./format";
import sales from "./module";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const SITE = "44444444-0000-4000-8000-000000000004";
const WAREHOUSE = "55555555-0000-4000-8000-000000000005";
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

const fill = (label: string | RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const amountAgreement: Agreement = {
  id: AGREEMENT_ID,
  legalEntityId: LE,
  agreementNumber: "SA-0000009",
  customerId: CUSTOMER,
  invoiceAccountId: CUSTOMER,
  productId: PRODUCT,
  unit: "EA",
  unitPrice: "12",
  currency: "USD",
  commitmentType: "AMOUNT",
  commitmentUnit: "USD",
  commitmentQty: "1000",
  releasedQty: "600",
  remainingQty: "400",
  status: "ACTIVE",
  version: 2,
  createdBy: "key-account",
  createdAt: "2027-03-01T09:00:00Z",
  updatedAt: "2027-03-02T09:00:00Z",
  releases: [{ id: "r1", quantity: "50", units: "600", salesOrderId: ORDER_ID, orderNumber: "SO-0000041", actor: "key-account", releasedAt: "2027-03-02T09:00:00Z" }],
};

const base = { legalEntityId: LE, customerId: CUSTOMER, invoiceAccountId: "", productId: PRODUCT, unit: "EA", unitPrice: "12", currency: "USD", commitmentQty: "", validFrom: "", validTo: "", cumTolerance: "", overdeliveryPct: "" };

describe("Sales agreements committed by amount", () => {
  it("validateAgreement sends commitmentAmount for an AMOUNT agreement and checks it", () => {
    const ok = validateAgreement({ ...base, commitmentType: "AMOUNT", commitmentAmount: "25000.50" });
    expect(ok.input).toMatchObject({ commitmentType: "AMOUNT", commitmentAmount: "25000.50" });
    expect(ok.input).not.toHaveProperty("commitmentQty");
    expect(validateAgreement({ ...base, commitmentType: "AMOUNT", commitmentAmount: "0" }).errors.commitmentAmount).toMatch(/above 0/);
    expect(validateAgreement({ ...base, commitmentType: "AMOUNT", commitmentAmount: "1.00001" }).errors.commitmentAmount).toMatch(/4 decimals/);
    expect(validateAgreement({ ...base, commitmentType: "AMOUNT", commitmentAmount: "100", unitPrice: "0" }).errors.unitPrice).toMatch(/above 0/);
    const quantity = validateAgreement({ ...base, commitmentType: "QUANTITY", commitmentQty: "10" });
    expect(quantity.input).toMatchObject({ commitmentQty: "10" });
    expect(quantity.input).not.toHaveProperty("commitmentAmount");
  });

  it("the new agreement form switches to a committed amount and posts it", async () => {
    handler = (_url, init) => (init.method === "POST" ? { status: 201, body: amountAgreement, etag: '"1"' } : { status: 200, body: amountAgreement, etag: '"2"' });
    const { container } = renderAt("/sales/agreements/new");
    expect(await screen.findByLabelText("Committed quantity")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Commitment by"), { target: { value: "AMOUNT" } });
    expect(screen.queryByLabelText("Committed quantity")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(screen.getByText("Enter an amount above 0 with up to 4 decimals.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
    fill("Legal entity", LE);
    fill("Customer", CUSTOMER);
    fill("Product", PRODUCT);
    fill("Committed amount", "1000");
    fill("Unit price", "12");
    fireEvent.click(screen.getByRole("button", { name: "Save agreement" }));
    expect(await screen.findByRole("heading", { name: "Sales agreement SA-0000009" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ legalEntityId: LE, customerId: CUSTOMER, productId: PRODUCT, unit: "EA", unitPrice: "12", currency: "USD", commitmentType: "AMOUNT", commitmentAmount: "1000" });
  });

  it("the list and the detail show money, and the release table shows what each release drew", async () => {
    handler = (url) =>
      url.pathname.endsWith(AGREEMENT_ID) ? { status: 200, body: amountAgreement, etag: '"2"' } : url.pathname.endsWith("/versions") ? { status: 200, body: { items: [] } } : { status: 200, body: { items: [amountAgreement] } };
    const list = renderAt("/sales/agreements");
    const table = await findTable({ name: "Sales agreements" });
    expect(within(table).getByText("1000 USD")).toBeInTheDocument();
    expect(within(table).getByText("400 USD")).toBeInTheDocument();
    list.unmount();

    const { container } = renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    expect(await screen.findByRole("heading", { name: "Sales agreement SA-0000009" })).toBeInTheDocument();
    expect(screen.getByText("Committed by")).toBeInTheDocument();
    expect(screen.getByText("Amount")).toBeInTheDocument();
    const releases = screen.getByRole("table", { name: "Releases" });
    expect(within(releases).getByText("Drawn at release")).toBeInTheDocument();
    expect(within(releases).getByText("600 USD")).toBeInTheDocument();
    expect(within(releases).getAllByText("0 USD")).toHaveLength(2);
    expect(screen.getByText(/draws 12 USD; up to 400 USD is left/)).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("revising an AMOUNT agreement sends commitmentAmount, never commitmentQty", () => {
    const values = { ...revisionValues(amountAgreement), commitmentQty: "1500", reason: "extended" };
    const { input, errors } = validateRevision(values, amountAgreement);
    expect(errors).toEqual({});
    expect(input).toEqual({ commitmentAmount: "1500", reason: "extended" });
    expect(validateRevision({ ...values, commitmentQty: "10.00001" }, amountAgreement).errors.commitmentQty).toMatch(/4 decimals/);
    expect(validateRevision({ ...values, commitmentQty: "500" }, amountAgreement).errors.commitmentQty).toMatch(/below the 600/);
    const quantity = validateRevision({ ...revisionValues({ ...amountAgreement, commitmentType: "QUANTITY" }), commitmentQty: "1500" }, { ...amountAgreement, commitmentType: "QUANTITY" });
    expect(quantity.input).toEqual({ commitmentQty: "1500" });
  });

  it("commitmentText formats an amount as money and a quantity with its unit", () => {
    expect(commitmentText({ commitmentType: "AMOUNT", currency: "EUR", unit: "EA" }, "250")).toBe("250 EUR");
    expect(commitmentText({ currency: "EUR", unit: "EA" }, "250")).toBe("250 EA");
    expect(commitmentText({ currency: "EUR", unit: "EA" }, undefined)).toBe("—");
  });

  it("a release on an AMOUNT agreement posts the quantity and shows the refusal", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 422, body: { code: "SLS-0008", detail: "AGREEMENT_EXHAUSTED: agreement SA-0000009 has 400 USD remaining; the release asks for 408" } }
        : { status: 200, body: amountAgreement, etag: '"2"' };
    renderAt(`/sales/agreements/${AGREEMENT_ID}`);
    await screen.findByRole("heading", { name: "Sales agreement SA-0000009" });
    fill("Quantity", "34");
    fill("Site", SITE);
    fill("Warehouse", WAREHOUSE);
    fireEvent.click(screen.getByRole("button", { name: "Release order" }));
    expect(await screen.findByText("Not enough commitment left")).toBeInTheDocument();
    expect(screen.getByText(/has 400 USD remaining/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({ quantity: "34" });
  });
});

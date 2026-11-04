import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { holdAge, overSla } from "./HoldQueuePage";
import { emptyLine } from "./lines";
import sales from "./module";
import { isExpired, validateQuotation, type QuotationValues } from "./QuotationsPage";
import { validateRule, type RuleValues } from "./SupplementaryRulesPage";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const SITE = "44444444-0000-4000-8000-000000000004";
const WAREHOUSE = "55555555-0000-4000-8000-000000000005";
const ORDER_ID = "66666666-0000-4000-8000-000000000006";
const QUOTE_ID = "77777777-0000-4000-8000-000000000007";
const HOLD_ID = "88888888-0000-4000-8000-000000000008";
const MANUAL = "99999999-0000-4000-8000-000000000009";
const RULE_ID = "aaaaaaaa-0000-4000-8000-00000000000a";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown; etag?: string };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
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
      return new Response(status === 204 ? null : JSON.stringify(body), { status, headers });
    }),
  );
});

afterEach(() => {
  vi.useRealTimers();
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

function line(lineNo: number, overrides: Record<string, unknown> = {}) {
  return {
    id: `l${lineNo}`,
    lineNo,
    productId: PRODUCT,
    quantity: String(lineNo),
    unit: "EA",
    siteId: SITE,
    warehouseId: WAREHOUSE,
    unitPrice: "10",
    discPct: "0",
    lineNet: String(10 * lineNo),
    requestedShipDate: "2099-03-04",
    lineStatus: "OPEN",
    ...overrides,
  };
}

function quotation(overrides: Record<string, unknown> = {}) {
  return {
    id: QUOTE_ID,
    legalEntityId: LE,
    quotationNumber: "SQ-0000012",
    customerId: CUSTOMER,
    invoiceAccountId: CUSTOMER,
    currency: "USD",
    status: "SENT",
    validUntil: "2099-03-31",
    probabilityPct: 60,
    netTotal: "60",
    version: 2,
    createdBy: "sales-rep",
    createdAt: "2027-03-01T09:00:00Z",
    updatedAt: "2027-03-01T09:00:00Z",
    lines: [line(1), line(2), line(3)],
    lineCount: 3,
    openLines: 3,
    ...overrides,
  };
}

const quoteValues = (): QuotationValues => ({
  legalEntityId: LE,
  customerId: CUSTOMER,
  invoiceAccountId: "",
  currency: "USD",
  validUntil: "",
  probabilityPct: "",
  lines: [{ ...emptyLine("2027-03-04"), productId: PRODUCT, siteId: SITE, warehouseId: WAREHOUSE, unitPrice: "19.99", quantity: "3" }],
});

describe("validateQuotation", () => {
  it("leaves out the defaults the service fills in", () => {
    const { errors, input } = validateQuotation(quoteValues(), "2027-03-01");
    expect(errors).toEqual({});
    expect(input).toEqual({
      legalEntityId: LE,
      customerId: CUSTOMER,
      currency: "USD",
      lines: [{ productId: PRODUCT, quantity: "3", unit: "EA", siteId: SITE, warehouseId: WAREHOUSE, unitPrice: "19.99", discPct: "0", requestedShipDate: "2027-03-04" }],
    });
  });

  it("refuses past validity, a probability above 100 and bad lines, keyed by the service's paths", () => {
    const values = { ...quoteValues(), validUntil: "2027-02-28", probabilityPct: "101" };
    values.lines = [{ ...values.lines[0], quantity: "0", unitPrice: "1,5" }];
    const { errors, input } = validateQuotation(values, "2027-03-01");
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["lines[0].quantity", "lines[0].unitPrice", "probabilityPct", "validUntil"]);
  });

  it("sends validity and probability when given", () => {
    const { input } = validateQuotation({ ...quoteValues(), validUntil: "2027-04-15", probabilityPct: "0" }, "2027-03-01");
    expect(input?.validUntil).toBe("2027-04-15");
    expect(input?.probabilityPct).toBe(0);
  });
});

describe("isExpired", () => {
  it("counts only DRAFT and SENT quotations past their last valid day", () => {
    expect(isExpired({ status: "SENT", validUntil: "2027-03-31" }, "2027-03-31")).toBe(false);
    expect(isExpired({ status: "SENT", validUntil: "2027-03-31" }, "2027-04-01")).toBe(true);
    expect(isExpired({ status: "CONVERTED", validUntil: "2027-03-31" }, "2027-04-01")).toBe(false);
  });
});

describe("Sales quotations list", () => {
  it("lists quotations with open lines and passes the status filter", async () => {
    handler = () => ({ status: 200, body: { items: [quotation({ lines: [], openLines: 1 })] } });
    const { container } = renderAt("/sales/quotations");
    const table = await findTable({ name: "Sales quotations" });
    await waitFor(() => expect(within(table).getByText("SQ-0000012")).toBeInTheDocument());
    expect(within(table).getByText("1 of 3")).toBeInTheDocument();
    expect(within(table).getByText("60%")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "SENT" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("status")).toBe("SENT"));
  });

  it("shows the empty state, and an error with a retry", async () => {
    handler = () => ({ status: 500, body: { code: "COM-0500", detail: "store unavailable" } });
    renderAt("/sales/quotations");
    expect(await screen.findByText(/COM-0500/)).toBeInTheDocument();
    handler = () => ({ status: 200, body: { items: [] } });
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "No quotations yet" })).toBeInTheDocument();
  });
});

describe("New sales quotation", () => {
  it("posts with an Idempotency-Key and opens the new quotation", async () => {
    handler = (url, init) =>
      init.method === "POST" ? { status: 201, body: quotation({ status: "DRAFT", version: 1 }), etag: '"1"' } : url.pathname.endsWith(QUOTE_ID) ? { status: 200, body: quotation({ status: "DRAFT", version: 1 }), etag: '"1"' } : { status: 200, body: { items: [] } };
    const { container } = renderAt("/sales/quotations/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Customer", CUSTOMER);
    fill("Product", PRODUCT);
    fill("Unit price", "19.99");
    fill("Site", SITE);
    fill("Warehouse", WAREHOUSE);
    fill(/Win probability/, "70");
    fireEvent.click(screen.getByRole("button", { name: "Save draft quotation" }));
    expect(await screen.findByRole("heading", { name: "Sales quotation SQ-0000012" })).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe("/api/v1/sales/quotations");
    expect(post?.headers.get("Idempotency-Key")).toBeTruthy();
    expect(post?.body).toMatchObject({ legalEntityId: LE, customerId: CUSTOMER, probabilityPct: 70, lines: [{ productId: PRODUCT, unitPrice: "19.99" }] });
  });

  it("shows field errors without calling the service", async () => {
    handler = () => ({ status: 200, body: {} });
    renderAt("/sales/quotations/new");
    fireEvent.click(screen.getByRole("button", { name: "Save draft quotation" }));
    expect(await screen.findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("Sales quotation detail", () => {
  it("converts the selected lines at the current ETag and links the new order", async () => {
    const converted = quotation({ status: "CONVERTED", version: 3, openLines: 1, lines: [line(1, { lineStatus: "CONVERTED", convertedOrderId: ORDER_ID }), line(2), line(3, { lineStatus: "CONVERTED", convertedOrderId: ORDER_ID })] });
    let current: unknown = quotation();
    handler = (_url, init) => {
      if (init.method === "POST") {
        current = converted;
        return { status: 201, body: { quotation: converted, order: { id: ORDER_ID, orderNumber: "SO-0000050", lines: [{}, {}, { supplementaryOf: "l1" }] } }, etag: '"3"' };
      }
      return { status: 200, body: current, etag: current === converted ? '"3"' : '"2"' };
    };
    const { container } = renderAt(`/sales/quotations/${QUOTE_ID}`);
    expect(await screen.findByRole("heading", { name: "Sales quotation SQ-0000012" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    expect(screen.queryByRole("button", { name: "Send quotation" })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("checkbox", { name: "Convert line 2" }));
    expect(screen.getByText(/2 of 3 open lines selected/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Convert selected lines" }));
    expect(await screen.findByRole("link", { name: "SO-0000050" })).toHaveAttribute("href", `/sales/orders/${ORDER_ID}`);
    // The mandatory supplementary line the service added is not counted as a quoted line (FR-27.14).
    expect(screen.getByText(/created with 2 lines at\s+the quoted prices\. It also has 1 mandatory supplementary line\./)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/quotations/${QUOTE_ID}:convert`);
    expect(post?.headers.get("If-Match")).toBe('"2"');
    expect(post?.body).toEqual({ lineNos: [1, 3] });
    await waitFor(() => expect(screen.getAllByRole("link", { name: "Converted" })).toHaveLength(2));
  });

  it("explains a listing refusal and sends the override reason on the next try", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 422, body: { code: "SLS-0011", detail: "PRODUCT_NOT_LISTED: line 2" } }
        : { status: 200, body: quotation(), etag: '"2"' };
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    fireEvent.click(await screen.findByRole("button", { name: "Convert selected lines" }));
    expect(await screen.findByText("A product is not listed for this customer")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ lineNos: [] });

    fill(/Listing override reason/, "customer accepted the substitute");
    fireEvent.click(screen.getByRole("button", { name: "Convert selected lines" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "POST")[1].body).toEqual({ lineNos: [], listingOverrideReason: "customer accepted the substitute" });
  });

  it("sends a draft, and marks it lost only with a reason", async () => {
    handler = (url, init) => {
      if (init.method === "POST" && url.pathname.endsWith(":send")) return { status: 200, body: quotation(), etag: '"2"' };
      if (init.method === "POST") return { status: 200, body: quotation({ status: "LOST", lostReason: "price" }), etag: '"3"' };
      return { status: 200, body: quotation({ status: "DRAFT", version: 1 }), etag: '"1"' };
    };
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    expect(await screen.findByRole("heading", { name: "Send the quotation first" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send quotation" }));
    expect(await screen.findByText("Quotation SQ-0000012 sent.")).toBeInTheDocument();
    const send = calls.find((c) => c.method === "POST");
    expect(send?.url.pathname).toBe(`/api/v1/sales/quotations/${QUOTE_ID}:send`);
    expect(send?.headers.get("If-Match")).toBe('"1"');

    await screen.findByRole("button", { name: "Mark lost" });
    fireEvent.click(screen.getByRole("button", { name: "Mark lost" }));
    expect(await screen.findByText(/Give a reason of 1 to 200 characters/)).toBeInTheDocument();
    fill("Reason", "  competitor 8% cheaper ");
    fireEvent.click(screen.getByRole("button", { name: "Mark lost" }));
    await waitFor(() => expect(calls.filter((c) => c.method === "POST")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "POST")[1].body).toEqual({ reason: "competitor 8% cheaper" });
  });

  it("offers neither send nor convert once the validity has passed", async () => {
    handler = () => ({ status: 200, body: quotation({ status: "DRAFT", validUntil: "2020-01-31" }), etag: '"1"' });
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    expect(await screen.findByText(/The validity passed on 2020-01-31/)).toBeInTheDocument();
    expect(screen.getByText("Validity passed")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send quotation" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Convert selected lines" })).not.toBeInTheDocument();
  });

  it("shows a load failure with a retry", async () => {
    handler = () => ({ status: 404, body: { code: "SLS-0105", detail: "quotation not found" } });
    renderAt(`/sales/quotations/${QUOTE_ID}`);
    expect(await screen.findByText(/SLS-0105/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

const creditHold = {
  holdId: HOLD_ID,
  legalEntityId: LE,
  orderId: ORDER_ID,
  orderNumber: "SO-0000042",
  orderVersion: 2,
  orderStatus: "OPEN",
  customerId: CUSTOMER,
  holdType: "CREDIT",
  reasonCode: "CR-1",
  amount: "3000",
  currency: "USD",
  appliedBy: "sales-rep",
  // Long past, so the hold shows as waiting beyond the SLA.
  appliedAt: "2020-03-01T09:00:00Z",
  slaDueAt: "2020-03-03T09:00:00Z",
  failedRule: { id: "CR-1", result: "FAIL", value: "11500", threshold: "11000" },
  creditLimit: "10000",
  limitCurrency: "USD",
};

describe("holdAge and overSla", () => {
  it("reads the wait in days, hours or minutes and flags holds past their SLA due time", () => {
    const now = new Date("2027-03-04T13:30:00Z");
    expect(holdAge("2027-03-01T09:00:00Z", now)).toBe("3 d 4 h");
    expect(holdAge("2027-03-04T08:30:00Z", now)).toBe("5 h");
    expect(holdAge("2027-03-04T13:18:00Z", now)).toBe("12 min");
    expect(overSla("2027-03-04T13:00:00Z", now)).toBe(true);
    expect(overSla("2027-03-04T14:00:00Z", now)).toBe(false);
  });
});

describe("Credit release queue", () => {
  it("asks for CREDIT holds and shows the failed rule against its threshold and the limit", async () => {
    handler = () => ({ status: 200, body: { items: [creditHold] } });
    const { container } = renderAt("/sales/credit-holds");
    const table = await findTable({ name: "Holds waiting for release" });
    await waitFor(() => expect(within(table).getByRole("link", { name: "SO-0000042" })).toBeInTheDocument());
    expect(calls[0].url.pathname).toBe("/api/v1/sales/holds");
    expect(calls[0].url.searchParams.get("holdType")).toBe("CREDIT");
    expect(within(table).getByText(/CR-1: 11500 vs 11000/)).toBeInTheDocument();
    expect(within(table).getByText("10000 USD")).toBeInTheDocument();
    expect(within(table).getByText("Past SLA")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("releases at the order's ETag and reloads the queue", async () => {
    let released = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        released = true;
        return { status: 200, body: { id: ORDER_ID, orderNumber: "SO-0000042", confirmed: true }, etag: '"3"' };
      }
      return { status: 200, body: { items: released ? [] : [creditHold] } };
    };
    renderAt("/sales/credit-holds");
    fireEvent.click(await screen.findByRole("button", { name: "Release Credit hold on SO-0000042" }));
    expect(await screen.findByText(/released and the order confirmed/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}/holds/${HOLD_ID}:release`);
    expect(post?.headers.get("If-Match")).toBe('"2"');
    expect(await screen.findByRole("heading", { name: "Nothing waiting for release" })).toBeInTheDocument();
  });

  it("rejects a credit hold with a required comment at the order's ETag and cancels the order", async () => {
    let rejected = false;
    handler = (_url, init) => {
      if (init.method === "POST") {
        rejected = true;
        return { status: 200, body: { id: ORDER_ID, orderNumber: "SO-0000042", status: "CANCELLED" }, etag: '"3"' };
      }
      return { status: 200, body: { items: rejected ? [] : [creditHold] } };
    };
    renderAt("/sales/credit-holds");
    fireEvent.click(await screen.findByRole("button", { name: "Reject Credit hold on SO-0000042" }));
    expect(await screen.findByText("Reject the credit hold on SO-0000042")).toBeInTheDocument();
    // The comment is required: nothing is sent without one.
    fireEvent.click(screen.getByRole("button", { name: "Reject hold and cancel order" }));
    expect(await screen.findByText("Say why the hold is rejected.")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText(/Comment/), { target: { value: "  Exposure too high  " } });
    fireEvent.click(screen.getByRole("button", { name: "Reject hold and cancel order" }));
    expect(await screen.findByText(/rejected and the order cancelled/)).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url.pathname).toBe(`/api/v1/sales/orders/${ORDER_ID}/holds/${HOLD_ID}:reject`);
    expect(post?.headers.get("If-Match")).toBe('"2"');
    expect(post?.body).toEqual({ comment: "Exposure too high" });
    expect(await screen.findByRole("heading", { name: "Nothing waiting for release" })).toBeInTheDocument();
    expect(screen.queryByText("Reject the credit hold on SO-0000042")).not.toBeInTheDocument();
  });

  it("shows the service's refusal of a rejection and keeps the form, and offers no reject for other hold types or confirmed orders", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 422, body: { code: "SLS-0062", detail: "only a CREDIT hold of an order awaiting confirmation can be rejected" } }
        : {
            status: 200,
            body: {
              items: [
                creditHold,
                { ...creditHold, holdId: "99999999-9999-4999-8999-999999999999", holdType: "FRAUD", orderNumber: "SO-0000043" },
                { ...creditHold, holdId: "88888888-8888-4888-8888-888888888888", orderNumber: "SO-0000044", orderStatus: "CONFIRMED" },
              ],
            },
          };
    renderAt("/sales/credit-holds");
    expect(await screen.findByRole("button", { name: "Release Fraud hold on SO-0000043" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject Fraud hold on SO-0000043" })).not.toBeInTheDocument();
    // A CREDIT hold raised on a confirmed order by a change is released, not rejected.
    expect(screen.getByRole("button", { name: "Release Credit hold on SO-0000044" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reject Credit hold on SO-0000044" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Reject Credit hold on SO-0000042" }));
    fireEvent.change(await screen.findByLabelText(/Comment/), { target: { value: "no" } });
    fireEvent.click(screen.getByRole("button", { name: "Reject hold and cancel order" }));
    expect(await screen.findByText(/SLS-0062/)).toBeInTheDocument();
    expect(screen.getByText("Could not decide the hold")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Keep the hold" }));
    expect(screen.queryByText("Reject the credit hold on SO-0000042")).not.toBeInTheDocument();
  });

  it("shows a refused release, and filters by any hold type", async () => {
    handler = (_url, init) =>
      init.method === "POST" ? { status: 403, body: { code: "AUTH-0002", detail: "missing duty sales.credit.release" } } : { status: 200, body: { items: [creditHold] } };
    renderAt("/sales/credit-holds");
    fireEvent.click(await screen.findByRole("button", { name: "Release Credit hold on SO-0000042" }));
    expect(await screen.findByText("Could not decide the hold")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Hold type"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.has("holdType")).toBe(false));
  });
});

const rule = {
  id: RULE_ID,
  legalEntityId: LE,
  parentProductId: PRODUCT,
  supplementaryProductId: MANUAL,
  unit: "EA",
  requirement: "MANDATORY",
  baseQuantity: "10",
  quantityPerBase: "1",
  pricing: "FREE",
  unitPrice: "0",
};

const ruleValues = (): RuleValues => ({
  legalEntityId: LE,
  parentProductId: PRODUCT,
  customerId: "",
  supplementaryProductId: MANUAL,
  unit: "EA",
  requirement: "MANDATORY",
  baseQuantity: "10",
  quantityPerBase: "1",
  pricing: "FREE",
  unitPrice: "",
});

describe("validateRule", () => {
  it("sends no price for a FREE rule and no customer for every customer", () => {
    expect(validateRule(ruleValues()).input).toEqual({ legalEntityId: LE, parentProductId: PRODUCT, supplementaryProductId: MANUAL, unit: "EA", requirement: "MANDATORY", baseQuantity: "10", quantityPerBase: "1", pricing: "FREE" });
  });

  it("needs a positive price for PRICED, positive quantities and a different item", () => {
    const { errors } = validateRule({ ...ruleValues(), pricing: "PRICED", unitPrice: "0", baseQuantity: "0", supplementaryProductId: PRODUCT });
    expect(Object.keys(errors).sort()).toEqual(["baseQuantity", "supplementaryProductId", "unitPrice"]);
  });
});

describe("Supplementary items", () => {
  it("lists rules and removes one after confirmation", async () => {
    handler = (_url, init) => (init.method === "DELETE" ? { status: 204, body: null } : { status: 200, body: { items: [rule] } });
    const { container } = renderAt("/sales/supplementary-items");
    const table = await findTable({ name: "Supplementary item rules" });
    await waitFor(() => expect(within(table).getByText("1 EA per 10")).toBeInTheDocument());
    expect(within(table).getByText("Free")).toBeInTheDocument();
    expect(within(table).getByText("Every customer")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: `Remove the rule for ${MANUAL}` }));
    fireEvent.click(within(table).getByRole("button", { name: `Confirm removing the rule for ${MANUAL}` }));
    expect(await screen.findByText(/was removed/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "DELETE")?.url.pathname).toBe(`/api/v1/sales/supplementary-item-rules/${RULE_ID}`);
  });

  it("checks an order line: AC-27.10 25 units bring 2 free manuals", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { items: [{ ruleId: RULE_ID, productId: MANUAL, unit: "EA", requirement: "MANDATORY", quantity: "2", unitPrice: "0", supplementaryOf: PRODUCT }] } }
        : { status: 200, body: { items: [] } };
    renderAt("/sales/supplementary-items");
    expect(await screen.findByRole("heading", { name: "No supplementary item rules yet" })).toBeInTheDocument();
    const form = screen.getByRole("form", { name: "Check an order line" });
    fireEvent.change(within(form).getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(within(form).getByLabelText("Ordered product"), { target: { value: PRODUCT } });
    fireEvent.change(within(form).getByLabelText("Ordered quantity"), { target: { value: "25" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check line" }));
    const result = await findTable({ name: "Supplementary items for the line" });
    expect(within(result).getByText("2 EA")).toBeInTheDocument();
    expect(within(result).getByText("Mandatory")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ legalEntityId: LE, productId: PRODUCT, quantity: "25" });
  });

  it("creates a rule and explains a duplicate", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0033", detail: "a rule for this parent product, customer and supplementary product already exists" } });
    const { container } = renderAt("/sales/supplementary-items/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Ordered product", PRODUCT);
    fill("Supplementary product", MANUAL);
    fill("Per ordered quantity", "10");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("This rule already exists")).toBeInTheDocument();
    expect(calls[0].body).toEqual({ legalEntityId: LE, parentProductId: PRODUCT, supplementaryProductId: MANUAL, unit: "EA", requirement: "MANDATORY", baseQuantity: "10", quantityPerBase: "1", pricing: "FREE" });
  });
});

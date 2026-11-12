import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import sales from "./module";
import { committedProducts } from "./OrderProposals";
import { acceptanceRate, parseIds, validateProposalRule, type ProposalRuleValues } from "./ProposalRulesPage";
import { emptyLine } from "./lines";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const SITE = "44444444-0000-4000-8000-000000000004";
const WAREHOUSE = "55555555-0000-4000-8000-000000000005";
const ACCESSORY = "66666666-0000-4000-8000-000000000006";
const UPGRADE = "77777777-0000-4000-8000-000000000007";
const RULE_ID = "aaaaaaaa-0000-4000-8000-00000000000a";
const RULE_2 = "bbbbbbbb-0000-4000-8000-00000000000b";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      // Picking a product asks pricing for the customer's price; these tests are about the order calls only.
      if (url.pathname === "/api/v1/pricing/resolve") return new Response("{}", { status: 404, headers: { "Content-Type": "application/json" } });
      calls.push({ method: init.method ?? "GET", url, headers: new Headers(init.headers), body: init.body ? JSON.parse(init.body as string) : undefined });
      const { status, body } = await handler(url, init);
      return new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
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

const rule = {
  id: RULE_ID,
  legalEntityId: LE,
  orderedProductId: PRODUCT,
  proposedProductId: ACCESSORY,
  reason: "ACCESSORY",
  acceptedCount: 3,
  declinedCount: 1,
};

const ruleValues = (): ProposalRuleValues => ({ legalEntityId: LE, orderedProductId: PRODUCT, proposedProductId: ACCESSORY, customerId: "", reason: "CROSS_SELL" });

describe("proposal helpers", () => {
  it("validates a rule as the service does and sends no customer for every customer", () => {
    expect(validateProposalRule(ruleValues()).input).toEqual({ legalEntityId: LE, orderedProductId: PRODUCT, proposedProductId: ACCESSORY, reason: "CROSS_SELL" });
    const { errors, input } = validateProposalRule({ ...ruleValues(), proposedProductId: PRODUCT, customerId: "C9" });
    expect(input).toBeUndefined();
    expect(Object.keys(errors).sort()).toEqual(["customerId", "proposedProductId"]);
  });

  it("reads the acceptance rate from whole counts, a dash before any answer", () => {
    expect(acceptanceRate({ acceptedCount: 3, declinedCount: 1 })).toBe("3 of 4 (75%)");
    expect(acceptanceRate({ acceptedCount: 0, declinedCount: 0 })).toBe("—");
  });

  it("parses id lists and proposes only for products entered as UUIDs, once each", () => {
    expect(parseIds(` ${PRODUCT}, ${ACCESSORY}\n`)).toEqual([PRODUCT, ACCESSORY]);
    const lines = [{ ...emptyLine("2099-03-04"), productId: PRODUCT }, { ...emptyLine("2099-03-04"), productId: ` ${PRODUCT} ` }, { ...emptyLine("2099-03-04"), productId: "FG-100" }];
    expect(committedProducts(lines)).toEqual([PRODUCT]);
  });
});

describe("Product proposal rules", () => {
  it("lists rules with their acceptance rate and removes one after confirmation", async () => {
    handler = (_url, init) => (init.method === "DELETE" ? { status: 204, body: null } : { status: 200, body: { items: [rule] } });
    const { container } = renderAt("/sales/product-proposals");
    const table = await findTable({ name: "Product proposal rules" });
    await waitFor(() => expect(within(table).getByText("3 of 4 (75%)")).toBeInTheDocument());
    expect(within(table).getByText("Every customer")).toBeInTheDocument();
    expect(within(table).getByText("Accessory")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: `Remove the rule for ${ACCESSORY}` }));
    fireEvent.click(within(table).getByRole("button", { name: `Confirm removing the rule for ${ACCESSORY}` }));
    expect(await screen.findByText(/was removed/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "DELETE")?.url.pathname).toBe(`/api/v1/sales/product-proposal-rules/${RULE_ID}`);
  });

  it("filters by legal entity and ordered product", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/product-proposals");
    expect(await screen.findByRole("heading", { name: "No product proposal rules yet" })).toBeInTheDocument();
    fireEvent.change(screen.getAllByLabelText(/Legal entity/)[0], { target: { value: LE } });
    fireEvent.change(screen.getByLabelText(/^Ordered product/), { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("orderedProductId")).toBe(PRODUCT));
    expect(calls.at(-1)?.url.searchParams.get("legalEntityId")).toBe(LE);
  });

  it("shows a load failure with a retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "service unavailable" } } : { status: 200, body: { items: [rule] } });
    renderAt("/sales/product-proposals");
    expect(await screen.findByText("Could not load product proposal rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Product proposal rules" })).toBeInTheDocument();
  });

  it("checks proposals for an order without storing anything", async () => {
    handler = (_url, init) =>
      init.method === "POST"
        ? { status: 200, body: { items: [{ ruleId: RULE_ID, orderedProductId: PRODUCT, proposedProductId: ACCESSORY, reason: "ACCESSORY" }] } }
        : { status: 200, body: { items: [] } };
    renderAt("/sales/product-proposals");
    await screen.findByRole("heading", { name: "No product proposal rules yet" });
    const form = screen.getByRole("form", { name: "Check proposals for an order" });
    fireEvent.click(within(form).getByRole("button", { name: "Check proposals" }));
    expect(await within(form).findByText("Enter 1 to 50 product UUIDs.")).toBeInTheDocument();
    fireEvent.change(within(form).getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(within(form).getByLabelText("Products being ordered"), { target: { value: PRODUCT } });
    fireEvent.change(within(form).getByLabelText(/Products already on the order/), { target: { value: UPGRADE } });
    fireEvent.click(within(form).getByRole("button", { name: "Check proposals" }));
    const result = await findTable({ name: "Proposals for the order" });
    expect(within(result).getByText(ACCESSORY)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({ legalEntityId: LE, productIds: [PRODUCT], currentProductIds: [UPGRADE] });
  });

  it("creates a rule and explains a duplicate", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0034", detail: "a proposal rule for this ordered product, customer and proposed product already exists" } });
    const { container } = renderAt("/sales/product-proposals/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Ordered product", PRODUCT);
    fill("Proposed product", ACCESSORY);
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "ACCESSORY" } });
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("This rule already exists")).toBeInTheDocument();
    expect(calls[0].body).toEqual({ legalEntityId: LE, orderedProductId: PRODUCT, proposedProductId: ACCESSORY, reason: "ACCESSORY" });
  });

  it("does not send an invalid rule", async () => {
    handler = () => ({ status: 201, body: rule });
    renderAt("/sales/product-proposals/new");
    fill("Ordered product", PRODUCT);
    fill("Proposed product", PRODUCT);
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("The proposed product must differ from the ordered product.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

describe("Proposals on the new order form", () => {
  function enterOrder() {
    fill("Legal entity", LE);
    fill("Customer", CUSTOMER);
    fill("Product", PRODUCT);
    fill("Site", SITE);
    fill("Warehouse", WAREHOUSE);
  }

  it("offers proposals for the lines; accepting adds a line and records it, declining records it", async () => {
    handler = (url) => {
      if (url.pathname.endsWith(":evaluate")) {
        return {
          status: 200,
          body: {
            items: [
              { ruleId: RULE_ID, orderedProductId: PRODUCT, proposedProductId: ACCESSORY, reason: "ACCESSORY" },
              { ruleId: RULE_2, orderedProductId: PRODUCT, proposedProductId: UPGRADE, reason: "UP_SELL" },
            ],
          },
        };
      }
      return { status: 201, body: { id: "r", accepted: true } };
    };
    const { container } = renderAt("/sales/orders/new");
    const show = screen.getByRole("button", { name: "Show proposals" });
    expect(show).toBeDisabled();
    enterOrder();
    expect(show).toBeEnabled();
    fireEvent.click(show);
    const table = await findTable({ name: "Proposed products" });
    expect(calls[0].body).toEqual({ legalEntityId: LE, customerId: CUSTOMER, productIds: [PRODUCT] });
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: `Add a line for ${ACCESSORY}` }));
    expect(await screen.findByRole("group", { name: "Line 2" })).toBeInTheDocument();
    const added = within(screen.getByRole("group", { name: "Line 2" }));
    expect(added.getByLabelText("Product")).toHaveAttribute("data-value", ACCESSORY);
    expect(added.getByLabelText("Site")).toHaveAttribute("data-value", SITE);
    expect(added.getByLabelText("Warehouse")).toHaveAttribute("data-value", WAREHOUSE);
    expect(added.getByLabelText("Unit price")).toHaveValue("");
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith(":respond"))).toHaveLength(1));
    const accepted = calls.find((c) => c.url.pathname.endsWith(":respond"))!;
    expect(accepted.body).toEqual({ legalEntityId: LE, ruleId: RULE_ID, customerId: CUSTOMER, accepted: true });
    expect(accepted.headers.get("Idempotency-Key")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: `Do not add ${UPGRADE}` }));
    await waitFor(() => expect(calls.filter((c) => c.url.pathname.endsWith(":respond"))).toHaveLength(2));
    expect(calls.at(-1)?.body).toEqual({ legalEntityId: LE, ruleId: RULE_2, customerId: CUSTOMER, accepted: false });
    expect(screen.queryByRole("group", { name: "Line 3" })).toBeNull();
    expect(await screen.findByRole("heading", { name: "No proposals" })).toBeInTheDocument();
  });

  it("shows a failed check with a retry, and keeps the line when an answer is not recorded", async () => {
    let evaluateFails = true;
    handler = (url) => {
      if (url.pathname.endsWith(":evaluate")) {
        return evaluateFails
          ? { status: 503, body: { code: "COM-0503", detail: "service unavailable" } }
          : { status: 200, body: { items: [{ ruleId: RULE_ID, orderedProductId: PRODUCT, proposedProductId: ACCESSORY, reason: "CROSS_SELL" }] } };
      }
      return { status: 404, body: { code: "SLS-0107", detail: "product proposal rule not found" } };
    };
    renderAt("/sales/orders/new");
    enterOrder();
    fireEvent.click(screen.getByRole("button", { name: "Show proposals" }));
    expect(await screen.findByText("Could not load proposals")).toBeInTheDocument();
    evaluateFails = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    const table = await findTable({ name: "Proposed products" });
    fireEvent.click(within(table).getByRole("button", { name: `Add a line for ${ACCESSORY}` }));
    expect(await screen.findByText("Your answer was not recorded")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Line 2" })).toBeInTheDocument();
  });

  it("says when nothing is proposed", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/orders/new");
    enterOrder();
    fireEvent.click(screen.getByRole("button", { name: "Show proposals" }));
    expect(await screen.findByRole("heading", { name: "No proposals" })).toBeInTheDocument();
  });
});

import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import sales from "./module";
import { orderedSummary, parseAlternativeBuckets, parseBuckets, validateAlternativeRule, validityText, type AlternativeRuleValues } from "./AlternativeItemsPage";

const LE = "11111111-0000-4000-8000-000000000001";
const CUSTOMER = "22222222-0000-4000-8000-000000000002";
const PRODUCT = "33333333-0000-4000-8000-000000000003";
const ALT_A = "66666666-0000-4000-8000-000000000006";
const ALT_B = "77777777-0000-4000-8000-000000000007";
const RULE_ID = "aaaaaaaa-0000-4000-8000-00000000000a";
const RULE_2 = "bbbbbbbb-0000-4000-8000-00000000000b";

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | Promise<Reply>;

let handler: Handler;
let calls: { method: string; url: URL; body: unknown }[];

beforeEach(() => {
  calls = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, body: init.body ? JSON.parse(init.body as string) : undefined });
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

const rule = { id: RULE_ID, legalEntityId: LE, orderedProductId: PRODUCT, alternativeProductId: ALT_A, priority: 1, validFrom: "2027-03-01", validTo: "2027-06-30" };
const openRule = { id: RULE_2, legalEntityId: LE, orderedProductId: PRODUCT, alternativeProductId: ALT_B, priority: 2, customerId: CUSTOMER };

const ruleValues = (): AlternativeRuleValues => ({ legalEntityId: LE, orderedProductId: PRODUCT, alternativeProductId: ALT_A, customerId: "", priority: "", validFrom: "", validTo: "" });

describe("alternative item helpers", () => {
  it("validates a rule as the service does; priority defaults to 1 and empty optionals are not sent", () => {
    expect(validateAlternativeRule(ruleValues()).input).toEqual({ legalEntityId: LE, orderedProductId: PRODUCT, alternativeProductId: ALT_A, priority: 1 });
    const full = validateAlternativeRule({ ...ruleValues(), customerId: CUSTOMER, priority: "3", validFrom: "2027-03-01", validTo: "2027-03-31" });
    expect(full.input).toEqual({ legalEntityId: LE, orderedProductId: PRODUCT, alternativeProductId: ALT_A, customerId: CUSTOMER, priority: 3, validFrom: "2027-03-01", validTo: "2027-03-31" });
    const bad = validateAlternativeRule({ ...ruleValues(), alternativeProductId: PRODUCT, customerId: "C9", priority: "100", validFrom: "2027-03-31", validTo: "2027-03-01" });
    expect(bad.input).toBeUndefined();
    expect(Object.keys(bad.errors).sort()).toEqual(["alternativeProductId", "customerId", "priority", "validTo"]);
    expect(validateAlternativeRule({ ...ruleValues(), priority: "1.5" }).errors.priority).toBeDefined();
  });

  it("words the validity", () => {
    expect(validityText({ validFrom: "2027-03-01", validTo: "2027-06-30" })).toBe("2027-03-01 to 2027-06-30");
    expect(validityText({ validFrom: "2027-03-01" })).toBe("from 2027-03-01");
    expect(validityText({ validTo: "2027-06-30" })).toBe("until 2027-06-30");
    expect(validityText({})).toBe("—");
  });

  it("parses bucket lines, demand defaulting to 0, and names the first bad line", () => {
    expect(parseBuckets("2027-03-01 30\n\n2027-03-10, 5, 2")).toEqual({ periods: [{ date: "2027-03-01", supply: "30", demand: "0" }, { date: "2027-03-10", supply: "5", demand: "2" }] });
    expect(parseBuckets("2027-03-01 x").error).toContain("2027-03-01 x");
    expect(parseBuckets("2027-03-10 5\n2027-03-01 5").error).toContain("dated after");
    expect(parseBuckets("2027-03-10 5 1 9").error).toBeDefined();
  });

  it("groups alternative bucket lines by product", () => {
    const parsed = parseAlternativeBuckets(`${ALT_A} 2027-03-01 10\n${ALT_B} 2027-03-02 4 1\n${ALT_A} 2027-03-09 5`);
    expect(parsed.alternatives.map((a) => [a.productId, a.periods.length])).toEqual([[ALT_A, 2], [ALT_B, 1]]);
    expect(parseAlternativeBuckets("FG-1 2027-03-01 10").error).toContain("UUID");
  });

  it("summarises the ordered product", () => {
    const ordered = { productId: PRODUCT, availableOnDate: "5", coversQuantity: false, promise: { complete: false, backorder: "20", source: "ATP", schedule: [] } };
    expect(orderedSummary({ ordered, needed: true, offers: [], unchecked: [] }, "25", "2027-03-01")).toBe("The ordered product can ship 5 of 25 by 2027-03-01.");
    expect(orderedSummary({ ordered, needed: false, offers: [], unchecked: [] }, "25", "2027-03-01")).toContain("no alternative is needed");
  });
});

describe("Alternative item rules", () => {
  it("lists rules by priority with validity and removes one after confirmation", async () => {
    handler = (_url, init) => (init.method === "DELETE" ? { status: 204, body: null } : { status: 200, body: { items: [rule, openRule] } });
    const { container } = renderAt("/sales/alternative-items");
    const table = await findTable({ name: "Alternative item rules" });
    expect(within(table).getByText("2027-03-01 to 2027-06-30")).toBeInTheDocument();
    expect(within(table).getByText("Every customer")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    fireEvent.click(within(table).getByRole("button", { name: `Remove the rule for ${ALT_A}` }));
    fireEvent.click(within(table).getByRole("button", { name: `Confirm removing the rule for ${ALT_A}` }));
    expect(await screen.findByText(/was removed/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "DELETE")?.url.pathname).toBe(`/api/v1/sales/alternative-item-rules/${RULE_ID}`);
  });

  it("filters by legal entity and ordered product", async () => {
    handler = () => ({ status: 200, body: { items: [] } });
    renderAt("/sales/alternative-items");
    expect(await screen.findByRole("heading", { name: "No alternative item rules yet" })).toBeInTheDocument();
    fireEvent.change(screen.getAllByLabelText(/Legal entity/)[0], { target: { value: LE } });
    fireEvent.change(screen.getAllByLabelText(/^Ordered product/)[0], { target: { value: PRODUCT } });
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    await waitFor(() => expect(calls.at(-1)?.url.searchParams.get("orderedProductId")).toBe(PRODUCT));
    expect(calls.at(-1)?.url.searchParams.get("legalEntityId")).toBe(LE);
  });

  it("shows a load failure with a retry", async () => {
    let fail = true;
    handler = () => (fail ? { status: 503, body: { code: "COM-0503", detail: "service unavailable" } } : { status: 200, body: { items: [rule] } });
    renderAt("/sales/alternative-items");
    expect(await screen.findByText("Could not load alternative item rules")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Alternative item rules" })).toBeInTheDocument();
  });

  it("shows the alternatives that can ship, the covering one first, and the unchecked ones", async () => {
    handler = (url, init) =>
      init.method === "POST" && url.pathname.endsWith(":evaluate")
        ? {
            status: 200,
            body: {
              ordered: { productId: PRODUCT, availableOnDate: "5", coversQuantity: false, promise: { complete: false, backorder: "20", source: "ATP", schedule: [] } },
              needed: true,
              offers: [
                { ruleId: RULE_ID, alternativeProductId: ALT_A, priority: 2, availableOnDate: "25", coversQuantity: true, promise: { promisedDate: "2027-03-01", complete: true, backorder: "0", source: "ATP", schedule: [] } },
              ],
              unchecked: [{ ruleId: RULE_2, alternativeProductId: ALT_B, priority: 3 }],
            },
          }
        : { status: 200, body: { items: [] } };
    renderAt("/sales/alternative-items");
    await screen.findByRole("heading", { name: "No alternative item rules yet" });
    const form = screen.getByRole("form", { name: "Check alternatives for a line" });
    fireEvent.click(within(form).getByRole("button", { name: "Check alternatives" }));
    expect(await within(form).findByText("Enter the legal entity's UUID.")).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    fireEvent.change(within(form).getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(within(form).getByLabelText("Ordered product"), { target: { value: PRODUCT } });
    fireEvent.change(within(form).getByLabelText("Quantity"), { target: { value: "25" } });
    fireEvent.change(within(form).getByLabelText("Requested date"), { target: { value: "2027-03-01" } });
    fireEvent.change(within(form).getByLabelText("Ordered product buckets"), { target: { value: "2027-03-01 5" } });
    fireEvent.change(within(form).getByLabelText(/Alternative product buckets/), { target: { value: `${ALT_A} 2027-03-01 40` } });
    fireEvent.click(within(form).getByRole("button", { name: "Check alternatives" }));

    const offers = await findTable({ name: "Offered alternatives" });
    expect(within(offers).getByText(ALT_A)).toBeInTheDocument();
    expect(within(offers).getByText("Covers it")).toBeInTheDocument();
    expect(screen.getByText("The ordered product can ship 5 of 25 by 2027-03-01.")).toBeInTheDocument();
    expect(screen.getByText(/came without buckets/)).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      legalEntityId: LE,
      productId: PRODUCT,
      quantity: "25",
      requested: "2027-03-01",
      periods: [{ date: "2027-03-01", supply: "5", demand: "0" }],
      alternatives: [{ productId: ALT_A, periods: [{ date: "2027-03-01", supply: "40", demand: "0" }] }],
    });
  });

  it("says when the ordered product covers the quantity or no alternative can ship", async () => {
    let needed = false;
    handler = (_url, init) =>
      init.method === "POST"
        ? {
            status: 200,
            body: {
              ordered: { productId: PRODUCT, availableOnDate: needed ? "0" : "25", coversQuantity: !needed, promise: { complete: !needed, backorder: "0", source: "ATP", schedule: [] } },
              needed,
              offers: [],
              unchecked: [],
            },
          }
        : { status: 200, body: { items: [] } };
    renderAt("/sales/alternative-items");
    await screen.findByRole("heading", { name: "No alternative item rules yet" });
    const form = screen.getByRole("form", { name: "Check alternatives for a line" });
    fireEvent.change(within(form).getByLabelText("Legal entity"), { target: { value: LE } });
    fireEvent.change(within(form).getByLabelText("Ordered product"), { target: { value: PRODUCT } });
    fireEvent.change(within(form).getByLabelText("Quantity"), { target: { value: "25" } });
    fireEvent.change(within(form).getByLabelText("Requested date"), { target: { value: "2027-03-01" } });
    fireEvent.change(within(form).getByLabelText("Ordered product buckets"), { target: { value: "2027-03-01 25" } });
    fireEvent.click(within(form).getByRole("button", { name: "Check alternatives" }));
    expect(await screen.findByText(/no alternative is needed/)).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "No alternative can ship by the date" })).toBeNull();

    needed = true;
    fireEvent.click(within(form).getByRole("button", { name: "Check alternatives" }));
    expect(await screen.findByRole("heading", { name: "No alternative can ship by the date" })).toBeInTheDocument();
  });

  it("creates a rule and explains a duplicate", async () => {
    handler = () => ({ status: 409, body: { code: "SLS-0055", detail: "an alternative item rule for this ordered product, customer and alternative already exists" } });
    const { container } = renderAt("/sales/alternative-items/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Legal entity", LE);
    fill("Ordered product", PRODUCT);
    fill("Alternative product", ALT_A);
    fill("Priority", "2");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("This rule already exists")).toBeInTheDocument();
    expect(calls[0].body).toEqual({ legalEntityId: LE, orderedProductId: PRODUCT, alternativeProductId: ALT_A, priority: 2 });
  });

  it("does not send an invalid rule", async () => {
    handler = () => ({ status: 201, body: rule });
    renderAt("/sales/alternative-items/new");
    fill("Ordered product", PRODUCT);
    fill("Alternative product", PRODUCT);
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("The alternative must differ from the ordered product.")).toBeInTheDocument();
    expect(calls).toHaveLength(0);
  });
});

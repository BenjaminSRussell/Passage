import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { Journal } from "./api/client";
import { initialValues, toInput, validateOrder, formKey, type OrderValues } from "./CreateOrderPage";
import { emptyDraft, toJournalInput } from "./JournalsPanel";
import production from "./module";
import { json, order, orderId } from "./testData";

afterEach(() => vi.unstubAllGlobals());

const uuid = (n: number) => `0195d6a0-0000-7000-8000-${String(n).padStart(12, "0")}`;

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <a href="#main">Skip to content</a>
      <nav aria-label="Workspaces" />
      <main id="main">
        <h1>Production</h1>
        <Routes>
          {production.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </main>
    </MemoryRouter>,
  );
}

const withCo = (extra = {}) =>
  order({
    status: "Started",
    version: 4,
    coProducts: [
      { lineNo: "C1", type: "co-product", itemId: "ITEM-GAS", qtyPer: "0.2", qtyPlanned: "20", qtyReported: "5" },
      { lineNo: "B1", type: "by-product", itemId: "ITEM-SLAG", qtyPer: "0.1", qtyPlanned: "10", qtyReported: "0" },
    ],
    ...extra,
  });

const costs = {
  orderId,
  currency: "USD",
  totals: {},
  total: "0",
  rows: [],
  actualTotals: {},
  actualTotal: "0",
  actualRows: [],
  wipBalance: "0",
  variances: [],
  varianceTotal: "0",
};

function serve(current: ReturnType<typeof withCo>, journals: Journal[] = []) {
  const mock = vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/status-log")) return json({ orderId, items: [] });
    if (url.endsWith("/costs")) return json(costs);
    if (url.endsWith("/journals") && init?.method === "POST") return json({ ...journals[0], id: "j-new", journalNumber: "PJ-000009" }, 201);
    if (url.endsWith("/journals")) return json({ orderId, items: journals });
    return json(current);
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

describe("Co-product forms (FR-21.14)", () => {
  const filled = () => {
    const v = initialValues();
    v.productId = uuid(1);
    v.siteId = uuid(2);
    v.warehouseId = uuid(3);
    v.quantity = "100";
    v.lines = [{ lineNo: "10", itemId: uuid(4), qtyPer: "3", scrapPct: "", unitCost: "10" }];
    v.operations = [{ operNumber: "10", resourceId: uuid(5), setupTimeH: "", runTimeHPer: "0.5", laborRate: "30", overheadRate: "" }];
    return v;
  };

  it("maps declared co-products into the create body and omits them when there are none", () => {
    expect(toInput(filled())).not.toHaveProperty("coProducts");
    const v: OrderValues = {
      ...filled(),
      coProducts: [
        { lineNo: " C1 ", type: "co-product", itemId: ` ${uuid(7)} `, qtyPer: "0.2" },
        { lineNo: "B1", type: "by-product", itemId: uuid(8), qtyPer: "0.1" },
      ],
    };
    expect(validateOrder(v)).toEqual({});
    expect(toInput(v).coProducts).toEqual([
      { lineNo: "C1", type: "co-product", itemId: uuid(7), qtyPer: "0.2" },
      { lineNo: "B1", type: "by-product", itemId: uuid(8), qtyPer: "0.1" },
    ]);
  });

  it("validates co-product lines like the service", () => {
    const v: OrderValues = {
      ...filled(),
      coProducts: [
        { lineNo: "C1", type: "co-product", itemId: "x", qtyPer: "0" },
        { lineNo: "C1", type: "co-product", itemId: uuid(7), qtyPer: "1" },
        { lineNo: "C3", type: "by-product", itemId: uuid(1), qtyPer: "1" },
      ],
    };
    expect(Object.keys(validateOrder(v)).sort()).toEqual(["coProducts.0.itemId", "coProducts.0.qtyPer", "coProducts.1.lineNo", "coProducts.2.itemId"]);
    expect(
      validateOrder({ ...filled(), rework: true, coProducts: [{ lineNo: "C1", type: "co-product", itemId: uuid(7), qtyPer: "1" }] }),
    ).toHaveProperty("coProducts");
    expect(formKey("coProducts[2].qtyPer")).toBe("coProducts.2.qtyPer");
  });

  it("adds a co-product line on the create page and sends it", async () => {
    const calls: Array<[string, RequestInit | undefined]> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push([url, init]);
        return json(order(), 201);
      }),
    );
    renderAt("/production/new");
    const form = await screen.findByRole("form", { name: "New production order" });
    expect(within(form).getByText(/yields besides its product/)).toBeInTheDocument();
    await userEvent.click(within(form).getByRole("button", { name: "Add co-product" }));
    const type = within(form).getByLabelText("Type") as HTMLSelectElement;
    expect(type.value).toBe("co-product");
    await userEvent.selectOptions(type, "by-product");
    expect(type.value).toBe("by-product");
    await userEvent.click(within(form).getByRole("button", { name: "Create production order" }));
    expect(await within(form).findAllByText("Choose an item.")).not.toHaveLength(0);
    expect(calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);
    await userEvent.click(within(form).getByRole("button", { name: "Remove co-product 1" }));
    expect(within(form).queryByText("Co-product 1")).not.toBeInTheDocument();
  });

  it("builds the RAF body with only the entered co-product quantities", () => {
    const ok = toJournalInput({ ...emptyDraft("report-as-finished"), quantity: "40", coProducts: { C1: "8", C2: "" } });
    expect(ok.errors).toEqual({});
    expect(ok.input?.coProducts).toEqual([{ lineNo: "C1", quantity: "8" }]);
    expect(toJournalInput({ ...emptyDraft("report-as-finished"), quantity: "40", coProducts: { C1: "0" } }).errors).toHaveProperty("co.C1");
    expect(toJournalInput({ ...emptyDraft("report-as-finished"), quantity: "40" }).input).not.toHaveProperty("coProducts");
  });
});

describe("Co-products on the order", () => {
  it("lists the co-products with planned and received quantities", async () => {
    serve(withCo());
    const { container } = renderAt(`/production/${orderId}`);
    const table = await findTable({ name: "Order co-products" });
    expect(within(table).getByText("ITEM-GAS")).toBeInTheDocument();
    expect(within(table).getByText("20")).toBeInTheDocument();
    expect(within(table).getByText("5")).toBeInTheDocument();
    expect(within(table).getByText("By-product")).toBeInTheDocument();
    expect(checkAxeAA(container)).toEqual([]);
  });

  it("shows no co-product card on an order without them", async () => {
    serve(order({ status: "Started", version: 4 }));
    renderAt(`/production/${orderId}`);
    await screen.findByRole("form", { name: "New journal" });
    expect(screen.queryByText("Co-products and by-products")).not.toBeInTheDocument();
  });

  it("offers a received quantity per co-product on a report as finished and sends it", async () => {
    const mock = serve(withCo(), [
      {
        id: "j1",
        orderId,
        type: "report-as-finished",
        journalNumber: "PJ-000001",
        postingDate: "2026-10-26",
        posted: false,
        qtyGood: "40",
        qtyError: "0",
        createdBy: "x",
        createdAt: "2026-10-26T13:00:00Z",
        lines: [],
        postings: [],
      },
    ]);
    renderAt(`/production/${orderId}`);
    const form = await screen.findByRole("form", { name: "New journal" });
    await userEvent.selectOptions(within(form).getByLabelText("Journal type"), "report-as-finished");
    expect(await within(form).findByText("Co-product C1 (5 of 20 received)")).toBeInTheDocument();
    const byProduct = within(form).getByRole("group", { name: "By-product B1 (0 of 10 received)" });
    expect(within(byProduct).getByText(/Received at zero cost/)).toBeInTheDocument();
    await userEvent.type(within(form).getByLabelText(/Good quantity/), "40");
    const coProduct = within(form).getByRole("group", { name: "Co-product C1 (5 of 20 received)" });
    await userEvent.type(within(coProduct).getByLabelText(/Quantity received/), "8");
    await userEvent.click(within(form).getByRole("button", { name: "Create journal" }));
    await waitFor(() => {
      const post = (mock.mock.calls as unknown as Array<[string, RequestInit | undefined]>).find(([, init]) => init?.method === "POST");
      expect(JSON.parse(String(post?.[1]?.body))).toMatchObject({ type: "report-as-finished", quantity: "40", coProducts: [{ lineNo: "C1", quantity: "8" }] });
    });
  });
});

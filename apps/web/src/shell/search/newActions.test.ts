import { describe, expect, it } from "vitest";
import type { RouteObject } from "react-router-dom";
import type { PassageModule } from "../../modules/registry";
import { newActions } from "./newActions";

const page = (path: string, title?: string, duty?: string): RouteObject => ({ path, handle: { title, duty } });

const purchasing: PassageModule = {
  id: "purchasing",
  title: "Purchasing",
  nav: [{ label: "Requisitions", to: "/purchasing/requisitions", duty: "procurement.read" }],
  routes: [
    page("/purchasing/requisitions", "Requisitions"),
    page("/purchasing/requisitions/new", "New requisition"),
    page("/purchasing/requisitions/:id/edit", "Edit requisition"),
    page("/purchasing/orders/new", "New purchase order", "procurement.purchase-order.maintain"),
    page("/purchasing/imports/new", "Import catalog"),
    page("/purchasing/:kind/new", "New thing"),
  ],
};
const sales: PassageModule = {
  id: "sales",
  title: "Sales",
  nav: [{ label: "Sales", to: "/sales", duty: "sales.order.read" }],
  routes: [page("/sales/orders/new", "New sales order"), { path: "/sales", children: [page("/sales/quotes/new", "New quotation")] }],
};

describe("new actions in the palette", () => {
  it("lists static create pages titled New, sorted, with their module", () => {
    const all = [purchasing, sales];
    const actions = newActions(all, all, ["procurement.read", "procurement.purchase-order.maintain", "sales.order.read"]);
    expect(actions.map((a) => [a.label, a.to, a.moduleTitle])).toEqual([
      ["New purchase order", "/purchasing/orders/new", "Purchasing"],
      ["New quotation", "/sales/quotes/new", "Sales"],
      ["New requisition", "/purchasing/requisitions/new", "Purchasing"],
      ["New sales order", "/sales/orders/new", "Sales"],
    ]);
  });

  it("leaves out pages the caller lacks the duty for, parameterised paths and pages not titled New", () => {
    const all = [purchasing, sales];
    const actions = newActions(all, all, ["procurement.read"]);
    expect(actions.map((a) => a.label)).toEqual(["New requisition"]);
  });

  it("lists nothing for a module that is not visible", () => {
    expect(newActions([], [purchasing], ["procurement.read"])).toEqual([]);
  });
});

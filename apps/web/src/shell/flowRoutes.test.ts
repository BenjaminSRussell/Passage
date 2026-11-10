import { FLOW_ROUTES } from "../../e2e/flowRoutes";
import { loadModules } from "../modules/registry";

const FLOW_MODULES = ["purchasing", "receiving", "ap-invoices", "payables", "sales", "fulfillment", "billing", "receivables"];

describe("flow routes scanned by axe", () => {
  it("lists every menu page of the procure-to-pay and order-to-cash modules", () => {
    const nav = new Set(loadModules().filter((m) => FLOW_MODULES.includes(m.id)).flatMap((m) => m.nav.map((n) => n.to)));
    expect([...nav].filter((to) => !FLOW_ROUTES.includes(to))).toEqual([]);
    expect(FLOW_ROUTES.filter((to) => !nav.has(to))).toEqual([]);
  });
});

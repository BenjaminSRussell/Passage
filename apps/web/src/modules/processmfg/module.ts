import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BatchOrderDetailPage, BatchOrdersPage, NewBatchOrderPage } from "./BatchOrderPages";
import { AllocationPage, PotencyPage } from "./CalculatorPages";
import { FormulaDetailPage, FormulasPage, NewFormulaPage } from "./FormulaPages";
import { ProcessHome } from "./pages";

const processmfg: PassageModule = {
  id: "processmfg",
  title: "Process manufacturing",
  description: "Formulas, batch orders scaled to batch size, potency consumption and co-product cost allocation.",
  routes: [
    { path: "/processmfg", element: createElement(ProcessHome), handle: { title: "Process manufacturing" } },
    { path: "/processmfg/formulas", element: createElement(FormulasPage), handle: { title: "Formulas" } },
    { path: "/processmfg/formulas/new", element: createElement(NewFormulaPage), handle: { title: "New formula" } },
    { path: "/processmfg/formulas/:id", element: createElement(FormulaDetailPage), handle: { title: "Formula" } },
    { path: "/processmfg/batch-orders", element: createElement(BatchOrdersPage), handle: { title: "Batch orders" } },
    { path: "/processmfg/batch-orders/new", element: createElement(NewBatchOrderPage), handle: { title: "New batch order" } },
    { path: "/processmfg/batch-orders/:id", element: createElement(BatchOrderDetailPage), handle: { title: "Batch order" } },
    { path: "/processmfg/potency", element: createElement(PotencyPage), handle: { title: "Potency consumption" } },
    { path: "/processmfg/allocation", element: createElement(AllocationPage), handle: { title: "Co-product allocation" } },
  ],
  nav: [
    { label: "Overview", to: "/processmfg", duty: "production.batch-order.read" },
    { label: "Formulas", to: "/processmfg/formulas", duty: "production.batch-order.read" },
    { label: "Batch orders", to: "/processmfg/batch-orders", duty: "production.batch-order.read" },
    { label: "Potency", to: "/processmfg/potency", duty: "production.batch-order.read" },
    { label: "Co-product allocation", to: "/processmfg/allocation", duty: "production.journal.post" },
  ],
};

export default processmfg;

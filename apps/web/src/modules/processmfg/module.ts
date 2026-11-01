import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AttributeDefinitionsPage, BatchDetailPage, BatchesPage, NewBatchPage } from "./BatchRecordPages";
import { BatchOrderDetailPage, BatchOrdersPage, NewBatchOrderPage } from "./BatchOrderPages";
import { BatchAttributesPage, PickingPage, ReworkPage } from "./BatchQualityPages";
import { ApprovalCheckPage, ConsolidationPage, DerivationPage, MergePage, RequirementCheckPage } from "./BatchRulePages";
import { AllocationPage, PotencyPage } from "./CalculatorPages";
import { FormulaDetailPage, FormulasPage, NewFormulaPage } from "./FormulaPages";
import { ProcessHome } from "./pages";
import { StoredPickingPage } from "./StoredPickingPage";

const processmfg: PassageModule = {
  id: "processmfg",
  title: "Process manufacturing",
  description:
    "Formulas, batch orders scaled to batch size, potency consumption, co-product cost allocation, batch derivation, merge, consolidation and customer requirements, batch attributes, picking (from typed or stored batches) and rework, and the batch quality record with its disposition history.",
  routes: [
    { path: "/processmfg", element: createElement(ProcessHome), handle: { title: "Process manufacturing" } },
    { path: "/processmfg/formulas", element: createElement(FormulasPage), handle: { title: "Formulas" } },
    { path: "/processmfg/formulas/new", element: createElement(NewFormulaPage), handle: { title: "New formula" } },
    { path: "/processmfg/formulas/:id", element: createElement(FormulaDetailPage), handle: { title: "Formula" } },
    { path: "/processmfg/batch-orders", element: createElement(BatchOrdersPage), handle: { title: "Batch orders" } },
    { path: "/processmfg/batch-orders/new", element: createElement(NewBatchOrderPage), handle: { title: "New batch order" } },
    { path: "/processmfg/batch-orders/:id", element: createElement(BatchOrderDetailPage), handle: { title: "Batch order" } },
    { path: "/processmfg/batches", element: createElement(BatchesPage), handle: { title: "Batches" } },
    { path: "/processmfg/batches/new", element: createElement(NewBatchPage), handle: { title: "Register batch" } },
    { path: "/processmfg/batches/:id", element: createElement(BatchDetailPage), handle: { title: "Batch" } },
    { path: "/processmfg/attribute-definitions", element: createElement(AttributeDefinitionsPage), handle: { title: "Batch attribute definitions" } },
    { path: "/processmfg/potency", element: createElement(PotencyPage), handle: { title: "Potency consumption" } },
    { path: "/processmfg/allocation", element: createElement(AllocationPage), handle: { title: "Co-product allocation" } },
    { path: "/processmfg/derivation", element: createElement(DerivationPage), handle: { title: "Batch derivation" } },
    { path: "/processmfg/merge", element: createElement(MergePage), handle: { title: "Batch merge" } },
    { path: "/processmfg/batch-requirements", element: createElement(RequirementCheckPage), handle: { title: "Customer batch requirements" } },
    { path: "/processmfg/consolidation", element: createElement(ConsolidationPage), handle: { title: "Bulk consolidation" } },
    { path: "/processmfg/batch-record-check", element: createElement(ApprovalCheckPage), handle: { title: "Batch record approval check" } },
    { path: "/processmfg/batch-attributes", element: createElement(BatchAttributesPage), handle: { title: "Batch attributes" } },
    { path: "/processmfg/picking", element: createElement(PickingPage), handle: { title: "Batch picking" } },
    { path: "/processmfg/picking-stored", element: createElement(StoredPickingPage), handle: { title: "Pick from stored batches" } },
    { path: "/processmfg/rework", element: createElement(ReworkPage), handle: { title: "Rework batch order" } },
  ],
  nav: [
    { label: "Overview", to: "/processmfg", duty: "production.batch-order.read" },
    { label: "Formulas", to: "/processmfg/formulas", duty: "production.batch-order.read" },
    { label: "Batch orders", to: "/processmfg/batch-orders", duty: "production.batch-order.read" },
    { label: "Batches", to: "/processmfg/batches", duty: "production.batch-order.read" },
    { label: "Attribute definitions", to: "/processmfg/attribute-definitions", duty: "production.batch-attribute.write" },
    { label: "Potency", to: "/processmfg/potency", duty: "production.batch-order.read" },
    { label: "Co-product allocation", to: "/processmfg/allocation", duty: "production.journal.post" },
    { label: "Batch derivation", to: "/processmfg/derivation", duty: "production.batch-attribute.write" },
    { label: "Batch merge", to: "/processmfg/merge", duty: "production.batch.merge" },
    { label: "Customer batch requirements", to: "/processmfg/batch-requirements", duty: "production.batch-order.read" },
    { label: "Bulk consolidation", to: "/processmfg/consolidation", duty: "production.batch-order.write" },
    { label: "Batch record approval", to: "/processmfg/batch-record-check", duty: "production.batch-record.approve" },
    { label: "Batch attributes", to: "/processmfg/batch-attributes", duty: "production.batch-attribute.write" },
    { label: "Batch picking", to: "/processmfg/picking", duty: "production.batch-order.read" },
    { label: "Pick from stored batches", to: "/processmfg/picking-stored", duty: "production.batch-order.read" },
    { label: "Rework", to: "/processmfg/rework", duty: "production.batch-order.write" },
  ],
};

export default processmfg;

import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AttributeDefinitionsPage, BatchDetailPage, BatchesPage, NewBatchPage } from "./BatchRecordPages";
import { BatchOrderDetailPage, BatchOrdersPage, NewBatchOrderPage } from "./BatchOrderPages";
import { BatchAttributesPage, PickingPage, ReworkPage } from "./BatchQualityPages";
import { ApprovalCheckPage, ConsolidationPage, DerivationPage, MergePage, RequirementCheckPage } from "./BatchRulePages";
import { DerivationMonitorPage, DeriveStoredPage } from "./DerivationPages";
import { InheritanceRuleDetailPage, InheritanceRulesPage, NewInheritanceRulePage } from "./InheritanceRulePages";
import { CustomerRequirementDetailPage, CustomerRequirementsPage, NewCustomerRequirementPage, StoredRequirementCheckPage } from "./CustomerRequirementPages";
import { BatchRecordDetailPage, BatchRecordsPage, NewRecordProfilePage, RecordProfilesPage } from "./EbrPages";
import { AllocationPage, PotencyPage } from "./CalculatorPages";
import { FormulaDetailPage, FormulasPage, NewFormulaPage } from "./FormulaPages";
import { NewReworkOrderPage, ReworkOrderDetailPage, ReworkOrdersPage } from "./ReworkOrderPages";
import { ProcessHome } from "./pages";
import { StoredMergePage } from "./StoredMergePage";
import { StoredPickingPage } from "./StoredPickingPage";

const processmfg: PassageModule = {
  id: "processmfg",
  title: "Process manufacturing",
  description:
    "Formulas, batch orders scaled to batch size, potency consumption, co-product cost allocation, batch derivation (typed, and from stored batches with attribute inheritance rules and a derivation monitor), merge, consolidation and customer requirements, batch attributes, picking (from typed or stored batches), merging stored batches with their genealogy, stored customer batch requirements with the check over stored batches, rework, and the batch quality record with its disposition history, and the electronic batch record with its profiles, review by exception, approval and seal.",
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
    { path: "/processmfg/inheritance-rules", element: createElement(InheritanceRulesPage), handle: { title: "Inheritance rules" } },
    { path: "/processmfg/inheritance-rules/new", element: createElement(NewInheritanceRulePage), handle: { title: "Define rule" } },
    { path: "/processmfg/inheritance-rules/:id", element: createElement(InheritanceRuleDetailPage), handle: { title: "Inheritance rule" } },
    { path: "/processmfg/derive-stored", element: createElement(DeriveStoredPage), handle: { title: "Derive batch attributes" } },
    { path: "/processmfg/derivations", element: createElement(DerivationMonitorPage), handle: { title: "Derivation monitor" } },
    { path: "/processmfg/merge", element: createElement(MergePage), handle: { title: "Batch merge" } },
    { path: "/processmfg/merge-stored", element: createElement(StoredMergePage), handle: { title: "Merge stored batches" } },
    { path: "/processmfg/batch-requirements", element: createElement(RequirementCheckPage), handle: { title: "Customer batch requirements" } },
    { path: "/processmfg/customer-requirements", element: createElement(CustomerRequirementsPage), handle: { title: "Customer batch requirements" } },
    { path: "/processmfg/customer-requirements/new", element: createElement(NewCustomerRequirementPage), handle: { title: "Define requirement" } },
    { path: "/processmfg/customer-requirements/:id", element: createElement(CustomerRequirementDetailPage), handle: { title: "Customer batch requirement" } },
    { path: "/processmfg/requirement-check-stored", element: createElement(StoredRequirementCheckPage), handle: { title: "Check stored batches" } },
    { path: "/processmfg/consolidation", element: createElement(ConsolidationPage), handle: { title: "Bulk consolidation" } },
    { path: "/processmfg/batch-records", element: createElement(BatchRecordsPage), handle: { title: "Batch records" } },
    { path: "/processmfg/batch-records/:id", element: createElement(BatchRecordDetailPage), handle: { title: "Batch record" } },
    { path: "/processmfg/batch-record-profiles", element: createElement(RecordProfilesPage), handle: { title: "Batch record profiles" } },
    { path: "/processmfg/batch-record-profiles/new", element: createElement(NewRecordProfilePage), handle: { title: "Define profile" } },
    { path: "/processmfg/batch-record-check", element: createElement(ApprovalCheckPage), handle: { title: "Batch record approval check" } },
    { path: "/processmfg/batch-attributes", element: createElement(BatchAttributesPage), handle: { title: "Batch attributes" } },
    { path: "/processmfg/picking", element: createElement(PickingPage), handle: { title: "Batch picking" } },
    { path: "/processmfg/picking-stored", element: createElement(StoredPickingPage), handle: { title: "Pick from stored batches" } },
    { path: "/processmfg/rework", element: createElement(ReworkPage), handle: { title: "Rework batch order" } },
    { path: "/processmfg/rework-orders", element: createElement(ReworkOrdersPage), handle: { title: "Rework orders" } },
    { path: "/processmfg/rework-orders/new", element: createElement(NewReworkOrderPage), handle: { title: "New rework order" } },
    { path: "/processmfg/rework-orders/:id", element: createElement(ReworkOrderDetailPage), handle: { title: "Rework order" } },
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
    { label: "Inheritance rules", to: "/processmfg/inheritance-rules", duty: "production.batch-order.read" },
    { label: "Derive batch attributes", to: "/processmfg/derive-stored", duty: "production.batch-attribute.write" },
    { label: "Derivation monitor", to: "/processmfg/derivations", duty: "production.batch-order.read" },
    { label: "Batch merge", to: "/processmfg/merge", duty: "production.batch.merge" },
    { label: "Merge stored batches", to: "/processmfg/merge-stored", duty: "production.batch.merge" },
    { label: "Customer batch requirements", to: "/processmfg/batch-requirements", duty: "production.batch-order.read" },
    { label: "Requirement rules", to: "/processmfg/customer-requirements", duty: "production.batch-order.read" },
    { label: "Check stored batches", to: "/processmfg/requirement-check-stored", duty: "production.batch-order.read" },
    { label: "Bulk consolidation", to: "/processmfg/consolidation", duty: "production.batch-order.write" },
    { label: "Batch records", to: "/processmfg/batch-records", duty: "production.batch-order.read" },
    { label: "Record profiles", to: "/processmfg/batch-record-profiles", duty: "production.batch-order.read" },
    { label: "Batch record approval", to: "/processmfg/batch-record-check", duty: "production.batch-record.approve" },
    { label: "Batch attributes", to: "/processmfg/batch-attributes", duty: "production.batch-attribute.write" },
    { label: "Batch picking", to: "/processmfg/picking", duty: "production.batch-order.read" },
    { label: "Pick from stored batches", to: "/processmfg/picking-stored", duty: "production.batch-order.read" },
    { label: "Rework", to: "/processmfg/rework", duty: "production.batch-order.write" },
    { label: "Rework orders", to: "/processmfg/rework-orders", duty: "production.batch-order.read" },
  ],
};

export default processmfg;

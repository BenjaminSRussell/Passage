import { createElement } from "react";
import type { PassageModule } from "../registry";
import { EntryScreen, TrialBalanceScreen } from "./pages";
import { EditReasonCodePage, NewReasonCodePage } from "./ReasonCodeFormPage";
import { ReasonCodesPage } from "./ReasonCodesPage";
import { DimensionValuesPage } from "./DimensionValuesPage";
import { EditDescriptionTemplatePage, NewDescriptionTemplatePage } from "./DescriptionTemplateFormPage";
import { DescriptionTemplatesPage } from "./DescriptionTemplatesPage";
import { EditStructurePage, NewStructurePage } from "./StructureFormPage";
import { StructuresPage } from "./StructuresPage";
import { JournalBatchesPage } from "./JournalBatchesPage";
import { JournalBatchPage, NewJournalBatchPage } from "./JournalBatchPage";
import { EditJournalNamePage, NewJournalNamePage } from "./JournalNameFormPage";
import { JournalNamesPage } from "./JournalNamesPage";
import { LedgerLinesPage } from "./LedgerLinesPage";
import { NewRecurringJournalPage, RecurringJournalPage } from "./RecurringJournalPage";
import { RecurringJournalsPage } from "./RecurringJournalsPage";
import { OpenItemsPage } from "./OpenItemsPage";
import { OpenItemAccountsPage } from "./OpenItemAccountsPage";
import { ClearingsPage } from "./ClearingsPage";
import { ClearingPage } from "./ClearingPage";
import { EditToleranceGroupPage, NewToleranceGroupPage } from "./ToleranceGroupFormPage";
import { ToleranceGroupsPage } from "./ToleranceGroupsPage";
import { AutoClearingRulesPage } from "./AutoClearingRulesPage";
import { EditAutoClearingRulePage, NewAutoClearingRulePage } from "./AutoClearingRuleFormPage";
import { LookupTablesPage } from "./LookupTablesPage";
import { EditLookupTablePage, NewLookupTablePage } from "./LookupTableFormPage";
import { DerivationStrategiesPage } from "./DerivationStrategiesPage";
import { DerivationStrategyPage, NewDerivationStrategyPage } from "./DerivationStrategyPage";
import { AutoClearingRunDetailPage, AutoClearingRunPage } from "./AutoClearingRunPage";

const ledger: PassageModule = {
  id: "ledger",
  title: "General ledger",
  description: "Universal journal: manual journal batches with approval, recurring journals, open items with manual and automatic clearing, clearing rules, tolerance groups, trial balance, ledger transactions inquiry, posted journal entries and their reversal, journal names, shared reason codes, account structures, dimension values, dimension derivation strategies with lookup tables and default description templates.",
  routes: [
    { path: "/ledger/journal-batches", element: createElement(JournalBatchesPage), handle: { title: "Journal batches" } },
    { path: "/ledger/journal-batches/new", element: createElement(NewJournalBatchPage), handle: { title: "New journal batch" } },
    { path: "/ledger/journal-batches/:id", element: createElement(JournalBatchPage), handle: { title: "Journal batch" } },
    { path: "/ledger/journal-names", element: createElement(JournalNamesPage), handle: { title: "Journal names" } },
    { path: "/ledger/journal-names/new", element: createElement(NewJournalNamePage), handle: { title: "New journal name" } },
    { path: "/ledger/journal-names/:code", element: createElement(EditJournalNamePage), handle: { title: "Journal name" } },
    { path: "/ledger/recurring-journals", element: createElement(RecurringJournalsPage), handle: { title: "Recurring journals" } },
    { path: "/ledger/recurring-journals/new", element: createElement(NewRecurringJournalPage), handle: { title: "New recurring journal" } },
    { path: "/ledger/recurring-journals/:id", element: createElement(RecurringJournalPage), handle: { title: "Recurring journal" } },
    { path: "/ledger/open-items", element: createElement(OpenItemsPage), handle: { title: "Open items" } },
    { path: "/ledger/open-item-accounts", element: createElement(OpenItemAccountsPage), handle: { title: "Open-item accounts" } },
    { path: "/ledger/clearings", element: createElement(ClearingsPage), handle: { title: "Clearing documents" } },
    { path: "/ledger/clearings/:id", element: createElement(ClearingPage), handle: { title: "Clearing document" } },
    { path: "/ledger/tolerance-groups", element: createElement(ToleranceGroupsPage), handle: { title: "Tolerance groups" } },
    { path: "/ledger/tolerance-groups/new", element: createElement(NewToleranceGroupPage), handle: { title: "New tolerance group" } },
    { path: "/ledger/tolerance-groups/:code", element: createElement(EditToleranceGroupPage), handle: { title: "Tolerance group" } },
    { path: "/ledger/auto-clearing", element: createElement(AutoClearingRunPage), handle: { title: "Automatic clearing" } },
    { path: "/ledger/auto-clearing-runs/:id", element: createElement(AutoClearingRunDetailPage), handle: { title: "Automatic clearing run", duty: "ledger.journal.read" } },
    { path: "/ledger/auto-clearing-rules", element: createElement(AutoClearingRulesPage), handle: { title: "Automatic clearing rules" } },
    { path: "/ledger/auto-clearing-rules/new", element: createElement(NewAutoClearingRulePage), handle: { title: "New automatic clearing rule" } },
    { path: "/ledger/auto-clearing-rules/:id", element: createElement(EditAutoClearingRulePage), handle: { title: "Automatic clearing rule" } },
    { path: "/ledger/lookup-tables", element: createElement(LookupTablesPage), handle: { title: "Lookup tables" } },
    { path: "/ledger/lookup-tables/new", element: createElement(NewLookupTablePage), handle: { title: "New lookup table" } },
    { path: "/ledger/lookup-tables/:id", element: createElement(EditLookupTablePage), handle: { title: "Lookup table" } },
    { path: "/ledger/derivation-strategies", element: createElement(DerivationStrategiesPage), handle: { title: "Derivation strategies" } },
    { path: "/ledger/derivation-strategies/new", element: createElement(NewDerivationStrategyPage), handle: { title: "New derivation strategy version" } },
    { path: "/ledger/derivation-strategies/:version", element: createElement(DerivationStrategyPage), handle: { title: "Derivation strategy" } },
    { path: "/ledger/trial-balance", element: createElement(TrialBalanceScreen), handle: { title: "Trial balance" } },
    { path: "/ledger/transactions", element: createElement(LedgerLinesPage), handle: { title: "Ledger transactions" } },
    { path: "/ledger/entries/:id", element: createElement(EntryScreen), handle: { title: "Journal entry", duty: "ledger.journal.read" } },
    { path: "/ledger/reason-codes", element: createElement(ReasonCodesPage), handle: { title: "Reason codes" } },
    { path: "/ledger/reason-codes/new", element: createElement(NewReasonCodePage), handle: { title: "New reason code" } },
    { path: "/ledger/reason-codes/:code", element: createElement(EditReasonCodePage), handle: { title: "Reason code" } },
    { path: "/ledger/structures", element: createElement(StructuresPage), handle: { title: "Account structures" } },
    { path: "/ledger/structures/new", element: createElement(NewStructurePage), handle: { title: "New account structure" } },
    { path: "/ledger/structures/:id", element: createElement(EditStructurePage), handle: { title: "Account structure" } },
    { path: "/ledger/dimension-values", element: createElement(DimensionValuesPage), handle: { title: "Dimension values" } },
    { path: "/ledger/description-templates", element: createElement(DescriptionTemplatesPage), handle: { title: "Description templates" } },
    { path: "/ledger/description-templates/new", element: createElement(NewDescriptionTemplatePage), handle: { title: "New description template" } },
    { path: "/ledger/description-templates/:id", element: createElement(EditDescriptionTemplatePage), handle: { title: "Description template" } },
  ],
  nav: [
    { label: "Journal batches", to: "/ledger/journal-batches", duty: "ledger.journal.read" },
    { label: "Recurring journals", to: "/ledger/recurring-journals", duty: "ledger.journal.read" },
    { label: "Open items", to: "/ledger/open-items", duty: "ledger.journal.read" },
    { label: "Clearing documents", to: "/ledger/clearings", duty: "ledger.journal.read" },
    { label: "Open-item accounts", to: "/ledger/open-item-accounts", duty: "ledger.journal.read" },
    { label: "Tolerance groups", to: "/ledger/tolerance-groups", duty: "ledger.journal.read" },
    { label: "Automatic clearing", to: "/ledger/auto-clearing", duty: "ledger.journal.read" },
    { label: "Clearing rules", to: "/ledger/auto-clearing-rules", duty: "ledger.journal.read" },
    { label: "Trial balance", to: "/ledger/trial-balance", duty: "ledger.balance.read" },
    { label: "Ledger transactions", to: "/ledger/transactions", duty: "ledger.journal.read" },
    { label: "Journal names", to: "/ledger/journal-names", duty: "ledger.journal.read" },
    { label: "Reason codes", to: "/ledger/reason-codes", duty: "ledger.journal.read" },
    { label: "Account structures", to: "/ledger/structures", duty: "ledger.journal.read" },
    { label: "Dimension values", to: "/ledger/dimension-values", duty: "ledger.journal.read" },
    { label: "Derivation strategies", to: "/ledger/derivation-strategies", duty: "ledger.journal.read" },
    { label: "Lookup tables", to: "/ledger/lookup-tables", duty: "ledger.journal.read" },
    { label: "Description templates", to: "/ledger/description-templates", duty: "ledger.journal.read" },
  ],
};

export default ledger;

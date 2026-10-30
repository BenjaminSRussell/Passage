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

const ledger: PassageModule = {
  id: "ledger",
  title: "General ledger",
  description: "Universal journal: manual journal batches with approval, recurring journals, trial balance, ledger transactions inquiry, posted journal entries and their reversal, journal names, shared reason codes, account structures, dimension values and default description templates.",
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
    { label: "Trial balance", to: "/ledger/trial-balance", duty: "ledger.balance.read" },
    { label: "Ledger transactions", to: "/ledger/transactions", duty: "ledger.journal.read" },
    { label: "Journal names", to: "/ledger/journal-names", duty: "ledger.journal.read" },
    { label: "Reason codes", to: "/ledger/reason-codes", duty: "ledger.journal.read" },
    { label: "Account structures", to: "/ledger/structures", duty: "ledger.journal.read" },
    { label: "Dimension values", to: "/ledger/dimension-values", duty: "ledger.journal.read" },
    { label: "Description templates", to: "/ledger/description-templates", duty: "ledger.journal.read" },
  ],
};

export default ledger;

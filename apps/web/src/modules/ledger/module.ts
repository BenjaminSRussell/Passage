import { createElement } from "react";
import type { PassageModule } from "../registry";
import { EntryScreen, TrialBalanceScreen } from "./pages";
import { EditReasonCodePage, NewReasonCodePage } from "./ReasonCodeFormPage";
import { ReasonCodesPage } from "./ReasonCodesPage";
import { DimensionValuesPage } from "./DimensionValuesPage";
import { EditStructurePage, NewStructurePage } from "./StructureFormPage";
import { StructuresPage } from "./StructuresPage";

const ledger: PassageModule = {
  id: "ledger",
  title: "General ledger",
  description: "Universal journal: trial balance, posted journal entries and their reversal, shared reason codes, account structures and dimension values.",
  routes: [
    { path: "/ledger/trial-balance", element: createElement(TrialBalanceScreen), handle: { title: "Trial balance" } },
    { path: "/ledger/entries/:id", element: createElement(EntryScreen), handle: { title: "Journal entry" } },
    { path: "/ledger/reason-codes", element: createElement(ReasonCodesPage), handle: { title: "Reason codes" } },
    { path: "/ledger/reason-codes/new", element: createElement(NewReasonCodePage), handle: { title: "New reason code" } },
    { path: "/ledger/reason-codes/:code", element: createElement(EditReasonCodePage), handle: { title: "Reason code" } },
    { path: "/ledger/structures", element: createElement(StructuresPage), handle: { title: "Account structures" } },
    { path: "/ledger/structures/new", element: createElement(NewStructurePage), handle: { title: "New account structure" } },
    { path: "/ledger/structures/:id", element: createElement(EditStructurePage), handle: { title: "Account structure" } },
    { path: "/ledger/dimension-values", element: createElement(DimensionValuesPage), handle: { title: "Dimension values" } },
  ],
  nav: [
    { label: "Trial balance", to: "/ledger/trial-balance", duty: "ledger.balance.read" },
    { label: "Reason codes", to: "/ledger/reason-codes", duty: "ledger.journal.read" },
    { label: "Account structures", to: "/ledger/structures", duty: "ledger.journal.read" },
    { label: "Dimension values", to: "/ledger/dimension-values", duty: "ledger.journal.read" },
  ],
};

export default ledger;

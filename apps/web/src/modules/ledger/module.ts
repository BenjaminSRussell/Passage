import { createElement } from "react";
import type { PassageModule } from "../registry";
import { EntryScreen, TrialBalanceScreen } from "./pages";
import { EditReasonCodePage, NewReasonCodePage } from "./ReasonCodeFormPage";
import { ReasonCodesPage } from "./ReasonCodesPage";

const ledger: PassageModule = {
  id: "ledger",
  title: "General ledger",
  description: "Universal journal: trial balance, posted journal entries and their reversal, and shared reason codes.",
  routes: [
    { path: "/ledger/trial-balance", element: createElement(TrialBalanceScreen), handle: { title: "Trial balance" } },
    { path: "/ledger/entries/:id", element: createElement(EntryScreen), handle: { title: "Journal entry" } },
    { path: "/ledger/reason-codes", element: createElement(ReasonCodesPage), handle: { title: "Reason codes" } },
    { path: "/ledger/reason-codes/new", element: createElement(NewReasonCodePage), handle: { title: "New reason code" } },
    { path: "/ledger/reason-codes/:code", element: createElement(EditReasonCodePage), handle: { title: "Reason code" } },
  ],
  nav: [
    { label: "Trial balance", to: "/ledger/trial-balance", duty: "ledger.balance.read" },
    { label: "Reason codes", to: "/ledger/reason-codes", duty: "ledger.journal.read" },
  ],
};

export default ledger;

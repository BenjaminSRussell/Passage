import { createElement } from "react";
import type { PassageModule } from "../registry";
import { EntryScreen, TrialBalanceScreen } from "./pages";

const ledger: PassageModule = {
  id: "ledger",
  title: "General ledger",
  description: "Universal journal: trial balance and posted journal entries.",
  routes: [
    { path: "/ledger/trial-balance", element: createElement(TrialBalanceScreen) },
    { path: "/ledger/entries/:id", element: createElement(EntryScreen) },
  ],
  nav: [{ label: "Trial balance", to: "/ledger/trial-balance", duty: "ledger.balance.read" }],
};

export default ledger;

import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BankAccountDetailPage, BankAccountsPage, NewBankAccountPage } from "./BankAccounts";
import { CashForecastPage } from "./CashForecast";
import { ImportStatementPage, StatementDetailPage } from "./Statements";

const treasury: PassageModule = {
  id: "treasury",
  title: "Treasury",
  description: "House bank accounts and their lifecycle, camt.053 bank statement import, and the cash flow forecast per legal entity.",
  routes: [
    { path: "/treasury", element: createElement(BankAccountsPage), handle: { title: "Bank accounts" } },
    { path: "/treasury/bank-accounts/new", element: createElement(NewBankAccountPage), handle: { title: "New bank account" } },
    { path: "/treasury/bank-accounts/:id", element: createElement(BankAccountDetailPage), handle: { title: "Bank account" } },
    { path: "/treasury/statements/import", element: createElement(ImportStatementPage), handle: { title: "Import bank statement" } },
    { path: "/treasury/statements/:id", element: createElement(StatementDetailPage), handle: { title: "Bank statement" } },
    { path: "/treasury/cash-forecast", element: createElement(CashForecastPage), handle: { title: "Cash forecast" } },
  ],
  nav: [
    { label: "Bank accounts", to: "/treasury", duty: "treasury.view" },
    { label: "Import statement", to: "/treasury/statements/import", duty: "treasury.statement.import" },
    { label: "Cash forecast", to: "/treasury/cash-forecast", duty: "treasury.view" },
  ],
};

export default treasury;

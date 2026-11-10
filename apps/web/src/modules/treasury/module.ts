import { createElement } from "react";
import type { PassageModule } from "../registry";
import { BankAccountDetailPage, BankAccountsPage, NewBankAccountPage } from "./BankAccounts";
import { CashDeskDetailPage, CashDesksPage } from "./CashDesks";
import { CashForecastPage } from "./CashForecast";
import { CashPositionPage } from "./CashPosition";
import { CheckDetailPage, ChecksPage, NewCheckPage } from "./Checks";
import { FacilitiesPage, FacilityDetailPage, NewFacilityPage } from "./Facilities";
import { LoanDetailPage, LoansPage, NewLoanPage } from "./Loans";
import { NewReconciliationPage, ReconciliationDetailPage, ReconciliationsPage } from "./Reconciliations";
import { ImportStatementPage, StatementDetailPage } from "./Statements";

const treasury: PassageModule = {
  id: "treasury",
  title: "Treasury",
  description:
    "House bank accounts and their lifecycle, camt.053, BAI2 and MT940 bank statement import, bank reconciliation with close and reopen, cash desks with a daily cash journal, the cash position from intraday reports, the cash flow forecast, checks, bank facilities with letters of credit and guarantees, and loan contracts.",
  routes: [
    { path: "/treasury", element: createElement(BankAccountsPage), handle: { title: "Bank accounts" } },
    { path: "/treasury/bank-accounts/new", element: createElement(NewBankAccountPage), handle: { title: "New bank account" } },
    { path: "/treasury/bank-accounts/:id", element: createElement(BankAccountDetailPage), handle: { title: "Bank account" } },
    { path: "/treasury/statements/import", element: createElement(ImportStatementPage), handle: { title: "Import bank statement" } },
    { path: "/treasury/statements/:id", element: createElement(StatementDetailPage), handle: { title: "Bank statement" } },
    { path: "/treasury/reconciliations", element: createElement(ReconciliationsPage), handle: { title: "Bank reconciliations" } },
    { path: "/treasury/reconciliations/new", element: createElement(NewReconciliationPage), handle: { title: "Open a reconciliation" } },
    { path: "/treasury/reconciliations/:id", element: createElement(ReconciliationDetailPage), handle: { title: "Bank reconciliation" } },
    { path: "/treasury/cash-desks", element: createElement(CashDesksPage), handle: { title: "Cash desks" } },
    { path: "/treasury/cash-desks/:id", element: createElement(CashDeskDetailPage), handle: { title: "Cash desk" } },
    { path: "/treasury/cash-position", element: createElement(CashPositionPage), handle: { title: "Cash position" } },
    { path: "/treasury/cash-forecast", element: createElement(CashForecastPage), handle: { title: "Cash forecast" } },
    { path: "/treasury/checks", element: createElement(ChecksPage), handle: { title: "Checks" } },
    { path: "/treasury/checks/new", element: createElement(NewCheckPage), handle: { title: "Issue check" } },
    { path: "/treasury/checks/:id", element: createElement(CheckDetailPage), handle: { title: "Check" } },
    { path: "/treasury/facilities", element: createElement(FacilitiesPage), handle: { title: "Facilities" } },
    { path: "/treasury/facilities/new", element: createElement(NewFacilityPage), handle: { title: "New facility" } },
    { path: "/treasury/facilities/:id", element: createElement(FacilityDetailPage), handle: { title: "Facility" } },
    { path: "/treasury/loans", element: createElement(LoansPage), handle: { title: "Loans" } },
    { path: "/treasury/loans/new", element: createElement(NewLoanPage), handle: { title: "New loan" } },
    { path: "/treasury/loans/:id", element: createElement(LoanDetailPage), handle: { title: "Loan" } },
  ],
  nav: [
    { label: "Bank accounts", to: "/treasury", duty: "treasury.view" },
    { label: "Import statement", to: "/treasury/statements/import", duty: "treasury.statement.import" },
    { label: "Reconciliations", to: "/treasury/reconciliations", duty: "treasury.reconciliation.view" },
    { label: "Cash desks", to: "/treasury/cash-desks", duty: "treasury.view" },
    { label: "Cash position", to: "/treasury/cash-position", duty: "treasury.view" },
    { label: "Cash forecast", to: "/treasury/cash-forecast", duty: "treasury.view" },
    { label: "Checks", to: "/treasury/checks", duty: "treasury.view" },
    { label: "Facilities", to: "/treasury/facilities", duty: "treasury.view" },
    { label: "Loans", to: "/treasury/loans", duty: "treasury.view" },
  ],
};

export default treasury;

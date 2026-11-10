import { createElement } from "react";
import type { PassageModule } from "../registry";
import { IntrastatJournalPage } from "./IntrastatJournalPage";
import { IntrastatJournalsPage } from "./IntrastatJournalsPage";
import { IntrastatMasterPage } from "./IntrastatMasterPage";
import { JurisdictionsPage } from "./JurisdictionsPage";
import { EslPage, VatReturnPage } from "./ReturnPages";
import { TaxCodesPage } from "./TaxCodesPage";
import { TaxGroupsPage } from "./TaxGroupsPage";
import { TaxHomePage } from "./TaxHomePage";
import { VatBoxesPage } from "./VatBoxesPage";

/** The tax configuration workspace: jurisdictions, tax codes and groups, the VAT return, the EU sales list and Intrastat. */
const tax: PassageModule = {
  id: "tax",
  title: "Tax",
  description: "Set up taxes and file the VAT return, the EU sales list and the Intrastat declaration.",
  routes: [
    { path: "/tax", element: createElement(TaxHomePage), handle: { title: "Tax", duty: "tax.config.read" } },
    { path: "/tax/jurisdictions", element: createElement(JurisdictionsPage), handle: { title: "Jurisdictions", duty: "tax.config.read" } },
    { path: "/tax/codes", element: createElement(TaxCodesPage), handle: { title: "Tax codes", duty: "tax.config.read" } },
    { path: "/tax/groups", element: createElement(TaxGroupsPage), handle: { title: "Tax groups", duty: "tax.config.read" } },
    { path: "/tax/vat-boxes", element: createElement(VatBoxesPage), handle: { title: "Return boxes", duty: "tax.config.read" } },
    { path: "/tax/vat-return", element: createElement(VatReturnPage), handle: { title: "VAT return", duty: "tax.vatreturn.read" } },
    { path: "/tax/esl", element: createElement(EslPage), handle: { title: "EU sales list", duty: "tax.vatreturn.read" } },
    { path: "/tax/intrastat", element: createElement(IntrastatJournalsPage), handle: { title: "Intrastat declarations", duty: "tax.intrastat.read" } },
    { path: "/tax/intrastat/journals/:id", element: createElement(IntrastatJournalPage), handle: { title: "Intrastat journal", duty: "tax.intrastat.read" } },
    { path: "/tax/intrastat/master", element: createElement(IntrastatMasterPage), handle: { title: "Intrastat master data", duty: "tax.intrastat.read" } },
  ],
  nav: [
    { label: "Tax", to: "/tax", duty: "tax.config.read" },
    { label: "Jurisdictions", to: "/tax/jurisdictions", duty: "tax.config.read" },
    { label: "Tax codes", to: "/tax/codes", duty: "tax.config.read" },
    { label: "Tax groups", to: "/tax/groups", duty: "tax.config.read" },
    { label: "Return boxes", to: "/tax/vat-boxes", duty: "tax.config.read" },
    { label: "VAT return", to: "/tax/vat-return", duty: "tax.vatreturn.read" },
    { label: "EU sales list", to: "/tax/esl", duty: "tax.vatreturn.read" },
    { label: "Intrastat", to: "/tax/intrastat", duty: "tax.intrastat.read" },
    { label: "Intrastat master data", to: "/tax/intrastat/master", duty: "tax.intrastat.read" },
  ],
};

export default tax;

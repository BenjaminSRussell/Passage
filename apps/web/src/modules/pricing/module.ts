import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage } from "./AgreementsPage";
import { CampaignDetailPage } from "./CampaignDetailPage";
import { CampaignsPage } from "./CampaignsPage";
import { CommissionTransactionsPage } from "./CommissionTransactionsPage";
import { FundDetailPage } from "./FundDetailPage";
import { FundsPage } from "./FundsPage";
import { JournalDetailPage } from "./JournalDetailPage";
import { JournalsPage } from "./JournalsPage";
import { NewAgreementPage } from "./NewAgreementPage";
import { NewCampaignPage } from "./NewCampaignPage";
import { NewFundPage } from "./NewFundPage";
import { NewJournalPage } from "./NewJournalPage";
import { NewProcedurePage } from "./NewProcedurePage";
import { NewPromotionPage } from "./NewPromotionPage";
import { NewRebatePage } from "./NewRebatePage";
import { NewSalesGroupPage } from "./NewSalesGroupPage";
import { ProcedureDetailPage } from "./ProcedureDetailPage";
import { ProceduresPage } from "./ProceduresPage";
import { PromotionDetailPage } from "./PromotionDetailPage";
import { PromotionsPage } from "./PromotionsPage";
import { RebateDetailPage } from "./RebateDetailPage";
import { RebatesPage } from "./RebatesPage";
import { SalesGroupDetailPage } from "./SalesGroupDetailPage";
import { SalesGroupsPage } from "./SalesGroupsPage";
import { SimulatePage } from "./SimulatePage";
import { VendorClaimDetailPage } from "./VendorClaimDetailPage";
import { VendorClaimsPage } from "./VendorClaimsPage";
import { WorkspacePage } from "./WorkspacePage";

const pricing: PassageModule = {
  id: "pricing",
  title: "Pricing",
  description:
    "Trade agreements, campaigns, pricing procedures, price journals, customer and vendor rebates, commissions, trade funds, promotions and the explanation behind every price.",
  routes: [
    { path: "/pricing", element: createElement(WorkspacePage), handle: { title: "Pricing workspace" } },
    { path: "/pricing/agreements", element: createElement(AgreementsPage), handle: { title: "Trade agreements" } },
    { path: "/pricing/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New trade agreement" } },
    { path: "/pricing/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Trade agreement" } },
    { path: "/pricing/campaigns", element: createElement(CampaignsPage), handle: { title: "Campaigns" } },
    { path: "/pricing/campaigns/new", element: createElement(NewCampaignPage), handle: { title: "New campaign" } },
    { path: "/pricing/campaigns/:id", element: createElement(CampaignDetailPage), handle: { title: "Campaign" } },
    { path: "/pricing/procedures", element: createElement(ProceduresPage), handle: { title: "Pricing procedures" } },
    { path: "/pricing/procedures/new", element: createElement(NewProcedurePage), handle: { title: "New pricing procedure" } },
    { path: "/pricing/procedures/:id", element: createElement(ProcedureDetailPage), handle: { title: "Pricing procedure" } },
    { path: "/pricing/journals", element: createElement(JournalsPage), handle: { title: "Price journals" } },
    { path: "/pricing/journals/new", element: createElement(NewJournalPage), handle: { title: "New price journal" } },
    { path: "/pricing/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Price journal" } },
    { path: "/pricing/rebates", element: createElement(RebatesPage), handle: { title: "Rebate agreements" } },
    { path: "/pricing/rebates/new", element: createElement(NewRebatePage), handle: { title: "New rebate agreement" } },
    { path: "/pricing/rebates/claims", element: createElement(VendorClaimsPage), handle: { title: "Vendor rebate claims" } },
    { path: "/pricing/rebates/claims/:id", element: createElement(VendorClaimDetailPage), handle: { title: "Vendor rebate claim" } },
    { path: "/pricing/rebates/:id", element: createElement(RebateDetailPage), handle: { title: "Rebate agreement" } },
    { path: "/pricing/commission/sales-groups", element: createElement(SalesGroupsPage), handle: { title: "Sales groups" } },
    { path: "/pricing/commission/sales-groups/new", element: createElement(NewSalesGroupPage), handle: { title: "New sales group" } },
    { path: "/pricing/commission/sales-groups/:id", element: createElement(SalesGroupDetailPage), handle: { title: "Sales group" } },
    { path: "/pricing/commission/transactions", element: createElement(CommissionTransactionsPage), handle: { title: "Commission transactions" } },
    { path: "/pricing/funds", element: createElement(FundsPage), handle: { title: "Trade funds" } },
    { path: "/pricing/funds/new", element: createElement(NewFundPage), handle: { title: "New trade fund" } },
    { path: "/pricing/funds/:id", element: createElement(FundDetailPage), handle: { title: "Trade fund" } },
    { path: "/pricing/promotions", element: createElement(PromotionsPage), handle: { title: "Promotions" } },
    { path: "/pricing/promotions/new", element: createElement(NewPromotionPage), handle: { title: "New promotion" } },
    { path: "/pricing/promotions/:id", element: createElement(PromotionDetailPage), handle: { title: "Promotion" } },
    { path: "/pricing/simulate", element: createElement(SimulatePage), handle: { title: "Price simulation" } },
  ],
  nav: [
    { label: "Overview", to: "/pricing", duty: "pricing.view" },
    { label: "Trade agreements", to: "/pricing/agreements", duty: "pricing.view" },
    { label: "Campaigns", to: "/pricing/campaigns", duty: "pricing.view" },
    { label: "Pricing procedures", to: "/pricing/procedures", duty: "pricing.view" },
    { label: "Price journals", to: "/pricing/journals", duty: "pricing.view" },
    { label: "Rebate agreements", to: "/pricing/rebates", duty: "pricing.rebate.view" },
    { label: "Vendor rebate claims", to: "/pricing/rebates/claims", duty: "pricing.rebate.view" },
    { label: "Sales groups", to: "/pricing/commission/sales-groups", duty: "pricing.commission.view" },
    { label: "Commission transactions", to: "/pricing/commission/transactions", duty: "pricing.commission.view" },
    { label: "Trade funds", to: "/pricing/funds", duty: "pricing.view" },
    { label: "Promotions", to: "/pricing/promotions", duty: "pricing.view" },
    { label: "Price simulation", to: "/pricing/simulate", duty: "pricing.view" },
  ],
};

export default pricing;

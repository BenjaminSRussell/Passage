import { createElement } from "react";
import type { PassageModule } from "../registry";
import { AgreementDetailPage } from "./AgreementDetailPage";
import { AgreementsPage } from "./AgreementsPage";
import { BrokerDetailPage } from "./BrokerDetailPage";
import { BrokersPage } from "./BrokersPage";
import { ContractClaimDetailPage } from "./ContractClaimDetailPage";
import { ContractClaimsPage } from "./ContractClaimsPage";
import { NewBrokerPage } from "./NewBrokerPage";
import { NewRoyaltyPage } from "./NewRoyaltyPage";
import { RoyaltiesPage } from "./RoyaltiesPage";
import { RoyaltyDetailPage } from "./RoyaltyDetailPage";
import { CampaignDetailPage } from "./CampaignDetailPage";
import { CampaignsPage } from "./CampaignsPage";
import { ComparePage } from "./ComparePage";
import { PriceListPage } from "./PriceListPage";
import { CommissionSettlementPage } from "./CommissionSettlementPage";
import { CommissionStatementPage } from "./CommissionStatementPage";
import { CommissionTransactionsPage } from "./CommissionTransactionsPage";
import { CommodityBasesPage } from "./CommodityBasesPage";
import { CommodityBasisDetailPage } from "./CommodityBasisDetailPage";
import { CommodityCalculationDetailPage } from "./CommodityCalculationDetailPage";
import { CommodityCalculationsPage } from "./CommodityCalculationsPage";
import { FreeGoodsDetailPage } from "./FreeGoodsDetailPage";
import { FreeGoodsPage } from "./FreeGoodsPage";
import { FundDetailPage } from "./FundDetailPage";
import { FundsPage } from "./FundsPage";
import { ImportJournalPage } from "./ImportJournalPage";
import { JournalDetailPage } from "./JournalDetailPage";
import { JournalsPage } from "./JournalsPage";
import { ParametersPage } from "./ParametersPage";
import { TenantParametersPage } from "./TenantParametersPage";
import { NewAgreementPage } from "./NewAgreementPage";
import { NewCampaignPage } from "./NewCampaignPage";
import { NewCommodityBasisPage } from "./NewCommodityBasisPage";
import { NewCommodityCalculationPage } from "./NewCommodityCalculationPage";
import { NewFundPage } from "./NewFundPage";
import { NewFreeGoodsPage } from "./NewFreeGoodsPage";
import { NewJournalPage } from "./NewJournalPage";
import { NewProcedurePage } from "./NewProcedurePage";
import { NewPromotionPage } from "./NewPromotionPage";
import { NewRebatePage } from "./NewRebatePage";
import { NewSalesGroupPage } from "./NewSalesGroupPage";
import { ProcedureAssignmentsPage } from "./ProcedureAssignmentsPage";
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
    "Trade agreements, campaigns, free goods, broker contracts and royalty agreements, commodity index pricing, pricing procedures, price journals, customer and vendor rebates, commissions, trade funds, promotions, journal CSV import, commission settlement and statement, price lists by customer, procedure version comparison and the explanation behind every price.",
  routes: [
    { path: "/pricing", element: createElement(WorkspacePage), handle: { title: "Pricing workspace" } },
    { path: "/pricing/agreements", element: createElement(AgreementsPage), handle: { title: "Trade agreements" } },
    { path: "/pricing/agreements/new", element: createElement(NewAgreementPage), handle: { title: "New trade agreement" } },
    { path: "/pricing/agreements/:id", element: createElement(AgreementDetailPage), handle: { title: "Trade agreement" } },
    { path: "/pricing/campaigns", element: createElement(CampaignsPage), handle: { title: "Campaigns" } },
    { path: "/pricing/campaigns/new", element: createElement(NewCampaignPage), handle: { title: "New campaign" } },
    { path: "/pricing/campaigns/:id", element: createElement(CampaignDetailPage), handle: { title: "Campaign" } },
    { path: "/pricing/free-goods", element: createElement(FreeGoodsPage), handle: { title: "Free goods" } },
    { path: "/pricing/free-goods/new", element: createElement(NewFreeGoodsPage), handle: { title: "New free goods rule" } },
    { path: "/pricing/free-goods/:id", element: createElement(FreeGoodsDetailPage), handle: { title: "Free goods rule" } },
    { path: "/pricing/commodity/bases", element: createElement(CommodityBasesPage), handle: { title: "Commodity prices" } },
    { path: "/pricing/commodity/bases/new", element: createElement(NewCommodityBasisPage), handle: { title: "New cost basis type" } },
    { path: "/pricing/commodity/bases/:id", element: createElement(CommodityBasisDetailPage), handle: { title: "Cost basis type" } },
    { path: "/pricing/commodity/calculations", element: createElement(CommodityCalculationsPage), handle: { title: "Price calculations" } },
    { path: "/pricing/commodity/calculations/new", element: createElement(NewCommodityCalculationPage), handle: { title: "New price calculation" } },
    { path: "/pricing/commodity/calculations/:id", element: createElement(CommodityCalculationDetailPage), handle: { title: "Price calculation" } },
    { path: "/pricing/procedures", element: createElement(ProceduresPage), handle: { title: "Pricing procedures" } },
    { path: "/pricing/procedures/new", element: createElement(NewProcedurePage), handle: { title: "New pricing procedure" } },
    { path: "/pricing/procedures/:id", element: createElement(ProcedureDetailPage), handle: { title: "Pricing procedure" } },
    { path: "/pricing/procedure-determination", element: createElement(ProcedureAssignmentsPage), handle: { title: "Procedure determination" } },
    { path: "/pricing/journals", element: createElement(JournalsPage), handle: { title: "Price journals" } },
    { path: "/pricing/journals/new", element: createElement(NewJournalPage), handle: { title: "New price journal" } },
    { path: "/pricing/journals/import", element: createElement(ImportJournalPage), handle: { title: "Import price journal" } },
    { path: "/pricing/journals/:id", element: createElement(JournalDetailPage), handle: { title: "Price journal" } },
    { path: "/pricing/rebates", element: createElement(RebatesPage), handle: { title: "Rebate agreements" } },
    { path: "/pricing/rebates/new", element: createElement(NewRebatePage), handle: { title: "New rebate agreement" } },
    { path: "/pricing/rebates/claims", element: createElement(VendorClaimsPage), handle: { title: "Vendor rebate claims" } },
    { path: "/pricing/rebates/claims/:id", element: createElement(VendorClaimDetailPage), handle: { title: "Vendor rebate claim" } },
    { path: "/pricing/rebates/:id", element: createElement(RebateDetailPage), handle: { title: "Rebate agreement" } },
    { path: "/pricing/brokers", element: createElement(BrokersPage), handle: { title: "Broker contracts" } },
    { path: "/pricing/brokers/new", element: createElement(NewBrokerPage), handle: { title: "New broker contract" } },
    { path: "/pricing/brokers/:id", element: createElement(BrokerDetailPage), handle: { title: "Broker contract" } },
    { path: "/pricing/royalties", element: createElement(RoyaltiesPage), handle: { title: "Royalty agreements" } },
    { path: "/pricing/royalties/new", element: createElement(NewRoyaltyPage), handle: { title: "New royalty agreement" } },
    { path: "/pricing/royalties/:id", element: createElement(RoyaltyDetailPage), handle: { title: "Royalty agreement" } },
    { path: "/pricing/contract-claims", element: createElement(ContractClaimsPage), handle: { title: "Broker and royalty claims" } },
    { path: "/pricing/contract-claims/:id", element: createElement(ContractClaimDetailPage), handle: { title: "Broker or royalty claim" } },
    { path: "/pricing/commission/sales-groups", element: createElement(SalesGroupsPage), handle: { title: "Sales groups" } },
    { path: "/pricing/commission/sales-groups/new", element: createElement(NewSalesGroupPage), handle: { title: "New sales group" } },
    { path: "/pricing/commission/sales-groups/:id", element: createElement(SalesGroupDetailPage), handle: { title: "Sales group" } },
    { path: "/pricing/commission/transactions", element: createElement(CommissionTransactionsPage), handle: { title: "Commission transactions" } },
    { path: "/pricing/commission/settlement", element: createElement(CommissionSettlementPage), handle: { title: "Commission settlement" } },
    { path: "/pricing/commission/statement", element: createElement(CommissionStatementPage), handle: { title: "Commission statement" } },
    { path: "/pricing/funds", element: createElement(FundsPage), handle: { title: "Trade funds" } },
    { path: "/pricing/funds/new", element: createElement(NewFundPage), handle: { title: "New trade fund" } },
    { path: "/pricing/funds/:id", element: createElement(FundDetailPage), handle: { title: "Trade fund" } },
    { path: "/pricing/promotions", element: createElement(PromotionsPage), handle: { title: "Promotions" } },
    { path: "/pricing/promotions/new", element: createElement(NewPromotionPage), handle: { title: "New promotion" } },
    { path: "/pricing/promotions/:id", element: createElement(PromotionDetailPage), handle: { title: "Promotion" } },
    { path: "/pricing/parameters", element: createElement(ParametersPage), handle: { title: "Pricing parameters" } },
    { path: "/pricing/tenant-parameters", element: createElement(TenantParametersPage), handle: { title: "Tenant pricing parameters" } },
    { path: "/pricing/simulate", element: createElement(SimulatePage), handle: { title: "Price simulation" } },
    { path: "/pricing/compare", element: createElement(ComparePage), handle: { title: "Compare procedure versions" } },
    { path: "/pricing/price-list", element: createElement(PriceListPage), handle: { title: "Price list by customer" } },
  ],
  nav: [
    { label: "Overview", to: "/pricing", duty: "pricing.view" },
    { label: "Trade agreements", to: "/pricing/agreements", duty: "pricing.view" },
    { label: "Campaigns", to: "/pricing/campaigns", duty: "pricing.view" },
    { label: "Free goods", to: "/pricing/free-goods", duty: "pricing.view" },
    { label: "Commodity prices", to: "/pricing/commodity/bases", duty: "pricing.view" },
    { label: "Price calculations", to: "/pricing/commodity/calculations", duty: "pricing.view" },
    { label: "Pricing procedures", to: "/pricing/procedures", duty: "pricing.view" },
    { label: "Procedure determination", to: "/pricing/procedure-determination", duty: "pricing.view" },
    { label: "Price journals", to: "/pricing/journals", duty: "pricing.view" },
    { label: "Rebate agreements", to: "/pricing/rebates", duty: "pricing.rebate.view" },
    { label: "Vendor rebate claims", to: "/pricing/rebates/claims", duty: "pricing.rebate.view" },
    { label: "Broker contracts", to: "/pricing/brokers", duty: "pricing.rebate.view" },
    { label: "Royalty agreements", to: "/pricing/royalties", duty: "pricing.rebate.view" },
    { label: "Broker and royalty claims", to: "/pricing/contract-claims", duty: "pricing.rebate.view" },
    { label: "Sales groups", to: "/pricing/commission/sales-groups", duty: "pricing.commission.view" },
    { label: "Commission transactions", to: "/pricing/commission/transactions", duty: "pricing.commission.view" },
    { label: "Commission settlement", to: "/pricing/commission/settlement", duty: "pricing.commission.view" },
    { label: "Commission statement", to: "/pricing/commission/statement", duty: "pricing.commission.view" },
    { label: "Trade funds", to: "/pricing/funds", duty: "pricing.view" },
    { label: "Promotions", to: "/pricing/promotions", duty: "pricing.view" },
    { label: "Parameters", to: "/pricing/parameters", duty: "pricing.view" },
    { label: "Tenant parameters", to: "/pricing/tenant-parameters", duty: "pricing.view" },
    { label: "Price simulation", to: "/pricing/simulate", duty: "pricing.view" },
    { label: "Compare procedure versions", to: "/pricing/compare", duty: "pricing.view" },
    { label: "Price list by customer", to: "/pricing/price-list", duty: "pricing.view" },
  ],
};

export default pricing;

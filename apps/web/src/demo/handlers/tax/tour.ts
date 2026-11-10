import type { TourStop } from "../../tour";

// Tax screens for the home page demo tour, in walkthrough order.
export const TOUR: TourStop[] = [
  { area: "Tax", label: "Tax workspace", to: "/tax", blurb: "The company's VAT country and number, and a count of the jurisdictions, tax codes and groups set up for US sales tax and German VAT." },
  { area: "Tax", label: "Jurisdictions", to: "/tax/jurisdictions", blurb: "Iowa, Texas and Minnesota with their counties, cities and the Dallas transit district, plus Germany, France and the Netherlands." },
  { area: "Tax", label: "Tax codes and rates", to: "/tax/codes", blurb: "Iowa and Texas sales taxes with local add-ons, and German VAT with its 2020 rate cut kept as dated rates. Edit one to see its history." },
  { area: "Tax", label: "Tax groups", to: "/tax/groups", blurb: "Which taxes apply to Iowa, Texas and Minnesota customers, resellers, German customers and EU businesses, and to pumps, installation and exempt items." },
  { area: "Tax", label: "Return boxes", to: "/tax/vat-boxes", blurb: "How German VAT amounts add up into the boxes of the return." },
  { area: "Tax", label: "VAT return", to: "/tax/vat-return", blurb: "Box totals for last month from the recorded sales and purchases, with the tax to pay at the bottom." },
  { area: "Tax", label: "EU sales list", to: "/tax/esl", blurb: "Sales to businesses in France, the Netherlands and Poland by VAT number. One Italian sale has no VAT number and is flagged." },
  { area: "Tax", label: "Intrastat declarations", to: "/tax/intrastat", blurb: "Last month's dispatches are exported; this month's journal is a draft. Open it, check the lines, merge matching ones and fix the problem line." },
  { area: "Tax", label: "Intrastat master data", to: "/tax/intrastat/master", blurb: "Commodity, transaction, transport, port, country and delivery-terms codes the declaration is checked against." },
];

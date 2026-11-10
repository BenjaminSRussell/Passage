/** Where each wizard step sends the person, and what to do there. Step keys come from the identity service. */
export interface StepGuide {
  /** The screen that does the work. */
  to: string;
  linkLabel: string;
  /** Short plain-language instructions shown with the step. */
  help: string[];
}

/** The step that checks and imports the first master data inside the guide, not on another screen. */
export const IMPORT_STEP = "import";

export const stepGuides: Record<string, StepGuide> = {
  features: {
    to: "/setup/features",
    linkLabel: "Choose features",
    help: ["Pick the template closest to your business, for example Distribution.", "Switch single features on or off afterwards. Anything you turn off disappears from menus and forms."],
  },
  company: {
    to: "/org/legal-entities",
    linkLabel: "Open legal entities",
    help: ["Add your company as a legal entity. The country and currency set the defaults for taxes and formats.", "Add one legal entity for each company that files its own accounts."],
  },
  "fiscal-calendar": {
    to: "/org/fiscal-calendars",
    linkLabel: "Open fiscal calendars",
    help: ["Create the fiscal year your company reports on, with its periods.", "Periods must exist before anything can be posted to the ledger."],
  },
  "chart-of-accounts": {
    to: "/ledger/accounts",
    linkLabel: "Open the chart of accounts",
    help: ["Load a chart of accounts for your industry and country, then adjust it.", "Vendor invoices and sales invoices post to these accounts."],
  },
  currencies: {
    to: "/fx/rates",
    linkLabel: "Open exchange rates",
    help: ["Enter exchange rates for each currency you trade in besides your company currency.", "If you only use one currency, mark this step as done."],
  },
  tax: {
    to: "/party/tax-registrations",
    linkLabel: "Open tax registrations",
    help: ["Add the tax codes and rates your invoices need.", "If you are not taxed on sales or purchases, skip this step."],
  },
  "sites-warehouses": {
    to: "/org/sites",
    linkLabel: "Open sites",
    help: ["Add each site, then the warehouses at it.", "Stock, receipts and shipments are always held at a warehouse."],
  },
  "number-sequences": {
    to: "/admin/sequences",
    linkLabel: "Open number sequences",
    help: ["Each document type has a default number sequence. Check the prefixes suit you.", "Change a sequence before the first document is numbered with it."],
  },
  "users-roles": {
    to: "/admin/security/users",
    linkLabel: "Open users",
    help: ["Add the people who will use Passage.", "Give each person the role that matches their job, so they land in the right workspace."],
  },
  [IMPORT_STEP]: {
    to: "/migration",
    linkLabel: "Open imports",
    help: ["Import vendors, customers and products from a CSV file saved from your spreadsheet.", "Check the file here first. Rows with problems are listed with the row number and what to fix, and nothing is loaded until you start the import."],
  },
};

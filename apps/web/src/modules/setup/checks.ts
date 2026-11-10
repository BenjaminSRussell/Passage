/**
 * What the setup checklist looks for. Each check reads one list from the service that owns it and asks whether
 * it holds anything. A check that cannot be read shows as "could not check", never as missing.
 *
 * The checks run here, with the person's own token, on purpose: no service owns all nine lists, and the
 * architecture keeps cross-context reads out of the services (docs/adr/0003-setup-checklist-reads.md). When the
 * web BFF gains its read gateway they move there as one query.
 */
export interface SetupCheck {
  id: string;
  /** The business area the check belongs to, as the checklist groups them. */
  area: string;
  /** What is missing, in plain words, shown when the list is empty. */
  missing: string;
  /** What exists, shown when the list has something. */
  present: string;
  path: string;
  /** The screen that fixes it. */
  to: string;
  /** Duty the person needs to read the list; without it the check is left out. */
  duty: string;
  /** Feature that must be on for the check to matter. */
  feature?: string;
}

export const checks: SetupCheck[] = [
  { id: "legal-entity", area: "Company", missing: "No legal entity yet", present: "Legal entity added", path: "/api/v1/org/legal-entities?limit=1", to: "/org/legal-entities", duty: "org.read" },
  { id: "fiscal-calendar", area: "Finance", missing: "No fiscal calendar yet, so nothing can be posted", present: "Fiscal calendar defined", path: "/api/v1/org/fiscal-calendars?limit=1", to: "/org/fiscal-calendars", duty: "org.read", feature: "finance" },
  { id: "ledger", area: "Finance", missing: "No ledger or chart of accounts yet", present: "Ledger set up", path: "/api/v1/ledger/ledgers?limit=1", to: "/ledger/accounts", duty: "ledger.journal.read", feature: "finance" },
  { id: "sites", area: "Inventory", missing: "No site yet", present: "Site added", path: "/api/v1/org/sites?limit=1", to: "/org/sites", duty: "org.read", feature: "inventory" },
  { id: "warehouses", area: "Inventory", missing: "No warehouse yet, so stock cannot be received", present: "Warehouse added", path: "/api/v1/org/warehouses?limit=1", to: "/org/warehouses", duty: "org.read", feature: "inventory" },
  { id: "products", area: "Inventory", missing: "No products yet", present: "Products added", path: "/api/v1/product/products?limit=1", to: "/product/items", duty: "product.read", feature: "inventory" },
  { id: "vendors", area: "Purchasing", missing: "No vendors yet, so nothing can be ordered", present: "Vendors added", path: "/api/v1/party/vendors:search?limit=1", to: "/party", duty: "party.read", feature: "purchasing" },
  { id: "customers", area: "Sales", missing: "No customers yet, so nothing can be sold", present: "Customers added", path: "/api/v1/party/customers:search?limit=1", to: "/party", duty: "party.read", feature: "sales" },
  { id: "sequences", area: "Company", missing: "No number sequences yet", present: "Number sequences in place", path: "/api/v1/numbering/sequences?limit=1", to: "/admin/sequences", duty: "numbering.read" },
];

/** True when the list in a service response holds at least one row. */
export function hasRows(body: unknown): boolean {
  if (Array.isArray(body)) return body.length > 0;
  const items = (body as { items?: unknown } | null)?.items;
  return Array.isArray(items) && items.length > 0;
}

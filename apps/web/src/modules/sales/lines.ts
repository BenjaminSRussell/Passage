import type { DeliveryDateControl, NewOrderLine } from "./api/client";
import { DECIMAL, UUID } from "./format";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface LineValues {
  productId: string;
  productHint?: string;
  autoUnit?: string;
  quantity: string;
  unit: string;
  siteId: string;
  warehouseId: string;
  unitPrice: string;
  /** Set while unitPrice is the price pricing suggested (customer, group and currency it was asked for); typing clears it. */
  pricedFor?: string;
  discPct: string;
  requestedShipDate: string;
  /** Empty: the legal entity default applies. Shown on the order form only. */
  deliveryDateControl?: DeliveryDateControl | "";
}

export function emptyLine(date: string): LineValues {
  return { productId: "", quantity: "1", unit: "EA", siteId: "", warehouseId: "", unitPrice: "", discPct: "0", requestedShipDate: date };
}

export const lineFields: { name: keyof LineValues; label: string; mono?: boolean; type?: string; inputMode?: "decimal" }[] = [
  { name: "productId", label: "Product" },
  { name: "quantity", label: "Quantity", inputMode: "decimal" },
  { name: "unit", label: "Unit" },
  { name: "unitPrice", label: "Unit price", inputMode: "decimal" },
  { name: "discPct", label: "Discount %", inputMode: "decimal" },
  { name: "siteId", label: "Site" },
  { name: "warehouseId", label: "Warehouse" },
  { name: "requestedShipDate", label: "Requested ship date", type: "date" },
];

/**
 * Checks lines as the service does (api/orders.go buildLine), adding errors
 * keyed by the service's field paths, and returns them as the API takes them.
 * Orders and quotations share it.
 */
export function validateLines(lines: LineValues[], errors: Record<string, string>): NewOrderLine[] {
  if (lines.length === 0) errors.lines = "Add at least one line.";
  lines.forEach((l, i) => {
    const at = (name: string) => `lines[${i}].${name}`;
    if (!UUID.test(l.productId.trim())) errors[at("productId")] = "Choose a product.";
    if (!DECIMAL.test(l.quantity) || /^0+(\.0+)?$/.test(l.quantity)) errors[at("quantity")] = "Enter a quantity above 0 with up to 6 decimals.";
    if (l.unit.trim().length < 1 || l.unit.trim().length > 10) errors[at("unit")] = "Enter a unit of up to 10 characters.";
    if (!UUID.test(l.siteId.trim())) errors[at("siteId")] = "Choose a site.";
    if (!UUID.test(l.warehouseId.trim())) errors[at("warehouseId")] = "Choose a warehouse.";
    if (!DECIMAL.test(l.unitPrice)) errors[at("unitPrice")] = "Enter a price of 0 or more.";
    if (!DECIMAL.test(l.discPct) || Number(l.discPct) > 100) errors[at("discPct")] = "Enter a discount from 0 to 100.";
    if (!DATE.test(l.requestedShipDate)) errors[at("requestedShipDate")] = "Choose the line's requested ship date.";
  });
  return lines.map((l) => ({
    productId: l.productId.trim(),
    quantity: l.quantity,
    unit: l.unit.trim(),
    siteId: l.siteId.trim(),
    warehouseId: l.warehouseId.trim(),
    unitPrice: l.unitPrice,
    discPct: l.discPct,
    requestedShipDate: l.requestedShipDate,
    ...(l.deliveryDateControl ? { deliveryDateControl: l.deliveryDateControl } : {}),
  }));
}

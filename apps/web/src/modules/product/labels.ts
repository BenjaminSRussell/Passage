import type { Lifecycle, ProductType, Subtype } from "./api/client";

export const DUTY_WRITE = "product.write";

export const lifecycleLabel: Record<Lifecycle, string> = {
  draft: "Draft",
  active: "Active",
  phase_out: "Being phased out",
  obsolete: "Obsolete",
};

export const subtypeLabel: Record<Subtype, string> = {
  product: "Product",
  master: "Master",
  variant: "Variant",
};

export const productTypeLabel: Record<ProductType, string> = {
  item: "Item",
  service: "Service",
};

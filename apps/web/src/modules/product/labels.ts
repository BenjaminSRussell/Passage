import type { Lifecycle, ProductType, Subtype, TrackingFlags } from "./api/client";

export const DUTY_WRITE = "product.write";
export const DUTY_SETUP = "product.setup.write";

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

export const trackingLabel: Record<keyof TrackingFlags, string> = {
  batchTracked: "Batch tracking",
  serialTracked: "Serial number tracking",
  catchWeight: "Catch weight",
};

/** The tenant feature that owns each tracking flag; the field is hidden while the feature is off. */
export const trackingFeature: Record<keyof TrackingFlags, string> = {
  batchTracked: "batch-tracking",
  serialTracked: "serial-tracking",
  catchWeight: "catch-weight",
};

export const trackingFlags = Object.keys(trackingLabel) as (keyof TrackingFlags)[];

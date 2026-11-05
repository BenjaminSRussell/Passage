// Request validation. Adapters only ever see shipments that passed these checks.

import type { Address, Parcel, Shipment } from "./adapter.ts";
import { parseFixed } from "./money.ts";

export type FieldErrors = Record<string, string>;

const COUNTRY = /^[A-Z]{2}$/;
const MAX_PARCELS = 50;
const MAX_PARCEL_GRAMS = 1_000_000;
const MAX_SIDE_MM = 3000;

function text(errors: FieldErrors, field: string, v: unknown, max: number, required: boolean): string | undefined {
  if (v === undefined && !required) return undefined;
  if (typeof v !== "string" || (required && v.trim() === "") || v.length > max) {
    errors[field] = required ? `is required and must be at most ${max} characters` : `must be at most ${max} characters`;
    return undefined;
  }
  return v;
}

function address(errors: FieldErrors, field: string, v: unknown): Address | undefined {
  if (typeof v !== "object" || v === null || Array.isArray(v)) {
    errors[field] = "is required";
    return undefined;
  }
  const a = v as Record<string, unknown>;
  const name = text(errors, `${field}.name`, a["name"], 60, false);
  const street1 = text(errors, `${field}.street1`, a["street1"], 100, true);
  const city = text(errors, `${field}.city`, a["city"], 60, true);
  const postalCode = text(errors, `${field}.postalCode`, a["postalCode"], 16, true);
  const state = text(errors, `${field}.state`, a["state"], 3, false);
  const country = a["country"];
  if (typeof country !== "string" || !COUNTRY.test(country)) errors[`${field}.country`] = "must be an ISO 3166-1 alpha-2 code in upper case";
  if (street1 === undefined || city === undefined || postalCode === undefined || typeof country !== "string") return undefined;
  return { street1, city, postalCode, country, ...(name !== undefined ? { name } : {}), ...(state !== undefined ? { state } : {}) };
}

function parcel(errors: FieldErrors, field: string, v: unknown): Parcel | undefined {
  if (typeof v !== "object" || v === null) {
    errors[field] = "must be an object";
    return undefined;
  }
  const p = v as Record<string, unknown>;
  const grams = parseFixed(p["weightKg"], 3);
  if (grams === null || grams === 0 || grams > MAX_PARCEL_GRAMS) errors[`${field}.weightKg`] = "must be a decimal string above 0 and at most 1000 with at most 3 decimals";
  for (const side of ["lengthCm", "widthCm", "heightCm"] as const) {
    const mm = parseFixed(p[side], 1);
    if (mm === null || mm === 0 || mm > MAX_SIDE_MM) errors[`${field}.${side}`] = "must be a decimal string above 0 and at most 300 with at most 1 decimal";
  }
  return { weightKg: p["weightKg"] as string, lengthCm: p["lengthCm"] as string, widthCm: p["widthCm"] as string, heightCm: p["heightCm"] as string };
}

/** Validates the shipment fields shared by rate and label requests. */
export function shipment(errors: FieldErrors, body: Record<string, unknown>): Shipment | undefined {
  const shipFrom = address(errors, "shipFrom", body["shipFrom"]);
  const shipTo = address(errors, "shipTo", body["shipTo"]);
  const raw = body["parcels"];
  if (!Array.isArray(raw) || raw.length === 0 || raw.length > MAX_PARCELS) {
    errors["parcels"] = `must be 1 to ${MAX_PARCELS} parcels`;
    return undefined;
  }
  const parcels = raw.map((p, i) => parcel(errors, `parcels[${i}]`, p));
  if (!shipFrom || !shipTo || Object.keys(errors).length > 0) return undefined;
  return { shipFrom, shipTo, parcels: parcels as Parcel[] };
}

const CODE = /^[A-Za-z0-9_-]{1,32}$/;

/** A list of carrier or service codes, or undefined when absent. */
export function codes(errors: FieldErrors, field: string, v: unknown): string[] | undefined {
  if (v === undefined) return undefined;
  if (!Array.isArray(v) || v.length === 0 || v.length > 20 || v.some((c) => typeof c !== "string" || !CODE.test(c))) {
    errors[field] = "must be 1 to 20 codes of letters, digits, '_' or '-'";
    return undefined;
  }
  return [...new Set(v as string[])];
}

export function code(errors: FieldErrors, field: string, v: unknown): string | undefined {
  if (typeof v !== "string" || !CODE.test(v)) {
    errors[field] = "is required: letters, digits, '_' or '-', at most 32";
    return undefined;
  }
  return v;
}

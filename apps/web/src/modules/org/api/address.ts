import { orgFetch } from "./client";

// Postal addresses of legal entities, sites, warehouses and locations. An address is created with its owner's id and
// the owner then points at it (PATCH addressId, or primaryAddressId on a legal entity), because an address needs the
// owner's id and the owner needs the address's. Every call names the owner's legal entity in X-Passage-LegalEntity:
// addresses sit behind the legal-entity row-level security policy.

export const addressPurposes = ["business", "delivery", "invoice", "remit_to", "service", "home", "other"] as const;
export type AddressPurpose = (typeof addressPurposes)[number];
export type AddressOwnerType = "legal_entity" | "site" | "warehouse" | "location";

export interface Address {
  id: string;
  versionId: string;
  ownerType: string;
  ownerId: string;
  countryCode: string;
  regionCode?: string;
  city: string;
  postalCode?: string;
  street1: string;
  street2?: string;
  formatted: string;
  purposes: { purpose: string; primary: boolean }[];
  validFrom: string;
}

export interface CreateAddressInput {
  ownerType: AddressOwnerType;
  ownerId: string;
  countryCode: string;
  regionCode?: string;
  city: string;
  postalCode?: string;
  street1: string;
  street2?: string;
  purposes: { purpose: AddressPurpose; primary: boolean }[];
}

export interface AddressChange {
  countryCode?: string;
  regionCode?: string; // "" clears it
  city?: string;
  postalCode?: string;
  street1?: string;
  street2?: string;
}

const jsonHeaders = (method: string): Record<string, string> =>
  method === "POST" ? { "Content-Type": "application/json", "Idempotency-Key": crypto.randomUUID() } : { "Content-Type": "application/json" };

export const getAddress = (legalEntityId: string, id: string) => orgFetch<Address>(`/addresses/${encodeURIComponent(id)}`, {}, legalEntityId);

// listOwnerAddresses returns the owner's current addresses (one page is plenty: an owner has a handful).
export const listOwnerAddresses = (legalEntityId: string, ownerType: AddressOwnerType, ownerId: string) =>
  orgFetch<{ items?: Address[] }>(
    `/addresses?${new URLSearchParams({ ownerType, ownerId, limit: "100" })}`,
    {},
    legalEntityId,
  ).then((page) => (Array.isArray(page?.items) ? page.items : []));

export const createAddress = (legalEntityId: string, input: CreateAddressInput) =>
  orgFetch<Address>("/addresses", { method: "POST", headers: jsonHeaders("POST"), body: JSON.stringify(input) }, legalEntityId);

// reviseAddress sends only the fields in change, with the version the caller read; a stale version is refused with
// ORG-VERSION-CONFLICT.
export const reviseAddress = (legalEntityId: string, id: string, versionId: string, change: AddressChange) =>
  orgFetch<Address>(
    `/addresses/${encodeURIComponent(id)}`,
    { method: "PATCH", headers: jsonHeaders("PATCH"), body: JSON.stringify({ versionId, ...change }) },
    legalEntityId,
  );

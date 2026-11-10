import { useState } from "react";
import { Alert, Button, FormField, Input, Select } from "../../shell/ui";
import {
  addressPurposes,
  createAddress,
  getAddress,
  listOwnerAddresses,
  reviseAddress,
  type Address,
  type AddressChange,
  type AddressOwnerType,
  type AddressPurpose,
} from "./api/address";
import { describeProblem, type Problem } from "./api/client";
import { EditAction, type EditErrors } from "./RowEdit";
import { ProblemMessage } from "../../shell/errors";

export interface AddressValues {
  countryCode: string;
  regionCode: string;
  city: string;
  postalCode: string;
  street1: string;
  street2: string;
  purpose: string;
}

type AddressField = keyof AddressValues;

const VERSION_CONFLICT = "ORG-VERSION-CONFLICT";
const emptyValues: AddressValues = { countryCode: "", regionCode: "", city: "", postalCode: "", street1: "", street2: "", purpose: "business" };

export function validateAddress({ countryCode, regionCode, city, postalCode, street1, street2 }: AddressValues): EditErrors<AddressField> {
  const errors: EditErrors<AddressField> = {};
  if (!/^[A-Za-z]{2}$/.test(countryCode.trim())) {
    errors.countryCode = "Enter a two-letter country code such as US.";
  }
  if (regionCode.trim() !== "" && !/^[A-Za-z0-9]{1,3}$/.test(regionCode.trim())) {
    errors.regionCode = "Use 1 to 3 letters or digits, such as MI.";
  }
  const required = (key: "city" | "street1", value: string, max: number) => {
    if (value.trim() === "" || value.trim().length > max) {
      errors[key] = `Enter 1 to ${max} characters.`;
    }
  };
  required("city", city, 60);
  required("street1", street1, 100);
  if (street2.trim().length > 100) {
    errors.street2 = "Use at most 100 characters.";
  }
  if (postalCode.trim().length > 12) {
    errors.postalCode = "Use at most 12 characters.";
  }
  return errors;
}

function valuesOf(address: Address | null): AddressValues {
  if (!address) {
    return emptyValues;
  }
  return {
    countryCode: address.countryCode,
    regionCode: address.regionCode ?? "",
    city: address.city,
    postalCode: address.postalCode ?? "",
    street1: address.street1,
    street2: address.street2 ?? "",
    purpose: address.purposes.find((p) => p.primary)?.purpose ?? address.purposes[0]?.purpose ?? "business",
  };
}

// The text fields the user changed, trimmed and upper-cased where the service upper-cases them; with a stored address,
// a field equal to its stored value is left out, so typing "us" over "US" sends nothing. Purposes are only chosen when
// the address is first created.
function changeOf(change: Partial<AddressValues>, stored?: AddressValues): AddressChange {
  const out: AddressChange = {};
  for (const key of ["countryCode", "regionCode", "city", "postalCode", "street1", "street2"] as const) {
    const value = change[key];
    if (value === undefined) {
      continue;
    }
    const next = key === "countryCode" || key === "regionCode" ? value.trim().toUpperCase() : value.trim();
    if (!stored || next !== stored[key]) {
      out[key] = next;
    }
  }
  return out;
}

// Service codes that belong to one field of the form.
const addressFieldCodes: Record<string, { field: AddressField; message: string }> = {
  "ORG-ADDR-COUNTRY-INVALID": { field: "countryCode", message: "This country is unknown or inactive." },
  "ORG-ADDR-REGION-UNKNOWN": { field: "regionCode", message: "This region is unknown or inactive for the country." },
  "ORG-ADDR-REGION-REQUIRED": { field: "regionCode", message: "This country requires a region." },
  "ORG-ADDR-POSTAL-FORMAT": { field: "postalCode", message: "This postal code does not match the country's format." },
  "ORG-ADDR-PRIMARY-DUPLICATE": { field: "purpose", message: "This record already has a primary address for that purpose." },
};

// The address the form starts from when the owner points at none: one the owner already has (a primary one first),
// for example created by an earlier attempt whose pointer step never finished.
function unlinkedOf(addresses: Address[]): Address | null {
  return addresses.find((a) => a.purposes.some((p) => p.primary)) ?? addresses[0] ?? null;
}

interface AddressActionProps {
  subject: string; // for example "site DET"
  ownerType: AddressOwnerType;
  ownerId: string;
  legalEntityId: string;
  // The owner's current address pointer (addressId, or primaryAddressId on a legal entity).
  addressId?: string;
  // setPointer saves the pointer on the owner through the page's conflict-aware save; it resolves once the row is
  // replaced from the response and throws the service's problem otherwise (a version conflict has re-read the row,
  // so the next attempt carries the new version).
  setPointer(addressId: string): Promise<void>;
}

// AddressAction is the row's Address button. It reads the owner's current address when there is one, then opens the
// shared edit form; only the changed fields are sent, with the version read, and the pointer follows revisions (the
// address id does not change). With no pointer it first looks for an address the owner already has but is not linked
// to (an earlier attempt whose pointer step failed, even before a page reload): the form starts from it and saving
// links it, after revising any changed field. Otherwise it creates one with the owner's id and then points the owner
// at it; if that second step fails the created address is kept, so a retry only repeats the pointer (with any field
// the user changed revised first) and never creates a duplicate.
export function AddressAction({ subject, ownerType, ownerId, legalEntityId, addressId, setPointer }: AddressActionProps) {
  const [address, setAddress] = useState<Address | null>(null);
  const [loading, setLoading] = useState(false);
  const [ready, setReady] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);

  const current = addressId && address?.id === addressId ? address : null;
  // An address of the owner that the owner does not point at yet.
  const unlinked = !addressId && address?.ownerId === ownerId ? address : null;
  const verb = addressId ? "Edit" : "Add";

  if (!ready || (addressId && !current)) {
    const load = async () => {
      setFailure(null);
      setLoading(true);
      try {
        setAddress(addressId ? await getAddress(legalEntityId, addressId) : unlinkedOf(await listOwnerAddresses(legalEntityId, ownerType, ownerId)));
        setReady(true);
      } catch (error) {
        setFailure(describeProblem(error));
      } finally {
        setLoading(false);
      }
    };
    return (
      <>
        <Button size="sm" aria-label={`${verb} address of ${subject}`} loading={loading} onClick={load}>
          Address
        </Button>
        {failure ? (
          <Alert tone="danger" title={`Could not load the address of ${subject}`} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
      </>
    );
  }

  const stored = current ?? unlinked;
  const save = async (change: Partial<AddressValues>) => {
    if (stored) {
      const patch = changeOf(change, valuesOf(stored));
      if (Object.keys(patch).length > 0) {
        try {
          setAddress(await reviseAddress(legalEntityId, stored.id, stored.versionId, patch));
        } catch (error) {
          if (describeProblem(error).code === VERSION_CONFLICT) {
            await getAddress(legalEntityId, stored.id).then(setAddress, () => undefined);
          }
          throw error;
        }
      }
      if (!current) {
        await setPointer(stored.id);
      }
      return;
    }
    const all = { ...emptyValues, ...change };
    const input = changeOf(all);
    const made = await createAddress(legalEntityId, {
      ownerType,
      ownerId,
      countryCode: input.countryCode ?? "",
      city: input.city ?? "",
      street1: input.street1 ?? "",
      ...(input.regionCode ? { regionCode: input.regionCode } : {}),
      ...(input.postalCode ? { postalCode: input.postalCode } : {}),
      ...(input.street2 ? { street2: input.street2 } : {}),
      purposes: [{ purpose: all.purpose as AddressPurpose, primary: true }],
    });
    setAddress(made);
    await setPointer(made.id);
  };

  return (
    <EditAction<AddressValues, AddressField>
      subject={`address of ${subject}`}
      openLabel="Address"
      startOpen
      saveUnchanged={unlinked !== null}
      initial={valuesOf(stored)}
      validate={validateAddress}
      save={save}
      fieldCodes={addressFieldCodes}
      fields={({ errors, text }) => (
        <>
          {unlinked ? (
            <Alert tone="info" title="Address not linked yet">
              This address belongs to the {subject} but is not linked to it. Saving links it.
            </Alert>
          ) : null}
          <FormField id={`addr-street1-${ownerId}`} label="Street" error={errors.street1}>
            {(control) => <Input {...control} {...text("street1")} maxLength={100} />}
          </FormField>
          <FormField id={`addr-street2-${ownerId}`} label="Street line 2" optional error={errors.street2}>
            {(control) => <Input {...control} {...text("street2")} maxLength={100} />}
          </FormField>
          <FormField id={`addr-city-${ownerId}`} label="City" error={errors.city}>
            {(control) => <Input {...control} {...text("city")} maxLength={60} />}
          </FormField>
          <FormField id={`addr-region-${ownerId}`} label="Region" optional hint="State or province code, for example MI." error={errors.regionCode}>
            {(control) => <Input {...control} {...text("regionCode")} maxLength={3} />}
          </FormField>
          <FormField id={`addr-postal-${ownerId}`} label="Postal code" optional error={errors.postalCode}>
            {(control) => <Input {...control} {...text("postalCode")} maxLength={12} />}
          </FormField>
          <FormField id={`addr-country-${ownerId}`} label="Country" hint="ISO 3166-1 alpha-2 code, for example US." error={errors.countryCode}>
            {(control) => <Input {...control} {...text("countryCode")} maxLength={2} />}
          </FormField>
          {stored ? null : (
            <FormField id={`addr-purpose-${ownerId}`} label="Purpose" error={errors.purpose}>
              {(control) => (
                <Select {...control} {...text("purpose")}>
                  {addressPurposes.map((purpose) => (
                    <option key={purpose} value={purpose}>
                      {purpose.replace("_", " ")}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          )}
        </>
      )}
    />
  );
}

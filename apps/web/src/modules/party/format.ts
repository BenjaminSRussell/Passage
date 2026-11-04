export const dash = "—";

export const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export const COUNTRY = /^[A-Za-z]{2}$/;

export const CURRENCY = /^[A-Za-z]{3}$/;

export const AMOUNT = /^[0-9]{1,16}(\.[0-9]{1,2})?$/;

export const DUTY_READ = "party.read";
export const DUTY_WRITE = "party.write";
export const DUTY_APPROVE = "party.approve";

export function isUuid(value: string): boolean {
  return UUID.test(value.trim());
}

export const partyTypeLabel: Record<string, string> = {
  organization: "Organization",
  person: "Person",
};

export const taxTypeLabel: Record<string, string> = {
  VAT: "VAT",
  EIN: "EIN",
  GSTIN: "GSTIN",
};

export const pendingFieldLabel: Record<string, string> = {
  credit_limit: "Credit limit",
};

/** "2026-10-26 08:00 UTC". */
export function formatDateTime(iso?: string | null): string {
  if (!iso) return dash;
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : `${date.toISOString().slice(0, 16).replace("T", " ")} UTC`;
}

export function displayOrDash(value?: string | null): string {
  return value === undefined || value === null || value === "" ? dash : value;
}

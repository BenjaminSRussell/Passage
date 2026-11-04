/** The duty PUT /charts/{chart}/accounts/{number} enforces; the service has the last word. */
export const DUTY_ACCOUNT_WRITE = "ledger.account.write";

export const CHART_CODE = /^[A-Z0-9]{1,10}$/;

/** Readable names of the account types and reconciliation types. */
export function typeLabel(value: string): string {
  return value.replaceAll("_", " ").replace(/^./, (c) => c.toUpperCase());
}

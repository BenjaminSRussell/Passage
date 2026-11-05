import { useState, type FormEvent } from "react";
import {
  Alert,
  Badge,
  Button,
  Card,
  DataTable,
  FormField,
  Input,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import { describeProblem, type Problem } from "./api/client";
import {
  cancelProfileChange,
  listMyProfileChanges,
  requestProfileChange,
  type Address,
  type NewProfileChange,
  type ProfileChange,
  type ProfileChangeKind,
} from "./api/selfService";
import { dayOf } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const changeKindLabel: Record<ProfileChangeKind, string> = {
  PREFERRED_NAME: "Preferred name",
  HOME_ADDRESS: "Home address",
  BANK_ACCOUNT: "Bank account",
};

const changeStatusLabel: Record<
  string,
  { label: string; tone: "neutral" | "success" | "warning" | "danger" | "info" }
> = {
  APPLIED: { label: "Applied", tone: "success" },
  PENDING: { label: "Waiting for approval", tone: "warning" },
  APPROVED: { label: "Approved", tone: "success" },
  REJECTED: { label: "Rejected", tone: "danger" },
  CANCELED: { label: "Withdrawn", tone: "neutral" },
};

export function ChangeStatus({ status }: { status: string }) {
  const known = changeStatusLabel[status];
  return <Badge tone={known?.tone ?? "neutral"}>{known?.label ?? status}</Badge>;
}

type Errors = Record<string, string>;
const emptyAddress: Address = { line1: "", line2: "", city: "", region: "", postalCode: "", country: "" };

/** Client checks that mirror the service; the service stays the authority. */
export function validateChange(kind: ProfileChangeKind, values: { preferredName: string; address: Address; bankAccount: string }): { errors: Errors; input?: NewProfileChange } {
  const errors: Errors = {};
  if (kind === "PREFERRED_NAME") {
    const name = values.preferredName.trim();
    if (name.length > 100) errors.preferredName = "Use at most 100 characters.";
    return { errors, input: Object.keys(errors).length ? undefined : { kind, preferredName: name } };
  }
  if (kind === "BANK_ACCOUNT") {
    const account = values.bankAccount.replace(/[\s-]/g, "").toUpperCase();
    if (!/^[A-Z0-9]{8,34}$/.test(account)) errors.bankAccount = "Enter 8 to 34 letters and digits, for example an IBAN.";
    return { errors, input: Object.keys(errors).length ? undefined : { kind, bankAccount: account } };
  }
  const a = values.address;
  const address: Address = { line1: a.line1.trim(), city: a.city.trim(), postalCode: a.postalCode.trim(), country: a.country.trim().toUpperCase() };
  if (a.line2?.trim()) address.line2 = a.line2.trim();
  if (a.region?.trim()) address.region = a.region.trim();
  if (!address.line1) errors["address.line1"] = "Enter the street address.";
  if (!address.city) errors["address.city"] = "Enter the city.";
  if (!address.postalCode) errors["address.postalCode"] = "Enter the postal code.";
  if (!/^[A-Z]{2}$/.test(address.country)) errors["address.country"] = "Enter a two-letter country code, such as US.";
  return { errors, input: Object.keys(errors).length ? undefined : { kind, address } };
}

const addressFields: { key: keyof Address; label: string; optional?: boolean }[] = [
  { key: "line1", label: "Street address" },
  { key: "line2", label: "Address line 2", optional: true },
  { key: "city", label: "City" },
  { key: "region", label: "State or region", optional: true },
  { key: "postalCode", label: "Postal code" },
  { key: "country", label: "Country code" },
];

function ChangeForm({ kind, onDone, onCancel }: { kind: ProfileChangeKind; onDone(change: ProfileChange): void; onCancel(): void }) {
  const [preferredName, setPreferredName] = useState("");
  const [address, setAddress] = useState<Address>(emptyAddress);
  const [bankAccount, setBankAccount] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateChange(kind, { preferredName, address, bankAccount });
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      onDone(await requestProfileChange(input));
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={`Change ${changeKindLabel[kind].toLowerCase()}`}>
      {kind === "PREFERRED_NAME" ? (
        <FormField id="change-preferredName" label="Preferred name" hint="Leave empty to use your legal name." error={errors.preferredName}>
          {(control) => <Input {...control} value={preferredName} invalid={Boolean(errors.preferredName)} onChange={(e) => setPreferredName(e.target.value)} />}
        </FormField>
      ) : null}
      {kind === "HOME_ADDRESS"
        ? addressFields.map((f) => (
            <FormField key={f.key} id={`change-address-${f.key}`} label={f.label} optional={f.optional} error={errors[`address.${f.key}`]}>
              {(control) => (
                <Input
                  {...control}
                  value={address[f.key] ?? ""}
                  invalid={Boolean(errors[`address.${f.key}`])}
                  autoComplete={f.key === "line1" ? "address-line1" : f.key === "postalCode" ? "postal-code" : undefined}
                  onChange={(e) => setAddress({ ...address, [f.key]: e.target.value })}
                />
              )}
            </FormField>
          ))
        : null}
      {kind === "BANK_ACCOUNT" ? (
        <FormField id="change-bankAccount" label="New bank account" hint="HR approves the change before your pay goes there." error={errors.bankAccount}>
          {(control) => <Input {...control} value={bankAccount} invalid={Boolean(errors.bankAccount)} autoComplete="off" onChange={(e) => setBankAccount(e.target.value)} />}
        </FormField>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="That didn't work" live="alert">
          {failure.message} ({failure.code})
        </Alert>
      ) : null}
      <div className={styles.rowActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {kind === "PREFERRED_NAME" ? "Save" : "Send for approval"}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** ESS: change your own preferred name now, or your address and bank account with approval; see and withdraw your changes. */
export function ProfileChangesCard({ onApplied }: { onApplied(): void }) {
  const state = useLoad((signal) => listMyProfileChanges(signal), "my-changes");
  const [editing, setEditing] = useState<ProfileChangeKind | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [withdrawing, setWithdrawing] = useState<string | null>(null);

  function done(change: ProfileChange) {
    setEditing(null);
    setNotice(
      change.status === "APPLIED"
        ? `${changeKindLabel[change.kind]} updated.`
        : `${changeKindLabel[change.kind]} change sent for approval.`,
    );
    state.reload();
    if (change.status === "APPLIED") onApplied();
  }

  async function withdraw(row: ProfileChange) {
    setWithdrawing(row.id);
    setFailure(null);
    try {
      await cancelProfileChange(row.id);
      setNotice(`${changeKindLabel[row.kind]} change withdrawn.`);
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setWithdrawing(null);
    }
  }

  const columns: DataTableColumn<ProfileChange>[] = [
    { key: "kind", header: "Change", render: (row) => changeKindLabel[row.kind] ?? row.kind },
    { key: "summary", header: "New value", mono: true },
    { key: "createdAt", header: "Requested", mono: true, render: (row) => dayOf(row.createdAt) },
    { key: "status", header: "Status", render: (row) => <ChangeStatus status={row.status} /> },
    {
      key: "actions",
      header: "Actions",
      render: (row) =>
        row.status === "PENDING" ? (
          <Button size="sm" variant="ghost" loading={withdrawing === row.id} aria-label={`Withdraw the ${changeKindLabel[row.kind].toLowerCase()} change`} onClick={() => void withdraw(row)}>
            Withdraw
          </Button>
        ) : null,
    },
  ];

  return (
    <Card title="Update my details">
      <div className={styles.stack}>
        <Alert tone="success" live="status">
          {notice}
        </Alert>
        {failure ? (
          <Alert tone="danger" title="That didn't work" live="alert">
            {failure.message} ({failure.code})
          </Alert>
        ) : null}
        {editing ? (
          <ChangeForm kind={editing} onDone={done} onCancel={() => setEditing(null)} />
        ) : (
          <div className={styles.rowActions}>
            {(Object.keys(changeKindLabel) as ProfileChangeKind[]).map((kind) => (
              <Button key={kind} size="sm" onClick={() => setEditing(kind)}>
                Change {changeKindLabel[kind].toLowerCase()}
              </Button>
            ))}
          </div>
        )}
        <p className={styles.note}>A preferred name changes at once. Your manager or HR approves an address; HR approves a bank account.</p>
        {state.error ? (
          <Alert tone="danger" title="Could not load your changes" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
            {state.error.message} ({state.error.code})
          </Alert>
        ) : !state.data ? (
          <Skeleton width="3/4" />
        ) : (
          <DataTable
            label="My changes"
            columns={columns}
            rows={state.data.items}
            getRowKey={(row) => row.id}
            empty={{ title: "No changes yet", description: "Changes you make appear here." }}
          />
        )}
      </div>
    </Card>
  );
}

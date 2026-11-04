import { useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, DataTable, EmptyState, FormField, Input, PageHeader, Skeleton, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import {
  describeProblem,
  getLeasePostingProfile,
  postLeases,
  saveLeasePostingProfile,
  type LeasePostInput,
  type LeasePostResult,
  type LeasePostingProfile,
  type Problem,
  type Versioned,
} from "./api/client";
import { DATE, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface PostValues {
  legalEntityId: string;
  ledgerId: string;
  postThrough: string;
  leaseId: string;
}

type PostErrors = Partial<Record<keyof PostValues, string>>;

/** Checks that mirror the service (internal/api postLeasesToGL). */
export function validatePost(values: PostValues): { errors: PostErrors; input?: LeasePostInput } {
  const errors: PostErrors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity's UUID.";
  if (!UUID.test(values.ledgerId.trim())) errors.ledgerId = "Enter the ledger's UUID.";
  if (values.leaseId.trim() !== "" && !UUID.test(values.leaseId.trim())) errors.leaseId = "Enter a lease UUID or leave it empty to book every lease.";
  if (!DATE.test(values.postThrough.trim()) || Number.isNaN(Date.parse(values.postThrough.trim()))) errors.postThrough = "Enter a date such as 2026-12-31.";
  if (Object.keys(errors).length > 0) return { errors };
  const input: LeasePostInput = { legalEntityId: values.legalEntityId.trim(), ledgerId: values.ledgerId.trim(), postThrough: values.postThrough.trim() };
  if (values.leaseId.trim() !== "") input.leaseId = values.leaseId.trim();
  return { errors, input };
}

/** The journal a booked or refused entry is: 0 the recognition, -1 the termination (POST /v1/leases/gl-postings), otherwise a schedule period. */
export const periodLabel = (period: number) => (period === 0 ? "Recognition" : period === -1 ? "Termination" : `Period ${period}`);

const bookedColumns: DataTableColumn<LeasePostResult["booked"][number]>[] = [
  { key: "leaseNumber", header: "Lease", mono: true },
  { key: "period", header: "Journal", render: (b) => periodLabel(b.period) },
  { key: "entryId", header: "Ledger entry", mono: true },
  { key: "lines", header: "Lines", align: "end" },
];

const failedColumns: DataTableColumn<LeasePostResult["failed"][number]>[] = [
  { key: "leaseNumber", header: "Lease", mono: true },
  { key: "period", header: "Journal", render: (f) => periodLabel(f.period) },
  { key: "code", header: "Reason", mono: true },
];

const today = () => new Date().toISOString().slice(0, 10);

/** Books the IFRS 16 recognition and schedule-period journals of a legal entity's leases in the general ledger (D4d). */
export function LeasePostingPage() {
  const [search] = useSearchParams();
  const [values, setValues] = useState<PostValues>(() => ({
    legalEntityId: search.get("legalEntityId") ?? "",
    ledgerId: "",
    postThrough: today(),
    leaseId: search.get("leaseId") ?? "",
  }));
  const [errors, setErrors] = useState<PostErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [result, setResult] = useState<LeasePostResult | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePost(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      setResult(await postLeases(input));
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as PostErrors);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const field = (name: keyof PostValues, label: string, hint: string, mono = true) => (
    <FormField id={`post-${name}`} label={label} hint={hint} error={errors[name]}>
      {(control) => <Input {...control} className={mono ? styles.mono : undefined} value={values[name]} onChange={(e) => setValues((v) => ({ ...v, [name]: e.target.value }))} invalid={Boolean(errors[name])} />}
    </FormField>
  );

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Lease ledger posting"
        description="Books the lease recognition (ROU asset against the lease liability) and one journal per payment period that has ended: interest, payment, ROU depreciation and any remeasurement. Periods go in order and a journal is never booked twice."
        actions={
          <Link className={buttonClassName("ghost")} to="/assets/leases/posting-profile">
            Lease accounts
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Post leases to the ledger">
          {field("legalEntityId", "Legal entity", "UUID of the legal entity.")}
          {field("ledgerId", "Ledger", "UUID of the ledger the journals go to.")}
          {field("postThrough", "Post through", "Journals dated on or before this day are booked; a period is dated on its last day.", false)}
          {field("leaseId", "Lease (optional)", "Leave empty to book every lease of the legal entity.")}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not post to the ledger" live="alert">
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Post to the ledger
            </Button>
          </div>
        </form>
      </Card>
      {result ? (
        <div className={styles.stack}>
          <StatGroup label="Posting result">
            <StatTile label="Journals booked" value={result.booked.length} />
            <StatTile label="Refused or failed" value={result.failed.length} />
          </StatGroup>
          {result.more ? (
            <Alert tone="info" title="More journals are waiting" live="status">
              A request books at most 200 journals. Post again to book the rest.
            </Alert>
          ) : null}
          {result.booked.some((b) => b.placeholderProfile) ? (
            <Alert tone="warning" title="Booked on placeholder accounts" live="status">
              This legal entity has no lease posting profile, so the journals used the built-in placeholder accounts. Ask the accountant to set the lease accounts.
            </Alert>
          ) : null}
          <Card title="Booked" actions={<Badge tone="success">{`${result.booked.length} booked`}</Badge>}>
            <DataTable
              label="Booked journals"
              columns={bookedColumns}
              rows={result.booked}
              getRowKey={(b) => `${b.leaseId}:${b.period}`}
              empty={{ title: "Nothing to book", description: "Every journal dated on or before that day is already in the ledger." }}
            />
          </Card>
          {result.failed.length > 0 ? (
            <Card title="Not booked" actions={<Badge tone="warning">{`${result.failed.length} not booked`}</Badge>}>
              <DataTable label="Journals not booked" columns={failedColumns} rows={result.failed} getRowKey={(f) => `${f.leaseId}:${f.period}`} empty={{ title: "None" }} />
              <p className={styles.note}>A lease stops at its first refused journal; post again once the cause (a closed period, for one) is fixed.</p>
            </Card>
          ) : null}
        </div>
      ) : (
        <EmptyState title="Nothing posted yet" description="Choose a legal entity, a ledger and a date, then post. Nothing is booked until you do." />
      )}
    </div>
  );
}

const ACCOUNTS = [
  ["rouAssetAccount", "ROU asset", "Debited on recognition and when a remeasurement raises the liability."],
  ["rouAccumulatedDepreciationAccount", "ROU accumulated depreciation", "Credited with each period's ROU depreciation."],
  ["liabilityAccount", "Lease liability", "Credited on recognition and with interest; debited by payments."],
  ["interestExpenseAccount", "Interest expense", "Debited with each period's interest."],
  ["depreciationExpenseAccount", "Depreciation expense", "Debited with each period's ROU depreciation."],
  ["paymentClearingAccount", "Payment clearing", "Credited with each period's payment (cash or payables clearing)."],
  ["remeasurementGainAccount", "Remeasurement gain", "Credited with the part of a lower liability beyond the ROU carrying amount."],
  ["terminationGainLossAccount", "Termination gain / loss", "Takes the gain (credit) or loss (debit) when a lease is terminated early."],
  ["exemptLeaseExpenseAccount", "Short-term and low-value lease expense", "Debited with each payment of a lease under a short-term or low-value election."],
] as const;

type AccountKey = (typeof ACCOUNTS)[number][0];
type ProfileValues = Record<AccountKey, string>;
type ProfileErrors = Partial<Record<AccountKey, string>>;

const ACCOUNT = /^[A-Za-z0-9][A-Za-z0-9._-]{0,39}$/;

/** Checks that mirror the service (internal/api saveLeasePostingProfile). */
export function validateProfile(values: ProfileValues): ProfileErrors {
  const errors: ProfileErrors = {};
  for (const [key] of ACCOUNTS) {
    if (!ACCOUNT.test((values[key] ?? "").trim())) errors[key] = "Enter an account number of at most 40 characters.";
  }
  return errors;
}

function ProfileForm({ entity, loaded, reload }: { entity: string; loaded: Versioned<LeasePostingProfile>; reload(): void }) {
  const { value, etag } = loaded;
  const [values, setValues] = useState<ProfileValues>(() => Object.fromEntries(ACCOUNTS.map(([key]) => [key, value[key] ?? ""])) as ProfileValues);
  const [errors, setErrors] = useState<ProfileErrors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateProfile(values);
    setErrors(found);
    setFailure(null);
    setSaved(false);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await saveLeasePostingProfile({ ...value, ...Object.fromEntries(ACCOUNTS.map(([key]) => [key, values[key].trim()])), legalEntityId: entity }, value.placeholder ? undefined : etag);
      setSaved(true);
      reload();
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as ProfileErrors);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      {value.placeholder ? (
        <Alert tone="warning" title="Placeholder accounts" live="status">
          This legal entity has no lease posting profile. Journals use the built-in placeholder accounts until the accountant saves the real ones; the ledger refuses an account that is not in its chart.
        </Alert>
      ) : null}
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Lease posting accounts">
          {ACCOUNTS.map(([key, label, hint]) => (
            <FormField key={key} id={`profile-${key}`} label={label} hint={hint} error={errors[key]}>
              {(control) => <Input {...control} className={styles.mono} value={values[key]} onChange={(e) => setValues((v) => ({ ...v, [key]: e.target.value }))} invalid={Boolean(errors[key])} />}
            </FormField>
          ))}
          {failure ? (
            <div className={styles.formActions}>
              <Alert
                tone="danger"
                title={failure.code === "AST-0006" ? "The profile changed since you opened it" : "Could not save the accounts"}
                live="alert"
                action={failure.code === "AST-0006" ? <Button size="sm" onClick={reload}>Reload</Button> : undefined}
              >
                {failure.message} ({failure.code})
              </Alert>
            </div>
          ) : null}
          {saved ? (
            <div className={styles.formActions}>
              <Alert tone="success" title="Accounts saved" live="status">
                Journals booked from now on use these accounts; journals already in the ledger keep theirs.
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              {value.placeholder ? "Save accounts" : "Replace accounts"}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  );
}

function ProfileLoader({ entity }: { entity: string }) {
  const state = useLoad((signal) => getLeasePostingProfile(entity, signal), entity);
  if (state.error) {
    return (
      <Alert tone="danger" title="Could not load the lease accounts" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        {state.error.message} ({state.error.code})
      </Alert>
    );
  }
  if (!state.data) {
    return (
      <div aria-busy="true">
        <span className="visually-hidden">Loading the lease accounts…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return <ProfileForm key={entity} entity={entity} loaded={state.data} reload={state.reload} />;
}

/** The accounts the lease journals of a legal entity use (D4d). */
export function LeaseProfilePage() {
  const [search] = useSearchParams();
  const [text, setText] = useState(search.get("legalEntityId") ?? "");
  const [entity, setEntity] = useState<string | null>(null);
  const [error, setError] = useState<string | undefined>();

  function submit(event: FormEvent) {
    event.preventDefault();
    if (!UUID.test(text.trim())) {
      setError("Enter the legal entity's UUID.");
      return;
    }
    setError(undefined);
    setEntity(text.trim());
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="Lease accounts" description="The ledger accounts of the lease journals: recognition, interest, payments, ROU depreciation and remeasurement." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Choose a legal entity">
          <FormField id="profile-legalEntityId" label="Legal entity" hint="UUID of the legal entity." error={error}>
            {(control) => <Input {...control} className={styles.mono} value={text} onChange={(e) => setText(e.target.value)} invalid={Boolean(error)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="primary">
              Show accounts
            </Button>
          </div>
        </form>
      </Card>
      {entity ? <ProfileLoader entity={entity} /> : <EmptyState title="Choose a legal entity" description="Its lease accounts, or the placeholders that apply until it has some, appear here." />}
    </div>
  );
}

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Skeleton } from "../../shell/ui";
import { fetchJournalName, JOURNAL_NAME_CODE, LedgerApiError, putJournalName, type JournalName, type JournalNameInput } from "./api/client";
import styles from "./pages.module.css";

const MAX_NAME = 60;
const MAX_LINES = 10000;

export interface JournalNameDraft {
  code: string;
  name: string;
  requiresApproval: boolean;
  selfApprovalAllowed: boolean;
  reversalReasonRequired: boolean;
  allowReconAccounts: boolean;
  linesLimit: string;
  active: boolean;
}

type Errors = Record<string, string>;

export const emptyJournalName: JournalNameDraft = {
  code: "",
  name: "",
  requiresApproval: false,
  selfApprovalAllowed: false,
  reversalReasonRequired: false,
  allowReconAccounts: false,
  linesLimit: "5000",
  active: true,
};

/** Client checks that mirror PUT /journal-names/{code}; the service has the last word. */
export function validateJournalName(v: JournalNameDraft): Errors {
  const errors: Errors = {};
  if (!JOURNAL_NAME_CODE.test(v.code)) errors.code = "Use 1 to 20 letters, digits, dashes or underscores.";
  const name = v.name.trim();
  if (name === "" || name.length > MAX_NAME) errors.name = `Enter a name of up to ${MAX_NAME} characters.`;
  const limit = Number(v.linesLimit);
  if (!/^\d+$/.test(v.linesLimit) || limit < 1 || limit > MAX_LINES) errors.linesLimit = `Enter a whole number from 1 to ${MAX_LINES}.`;
  return errors;
}

export function toJournalNameInput(v: JournalNameDraft): JournalNameInput {
  return {
    name: v.name.trim(),
    requiresApproval: v.requiresApproval,
    selfApprovalAllowed: v.requiresApproval && v.selfApprovalAllowed,
    reversalReasonRequired: v.reversalReasonRequired,
    allowReconAccounts: v.allowReconAccounts,
    linesLimit: Number(v.linesLimit),
    active: v.active,
  };
}

type Flag = "requiresApproval" | "selfApprovalAllowed" | "reversalReasonRequired" | "allowReconAccounts" | "active";

const flags: { key: Flag; label: string; note: string }[] = [
  { key: "requiresApproval", label: "Approval required", note: "Submitted batches wait for an approver before they can be posted." },
  { key: "selfApprovalAllowed", label: "Submitter may approve", note: "Only for names that require approval. Leave off to keep approval with someone else." },
  { key: "reversalReasonRequired", label: "Reversal reason required", note: "Reversing an entry posted from these batches needs a reason code." },
  { key: "allowReconAccounts", label: "Reconciliation accounts allowed", note: "Lines may post to customer, vendor, bank or other subledger control accounts. For migration and corrections only." },
  { key: "active", label: "Active", note: "Inactive names keep their batches but can't be used for new or submitted batches." },
];

function JournalNameForm({ initial, existing }: { initial: JournalNameDraft; existing: boolean }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState<JournalNameDraft>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const update = (patch: Partial<JournalNameDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateJournalName(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putJournalName(values.code, toJournalNameInput(values));
      await queryClient.invalidateQueries({ queryKey: ["ledger", "journal-names"] });
      navigate("/ledger/journal-names");
    } catch (error) {
      setFailure(error instanceof LedgerApiError ? `${error.message} (${error.code})` : (error as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={submit} noValidate aria-label={existing ? `Edit journal name ${initial.code}` : "New journal name"}>
        <FormField id="jn-code" label="Code" hint={existing ? "The code can't be changed." : "Up to 20 letters, digits, dashes or underscores, such as GJ."} error={errors.code}>
          {(c) => (
            <Input {...c} className={styles.mono} value={values.code} readOnly={existing} maxLength={20} onChange={(e) => update({ code: e.target.value })} invalid={Boolean(errors.code)} />
          )}
        </FormField>
        <FormField id="jn-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={MAX_NAME} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="jn-lines" label="Lines limit" hint="Most lines one batch may have." error={errors.linesLimit}>
          {(c) => <Input {...c} inputMode="numeric" value={values.linesLimit} onChange={(e) => update({ linesLimit: e.target.value })} invalid={Boolean(errors.linesLimit)} />}
        </FormField>
        {flags.map((f) => (
          <div key={f.key} className={styles.formActions}>
            <span className={styles.check}>
              <Input
                id={`jn-${f.key}`}
                type="checkbox"
                checked={values[f.key]}
                disabled={f.key === "selfApprovalAllowed" && !values.requiresApproval}
                onChange={(e) => update({ [f.key]: e.target.checked })}
              />
              <label htmlFor={`jn-${f.key}`}>{f.label}</label>
            </span>
            <span className={styles.note}>{f.note}</span>
          </div>
        ))}
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the journal name" live="alert">
              {failure}
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save journal name
          </Button>
          <Link className={buttonClassName("ghost")} to="/ledger/journal-names">
            Cancel
          </Link>
        </div>
      </form>
    </Card>
  );
}

/** Create a journal name (FR-11.9). */
export function NewJournalNamePage() {
  return (
    <div className={styles.stack}>
      <PageHeader title="New journal name" description="Set how batches of this kind are approved and what their lines may post to." />
      <JournalNameForm initial={emptyJournalName} existing={false} />
    </div>
  );
}

function toDraft(n: JournalName): JournalNameDraft {
  return {
    code: n.code,
    name: n.name,
    requiresApproval: n.requiresApproval,
    selfApprovalAllowed: n.selfApprovalAllowed,
    reversalReasonRequired: n.reversalReasonRequired,
    allowReconAccounts: n.allowReconAccounts,
    linesLimit: String(n.linesLimit),
    active: n.active,
  };
}

/** Edit or deactivate an existing journal name. */
export function EditJournalNamePage() {
  const { code = "" } = useParams();
  const result = useQuery({ queryKey: ["ledger", "journal-names", code], queryFn: () => fetchJournalName(code) });

  return (
    <div className={styles.stack}>
      <PageHeader title={`Journal name ${code}`} description="Changes apply to the next submit, approval or post of each batch." />
      {result.isError ? (
        <Alert tone="danger" title="Could not load the journal name" live="alert" action={<Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}>
          {(result.error as Error).message}
        </Alert>
      ) : result.isPending ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the journal name…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <JournalNameForm initial={toDraft(result.data)} existing />
      )}
    </div>
  );
}

import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createJournal, describeProblem, KEYED_JOURNAL_TYPES, listJournalNames, type JournalName, type KeyedJournalType, type Problem } from "./api/client";
import { today, typeLabels, UUID } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

interface Values {
  journalType: KeyedJournalType;
  legalEntityId: string;
  postingDate: string;
  currency: string;
  offsetAccountId: string;
  journalNameId?: string;
}

type Errors = Partial<Record<keyof Values, string>>;

const typeHints: Record<KeyedJournalType, string> = {
  MOVEMENT: "Receipts and issues against an offset account you choose.",
  ADJUSTMENT: "Stock corrections posted to the inventory adjustment account.",
  OPENING_BALANCE: "Initial stock posted against the migration clearing account.",
  OWNERSHIP_CHANGE: "Takes vendor-owned consignment stock into company ownership at the consignment price, against the consignment accrual account.",
  TRANSFER: "Moves stock between two locations, warehouses or segments of the legal entity at its running average cost. No ledger entry.",
};

/** Client-side checks that mirror the service, so most mistakes never leave the form. */
export function validateJournal(values: Values, name?: JournalName): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.postingDate)) errors.postingDate = "Choose a posting date.";
  if (!/^[A-Z]{3}$/.test(values.currency.trim())) errors.currency = "Enter a three-letter currency code, such as USD.";
  if (values.journalType === "MOVEMENT" && values.offsetAccountId.trim() === "" && !name?.offsetAccountId) {
    errors.offsetAccountId = "A movement journal needs an offset account.";
  }
  return errors;
}

const fieldNames: Record<string, keyof Values> = {
  journalType: "journalType",
  legalEntityId: "legalEntityId",
  postingDate: "postingDate",
  currency: "currency",
  offsetAccountId: "offsetAccountId",
  journalNameId: "journalNameId",
};

/** New journal header (DRAFT). On success the user lands on the journal to add lines. */
export function NewJournalPage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  // ?type=OWNERSHIP_CHANGE (from the consignment stock report) preselects the type.
  const requested = KEYED_JOURNAL_TYPES.find((t) => t === params.get("type")) ?? "MOVEMENT";
  const [values, setValues] = useState<Values>({ journalType: requested, legalEntityId: "", postingDate: today(), currency: "", offsetAccountId: "", journalNameId: "" });
  // Active journal names; the form offers those of the chosen type and legal entity.
  const names = useLoad((signal) => listJournalNames({ active: "true" }, signal), "names");
  const choices = (names.data ?? []).filter((n) => n.journalType === values.journalType && n.legalEntityId === values.legalEntityId.trim());
  const chosen = choices.find((n) => n.id === values.journalNameId);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form: a double submit or a retry after a lost response replays
  // the same create on the server instead of making a second journal.
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateJournal(values, chosen);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const journal = await createJournal(
        {
          journalType: values.journalType,
          legalEntityId: values.legalEntityId.trim(),
          postingDate: values.postingDate,
          currency: values.currency.trim(),
          offsetAccountId: values.journalType === "MOVEMENT" ? values.offsetAccountId.trim() : undefined,
          journalNameId: chosen?.id,
        },
        key.current,
      );
      navigate(`/inventory/journals/${journal.id}`);
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors: Errors = {};
      for (const [name, message] of Object.entries(problem.fields)) {
        const field = fieldNames[name];
        if (field) fieldErrors[field] = message;
      }
      if (Object.keys(fieldErrors).length > 0) {
        setErrors(fieldErrors);
      } else {
        setFailure(problem);
      }
      if (problem.code === "idempotency_key_reuse") key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  const set = (name: keyof Values) => (event: { target: { value: string } }) => {
    const value = name === "currency" ? event.target.value.toUpperCase() : event.target.value;
    // A name belongs to one type and legal entity, so changing either drops the choice.
    const dropName = name === "journalType" || name === "legalEntityId" ? { journalNameId: "" } : {};
    setValues({ ...values, [name]: value, ...dropName });
    key.current = crypto.randomUUID();
  };

  return (
    <div className={styles.stack}>
      <PageHeader title="New inventory journal" description="Create the journal header, then add lines and post it." />
      <Card>
        <form onSubmit={submit} noValidate className={styles.form}>
          <FormField id="journal-type" label="Journal type" hint={typeHints[values.journalType]}>
            {(control) => (
              <Select {...control} value={values.journalType} onChange={set("journalType")}>
                {KEYED_JOURNAL_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {typeLabels[t]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <FormField id="journal-legal-entity" label="Legal entity" hint="The legal entity id the stock belongs to." error={errors.legalEntityId}>
            {(control) => <Input {...control} value={values.legalEntityId} onChange={set("legalEntityId")} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="journal-posting-date" label="Posting date" error={errors.postingDate}>
            {(control) => <Input {...control} type="date" value={values.postingDate} onChange={set("postingDate")} invalid={Boolean(errors.postingDate)} />}
          </FormField>
          <FormField id="journal-currency" label="Currency" hint="The legal entity's accounting currency." error={errors.currency}>
            {(control) => <Input {...control} value={values.currency} maxLength={3} onChange={set("currency")} invalid={Boolean(errors.currency)} />}
          </FormField>
          {choices.length > 0 ? (
            <FormField
              id="journal-name"
              label="Journal name"
              optional
              hint={chosen?.requiresApproval ? "This name requires the journal to be approved before it posts." : "Journal names of this type for the legal entity."}
              error={errors.journalNameId}
            >
              {(control) => (
                <Select {...control} value={values.journalNameId ?? ""} onChange={set("journalNameId")}>
                  <option value="">None</option>
                  {choices.map((n) => (
                    <option key={n.id} value={n.id}>
                      {n.description ? `${n.code} (${n.description})` : n.code}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          {values.journalType === "MOVEMENT" ? (
            <FormField id="journal-offset" label="Offset account" hint={chosen?.offsetAccountId ? `Leave empty to use ${chosen.offsetAccountId} from the journal name.` : "Ledger account the movement posts against."} error={errors.offsetAccountId}>
              {(control) => <Input {...control} value={values.offsetAccountId} onChange={set("offsetAccountId")} invalid={Boolean(errors.offsetAccountId)} />}
            </FormField>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the journal" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Create journal
            </Button>
            <Link className={buttonClassName("ghost")} to="/inventory/journals">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
    </div>
  );
}

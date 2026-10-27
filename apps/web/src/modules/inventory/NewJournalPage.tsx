import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, FormField, Input, PageHeader, Select } from "../../shell/ui";
import { createJournal, describeProblem, JOURNAL_TYPES, type JournalType, type Problem } from "./api/client";
import { today, typeLabels, UUID } from "./format";
import styles from "./pages.module.css";

interface Values {
  journalType: JournalType;
  legalEntityId: string;
  postingDate: string;
  currency: string;
  offsetAccountId: string;
}

type Errors = Partial<Record<keyof Values, string>>;

const typeHints: Record<JournalType, string> = {
  MOVEMENT: "Receipts and issues against an offset account you choose.",
  ADJUSTMENT: "Stock corrections posted to the inventory adjustment account.",
  OPENING_BALANCE: "Initial stock posted against the migration clearing account.",
};

/** Client-side checks that mirror the service, so most mistakes never leave the form. */
export function validateJournal(values: Values): Errors {
  const errors: Errors = {};
  if (!UUID.test(values.legalEntityId.trim())) errors.legalEntityId = "Enter the legal entity id (a UUID).";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(values.postingDate)) errors.postingDate = "Choose a posting date.";
  if (!/^[A-Z]{3}$/.test(values.currency.trim())) errors.currency = "Enter a three-letter currency code, such as USD.";
  if (values.journalType === "MOVEMENT" && values.offsetAccountId.trim() === "") {
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
};

/** New journal header (DRAFT). On success the user lands on the journal to add lines. */
export function NewJournalPage() {
  const navigate = useNavigate();
  const [values, setValues] = useState<Values>({ journalType: "MOVEMENT", legalEntityId: "", postingDate: today(), currency: "", offsetAccountId: "" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  // One key per form: a double submit or a retry after a lost response replays
  // the same create on the server instead of making a second journal.
  const key = useRef(crypto.randomUUID());

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateJournal(values);
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
    setValues({ ...values, [name]: value });
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
                {JOURNAL_TYPES.map((t) => (
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
          {values.journalType === "MOVEMENT" ? (
            <FormField id="journal-offset" label="Offset account" hint="Ledger account the movement posts against." error={errors.offsetAccountId}>
              {(control) => <Input {...control} value={values.offsetAccountId} onChange={set("offsetAccountId")} invalid={Boolean(errors.offsetAccountId)} />}
            </FormField>
          ) : null}
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not create the journal" live="alert">
                {failure.message} ({failure.code})
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

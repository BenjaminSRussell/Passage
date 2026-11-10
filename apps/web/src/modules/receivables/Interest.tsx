import { useRef, useState, type FormEvent } from "react";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { Link } from "react-router-dom";
import { Alert, Button, buttonClassName, Card, DataTable, FormField, Input, PageHeader, Select, type DataTableColumn } from "../../shell/ui";
import {
  calculateInterest,
  describeProblem,
  listInterestNotes,
  newKey,
  postInterest,
  type Exclusion,
  type InterestBasis,
  type InterestCandidate,
  type InterestInput,
  type InterestNote,
  type InterestResult,
  type Problem,
} from "./api/client";
import { DAY, EXCLUSION_LABELS, money, shortId, today, UUID } from "./format";
import { CommandFailure, keepKeyAfter, LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;
const RATE = /^(0(\.\d{1,6})?|1(\.0{1,6})?)$/;

const noteColumns: DataTableColumn<InterestNote>[] = [
  { key: "documentNumber", header: "Document", mono: true },
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "period", header: "Period", mono: true },
  { key: "calculationDate", header: "Calculated to", mono: true },
  { key: "days", header: "Days", mono: true, align: "end" },
  { key: "principal", header: "Principal", mono: true, align: "end", render: (row) => money(row.principal, row.currency) },
  { key: "annualRate", header: "Rate", mono: true, align: "end", render: (row) => `${row.annualRate} ${row.basis === "ACT_360" ? "ACT/360" : "ACT/365"}` },
  { key: "amount", header: "Interest", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
];

/** Interest notes (FR-30.8): interest posted on overdue items, one note per item and month. */
export function InterestNotesPage() {
  const state = useLoad((signal) => listInterestNotes("", signal), "notes");
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Interest notes"
        description="Interest charged on overdue items, latest first. Each note is also an INTEREST open item on the customer's account."
        actions={
          <Link className={buttonClassName("primary")} to="/receivables/interest-notes/new">
            Calculate interest
          </Link>
        }
      />
      {state.error ? (
        <LoadFailure title="Could not load the interest notes" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Interest notes"
          columns={noteColumns}
          rows={state.data ?? []}
          getRowKey={(row) => row.id}
          loading={state.loading}
          empty={{ title: "No interest notes", description: "Calculate interest to preview the charges, then post them." }}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the latest {PAGE} notes.</p> : null}
    </div>
  );
}

type Errors = Record<string, string>;

export interface InterestDraft {
  legalEntityId: string;
  calculationDate: string;
  annualRate: string;
  basis: InterestBasis;
  graceDays: string;
}

export function validateInterest(v: InterestDraft): Errors {
  const errors: Errors = {};
  if (!UUID.test(v.legalEntityId.trim())) errors.legalEntityId = "Choose a legal entity.";
  if (!DAY.test(v.calculationDate)) errors.calculationDate = "Enter the calculation date.";
  const rate = v.annualRate.trim();
  if (!RATE.test(rate) || /^0(\.0+)?$/.test(rate)) errors.annualRate = "Enter the annual rate as a fraction above 0 and at most 1, such as 0.09 for 9%.";
  if (!/^\d{1,2}$/.test(v.graceDays.trim()) || Number(v.graceDays) > 60) errors.graceDays = "Enter whole days from 0 to 60.";
  return errors;
}

function toInput(v: InterestDraft): InterestInput {
  return {
    legalEntityId: v.legalEntityId.trim(),
    calculationDate: v.calculationDate,
    annualRate: v.annualRate.trim(),
    basis: v.basis,
    graceDays: Number(v.graceDays),
  };
}

const candidateColumns: DataTableColumn<InterestCandidate>[] = [
  { key: "documentNumber", header: "Document", mono: true },
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "principal", header: "Open amount", mono: true, align: "end", render: (row) => money(row.principal, row.currency) },
  { key: "days", header: "Days", mono: true, align: "end" },
  { key: "amount", header: "Interest", mono: true, align: "end", render: (row) => money(row.amount, row.currency) },
];

const exclusionColumns: DataTableColumn<Exclusion>[] = [
  { key: "openItemId", header: "Open item", mono: true, render: (row) => <span title={row.openItemId}>{shortId(row.openItemId)}</span> },
  { key: "customerId", header: "Customer", mono: true, render: (row) => <span title={row.customerId}>{shortId(row.customerId)}</span> },
  { key: "reason", header: "Why no interest", render: (row) => (row.reason === "BELOW_MINIMUM" ? "Below the 5.00 minimum" : EXCLUSION_LABELS[row.reason] ?? row.reason) },
];

/** Calculate interest (BR-30.5) as a preview, then post it as INTEREST open items. */
export function InterestRunPage() {
  const [values, setValues] = useState<InterestDraft>({ legalEntityId: "", calculationDate: today(), annualRate: "", basis: "ACT_365", graceDays: "5" });
  const [errors, setErrors] = useState<Errors>({});
  const [preview, setPreview] = useState<InterestResult | null>(null);
  const [posted, setPosted] = useState<InterestResult | null>(null);
  const [failure, setFailure] = useState<{ title: string; problem: Problem } | null>(null);
  const [busy, setBusy] = useState<"calculate" | "post" | null>(null);
  const key = useRef(newKey());

  function update(patch: Partial<InterestDraft>) {
    setValues((prev) => ({ ...prev, ...patch }));
    setPreview(null);
    setPosted(null);
    key.current = newKey();
  }

  async function calculate(event: FormEvent) {
    event.preventDefault();
    const found = validateInterest(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy("calculate");
    try {
      setPreview(await calculateInterest(toInput(values)));
      setPosted(null);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure({ title: "Could not calculate the interest", problem });
    } finally {
      setBusy(null);
    }
  }

  async function post() {
    setBusy("post");
    setFailure(null);
    try {
      setPosted(await postInterest(toInput(values), key.current));
      key.current = newKey();
    } catch (error) {
      setFailure({ title: "Could not post the interest", problem: describeProblem(error) });
      if (!keepKeyAfter(error)) key.current = newKey();
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Calculate interest"
        description="Interest = open amount × annual rate × days ÷ 365 (or 360), from due date plus grace, or from the last note. Disputed and promised items and amounts under 5.00 are left out."
        actions={
          <Link className={buttonClassName("secondary")} to="/receivables/interest-notes">
            Interest notes
          </Link>
        }
      />
      <Card>
        <form className={styles.form} onSubmit={calculate} noValidate>
          <FormField id="in-entity" label="Legal entity" error={errors.legalEntityId}>
            {(c) => <EntityLookup entity="legalEntity" {...c} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
          </FormField>
          <FormField id="in-date" label="Calculation date" error={errors.calculationDate}>
            {(c) => <Input {...c} type="date" value={values.calculationDate} onChange={(e) => update({ calculationDate: e.target.value })} invalid={Boolean(errors.calculationDate)} />}
          </FormField>
          <FormField id="in-rate" label="Annual rate" hint="A fraction: 0.09 is 9% a year." error={errors.annualRate}>
            {(c) => <Input {...c} className={styles.mono} inputMode="decimal" value={values.annualRate} onChange={(e) => update({ annualRate: e.target.value })} invalid={Boolean(errors.annualRate)} />}
          </FormField>
          <FormField id="in-basis" label="Day count">
            {(c) => (
              <Select {...c} value={values.basis} onChange={(e) => update({ basis: e.target.value as InterestBasis })}>
                <option value="ACT_365">Actual/365</option>
                <option value="ACT_360">Actual/360</option>
              </Select>
            )}
          </FormField>
          <FormField id="in-grace" label="Grace days" error={errors.graceDays}>
            {(c) => <Input {...c} inputMode="numeric" className={styles.mono} value={values.graceDays} onChange={(e) => update({ graceDays: e.target.value })} invalid={Boolean(errors.graceDays)} />}
          </FormField>
          <div className={styles.formActions}>
            <Button type="submit" variant="secondary" loading={busy === "calculate"}>
              Preview interest
            </Button>
          </div>
        </form>
      </Card>
      {failure ? <CommandFailure title={failure.title} problem={failure.problem} /> : null}
      {posted ? (
        <Alert tone="success" title="Interest posted" live="status">
          {posted.posted.length} interest note(s) posted. Items already charged this month were skipped.
        </Alert>
      ) : null}
      {preview ? (
        <>
          <Card
            title="Interest to charge"
            headingLevel={3}
            actions={
              preview.candidates.length > 0 && !posted ? (
                <Button size="sm" variant="primary" loading={busy === "post"} onClick={() => void post()}>
                  Post interest notes
                </Button>
              ) : null
            }
          >
            <DataTable
              label="Interest to charge"
              columns={candidateColumns}
              rows={preview.candidates}
              getRowKey={(row) => row.openItemId}
              empty={{ title: "No interest due", description: "No item is overdue past the grace days with interest of 5.00 or more." }}
            />
          </Card>
          <Card title="Left out" headingLevel={3}>
            <DataTable
              label="Items left out of interest"
              columns={exclusionColumns}
              rows={preview.exclusions}
              getRowKey={(row) => `${row.openItemId}|${row.reason}`}
              empty={{ title: "Nothing left out" }}
            />
          </Card>
        </>
      ) : null}
    </div>
  );
}

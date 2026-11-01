import { useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Textarea } from "../../shell/ui";
import { createRealignment, newKey, type RealignmentInput, type RealignmentMapping } from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const DIM_VALUE = /^[A-Za-z0-9._-]{1,20}$/;
const PERIOD = /^\d{4}-(0[1-9]|1[0-2])$/;
const ACCOUNT = /^[A-Za-z0-9._-]{1,20}$/;

export interface RealignmentDraft {
  accountFrom: string;
  accountTo: string;
  periodFrom: string;
  periodTo: string;
  mapping: string;
}

export const emptyRealignment: RealignmentDraft = { accountFrom: "", accountTo: "", periodFrom: "", periodTo: "", mapping: "" };

type Errors = Record<string, string>;

/** One pair per line, written "DEPT: C100 > C150". Returns the first malformed line as an error. */
export function parseMapping(text: string): { mapping: RealignmentMapping; error?: string } {
  const mapping: RealignmentMapping = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "") continue;
    const m = line.match(/^([A-Za-z][A-Za-z0-9_]*)\s*:\s*(\S+)\s*>\s*(\S+)$/);
    if (!m) return { mapping, error: `"${line}" must be written DIMENSION: old > new, for example DEPT: C100 > C150.` };
    const attr = m[1].toUpperCase();
    const [, , from, to] = m;
    if (!DIM_VALUE.test(from) || !DIM_VALUE.test(to)) return { mapping, error: `"${line}": values are up to 20 letters, digits, dots, underscores or dashes.` };
    if (from === to) return { mapping, error: `"${line}": the new value is the same as the old one.` };
    mapping[attr] = { ...(mapping[attr] ?? {}), [from]: to };
  }
  return { mapping };
}

function months(from: string, to: string): number {
  const [fy, fm] = from.split("-").map(Number);
  const [ty, tm] = to.split("-").map(Number);
  return (ty - fy) * 12 + (tm - fm) + 1;
}

/** Client checks that mirror POST .../realignments; the service checks the attributes against the ledger. */
export function validateRealignment(v: RealignmentDraft): Errors {
  const errors: Errors = {};
  if (!PERIOD.test(v.periodFrom.trim())) errors.periodFrom = "Enter the first period as YYYY-MM.";
  if (!PERIOD.test(v.periodTo.trim())) errors.periodTo = "Enter the last period as YYYY-MM.";
  if (!errors.periodFrom && !errors.periodTo) {
    const n = months(v.periodFrom.trim(), v.periodTo.trim());
    if (n < 1) errors.periodTo = "The last period is before the first.";
    else if (n > 60) errors.periodTo = "A run covers at most 60 periods.";
  }
  for (const key of ["accountFrom", "accountTo"] as const) {
    if (v[key].trim() !== "" && !ACCOUNT.test(v[key].trim())) errors[key] = "Enter an account number of up to 20 letters, digits, dots, underscores or dashes.";
  }
  if (!errors.accountFrom && !errors.accountTo && v.accountFrom.trim() !== "" && v.accountTo.trim() !== "" && v.accountFrom.trim() > v.accountTo.trim()) {
    errors.accountTo = "The last account is before the first.";
  }
  const parsed = parseMapping(v.mapping);
  if (parsed.error) errors.mapping = parsed.error;
  else if (Object.keys(parsed.mapping).length === 0) errors.mapping = "Enter at least one old and new value, for example DEPT: C100 > C150.";
  return errors;
}

export function toRealignmentInput(v: RealignmentDraft): RealignmentInput {
  const from = v.accountFrom.trim();
  const to = v.accountTo.trim();
  return {
    filter: { ...(from ? { accountFrom: from } : {}), ...(to ? { accountTo: to } : {}), periodFrom: v.periodFrom.trim(), periodTo: v.periodTo.trim() },
    mapping: parseMapping(v.mapping).mapping,
  };
}

function RealignmentForm({ ledgerId }: { ledgerId: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState(emptyRealignment);
  const [key] = useState(newKey);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<RealignmentDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateRealignment(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const run = await createRealignment(ledgerId, toRealignmentInput(values), key);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "realignments"] });
      navigate(`/ledger/realignments/${encodeURIComponent(run.id)}${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="New realignment">
        <FormField id="ra-pfrom" label="First period" hint="YYYY-MM" error={errors.periodFrom}>
          {(c) => <Input {...c} className={styles.mono} placeholder="2026-01" value={values.periodFrom} onChange={(e) => update({ periodFrom: e.target.value })} invalid={Boolean(errors.periodFrom)} />}
        </FormField>
        <FormField id="ra-pto" label="Last period" hint="YYYY-MM, at most 60 periods after the first" error={errors.periodTo}>
          {(c) => <Input {...c} className={styles.mono} placeholder="2026-12" value={values.periodTo} onChange={(e) => update({ periodTo: e.target.value })} invalid={Boolean(errors.periodTo)} />}
        </FormField>
        <FormField id="ra-afrom" label="First account" optional hint="Leave both account fields empty for every account." error={errors.accountFrom}>
          {(c) => <Input {...c} className={styles.mono} value={values.accountFrom} onChange={(e) => update({ accountFrom: e.target.value })} invalid={Boolean(errors.accountFrom)} />}
        </FormField>
        <FormField id="ra-ato" label="Last account" optional error={errors.accountTo}>
          {(c) => <Input {...c} className={styles.mono} value={values.accountTo} onChange={(e) => update({ accountTo: e.target.value })} invalid={Boolean(errors.accountTo)} />}
        </FormField>
        <div className={styles.wide}>
          <FormField id="ra-mapping" label="Old and new values" hint="One pair per line, written DIMENSION: old > new. Balances on accounts that track open items are left out." error={errors.mapping}>
            {(c) => <Textarea {...c} className={styles.mono} rows={4} placeholder="DEPT: C100 > C150" value={values.mapping} onChange={(e) => update({ mapping: e.target.value })} invalid={Boolean(errors.mapping)} />}
          </FormField>
        </div>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not simulate the realignment" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Simulate realignment</Button>
          <Link to={`/ledger/realignments${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

export function NewRealignmentPage() {
  const [ledgerId] = useLedgerParam();
  return (
    <div className={styles.stack}>
      <PageHeader title="New realignment" description="Nothing is posted yet. The simulation lists every balance the run would move." />
      {ledgerId ? <RealignmentForm ledgerId={ledgerId} /> : <p className={styles.note}>Open this page from the realignment list of a ledger so it knows which ledger to use.</p>}
    </div>
  );
}

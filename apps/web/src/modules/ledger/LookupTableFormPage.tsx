import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type FormEvent, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { Alert, Button, Card, FormField, Input, PageHeader, Skeleton, Textarea } from "../../shell/ui";
import { fetchLookupTable, putLookupTable, type LookupRow, type LookupTable, type LookupTableInput } from "./api/client";
import { ledgerQuery, useLedgerParam } from "./LedgerPicker";
import { errorText } from "./OpenItemsPage";
import styles from "./pages.module.css";

const MAX_NAME = 80;
const MAX_ROWS = 5000;
const DIM_VALUE = /^[A-Za-z0-9._-]{1,20}$/;
const DATE = /^\d{4}-\d{2}-\d{2}$/;

export interface LookupDraft {
  name: string;
  sourceFields: string;
  target: string;
  rows: string;
}

export const emptyLookup: LookupDraft = { name: "", sourceFields: "", target: "", rows: "" };

type Errors = Record<string, string>;

/** "ACCOUNT, CC" to ["ACCOUNT", "CC"]. */
export function splitFields(text: string): string[] {
  return text.split(",").map((p) => p.trim()).filter((p) => p !== "");
}

/**
 * Rows are typed or pasted one per line: the source values, the target value,
 * the first valid date and optionally the last valid date, separated by commas
 * or tabs (a spreadsheet paste works). `*` matches any value. Returns the rows
 * or the first problem with its line number.
 */
export function parseLookupRows(text: string, sourceCount: number): { rows: LookupRow[]; error?: string } {
  const rows: LookupRow[] = [];
  const lines = text.split(/\r?\n/);
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim() === "") continue;
    const cells = lines[i].split(/[,\t]/).map((c) => c.trim());
    const where = `Line ${i + 1}: `;
    if (cells.length < sourceCount + 2 || cells.length > sourceCount + 3) {
      return { rows, error: `${where}write ${sourceCount} source value(s), the target value, the valid-from date and optionally the valid-to date.` };
    }
    const source = cells.slice(0, sourceCount);
    const [target, validFrom, validTo] = cells.slice(sourceCount);
    if (source.some((s) => s !== "*" && !DIM_VALUE.test(s))) return { rows, error: `${where}source values are * or up to 20 letters, digits, dots, underscores or dashes.` };
    if (!DIM_VALUE.test(target)) return { rows, error: `${where}the target value is up to 20 letters, digits, dots, underscores or dashes.` };
    if (!DATE.test(validFrom)) return { rows, error: `${where}the valid-from date is written YYYY-MM-DD.` };
    if (validTo && (!DATE.test(validTo) || validTo < validFrom)) return { rows, error: `${where}the valid-to date is written YYYY-MM-DD and is not before valid-from.` };
    rows.push({ source, target, validFrom, ...(validTo ? { validTo } : {}) });
  }
  return { rows };
}

/** One row as the text the editor shows. */
export function formatLookupRow(r: LookupRow): string {
  return [...r.source, r.target, r.validFrom, ...(r.validTo ? [r.validTo] : [])].join(", ");
}

/** Client checks that mirror PUT .../lookup-tables/{id}; the service checks the names against the ledger. */
export function validateLookup(v: LookupDraft): Errors {
  const errors: Errors = {};
  const name = v.name.trim();
  if (name === "" || name.length > MAX_NAME) errors.name = `Enter a name of up to ${MAX_NAME} characters.`;
  const fields = splitFields(v.sourceFields);
  if (fields.length < 1 || fields.length > 5) errors.sourceFields = "List 1 to 5 fields, separated by commas.";
  else if (new Set(fields).size !== fields.length) errors.sourceFields = "A field is listed twice.";
  if (!/^[A-Z][A-Z0-9_]{0,19}$/.test(v.target.trim())) errors.target = "Enter the dimension to fill, in capitals, such as FUNC_AREA.";
  else if (fields.includes(v.target.trim())) errors.target = "The dimension to fill can't also be read.";
  if (!errors.sourceFields) {
    const parsed = parseLookupRows(v.rows, fields.length);
    if (parsed.error) errors.rows = parsed.error;
    else if (parsed.rows.length > MAX_ROWS) errors.rows = `At most ${MAX_ROWS} rows.`;
  }
  return errors;
}

export function toLookupInput(v: LookupDraft): LookupTableInput {
  const fields = splitFields(v.sourceFields);
  return { name: v.name.trim(), sourceFields: fields, target: v.target.trim(), rows: parseLookupRows(v.rows, fields.length).rows };
}

function LookupForm({ ledgerId, tableId, initial, existing }: { ledgerId: string; tableId: string; initial: LookupDraft; existing?: LookupTable }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<ReactNode>(null);
  const [busy, setBusy] = useState(false);
  const update = (patch: Partial<LookupDraft>) => setValues((prev) => ({ ...prev, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateLookup(values);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await putLookupTable(ledgerId, tableId, toLookupInput(values), existing?.version);
      await queryClient.invalidateQueries({ queryKey: ["ledger", "lookup-tables"] });
      navigate(`/ledger/lookup-tables${ledgerQuery(ledgerId)}`);
    } catch (e) {
      setFailure(errorText(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label={existing ? `Edit lookup table ${initial.name}` : "New lookup table"}>
        <FormField id="lt-name" label="Name" error={errors.name}>
          {(c) => <Input {...c} value={values.name} maxLength={MAX_NAME} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
        </FormField>
        <FormField id="lt-target" label="Dimension to fill" hint="The dimension of this ledger that a matching row sets." error={errors.target}>
          {(c) => <Input {...c} className={styles.mono} placeholder="FUNC_AREA" value={values.target} onChange={(e) => update({ target: e.target.value.toUpperCase() })} invalid={Boolean(errors.target)} />}
        </FormField>
        <FormField
          id="lt-fields"
          label="Values to read"
          hint="ACCOUNT, ACCOUNT_TYPE or dimensions of this ledger, in the order the rows list them."
          error={errors.sourceFields}
        >
          {(c) => <Input {...c} className={styles.mono} placeholder="ACCOUNT, CC" value={values.sourceFields} onChange={(e) => update({ sourceFields: e.target.value.toUpperCase() })} invalid={Boolean(errors.sourceFields)} />}
        </FormField>
        <div className={styles.wide}>
          <FormField
            id="lt-rows"
            label="Rows"
            optional
            hint="One row per line: the values to read, the value to set, valid from and optionally valid to (YYYY-MM-DD). * matches any value; the most specific row wins. Saving replaces all rows."
            error={errors.rows}
          >
            {(c) => <Textarea {...c} mono rows={10} placeholder="6010, C200, PROD, 2026-01-01" value={values.rows} onChange={(e) => update({ rows: e.target.value })} invalid={Boolean(errors.rows)} />}
          </FormField>
        </div>
        {failure ? (
          <div className={styles.wide}>
            <Alert tone="danger" title="Could not save the lookup table" live="alert">{failure}</Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>Save lookup table</Button>
          <Link to={`/ledger/lookup-tables${ledgerQuery(ledgerId)}`}>Cancel</Link>
        </div>
      </form>
    </Card>
  );
}

function NeedsLedger() {
  return <p className={styles.note}>Open this page from the lookup tables list of a ledger so it knows which ledger to use.</p>;
}

export function NewLookupTablePage() {
  const [ledgerId] = useLedgerParam();
  const [id] = useState(() => crypto.randomUUID());
  return (
    <div className={styles.stack}>
      <PageHeader title="New lookup table" />
      {ledgerId ? <LookupForm ledgerId={ledgerId} tableId={id} initial={emptyLookup} /> : <NeedsLedger />}
    </div>
  );
}

export function EditLookupTablePage() {
  const { id = "" } = useParams();
  const [ledgerId] = useLedgerParam();
  const table = useQuery({ queryKey: ["ledger", "lookup-tables", ledgerId, id], queryFn: () => fetchLookupTable(ledgerId, id), enabled: ledgerId !== "" });
  return (
    <div className={styles.stack}>
      <PageHeader title={table.data ? `Lookup table ${table.data.name}` : "Lookup table"} />
      {ledgerId === "" ? (
        <NeedsLedger />
      ) : table.isPending ? (
        <Skeleton height="tile" />
      ) : table.isError ? (
        <Alert tone="danger" title="Could not load the lookup table" live="alert" action={<Button size="sm" onClick={() => void table.refetch()}>Try again</Button>}>
          {errorText(table.error)}
        </Alert>
      ) : (
        <LookupForm
          key={table.data.version}
          ledgerId={ledgerId}
          tableId={id}
          existing={table.data}
          initial={{
            name: table.data.name,
            sourceFields: table.data.sourceFields.join(", "),
            target: table.data.target,
            rows: (table.data.rows ?? []).map(formatLookupRow).join("\n"),
          }}
        />
      )}
    </div>
  );
}
